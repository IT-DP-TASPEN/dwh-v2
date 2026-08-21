package ingestion

import (
	"database/sql"
	"strings"
	"time"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
)

const RunPageSize = 50

type RunFilter struct {
	Job, Status, Kind, Trigger string
}

type runRow struct {
	ID                  uint64         `db:"id"`
	Kind                string         `db:"kind"`
	ParentRunID         sql.NullInt64  `db:"parent_run_id"`
	ChildPosition       sql.NullInt64  `db:"child_position"`
	JobKey              sql.NullString `db:"job_key"`
	Status              string         `db:"status"`
	ParameterKind       string         `db:"parameter_kind"`
	ParameterVersion    uint16         `db:"parameter_version"`
	ParametersJSON      []byte         `db:"parameters_json"`
	ParameterChecksum   []byte         `db:"parameter_checksum"`
	TriggerType         string         `db:"trigger_type"`
	TriggerReference    sql.NullString `db:"trigger_reference"`
	RequestedByUserID   sql.NullInt64  `db:"requested_by_user_id"`
	RequestedByUsername sql.NullString `db:"requested_by_username"`
	SkipReason          sql.NullString `db:"skip_reason"`
	CancelRequestedAt   sql.NullTime   `db:"cancel_requested_at"`
	CancelReason        sql.NullString `db:"cancel_reason"`
	OwnerID             sql.NullString `db:"owner_id"`
	HeartbeatAt         sql.NullTime   `db:"heartbeat_at"`
	SnapshotDate        sql.NullTime   `db:"snapshot_date"`
	ProgressTotal       uint64         `db:"progress_total"`
	ProgressStarted     uint64         `db:"progress_started"`
	ProgressSucceeded   uint64         `db:"progress_succeeded"`
	ProgressFailed      uint64         `db:"progress_failed"`
	RowsProcessed       uint64         `db:"rows_processed"`
	CurrentStep         sql.NullString `db:"current_step"`
	ErrorClass          sql.NullString `db:"error_class"`
	ErrorMessage        sql.NullString `db:"error_message"`
	ErrorStep           sql.NullString `db:"error_step"`
	MapperDiagnostics   []byte         `db:"mapper_diagnostics"`
	CreatedAt           time.Time      `db:"created_at"`
	StartedAt           sql.NullTime   `db:"started_at"`
	FinishedAt          sql.NullTime   `db:"finished_at"`
}

type StatusView struct {
	Key, Label, Class, Description string
}

func PresentStatus(status string) StatusView {
	view := StatusView{Key: status, Label: status, Class: "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300"}
	switch status {
	case "planned":
		view.Label = "Planned"
	case "queued":
		view.Label, view.Class = "Queued", "bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300"
	case "running":
		view.Label, view.Class = "Running", "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300"
	case "succeeded", "completed":
		view.Label, view.Class = title(status), "bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300"
	case "completed_with_skips":
		view.Label, view.Class = "Completed with skips", "bg-yellow-100 text-yellow-800 dark:bg-yellow-950 dark:text-yellow-300"
	case "failed":
		view.Label, view.Class = "Failed", "bg-red-100 text-red-700 dark:bg-red-950 dark:text-red-300"
	case "skipped":
		view.Label, view.Class = "Skipped", "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300"
	case "cancelled":
		view.Label, view.Class = "Cancelled", "bg-orange-100 text-orange-700 dark:bg-orange-950 dark:text-orange-300"
	case "abandoned":
		view.Label, view.Class, view.Description = "Abandoned", "bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300", "Worker ownership was lost; final business outcome could not be proven."
	}
	return view
}

type ParameterField struct{ Label, Value string }

type RunView struct {
	ID                                           uint64
	Kind, KindLabel, JobKey, JobName, Trigger    string
	ParentRunID                                  *uint64
	ChildPosition                                uint16
	Status                                       StatusView
	Parameters                                   []ParameterField
	TriggerReference, RequestedBy                string
	SkipReason, CancelReason                     string
	CancelRequested                              bool
	OwnerID                                      string
	HeartbeatAt, HeartbeatEvidence, SnapshotDate string
	ProgressTotal, ProgressStarted               uint64
	ProgressSucceeded, ProgressFailed, Rows      uint64
	ProgressUnit, CurrentStep                    string
	ErrorClass, ErrorMessage, ErrorStep          string
	CreatedAt, StartedAt, FinishedAt             string
	Terminal, CanCancel, CanRecover              bool
}

type RunPage struct {
	Rows                      []RunView
	Filter                    RunFilter
	Pagination                pagination.Page
	PreviousURL, NextURL      string
	Jobs                      []core.JobDefinition
	Statuses, Kinds, Triggers []string
}

type SchedulerProvenance struct {
	ScheduleID, OccurrenceID uint64
	ScheduleName             string
	ScheduledFor             time.Time
	ResolutionMode           string
	OccurrenceStatus         string
	AttemptNo                uint32
	RetryNotBefore           *time.Time
}

type RunDetail struct {
	Run                   RunView
	Children              []RunView
	Scheduler             *SchedulerProvenance
	TechnicalErrors       []TechnicalEventView
	MapperDiagnostics     *ingestionrun.MapperDiagnostics
	CanCancel, CanRecover bool
	Polling               bool
}

type TechnicalEventView struct {
	ID, AffectedItems, OmittedExamples uint64
	OccurredAt, LastOccurredAt         string
	Severity, EventKind, RecoveryState string
	Terminal                           bool
	Recovered                          *bool
	Class, Step, Operation, JobKey     string
	ItemIdentifier, MemberKey          string
	Attempt                            uint16
	ErrorType, ErrorMessage            string
	AggregationScope                   string
	CapturedExamples                   int
	Samples                            []ingestionrun.TechnicalSample
	Details, Body, BodyEncoding        string
}

type RunOverview struct {
	Queued, Running uint64
	RecentProblems  []RunView
	RecentSuccesses []RunView
}

type SourceOverview struct {
	Enabled  uint64 `db:"enabled"`
	Disabled uint64 `db:"disabled"`
}
type ScheduleOverview struct {
	Overdue         uint64 `db:"overdue"`
	Retrying        uint64 `db:"retrying"`
	BlockedBusy     uint64 `db:"blocked_busy"`
	BlockedDisabled uint64 `db:"blocked_disabled"`
}

type OverviewData struct {
	Runs      *RunOverview
	Sources   *SourceOverview
	Schedules *ScheduleOverview
	CanRunAll bool
}

type RunAllForm struct {
	From, To string
	Errors   map[string]string
}

func (row runRow) parameters() ingestionrun.Parameters {
	var checksum [32]byte
	copy(checksum[:], row.ParameterChecksum)
	return ingestionrun.Parameters{Kind: ingestionrun.ParameterKind(row.ParameterKind), Version: row.ParameterVersion, JSON: append([]byte(nil), row.ParametersJSON...), Checksum: checksum}
}

func title(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
