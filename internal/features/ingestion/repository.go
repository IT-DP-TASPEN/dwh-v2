package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

const runColumns = `r.id,r.kind,r.parent_run_id,r.child_position,r.job_key,r.status,r.parameter_kind,r.parameter_version,
	r.parameters_json,r.parameter_checksum,r.trigger_type,r.trigger_reference,r.requested_by_user_id,u.username requested_by_username,
	r.skip_reason,r.cancel_requested_at,r.cancel_reason,r.owner_id,r.heartbeat_at,r.snapshot_date,
	r.progress_total,r.progress_started,r.progress_succeeded,r.progress_failed,r.rows_processed,r.current_step,
	r.error_class,r.error_message,r.error_step,r.created_at,r.started_at,r.finished_at`

const runDetailColumns = runColumns + `,r.mapper_diagnostics`

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

func (repository *Repository) ListRuns(ctx context.Context, filter RunFilter, limit, offset int) ([]runRow, int64, error) {
	where, arguments := runWhere(filter)
	var total int64
	if err := repository.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM ingestion_runs r`+where, arguments...); err != nil {
		return nil, 0, fmt.Errorf("count ingestion runs: %w", err)
	}
	query := `SELECT ` + runColumns + ` FROM ingestion_runs r LEFT JOIN users u ON u.id=r.requested_by_user_id` + where + ` ORDER BY r.id DESC LIMIT ? OFFSET ?`
	arguments = append(arguments, limit, offset)
	rows := make([]runRow, 0, limit)
	if err := repository.db.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return nil, 0, fmt.Errorf("list ingestion runs: %w", err)
	}
	return rows, total, nil
}

func runWhere(filter RunFilter) (string, []any) {
	clauses, arguments := make([]string, 0, 4), make([]any, 0, 4)
	for _, value := range []struct{ column, value string }{{"r.job_key", filter.Job}, {"r.status", filter.Status}, {"r.kind", filter.Kind}, {"r.trigger_type", filter.Trigger}} {
		if value.value != "" {
			clauses, arguments = append(clauses, value.column+`=?`), append(arguments, value.value)
		}
	}
	if len(clauses) == 0 {
		return "", arguments
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), arguments
}

func (repository *Repository) FindRun(ctx context.Context, id uint64) (runRow, error) {
	var row runRow
	err := repository.db.GetContext(ctx, &row, `SELECT `+runDetailColumns+` FROM ingestion_runs r LEFT JOIN users u ON u.id=r.requested_by_user_id WHERE r.id=?`, id)
	return row, err
}

func (repository *Repository) Children(ctx context.Context, parentID uint64) ([]runRow, error) {
	rows := []runRow{}
	err := repository.db.SelectContext(ctx, &rows, `SELECT `+runColumns+` FROM ingestion_runs r LEFT JOIN users u ON u.id=r.requested_by_user_id WHERE r.parent_run_id=? ORDER BY r.child_position`, parentID)
	return rows, err
}

func (repository *Repository) ActiveRunID(ctx context.Context, jobKey string) (uint64, bool, error) {
	var id uint64
	err := repository.db.GetContext(ctx, &id, `SELECT id FROM ingestion_runs WHERE active_job_key=?`, jobKey)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (repository *Repository) SchedulerProvenance(ctx context.Context, runID uint64) (*SchedulerProvenance, error) {
	var row struct {
		ScheduleID       uint64       `db:"schedule_id"`
		OccurrenceID     uint64       `db:"occurrence_id"`
		ScheduleName     string       `db:"schedule_name"`
		ScheduledFor     sql.NullTime `db:"scheduled_for"`
		ResolutionMode   string       `db:"resolution_mode"`
		OccurrenceStatus string       `db:"occurrence_status"`
		AttemptNo        uint32       `db:"attempt_no"`
		RetryNotBefore   sql.NullTime `db:"retry_not_before"`
	}
	err := repository.db.GetContext(ctx, &row, `SELECT s.id schedule_id,s.name schedule_name,o.id occurrence_id,o.scheduled_for,
		o.resolution_mode,o.status occurrence_status,o.retry_not_before,a.attempt_no
		FROM schedule_attempts a JOIN schedule_occurrences o ON o.id=a.occurrence_id
		JOIN schedules s ON s.id=o.schedule_id WHERE a.ingestion_run_id=?`, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value := &SchedulerProvenance{ScheduleID: row.ScheduleID, ScheduleName: row.ScheduleName, OccurrenceID: row.OccurrenceID,
		ScheduledFor: row.ScheduledFor.Time.UTC(), ResolutionMode: row.ResolutionMode, OccurrenceStatus: row.OccurrenceStatus, AttemptNo: row.AttemptNo}
	if row.RetryNotBefore.Valid {
		retry := row.RetryNotBefore.Time.UTC()
		value.RetryNotBefore = &retry
	}
	return value, nil
}

type runOverviewRows struct {
	Queued, Running     uint64
	Problems, Successes []runRow
}

func (repository *Repository) RunOverview(ctx context.Context) (runOverviewRows, error) {
	result := runOverviewRows{}
	var counts []struct {
		Status string `db:"status"`
		Count  uint64 `db:"count"`
	}
	if err := repository.db.SelectContext(ctx, &counts, `SELECT status,COUNT(*) count FROM ingestion_runs
		WHERE kind IN ('job','run_all_child') AND status IN ('queued','running') GROUP BY status`); err != nil {
		return result, err
	}
	for _, count := range counts {
		if count.Status == "queued" {
			result.Queued = count.Count
		} else {
			result.Running = count.Count
		}
	}
	problems, err := repository.recentRuns(ctx, `('failed','abandoned')`)
	if err != nil {
		return result, err
	}
	successes, err := repository.recentRuns(ctx, `('succeeded','completed','completed_with_skips')`)
	if err != nil {
		return result, err
	}
	result.Problems, result.Successes = problems, successes
	return result, nil
}

func (repository *Repository) recentRuns(ctx context.Context, statuses string) ([]runRow, error) {
	rows := []runRow{}
	query := `SELECT ` + runColumns + ` FROM ingestion_runs r LEFT JOIN users u ON u.id=r.requested_by_user_id WHERE r.status IN ` + statuses + ` ORDER BY r.id DESC LIMIT 10`
	return rows, repository.db.SelectContext(ctx, &rows, query)
}

func (repository *Repository) SourceOverview(ctx context.Context, sourceKeys []string) (SourceOverview, error) {
	var result SourceOverview
	if len(sourceKeys) == 0 {
		return result, nil
	}
	query, arguments, err := sqlx.In(`SELECT COALESCE(SUM(enabled=TRUE),0) enabled,COALESCE(SUM(enabled=FALSE),0) disabled
		FROM source_settings WHERE source_id IN (?)`, sourceKeys)
	if err != nil {
		return result, err
	}
	err = repository.db.GetContext(ctx, &result, query, arguments...)
	return result, err
}

func (repository *Repository) ScheduleOverview(ctx context.Context) (ScheduleOverview, error) {
	var result ScheduleOverview
	err := repository.db.GetContext(ctx, &result, `SELECT
		COALESCE(SUM(s.enabled=TRUE AND s.next_run_at<=UTC_TIMESTAMP(6)),0) overdue,
		COALESCE(SUM(o.status='unresolved' AND o.retry_not_before>UTC_TIMESTAMP(6)),0) retrying,
		COALESCE(SUM(s.delivery_block_reason='job_busy'),0) blocked_busy,
		COALESCE(SUM(s.delivery_block_reason='source_disabled'),0) blocked_disabled
		FROM schedules s LEFT JOIN schedule_occurrences o ON o.active_schedule_id=s.id WHERE s.archived_at IS NULL`)
	return result, err
}
