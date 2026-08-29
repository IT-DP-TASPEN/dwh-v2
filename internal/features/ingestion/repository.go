package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestionrun"
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

func (repository *Repository) ListRunEntities(ctx context.Context, filter RunFilter, limit, offset int) ([]runListEntityRow, int64, error) {
	entities, arguments := groupedRunEntitiesSQL(filter)
	var total int64
	if err := repository.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM (`+entities+`) entities`, arguments...); err != nil {
		return nil, 0, fmt.Errorf("count run list entities: %w", err)
	}
	query := `SELECT entity_kind,run_id,scheduled_for,activity_at,activity_id FROM (` + entities + `) entities
		ORDER BY activity_at DESC,activity_id DESC,entity_kind ASC LIMIT ? OFFSET ?`
	queryArguments := append(append([]any(nil), arguments...), limit, offset)
	rows := make([]runListEntityRow, 0, limit)
	if err := repository.db.SelectContext(ctx, &rows, query, queryArguments...); err != nil {
		return nil, 0, fmt.Errorf("list run entities: %w", err)
	}
	for index := range rows {
		if rows[index].ScheduledFor.Valid {
			rows[index].ScheduledFor.Time = rows[index].ScheduledFor.Time.UTC()
		}
		rows[index].ActivityAt = rows[index].ActivityAt.UTC()
	}
	return rows, total, nil
}

func groupedRunEntitiesSQL(filter RunFilter) (string, []any) {
	physicalClauses := []string{`r.kind<>'run_all_child'`, `NOT EXISTS (SELECT 1 FROM schedule_attempts linked WHERE linked.ingestion_run_id=r.id)`}
	arguments := make([]any, 0, 4)
	for _, value := range []struct{ column, value string }{{"r.job_key", filter.Job}, {"r.status", filter.Status}} {
		if value.value != "" {
			physicalClauses, arguments = append(physicalClauses, value.column+`=?`), append(arguments, value.value)
		}
	}
	waveClauses := []string{}
	membership := []string{`member_occurrence.scheduled_for=o.scheduled_for`}
	waveArguments := make([]any, 0, 2)
	for _, value := range []struct{ column, value string }{{"member_run.job_key", filter.Job}, {"member_run.status", filter.Status}} {
		if value.value != "" {
			membership, waveArguments = append(membership, value.column+`=?`), append(waveArguments, value.value)
		}
	}
	if len(waveArguments) != 0 {
		waveClauses = append(waveClauses, `EXISTS (SELECT 1 FROM schedule_occurrences member_occurrence
			JOIN schedule_attempts member_attempt ON member_attempt.occurrence_id=member_occurrence.id
			JOIN ingestion_runs member_run ON member_run.id=member_attempt.ingestion_run_id
			WHERE `+strings.Join(membership, ` AND `)+`)`)
	}
	waveWhere := ""
	if len(waveClauses) != 0 {
		waveWhere = ` WHERE ` + strings.Join(waveClauses, ` AND `)
	}
	arguments = append(arguments, waveArguments...)
	return `SELECT 'run' entity_kind,r.id run_id,NULL scheduled_for,r.created_at activity_at,r.id activity_id
		FROM ingestion_runs r WHERE ` + strings.Join(physicalClauses, ` AND `) + `
		UNION ALL
		SELECT 'scheduler_wave' entity_kind,0 run_id,o.scheduled_for,MAX(attempt_run.created_at) activity_at,MAX(attempt_run.id) activity_id
		FROM schedule_occurrences o
		JOIN schedule_attempts attempt ON attempt.occurrence_id=o.id
		JOIN ingestion_runs attempt_run ON attempt_run.id=attempt.ingestion_run_id` + waveWhere + `
		GROUP BY o.scheduled_for`, arguments
}

func (repository *Repository) runsByIDs(ctx context.Context, ids []uint64) ([]runRow, error) {
	if len(ids) == 0 {
		return []runRow{}, nil
	}
	query, arguments, err := sqlx.In(`SELECT `+runColumns+` FROM ingestion_runs r
		LEFT JOIN users u ON u.id=r.requested_by_user_id WHERE r.id IN (?)`, ids)
	if err != nil {
		return nil, fmt.Errorf("prepare run hydration: %w", err)
	}
	rows := []runRow{}
	if err := repository.db.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return nil, fmt.Errorf("hydrate runs: %w", err)
	}
	return rows, nil
}

func (repository *Repository) schedulerWaveSummaries(ctx context.Context, scheduledFor []time.Time) ([]schedulerWaveSummaryRow, error) {
	if len(scheduledFor) == 0 {
		return []schedulerWaveSummaryRow{}, nil
	}
	query, arguments, err := sqlx.In(`SELECT o.scheduled_for,
		COUNT(DISTINCT o.id) total,
		COUNT(DISTINCT CASE WHEN o.status='resolved' THEN o.id END) resolved,
		COUNT(DISTINCT CASE WHEN o.status='unresolved' THEN o.id END) unresolved,
		COUNT(DISTINCT CASE WHEN o.status='discarded' THEN o.id END) discarded,
		COUNT(DISTINCT CASE WHEN o.status='rejected_invalid' THEN o.id END) rejected,
		COUNT(a.id) attempts
		FROM schedule_occurrences o LEFT JOIN schedule_attempts a ON a.occurrence_id=o.id
		WHERE o.scheduled_for IN (?) GROUP BY o.scheduled_for`, scheduledFor)
	if err != nil {
		return nil, fmt.Errorf("prepare scheduler wave summaries: %w", err)
	}
	rows := []schedulerWaveSummaryRow{}
	if err := repository.db.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return nil, fmt.Errorf("summarize scheduler waves: %w", err)
	}
	for index := range rows {
		rows[index].ScheduledFor = rows[index].ScheduledFor.UTC()
	}
	return rows, nil
}

func (repository *Repository) SchedulerWave(ctx context.Context, scheduledFor time.Time) ([]schedulerWaveOccurrenceRow, error) {
	rows := []schedulerWaveOccurrenceRow{}
	err := repository.db.SelectContext(ctx, &rows, `SELECT s.id schedule_id,s.name schedule_name,o.id occurrence_id,
		o.status occurrence_status,o.job_key,a.attempt_no,r.id run_id,r.status run_status,r.job_key run_job_key,r.created_at run_created_at
		FROM schedule_occurrences o JOIN schedules s ON s.id=o.schedule_id
		LEFT JOIN schedule_attempts a ON a.occurrence_id=o.id
		LEFT JOIN ingestion_runs r ON r.id=a.ingestion_run_id
		WHERE o.scheduled_for=? AND EXISTS (
			SELECT 1 FROM schedule_occurrences visible_occurrence
			JOIN schedule_attempts visible_attempt ON visible_attempt.occurrence_id=visible_occurrence.id
			WHERE visible_occurrence.scheduled_for=o.scheduled_for)
		ORDER BY s.name ASC,s.id ASC,o.id ASC,a.attempt_no ASC`, scheduledFor.UTC())
	if err != nil {
		return nil, fmt.Errorf("load scheduler wave: %w", err)
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows, nil
}

func runWhere(filter RunFilter) (string, []any) {
	clauses, arguments := make([]string, 0, 5), make([]any, 0, 4)
	if filter.Kind == "" {
		clauses = append(clauses, `r.kind<>'run_all_child'`)
	}
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

func (repository *Repository) runAllSummaries(ctx context.Context, parentIDs []uint64) (map[uint64]RunAllSummary, error) {
	result := make(map[uint64]RunAllSummary, len(parentIDs))
	for _, parentID := range parentIDs {
		result[parentID] = RunAllSummary{}
	}
	if len(parentIDs) == 0 {
		return result, nil
	}
	query, arguments, err := sqlx.In(`SELECT parent_run_id,status,COUNT(*) count FROM ingestion_runs
		WHERE parent_run_id IN (?) GROUP BY parent_run_id,status`, parentIDs)
	if err != nil {
		return nil, fmt.Errorf("prepare Run All summaries: %w", err)
	}
	var rows []struct {
		ParentID uint64 `db:"parent_run_id"`
		Status   string `db:"status"`
		Count    uint64 `db:"count"`
	}
	if err := repository.db.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return nil, fmt.Errorf("summarize Run All children: %w", err)
	}
	for _, row := range rows {
		summary := result[row.ParentID]
		summary.add(row.Status, row.Count)
		result[row.ParentID] = summary
	}
	return result, nil
}

func (summary *RunAllSummary) add(status string, count uint64) {
	summary.Total += count
	if ingestionrun.IsTerminal(ingestionrun.Status(status)) {
		summary.Complete += count
	}
	if status == string(ingestionrun.StatusFailed) {
		summary.Failed += count
	}
	if status == string(ingestionrun.StatusRunning) {
		summary.Running += count
	}
}

func (repository *Repository) FindRun(ctx context.Context, id uint64) (runRow, error) {
	var row runRow
	err := repository.db.GetContext(ctx, &row, `SELECT `+runDetailColumns+` FROM ingestion_runs r LEFT JOIN users u ON u.id=r.requested_by_user_id WHERE r.id=?`, id)
	return row, err
}

func (repository *Repository) Children(ctx context.Context, parentID uint64) ([]runRow, error) {
	rows := []runRow{}
	err := repository.db.SelectContext(ctx, &rows, `SELECT `+runColumns+` FROM ingestion_runs r LEFT JOIN users u ON u.id=r.requested_by_user_id WHERE r.parent_run_id=? AND r.kind='run_all_child' ORDER BY r.child_position ASC`, parentID)
	return rows, err
}

func (repository *Repository) RunAllChildren(ctx context.Context, parentID uint64) (runRow, []runRow, error) {
	rows := []runRow{}
	err := repository.db.SelectContext(ctx, &rows, `SELECT `+runColumns+` FROM ingestion_runs r
		LEFT JOIN users u ON u.id=r.requested_by_user_id
		WHERE (r.id=? AND r.kind='run_all_parent') OR (r.parent_run_id=? AND r.kind='run_all_child')
		ORDER BY CASE WHEN r.id=? THEN 0 ELSE 1 END,r.child_position ASC`, parentID, parentID, parentID)
	if err != nil {
		return runRow{}, nil, err
	}
	if len(rows) == 0 || rows[0].ID != parentID || rows[0].Kind != string(ingestionrun.KindRunAllParent) {
		return runRow{}, nil, sql.ErrNoRows
	}
	return rows[0], rows[1:], nil
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
