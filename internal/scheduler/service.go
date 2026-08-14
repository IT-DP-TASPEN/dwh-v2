package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

const (
	deliveryDelay = 15 * time.Second
	sweepLimit    = 100
)

type SubmitInTxFunc func(context.Context, *sqlx.Tx, string, ingestionrun.Parameters, ingestionrun.Trigger, string, *uint64) (uint64, error)

// Every mutating path locks schedule -> occurrence -> attempt -> run.
type Service struct {
	db      *sqlx.DB
	catalog ingestion.Catalog
	submit  SubmitInTxFunc
	logger  *slog.Logger
}

func New(db *sqlx.DB, submit SubmitInTxFunc, logger *slog.Logger) (*Service, error) {
	if db == nil || submit == nil || logger == nil {
		return nil, fmt.Errorf("database, submit function, and logger are required")
	}
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		return nil, err
	}
	return &Service{db: db, catalog: catalog, submit: submit, logger: logger}, nil
}

func (service *Service) Create(ctx context.Context, input CreateInput) (Schedule, error) {
	tx, err := service.db.BeginTxx(ctx, nil)
	if err != nil {
		return Schedule{}, err
	}
	defer tx.Rollback()
	now, err := dbNow(ctx, tx)
	if err != nil {
		return Schedule{}, err
	}
	if strings.TrimSpace(input.Definition.Timezone) == "" {
		input.Definition.Timezone = DefaultTimezone
	}
	definition, parsed, err := validateDefinition(service.catalog, input.Definition, now)
	if err != nil {
		return Schedule{}, err
	}
	var next any
	if input.Enabled {
		value := parsed.Next(now)
		if value.IsZero() {
			return Schedule{}, fmt.Errorf("%w: cron has no future occurrence", ErrInvalidDefinition)
		}
		next = value
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO schedules
		(name,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,enabled,next_run_at,created_by_user_id,updated_by_user_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, definition.Name, definition.JobKey, definition.CronExpression, definition.Timezone,
		definition.Policy.Kind, definition.Policy.Version, definition.Policy.Payload, definition.Policy.Checksum[:], input.Enabled, next, input.ActorID, input.ActorID)
	if err != nil {
		return Schedule{}, err
	}
	id, _ := result.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Schedule{}, err
	}
	return service.Get(ctx, uint64(id))
}

func (service *Service) Get(ctx context.Context, id uint64) (Schedule, error) {
	var row scheduleRow
	if err := service.db.GetContext(ctx, &row, scheduleSelect+` WHERE id=?`, id); err != nil {
		return Schedule{}, err
	}
	return row.value(), nil
}

