package schedules

import (
	"database/sql"
	"time"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	domain "github.com/ibldzn/go-admin/internal/scheduler"
)

const PageSize = domain.MaxBulkSelection

type Filter struct{ Job, Enabled, Archived string }

type Schedule struct {
	ID                                                           uint64
	Name, JobKey, JobName, CronExpression, Timezone              string
	PolicyKind, DeliveryBlockReason, ValidationError, StateLabel string
	Enabled, Archived                                            bool
	Revision                                                     uint64
	NextRunAt, NextRunLocal, BacklogAge                          string
	SchedulerNotBefore, ArchivedAt, CreatedAt                    string
	OccurrenceID                                                 *uint64
	OccurrenceMode, OccurrenceStatus, OccurrenceScheduledFor     string
	AttemptCount                                                 uint32
	RetryNotBefore                                               string
	LatestAttemptNo                                              uint32
	LatestRunID                                                  *uint64
	LatestRunStatus                                              string
}

type scheduleRow struct {
	ID                     uint64         `db:"id"`
	Name                   string         `db:"name"`
	JobKey                 string         `db:"job_key"`
	CronExpression         string         `db:"cron_expression"`
	Timezone               string         `db:"timezone"`
	PolicyKind             string         `db:"policy_kind"`
	Enabled                bool           `db:"enabled"`
	NextRunAt              sql.NullTime   `db:"next_run_at"`
	Revision               uint64         `db:"revision"`
	SchedulerNotBefore     sql.NullTime   `db:"scheduler_not_before"`
	DeliveryBlockReason    sql.NullString `db:"delivery_block_reason"`
	ValidationErrorClass   sql.NullString `db:"validation_error_class"`
	ValidationErrorMessage sql.NullString `db:"validation_error_message"`
	ArchivedAt             sql.NullTime   `db:"archived_at"`
	CreatedAt              time.Time      `db:"created_at"`
	BacklogSeconds         sql.NullInt64  `db:"backlog_seconds"`
	Overdue                bool           `db:"overdue"`
	OccurrenceID           sql.NullInt64  `db:"occurrence_id"`
	ResolutionMode         sql.NullString `db:"resolution_mode"`
	OccurrenceStatus       sql.NullString `db:"occurrence_status"`
	ScheduledFor           sql.NullTime   `db:"scheduled_for"`
	AttemptCount           sql.NullInt64  `db:"attempt_count"`
	RetryNotBefore         sql.NullTime   `db:"retry_not_before"`
	LatestAttemptNo        sql.NullInt64  `db:"latest_attempt_no"`
	LatestRunID            sql.NullInt64  `db:"latest_run_id"`
	LatestRunStatus        sql.NullString `db:"latest_run_status"`
}

type Occurrence struct {
	ID, ScheduleID                                       uint64
	ScheduledFor, IdentitySource, ResolutionMode, Status string
	AttemptCount                                         uint32
	RetryNotBefore, ClosedAt                             string
}

type occurrenceRow struct {
	ID             uint64       `db:"id"`
	ScheduleID     uint64       `db:"schedule_id"`
	ScheduledFor   time.Time    `db:"scheduled_for"`
	IdentitySource string       `db:"identity_source"`
	ResolutionMode string       `db:"resolution_mode"`
	Status         string       `db:"status"`
	AttemptCount   uint32       `db:"attempt_count"`
	RetryNotBefore sql.NullTime `db:"retry_not_before"`
	ClosedAt       sql.NullTime `db:"closed_at"`
}

type Attempt struct {
	AttemptNo                                            uint32
	RunID                                                uint64
	RunStatus, TriggerReference, SubmittedAt, FinishedAt string
}

type attemptRow struct {
	AttemptNo        uint32       `db:"attempt_no"`
	RunID            uint64       `db:"run_id"`
	RunStatus        string       `db:"run_status"`
	TriggerReference string       `db:"trigger_reference"`
	SubmittedAt      time.Time    `db:"submitted_at"`
	FinishedAt       sql.NullTime `db:"finished_at"`
}

type ListData struct {
	Rows                                               []Schedule
	Filter                                             Filter
	Pagination                                         pagination.Page
	PreviousURL, NextURL                               string
	ReturnQuery                                        string
	Jobs                                               []core.JobDefinition
	CanCreate, CanEnableDisable, CanArchive, CanSelect bool
	SelectableCount                                    int
}

type DetailData struct {
	Schedule                                Schedule
	Occurrences                             []Occurrence
	CanUpdate, CanEnableDisable, CanArchive bool
}

type OccurrenceData struct {
	Schedule   Schedule
	Occurrence Occurrence
	Attempts   []Attempt
}

type FormData struct {
	ID, ExpectedRevision                   uint64
	Name, JobKey, CronExpression, Timezone string
	Enabled                                bool
	Jobs                                   []core.JobDefinition
	Errors                                 map[string]string
}

type BulkFormData struct {
	JobKeys                  []string
	SelectedJobs             map[string]bool
	CronExpression, Timezone string
	Enabled                  bool
	Jobs                     []core.JobDefinition
	Errors                   map[string]string
	Result                   *BulkResultData
}

type BulkResultData struct {
	Selected, Created, Skipped int
	CronExpression, Timezone   string
	Enabled                    bool
	CreatedSchedules           []BulkSchedule
	SkippedJobs                []BulkSkippedJob
}

type BulkSchedule struct {
	ID      uint64
	JobName string
	Enabled bool
}

type BulkSkippedJob struct {
	JobName  string
	Existing []BulkSchedule
}
