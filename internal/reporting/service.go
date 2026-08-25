package reporting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type ServiceConfig struct {
	ConnectTimeout          time.Duration
	InteractiveTimeout      time.Duration
	InteractiveMaxRows      int
	InteractivePayloadBytes int64
	CellPreviewBytes        int
}

type Service struct {
	repository *Repository
	pools      *PoolManager
	engine     QueryEngine
	config     ServiceConfig
}

func NewService(repository *Repository, pools *PoolManager, config ServiceConfig) (*Service, error) {
	if repository == nil || pools == nil || config.ConnectTimeout <= 0 || config.InteractiveTimeout <= 0 || config.InteractiveMaxRows <= 0 || config.InteractivePayloadBytes < 4096 || config.CellPreviewBytes <= 0 {
		return nil, fmt.Errorf("reporting service dependencies and bounds are required")
	}
	return &Service{repository: repository, pools: pools, config: config}, nil
}

func (service *Service) TestDatasource(ctx context.Context, requester securityctx.Requester, id uint64) error {
	datasource, err := service.repository.FindDatasource(ctx, id)
	if err != nil {
		return err
	}
	testContext, cancel := context.WithTimeout(ctx, service.config.ConnectTimeout)
	defer cancel()
	database, testErr := service.pools.Database(testContext, datasource, true)
	if testErr == nil {
		var one int
		if err := database.QueryRowContext(testContext, `SELECT 1`).Scan(&one); err != nil {
			testErr = fmt.Errorf("test report datasource: %w", err)
		} else if one != 1 {
			testErr = fmt.Errorf("test report datasource returned an unexpected result")
		}
	}
	outcome := "succeeded"
	if testErr != nil {
		outcome = "failed"
	}
	auditContext, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer auditCancel()
	auditErr := service.repository.AppendEvent(auditContext, requester, audit.ActionReportDatasourceTested, audit.ResourceReportDatasource, id, audit.OutcomeMetadata{Outcome: outcome}, time.Now().UTC())
	return errors.Join(testErr, auditErr)
}

func (service *Service) SetDatasourceStatus(ctx context.Context, requester securityctx.Requester, id, revision uint64, status Status) error {
	if status == StatusActive {
		if err := service.TestDatasource(ctx, requester, id); err != nil {
			return err
		}
	}
	if err := service.repository.SetDatasourceStatus(ctx, requester, id, revision, status, time.Now().UTC()); err != nil {
		return err
	}
	service.pools.Invalidate(id)
	return nil
}

func (service *Service) SetTemplateStatus(ctx context.Context, requester securityctx.Requester, id, revision uint64, status Status) error {
	if status == StatusActive {
		report, err := service.repository.FindTemplate(ctx, id)
		if err != nil {
			return err
		}
		datasource, err := service.repository.FindDatasource(ctx, report.DatasourceID)
		if err != nil {
			return err
		}
		if datasource.Status != StatusActive {
			return fmt.Errorf("%w: datasource is not active", ErrInvalid)
		}
		validateContext, cancel := context.WithTimeout(ctx, service.config.ConnectTimeout)
		defer cancel()
		database, err := service.pools.Database(validateContext, datasource, false)
		if err != nil {
			return err
		}
		if err := service.engine.Validate(validateContext, database, report.SQLText, report.Parameters); err != nil {
			return err
		}
	}
	return service.repository.SetTemplateStatus(ctx, requester, id, revision, status, time.Now().UTC())
}

func (service *Service) UpdateTemplate(ctx context.Context, requester securityctx.Requester, id, revision uint64, input TemplateInput) (Template, error) {
	current, err := service.repository.FindTemplate(ctx, id)
	if err != nil {
		return Template{}, err
	}
	if current.Status == StatusActive {
		datasource, err := service.repository.FindDatasource(ctx, input.DatasourceID)
		if err != nil {
			return Template{}, err
		}
		if datasource.Status != StatusActive {
			return Template{}, fmt.Errorf("%w: datasource is not active", ErrInvalid)
		}
		validateContext, cancel := context.WithTimeout(ctx, service.config.ConnectTimeout)
		defer cancel()
		database, err := service.pools.Database(validateContext, datasource, false)
		if err != nil {
			return Template{}, err
		}
		if err := service.engine.Validate(validateContext, database, input.SQLText, input.Parameters); err != nil {
			return Template{}, err
		}
	}
	return service.repository.UpdateTemplate(ctx, requester, id, revision, input, time.Now().UTC())
}

func (service *Service) Run(ctx context.Context, requester securityctx.Requester, reportID uint64, input map[string]InputValue) (InteractiveResult, error) {
	report, err := service.repository.FindTemplate(ctx, reportID)
	if err != nil {
		return InteractiveResult{}, err
	}
	if report.Status != StatusActive || report.DatasourceStatus != StatusActive {
		return InteractiveResult{}, ErrInactive
	}
	allowed, err := service.repository.HasAccess(ctx, reportID, requester.Effective.UserID)
	if err != nil {
		return InteractiveResult{}, err
	}
	if !allowed {
		return InteractiveResult{}, ErrForbidden
	}
	return service.execute(ctx, requester, reportID, report.DatasourceID, report.SQLText, report.Parameters, input)
}

func (service *Service) TestQuery(ctx context.Context, requester securityctx.Requester, savedReportID uint64, draft TemplateInput, input map[string]InputValue) (InteractiveResult, error) {
	if _, err := service.repository.FindTemplate(ctx, savedReportID); err != nil {
		return InteractiveResult{}, err
	}
	allowed, err := service.repository.HasAccess(ctx, savedReportID, requester.Effective.UserID)
	if err != nil {
		return InteractiveResult{}, err
	}
	if !allowed {
		return InteractiveResult{}, ErrForbidden
	}
	if err := validateTemplateInput(draft); err != nil {
		return InteractiveResult{}, err
	}
	return service.execute(ctx, requester, savedReportID, draft.DatasourceID, draft.SQLText, draft.Parameters, input)
}

func (service *Service) execute(ctx context.Context, requester securityctx.Requester, auditReportID, datasourceID uint64, statement string, parameters []Parameter, input map[string]InputValue) (InteractiveResult, error) {
	datasource, err := service.repository.FindDatasource(ctx, datasourceID)
	if err != nil {
		return InteractiveResult{}, err
	}
	if datasource.Status != StatusActive {
		return InteractiveResult{}, ErrInactive
	}
	database, err := service.pools.Database(ctx, datasource, false)
	if err != nil {
		return InteractiveResult{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, service.config.InteractiveTimeout)
	defer cancel()
	result, runErr := RunInteractive(runContext, service.engine, database, statement, parameters, input, service.config.InteractiveMaxRows, service.config.InteractivePayloadBytes, service.config.CellPreviewBytes)
	outcome := "succeeded"
	if runErr != nil {
		outcome = "failed"
	}
	auditContext, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer auditCancel()
	auditErr := service.repository.AppendEvent(auditContext, requester, audit.ActionReportExecuted, audit.ResourceReportTemplate, auditReportID, audit.OutcomeMetadata{Outcome: outcome}, time.Now().UTC())
	return result, errors.Join(runErr, auditErr)
}