func (service *Service) Update(ctx context.Context, id uint64, input UpdateInput) (Schedule, error) {
	tx, err := service.db.BeginTxx(ctx, nil)
	if err != nil {
		return Schedule{}, err
	}
	defer tx.Rollback()
	now, err := dbNow(ctx, tx)
	if err != nil {
		return Schedule{}, err
	}
	row, err := lockSchedule(ctx, tx, id)
	if err != nil {
		return Schedule{}, err
	}
	if row.Revision != input.ExpectedRevision {
		return Schedule{}, ErrConflict
	}
	if row.ArchivedAt.Valid {
		return Schedule{}, ErrArchived
	}
	if strings.TrimSpace(input.Definition.Timezone) == "" {
		input.Definition.Timezone = DefaultTimezone
	}
	definition, parsed, err := validateDefinition(service.catalog, input.Definition, now)
	if err != nil {
		return Schedule{}, err
	}
	semanticChange := !row.semanticMatches(definition)
	next := nullableTime(row.NextRunAt)
	if semanticChange {
		_, found, err := lockActiveOccurrence(ctx, tx, id)
		if err != nil {
			return Schedule{}, err
		}
		if found || (row.Enabled && row.NextRunAt.Valid && !row.NextRunAt.Time.After(now)) {
			return Schedule{}, ErrBacklog
		}
		if row.Enabled {
			next = parsed.Next(now)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET name=?,job_key=?,cron_expression=?,timezone=?,policy_kind=?,policy_version=?,
		policy_json=?,policy_checksum=?,next_run_at=?,updated_by_user_id=?,revision=revision+1,
		validation_error_class=NULL,validation_error_message=NULL,validation_error_at=NULL
		WHERE id=? AND revision=?`, definition.Name, definition.JobKey, definition.CronExpression, definition.Timezone,
		definition.Policy.Kind, definition.Policy.Version, definition.Policy.Payload, definition.Policy.Checksum[:], next, input.ActorID, id, input.ExpectedRevision)
	if err != nil {
		return Schedule{}, err
	}
	if err := requireOne(result); err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, err
	}
	return service.Get(ctx, id)
}

func (service *Service) Enable(ctx context.Context, id, expectedRevision uint64, actor *uint64) (Schedule, error) {
	tx, err := service.db.BeginTxx(ctx, nil)
	if err != nil {
		return Schedule{}, err
	}
	defer tx.Rollback()
	now, err := dbNow(ctx, tx)
	if err != nil {
		return Schedule{}, err
	}
	row, err := lockSchedule(ctx, tx, id)
	if err != nil {
		return Schedule{}, err
	}
	if row.Revision != expectedRevision {
		return Schedule{}, ErrConflict
	}
	if row.ArchivedAt.Valid {
		return Schedule{}, ErrArchived
	}
	if _, found, err := lockActiveOccurrence(ctx, tx, id); err != nil {
		return Schedule{}, err
	} else if found {
		return Schedule{}, ErrBacklog
	}
	_, parsed, err := validateDefinition(service.catalog, row.definition(), now)
	if err != nil {
		return Schedule{}, err
	}
	next := parsed.Next(now)
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled=TRUE,next_run_at=?,scheduler_not_before=NULL,
		delivery_block_reason=NULL,delivery_blocked_at=NULL,validation_error_class=NULL,validation_error_message=NULL,
		validation_error_at=NULL,updated_by_user_id=?,revision=revision+1 WHERE id=? AND revision=? AND enabled=FALSE AND archived_at IS NULL`,
		next, actor, id, expectedRevision)
	if err != nil {
		return Schedule{}, err
	}
	if err := requireOne(result); err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, err
	}
	return service.Get(ctx, id)
}

func (service *Service) Disable(ctx context.Context, id, expectedRevision uint64, actor *uint64) (Schedule, error) {
	return service.stop(ctx, id, expectedRevision, actor, false)
}

func (service *Service) Archive(ctx context.Context, id, expectedRevision uint64, actor *uint64) (Schedule, error) {
	return service.stop(ctx, id, expectedRevision, actor, true)
}

func (service *Service) stop(ctx context.Context, id, expectedRevision uint64, actor *uint64, archive bool) (Schedule, error) {
	tx, err := service.db.BeginTxx(ctx, nil)
	if err != nil {
		return Schedule{}, err
	}
	defer tx.Rollback()
	now, err := dbNow(ctx, tx)
	if err != nil {
		return Schedule{}, err
	}
	row, err := lockSchedule(ctx, tx, id)
	if err != nil {
		return Schedule{}, err
	}
	if row.Revision != expectedRevision {
		return Schedule{}, ErrConflict
	}
	occurrence, found, err := lockActiveOccurrence(ctx, tx, id)
	if err != nil {
		return Schedule{}, err
	}
	if found {
		result, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status='discarded',retry_not_before=NULL,
			closed_at=?,closed_by_user_id=? WHERE id=? AND status='unresolved'`, now, actor, occurrence.ID)
		if err != nil {
			return Schedule{}, err
		}
		if err := requireOne(result); err != nil {
			return Schedule{}, err
		}
	}
	archivedAt := nullableTime(row.ArchivedAt)
	if archive {
		archivedAt = now
	}
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled=FALSE,next_run_at=NULL,scheduler_not_before=NULL,
		delivery_block_reason=NULL,delivery_blocked_at=NULL,archived_at=?,updated_by_user_id=?,revision=revision+1
		WHERE id=? AND revision=?`, archivedAt, actor, id, expectedRevision)
	if err != nil {
		return Schedule{}, err
	}
	if err := requireOne(result); err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, err
	}
	return service.Get(ctx, id)
}

