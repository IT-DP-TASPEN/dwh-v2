package reporting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type ServiceConfig struct {
	ConnectTimeout            time.Duration
	InteractiveTimeout        time.Duration
	InteractiveMaxRows        int
	InteractivePayloadBytes   int64
	DynamicOptionMaxRows      int
	DynamicOptionPayloadBytes int64
	CellPreviewBytes          int
}

type Service struct {
	repository *Repository
	pools      *PoolManager
	engine     QueryEngine
	config     ServiceConfig
}

func NewService(repository *Repository, pools *PoolManager, config ServiceConfig) (*Service, error) {
	if repository == nil || pools == nil || config.ConnectTimeout <= 0 || config.InteractiveTimeout <= 0 || config.InteractiveMaxRows <= 0 || config.InteractivePayloadBytes < 4096 || config.DynamicOptionMaxRows <= 0 || config.DynamicOptionPayloadBytes < 4096 || config.CellPreviewBytes <= 0 {
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
		if err := service.engine.ValidateTemplate(validateContext, database, report.SQLText, report.Parameters); err != nil {
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
		if err := service.engine.ValidateTemplate(validateContext, database, input.SQLText, input.Parameters); err != nil {
			return Template{}, err
		}
	}
	return service.repository.UpdateTemplate(ctx, requester, id, revision, input, time.Now().UTC())
}

func (service *Service) Run(ctx context.Context, requester securityctx.Requester, reportID uint64, input map[string]InputValue) (report Template, result InteractiveResult, err error) {
	started := time.Now()
	runContext, cancel := context.WithTimeout(ctx, service.config.InteractiveTimeout)
	defer cancel()
	var database *sql.DB
	var normalized map[string]NormalizedValue
	report, database, normalized, err = service.prepare(runContext, requester, reportID, input)
	if report.ID == 0 {
		return report, result, err
	}
	defer func() {
		err = errors.Join(err, service.appendExecutionAudit(ctx, requester, audit.ActionReportExecuted, "interactive", false, report, report.Parameters, normalized, result, err, time.Since(started)))
	}()
	if err != nil {
		return report, result, err
	}
	result, err = RunInteractiveNormalized(runContext, service.engine, database, report.SQLText, report.Parameters, normalized, service.config.InteractiveMaxRows, service.config.InteractivePayloadBytes, service.config.CellPreviewBytes)
	err = withFailureStage(failureStageQueryExecution, err)
	return report, result, err
}

func (service *Service) TestQuery(ctx context.Context, requester securityctx.Requester, savedReportID uint64, draft TemplateInput, input map[string]InputValue) (result InteractiveResult, err error) {
	started := time.Now()
	saved, err := service.repository.FindTemplate(ctx, savedReportID)
	if err != nil {
		return result, err
	}
	var normalized map[string]NormalizedValue
	defer func() {
		err = errors.Join(err, service.appendExecutionAudit(ctx, requester, audit.ActionReportTemplateQueryTested, "test_query", true, saved, draft.Parameters, normalized, result, err, time.Since(started)))
	}()
	allowed, err := service.repository.HasAccess(ctx, savedReportID, requester.Effective.UserID)
	if err != nil {
		return result, withFailureStage(failureStageAuthorization, err)
	}
	if !allowed {
		return result, withFailureStage(failureStageAuthorization, ErrForbidden)
	}
	if err := validateTemplateInput(draft); err != nil {
		return result, withFailureStage(failureStageParameterValidation, err)
	}
	datasource, err := service.repository.FindDatasource(ctx, saved.DatasourceID)
	if err != nil {
		return result, withFailureStage(failureStageQueryExecution, err)
	}
	if datasource.Status != StatusActive {
		return result, withFailureStage(failureStageAuthorization, ErrInactive)
	}
	database, err := service.pools.Database(ctx, datasource, false)
	if err != nil {
		return result, withFailureStage(failureStageQueryExecution, err)
	}
	runContext, cancel := context.WithTimeout(ctx, service.config.InteractiveTimeout)
	defer cancel()
	mode, err := service.engine.SQLMode(runContext, database)
	if err == nil {
		err = ValidateTemplateBinding(draft.SQLText, draft.Parameters, mode)
		if err != nil {
			err = withFailureStage(failureStageParameterValidation, err)
		}
	} else {
		err = withFailureStage(failureStageQueryExecution, err)
	}
	if err == nil {
		normalized, err = service.resolveAll(runContext, database, draft.Parameters, input, mode)
	}
	if err == nil {
		result, err = RunInteractiveNormalized(runContext, service.engine, database, draft.SQLText, draft.Parameters, normalized, service.config.InteractiveMaxRows, service.config.InteractivePayloadBytes, service.config.CellPreviewBytes)
		err = withFailureStage(failureStageQueryExecution, err)
	}
	return result, err
}

func (service *Service) PrepareExport(ctx context.Context, requester securityctx.Requester, reportID uint64, input map[string]InputValue) (Template, map[string]NormalizedValue, error) {
	runContext, cancel := context.WithTimeout(ctx, service.config.InteractiveTimeout)
	defer cancel()
	report, _, normalized, err := service.prepare(runContext, requester, reportID, input)
	return report, normalized, err
}

func (service *Service) prepare(ctx context.Context, requester securityctx.Requester, reportID uint64, input map[string]InputValue) (Template, *sql.DB, map[string]NormalizedValue, error) {
	report, err := service.repository.FindTemplate(ctx, reportID)
	if err != nil {
		return Template{}, nil, nil, err
	}
	if report.Status != StatusActive || report.DatasourceStatus != StatusActive {
		return report, nil, nil, withFailureStage(failureStageAuthorization, ErrInactive)
	}
	allowed, err := service.repository.HasAccess(ctx, reportID, requester.Effective.UserID)
	if err != nil {
		return report, nil, nil, withFailureStage(failureStageAuthorization, err)
	}
	if !allowed {
		return report, nil, nil, withFailureStage(failureStageAuthorization, ErrForbidden)
	}
	datasource, err := service.repository.FindDatasource(ctx, report.DatasourceID)
	if err != nil {
		return report, nil, nil, withFailureStage(failureStageQueryExecution, err)
	}
	database, err := service.pools.Database(ctx, datasource, false)
	if err != nil {
		return report, nil, nil, withFailureStage(failureStageQueryExecution, err)
	}
	mode, err := service.engine.SQLMode(ctx, database)
	if err != nil {
		return report, database, nil, withFailureStage(failureStageQueryExecution, err)
	}
	normalized, err := service.resolveAll(ctx, database, report.Parameters, input, mode)
	if err != nil {
		return report, database, normalized, err
	}
	return report, database, normalized, nil
}
