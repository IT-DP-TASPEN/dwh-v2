package sources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/audit"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

var ErrConflict = errors.New("source state changed")

type Source struct {
	Job           core.JobDefinition
	Category      string
	Enabled       bool
	ActiveRunID   *uint64
	ScheduleCount uint64
}

type sourceState struct {
	SourceID      string        `db:"source_id"`
	Enabled       bool          `db:"enabled"`
	ActiveRunID   sql.NullInt64 `db:"active_run_id"`
	ScheduleCount uint64        `db:"schedule_count"`
}

type Service struct {
	db          *sqlx.DB
	catalog     core.Catalog
	appendAudit audit.AppendFunc
}

func NewService(db *sqlx.DB, appenders ...audit.AppendFunc) (*Service, error) {
	catalog, err := core.NewCatalog()
	if err != nil {
		return nil, err
	}
	appender := audit.Append
	if len(appenders) != 0 {
		appender = appenders[0]
	}
	return &Service{db: db, catalog: catalog, appendAudit: appender}, nil
}

func (service *Service) List(ctx context.Context, includeActiveRuns bool) ([]Source, error) {
	var states []sourceState
	err := service.db.SelectContext(ctx, &states, `SELECT s.source_id,s.enabled,
		NULL active_run_id,
		COUNT(DISTINCT CASE WHEN sc.archived_at IS NULL THEN sc.id END) schedule_count
		FROM source_settings s
		LEFT JOIN schedules sc ON sc.job_key=s.source_id
		GROUP BY s.source_id,s.enabled`)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]sourceState, len(states))
	for _, state := range states {
		byKey[state.SourceID] = state
	}
	if includeActiveRuns {
		var active []struct {
			JobKey string `db:"job_key"`
			ID     uint64 `db:"id"`
		}
		if err := service.db.SelectContext(ctx, &active, `SELECT job_key,id FROM ingestion_runs WHERE active_job_key IS NOT NULL`); err != nil {
			return nil, err
		}
		for _, run := range active {
			state := byKey[run.JobKey]
			state.ActiveRunID = sql.NullInt64{Int64: int64(run.ID), Valid: true}
			byKey[run.JobKey] = state
		}
	}
	jobs := service.catalog.Jobs()
	result := make([]Source, 0, len(jobs))
	for _, job := range jobs {
		state, found := byKey[job.Key]
		if !found {
			return nil, fmt.Errorf("canonical source setting %q is missing", job.Key)
		}
		row := Source{Job: job, Category: categoryLabel(job.Category), Enabled: state.Enabled, ScheduleCount: state.ScheduleCount}
		if state.ActiveRunID.Valid {
			id := uint64(state.ActiveRunID.Int64)
			row.ActiveRunID = &id
		}
		result = append(result, row)
	}
	return result, nil
}

func (service *Service) Find(key string) (core.JobDefinition, bool) { return service.catalog.Find(key) }

func (service *Service) SetEnabled(ctx context.Context, key string, expected, enabled bool, actor uint64, requesters ...securityctx.Requester) error {
	if _, found := service.catalog.Find(key); !found {
		return sql.ErrNoRows
	}
	tx, err := service.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE source_settings SET enabled=?,updated_by_user_id=?
		WHERE source_id=? AND enabled=?`, enabled, actor, key, expected)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	if len(requesters) != 0 {
		requester := requesters[0]
		actorIdentity := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
		effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
		state := func(value bool) string {
			if value {
				return "enabled"
			}
			return "disabled"
		}
		if err := service.appendAudit(ctx, tx, audit.Event{
			Attribution: audit.Attribution{Actor: &actorIdentity, Effective: &effective},
			Action:      audit.ActionSourceStateChanged,
			Metadata:    audit.SourceStateChangeMetadata{SourceKey: key, From: state(expected), To: state(enabled)},
			CreatedAt:   time.Now().UTC(),
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func categoryLabel(category core.JobCategory) string {
	switch category {
	case core.CategoryFixed:
		return "Fixed"
	case core.CategoryEOD:
		return "EOD"
	case core.CategoryCBR:
		return "CBR"
	default:
		return "Detail"
	}
}

func Parameters(job core.JobDefinition, fromValue, toValue string) (ingestionrun.Parameters, map[string]string) {
	errorsByField := map[string]string{}
	if job.DateStrategy == core.NoDate {
		parameters, err := ingestionrun.NewLiveSnapshotExecution(job.Key)
		if err != nil {
			errorsByField["form"] = "Unable to build execution parameters."
		}
		return parameters, errorsByField
	}
	from, err := core.ParseCalendarDate(fromValue)
	if err != nil {
		errorsByField["from"] = "Choose a valid From date."
	}
	to, err := core.ParseCalendarDate(toValue)
	if err != nil {
		errorsByField["to"] = "Choose a valid To date."
	}
	if len(errorsByField) != 0 {
		return ingestionrun.Parameters{}, errorsByField
	}
	var parameters ingestionrun.Parameters
	if job.DateStrategy == core.RangeCapable {
		parameters, err = ingestionrun.NewRangeExecution(job.Key, from, to)
	} else if job.Category == core.CategoryFixed {
		parameters, err = ingestionrun.NewDateSeriesExecution(job.Key, from, to)
	} else {
		parameters, err = ingestionrun.NewMaintenanceSeriesExecution(job.Key, from, to)
	}
	if err != nil {
		errorsByField["form"] = err.Error()
	}
	return parameters, errorsByField
}