func (service *Service) Sweep(ctx context.Context) error {
	ids := []uint64{}
	if err := service.db.SelectContext(ctx, &ids, `SELECT s.id FROM schedules s
		LEFT JOIN schedule_occurrences o ON o.active_schedule_id=s.id
		WHERE s.enabled=TRUE AND s.archived_at IS NULL AND s.next_run_at<=UTC_TIMESTAMP(6)
		AND (s.scheduler_not_before IS NULL OR s.scheduler_not_before<=UTC_TIMESTAMP(6))
		AND (o.id IS NULL OR o.retry_not_before IS NULL OR o.retry_not_before<=UTC_TIMESTAMP(6))
		AND NOT EXISTS (
			SELECT 1 FROM schedule_attempts a JOIN ingestion_runs r ON r.id=a.ingestion_run_id
			WHERE a.occurrence_id=o.id AND r.status IN ('queued','running')
		)
		ORDER BY s.next_run_at,s.id LIMIT ?`, sweepLimit); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := service.process(ctx, id); err != nil && ctx.Err() == nil {
			service.logger.Error("process schedule", "schedule_id", id, "error", err)
		}
	}
	return nil
}

func (service *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(deliveryDelay)
	defer ticker.Stop()
	for {
		if err := service.Sweep(ctx); err != nil && ctx.Err() == nil {
			service.logger.Error("sweep schedules", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (service *Service) process(ctx context.Context, id uint64) (bool, error) {
	tx, err := service.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now, err := dbNow(ctx, tx)
	if err != nil {
		return false, err
	}
	schedule, err := lockSchedule(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !schedule.Enabled || schedule.ArchivedAt.Valid || !schedule.NextRunAt.Valid || schedule.NextRunAt.Time.After(now) ||
		(schedule.SchedulerNotBefore.Valid && schedule.SchedulerNotBefore.Time.After(now)) {
		return false, nil
	}
	definition, parsed, definitionErr := validateDefinition(service.catalog, schedule.definition(), now)
	job, jobFound := service.catalog.Find(schedule.JobKey)
	if definitionErr != nil || !jobFound || !parsed.IsOccurrence(schedule.NextRunAt.Time) {
		if definitionErr == nil {
			definitionErr = fmt.Errorf("%w: persisted cursor is not a cron occurrence", ErrInvalidDefinition)
		}
		return true, service.rejectInvalid(ctx, tx, schedule, now, definitionErr)
	}
	occurrence, found, err := lockActiveOccurrence(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if found && !occurrence.ScheduledFor.Equal(schedule.NextRunAt.Time) {
		return true, service.rejectLockedOccurrence(ctx, tx, schedule, occurrence, now,
			fmt.Errorf("%w: active occurrence does not match durable cursor", ErrInvalidDefinition))
	}
	if !found {
		mode := "historical"
		if job.DateStrategy == ingestion.NoDate {
			mode = "live_coalesced"
		}
		occurrence, err = createOccurrence(ctx, tx, schedule, mode)
		if err != nil {
			return false, err
		}
	}
	if !occurrence.semanticMatches(definition) {
		return true, service.rejectLockedOccurrence(ctx, tx, schedule, occurrence, now,
			fmt.Errorf("%w: occurrence definition differs from schedule", ErrInvalidDefinition))
	}
	expectedMode := "historical"
	if job.DateStrategy == ingestion.NoDate {
		expectedMode = "live_coalesced"
	}
	if occurrence.IdentitySource != "validated_cron" || occurrence.ResolutionMode != expectedMode {
		return true, service.rejectLockedOccurrence(ctx, tx, schedule, occurrence, now,
			fmt.Errorf("%w: occurrence resolution contract is invalid", ErrInvalidDefinition))
	}
	attempt, hasAttempt, err := lockLatestAttempt(ctx, tx, occurrence.ID)
	if err != nil {
		return false, err
	}
	if hasAttempt {
		if attempt.AttemptNo != occurrence.AttemptCount {
			return true, service.rejectLockedOccurrence(ctx, tx, schedule, occurrence, now,
				fmt.Errorf("%w: occurrence attempt sequence is invalid", ErrInvalidDefinition))
		}
		run, err := lockAttemptRun(ctx, tx, attempt.RunID)
		if err != nil {
			return false, err
		}
		switch run.Status {
		case ingestionrun.StatusSucceeded:
			return true, service.resolveSuccess(ctx, tx, schedule, occurrence, attempt, run, parsed, now)
		case ingestionrun.StatusQueued, ingestionrun.StatusRunning:
			return false, nil
		case ingestionrun.StatusFailed, ingestionrun.StatusCancelled, ingestionrun.StatusAbandoned:
			if !run.FinishedAt.Valid {
				return false, fmt.Errorf("terminal scheduled run %d has no finished_at", run.ID)
			}
			if !occurrence.RetryNotBefore.Valid {
				return true, service.recordBackoff(ctx, tx, schedule, occurrence, attempt, run)
			}
			if occurrence.RetryNotBefore.Time.After(now) {
				return false, nil
			}
		default:
			return false, fmt.Errorf("scheduled run %d has invalid status %q", run.ID, run.Status)
		}
	} else if occurrence.AttemptCount != 0 {
		return true, service.rejectLockedOccurrence(ctx, tx, schedule, occurrence, now,
			fmt.Errorf("%w: occurrence attempt history is missing", ErrInvalidDefinition))
	}
	parameters, err := parametersForOccurrence(job, occurrence.ScheduledFor)
	if err != nil {
		return false, err
	}
	attemptNo := occurrence.AttemptCount + 1
	reference := fmt.Sprintf("schedule:%d:%s:attempt:%d", schedule.ID, occurrence.ScheduledFor.UTC().Format(time.RFC3339Nano), attemptNo)
	runID, err := service.submit(ctx, tx, schedule.JobKey, parameters, ingestionrun.TriggerScheduler, reference, nullableUint64(schedule.CreatedByUserID))
	if errors.Is(err, ingestionrun.ErrJobBusy) || errors.Is(err, ingestionrun.ErrSourceDisabled) {
		reason := "job_busy"
		if errors.Is(err, ingestionrun.ErrSourceDisabled) {
			reason = "source_disabled"
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE schedules SET delivery_block_reason=?,delivery_blocked_at=?,scheduler_not_before=?
			WHERE id=? AND enabled=TRUE AND next_run_at=?`, reason, now, now.Add(deliveryDelay), schedule.ID, occurrence.ScheduledFor)
		if updateErr != nil {
			return false, updateErr
		}
		if err := requireOne(result); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_attempts (occurrence_id,attempt_no,ingestion_run_id,trigger_reference,submitted_at)
		VALUES (?,?,?,?,?)`, occurrence.ID, attemptNo, runID, reference, now); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET attempt_count=?,retry_not_before=NULL
		WHERE id=? AND status='unresolved' AND attempt_count=?`, attemptNo, occurrence.ID, occurrence.AttemptCount)
	if err != nil {
		return false, err
	}
	if err := requireOne(result); err != nil {
		return false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE schedules SET delivery_block_reason=NULL,delivery_blocked_at=NULL,scheduler_not_before=?
		WHERE id=? AND enabled=TRUE AND next_run_at=?`, now.Add(deliveryDelay), schedule.ID, occurrence.ScheduledFor)
	if err != nil {
		return false, err
	}
	if err := requireOne(result); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (service *Service) resolveSuccess(ctx context.Context, tx *sqlx.Tx, schedule scheduleRow, occurrence occurrenceRow, attempt attemptRow, run attemptRunRow, parsed cronDefinition, now time.Time) error {
	if !currentFence(schedule, occurrence) || attempt.RunID != run.ID || occurrence.AttemptCount != attempt.AttemptNo || !run.FinishedAt.Valid {
		return ErrConflict
	}
	nextFrom := occurrence.ScheduledFor
	if occurrence.ResolutionMode == "live_coalesced" {
		nextFrom = run.FinishedAt.Time
	}
	next := parsed.Next(nextFrom)
	if next.IsZero() {
		return fmt.Errorf("%w: cron has no next occurrence", ErrInvalidDefinition)
	}
	result, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status='resolved',retry_not_before=NULL,closed_at=?
		WHERE id=? AND status='unresolved' AND attempt_count=?`, now, occurrence.ID, attempt.AttemptNo)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE schedules SET next_run_at=?,scheduler_not_before=NULL,delivery_block_reason=NULL,
		delivery_blocked_at=NULL WHERE id=? AND enabled=TRUE AND next_run_at=? AND job_key=?
		AND cron_expression=? AND timezone=? AND policy_kind=?
		AND policy_version=? AND policy_checksum=?`, next, schedule.ID, occurrence.ScheduledFor, occurrence.JobKey,
		occurrence.CronExpression, occurrence.Timezone, occurrence.PolicyKind, occurrence.PolicyVersion, occurrence.PolicyChecksum)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) recordBackoff(ctx context.Context, tx *sqlx.Tx, schedule scheduleRow, occurrence occurrenceRow, attempt attemptRow, run attemptRunRow) error {
	if !currentFence(schedule, occurrence) || occurrence.AttemptCount != attempt.AttemptNo || attempt.RunID != run.ID {
		return ErrConflict
	}
	deadline := run.FinishedAt.Time.Add(retryDelay(attempt.AttemptNo))
	result, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET retry_not_before=?
		WHERE id=? AND status='unresolved' AND attempt_count=? AND retry_not_before IS NULL`, deadline, occurrence.ID, attempt.AttemptNo)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE schedules SET scheduler_not_before=? WHERE id=? AND enabled=TRUE AND next_run_at=?
		AND job_key=? AND cron_expression=? AND timezone=? AND policy_kind=? AND policy_version=? AND policy_checksum=?`, deadline, schedule.ID, occurrence.ScheduledFor,
		occurrence.JobKey, occurrence.CronExpression, occurrence.Timezone, occurrence.PolicyKind, occurrence.PolicyVersion, occurrence.PolicyChecksum)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) rejectInvalid(ctx context.Context, tx *sqlx.Tx, schedule scheduleRow, now time.Time, cause error) error {
	occurrence, found, err := lockActiveOccurrence(ctx, tx, schedule.ID)
	if err != nil {
		return err
	}
	if found {
		return service.rejectLockedOccurrence(ctx, tx, schedule, occurrence, now, cause)
	}
	message := safeMessage(cause)
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_occurrences
		(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,
		policy_kind,policy_version,policy_json,policy_checksum,rejection_class,rejection_message,rejection_revision,closed_at)
		VALUES (?,?,'persisted_cursor_fallback','invalid','rejected_invalid',?,?,?,?,?,?,?,?, 'invalid_definition',?,?,?)`,
		schedule.ID, schedule.NextRunAt.Time, schedule.Revision, schedule.JobKey, schedule.CronExpression, schedule.Timezone,
		schedule.PolicyKind, schedule.PolicyVersion, schedule.PolicyJSON, schedule.PolicyChecksum, message, schedule.Revision, now)
	if err != nil {
		return err
	}
	return service.disableInvalid(ctx, tx, schedule, now, message)
}

func (service *Service) rejectLockedOccurrence(ctx context.Context, tx *sqlx.Tx, schedule scheduleRow, occurrence occurrenceRow, now time.Time, cause error) error {
	message := safeMessage(cause)
	result, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status='rejected_invalid',resolution_mode='invalid',
		retry_not_before=NULL,rejection_class='invalid_definition',rejection_message=?,rejection_revision=?,closed_at=?
		WHERE id=? AND status='unresolved'`, message, schedule.Revision, now, occurrence.ID)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	return service.disableInvalid(ctx, tx, schedule, now, message)
}

func (service *Service) disableInvalid(ctx context.Context, tx *sqlx.Tx, schedule scheduleRow, now time.Time, message string) error {
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled=FALSE,next_run_at=NULL,scheduler_not_before=NULL,
		delivery_block_reason=NULL,delivery_blocked_at=NULL,validation_error_class='invalid_definition',validation_error_message=?,
		validation_error_at=?,revision=revision+1 WHERE id=? AND revision=? AND enabled=TRUE`, message, now, schedule.ID, schedule.Revision)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	return tx.Commit()
}

const scheduleSelect = `SELECT id,name,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,
	enabled,next_run_at,revision,scheduler_not_before,created_by_user_id,archived_at FROM schedules`

type scheduleRow struct {
	ID                 uint64        `db:"id"`
	Name               string        `db:"name"`
	JobKey             string        `db:"job_key"`
	CronExpression     string        `db:"cron_expression"`
	Timezone           string        `db:"timezone"`
	PolicyKind         string        `db:"policy_kind"`
	PolicyVersion      uint16        `db:"policy_version"`
	PolicyJSON         []byte        `db:"policy_json"`
	PolicyChecksum     []byte        `db:"policy_checksum"`
	Enabled            bool          `db:"enabled"`
	NextRunAt          sql.NullTime  `db:"next_run_at"`
	SchedulerNotBefore sql.NullTime  `db:"scheduler_not_before"`
	ArchivedAt         sql.NullTime  `db:"archived_at"`
	Revision           uint64        `db:"revision"`
	CreatedByUserID    sql.NullInt64 `db:"created_by_user_id"`
}

func (row scheduleRow) definition() Definition {
	var checksum [32]byte
	copy(checksum[:], row.PolicyChecksum)
	return Definition{Name: row.Name, JobKey: row.JobKey, CronExpression: row.CronExpression, Timezone: row.Timezone,
		Policy: Policy{Kind: row.PolicyKind, Version: row.PolicyVersion, Payload: append([]byte(nil), row.PolicyJSON...), Checksum: checksum}}
}

func (row scheduleRow) semanticMatches(definition Definition) bool {
	return row.JobKey == definition.JobKey && row.CronExpression == definition.CronExpression && row.Timezone == definition.Timezone &&
		row.PolicyKind == definition.Policy.Kind && row.PolicyVersion == definition.Policy.Version && bytesEqual(row.PolicyChecksum, definition.Policy.Checksum[:])
}

func (row scheduleRow) value() Schedule {
	value := Schedule{ID: row.ID, Definition: row.definition(), Enabled: row.Enabled, Revision: row.Revision}
	value.NextRunAt, value.SchedulerNotBefore, value.ArchivedAt = timePointer(row.NextRunAt), timePointer(row.SchedulerNotBefore), timePointer(row.ArchivedAt)
	return value
}

type occurrenceRow struct {
	ID               uint64       `db:"id"`
	ScheduleID       uint64       `db:"schedule_id"`
	ScheduledFor     time.Time    `db:"scheduled_for"`
	IdentitySource   string       `db:"identity_source"`
	ResolutionMode   string       `db:"resolution_mode"`
	Status           string       `db:"status"`
	ScheduleRevision uint64       `db:"schedule_revision"`
	JobKey           string       `db:"job_key"`
	CronExpression   string       `db:"cron_expression"`
	Timezone         string       `db:"timezone"`
	PolicyKind       string       `db:"policy_kind"`
	PolicyVersion    uint16       `db:"policy_version"`
	PolicyJSON       []byte       `db:"policy_json"`
	PolicyChecksum   []byte       `db:"policy_checksum"`
	AttemptCount     uint32       `db:"attempt_count"`
	RetryNotBefore   sql.NullTime `db:"retry_not_before"`
}

func (row occurrenceRow) semanticMatches(definition Definition) bool {
	return row.JobKey == definition.JobKey && row.CronExpression == definition.CronExpression && row.Timezone == definition.Timezone &&
		row.PolicyKind == definition.Policy.Kind && row.PolicyVersion == definition.Policy.Version && bytesEqual(row.PolicyChecksum, definition.Policy.Checksum[:])
}

type attemptRow struct {
	ID               uint64 `db:"id"`
	OccurrenceID     uint64 `db:"occurrence_id"`
	RunID            uint64 `db:"run_id"`
	AttemptNo        uint32 `db:"attempt_no"`
	TriggerReference string `db:"trigger_reference"`
}

type attemptRunRow struct {
	ID         uint64              `db:"id"`
	Status     ingestionrun.Status `db:"status"`
	FinishedAt sql.NullTime        `db:"finished_at"`
}

func lockSchedule(ctx context.Context, tx *sqlx.Tx, id uint64) (scheduleRow, error) {
	var row scheduleRow
	err := tx.GetContext(ctx, &row, scheduleSelect+` WHERE id=? FOR UPDATE`, id)
	return row, err
}

const occurrenceSelect = `SELECT id,schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,
	job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,attempt_count,retry_not_before
	FROM schedule_occurrences`

func lockActiveOccurrence(ctx context.Context, tx *sqlx.Tx, scheduleID uint64) (occurrenceRow, bool, error) {
	var row occurrenceRow
	err := tx.GetContext(ctx, &row, occurrenceSelect+` WHERE active_schedule_id=? FOR UPDATE`, scheduleID)
	if errors.Is(err, sql.ErrNoRows) {
		return occurrenceRow{}, false, nil
	}
	return row, err == nil, err
}

func createOccurrence(ctx context.Context, tx *sqlx.Tx, schedule scheduleRow, mode string) (occurrenceRow, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO schedule_occurrences
		(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,
		policy_kind,policy_version,policy_json,policy_checksum)
		VALUES (?,?,'validated_cron',?,'unresolved',?,?,?,?,?,?,?,?)`, schedule.ID, schedule.NextRunAt.Time, mode,
		schedule.Revision, schedule.JobKey, schedule.CronExpression, schedule.Timezone, schedule.PolicyKind, schedule.PolicyVersion,
		schedule.PolicyJSON, schedule.PolicyChecksum)
	if err != nil {
		return occurrenceRow{}, err
	}
	id, _ := result.LastInsertId()
	var row occurrenceRow
	if err := tx.GetContext(ctx, &row, occurrenceSelect+` WHERE id=? FOR UPDATE`, id); err != nil {
		return occurrenceRow{}, err
	}
	return row, nil
}

func lockLatestAttempt(ctx context.Context, tx *sqlx.Tx, occurrenceID uint64) (attemptRow, bool, error) {
	var row attemptRow
	err := tx.GetContext(ctx, &row, `SELECT id,occurrence_id,attempt_no,ingestion_run_id run_id,trigger_reference
		FROM schedule_attempts WHERE occurrence_id=? ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, occurrenceID)
	if errors.Is(err, sql.ErrNoRows) {
		return attemptRow{}, false, nil
	}
	return row, err == nil, err
}

func lockAttemptRun(ctx context.Context, tx *sqlx.Tx, runID uint64) (attemptRunRow, error) {
	var row attemptRunRow
	err := tx.GetContext(ctx, &row, `SELECT id,status,finished_at FROM ingestion_runs WHERE id=? FOR UPDATE`, runID)
	return row, err
}

func currentFence(schedule scheduleRow, occurrence occurrenceRow) bool {
	return schedule.Enabled && occurrence.Status == "unresolved" && schedule.NextRunAt.Valid && schedule.NextRunAt.Time.Equal(occurrence.ScheduledFor) &&
		schedule.JobKey == occurrence.JobKey && schedule.CronExpression == occurrence.CronExpression && schedule.Timezone == occurrence.Timezone &&
		schedule.PolicyKind == occurrence.PolicyKind && schedule.PolicyVersion == occurrence.PolicyVersion && bytesEqual(schedule.PolicyChecksum, occurrence.PolicyChecksum)
}

func dbNow(ctx context.Context, query sqlx.QueryerContext) (time.Time, error) {
	var now time.Time
	err := sqlx.GetContext(ctx, query, &now, `SELECT UTC_TIMESTAMP(6)`)
	return now.UTC(), err
}

func requireOne(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

func nullableTime(value sql.NullTime) any {
	if value.Valid {
		return value.Time
	}
	return nil
}

func nullableUint64(value sql.NullInt64) *uint64 {
	if !value.Valid {
		return nil
	}
	result := uint64(value.Int64)
	return &result
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func safeMessage(err error) string {
	message := "invalid persisted schedule definition"
	if err != nil {
		message = strings.Map(func(value rune) rune {
			if value < 0x20 || value == 0x7f {
				return ' '
			}
			return value
		}, err.Error())
	}
	runes := []rune(message)
	if len(runes) > 500 {
		message = string(runes[:500])
	}
	return message
}
