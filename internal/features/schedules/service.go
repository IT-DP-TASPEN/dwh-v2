package schedules

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	domain "github.com/ibldzn/go-admin/internal/scheduler"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

const scheduleSelect = `SELECT s.id,s.name,s.job_key,s.cron_expression,s.timezone,s.policy_kind,s.enabled,s.next_run_at,s.revision,
	s.scheduler_not_before,s.delivery_block_reason,s.validation_error_class,s.validation_error_message,s.archived_at,s.created_at,
	TIMESTAMPDIFF(SECOND,s.next_run_at,UTC_TIMESTAMP(6)) backlog_seconds,COALESCE(s.next_run_at<=UTC_TIMESTAMP(6),FALSE) overdue,
	o.id occurrence_id,o.resolution_mode,o.status occurrence_status,o.scheduled_for,o.attempt_count,o.retry_not_before,
	a.attempt_no latest_attempt_no,a.ingestion_run_id latest_run_id,r.status latest_run_status
	FROM schedules s
	LEFT JOIN schedule_occurrences o ON o.active_schedule_id=s.id
	LEFT JOIN schedule_attempts a ON a.id=(SELECT a2.id FROM schedule_attempts a2 WHERE a2.occurrence_id=o.id ORDER BY a2.attempt_no DESC LIMIT 1)
	LEFT JOIN ingestion_runs r ON r.id=a.ingestion_run_id`

type Service struct {
	db      *sqlx.DB
	domain  *domain.Service
	catalog core.Catalog
}

func NewService(db *sqlx.DB, scheduler *domain.Service) (*Service, error) {
	catalog, err := core.NewCatalog()
	if err != nil {
		return nil, err
	}
	return &Service{db: db, domain: scheduler, catalog: catalog}, nil
}

func (service *Service) Jobs() []core.JobDefinition { return service.catalog.Jobs() }

func (service *Service) List(ctx context.Context, filter Filter, page int) (ListData, error) {
	if err := service.validateFilter(filter); err != nil {
		return ListData{}, err
	}
	where, args := filterSQL(filter)
	var total int64
	if err := service.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM schedules s `+where, args...); err != nil {
		return ListData{}, err
	}
	info := pagination.New(page, PageSize, total)
	queryArgs := append(append([]any{}, args...), PageSize, info.Offset())
	var rows []scheduleRow
	if err := service.db.SelectContext(ctx, &rows, scheduleSelect+` `+where+` ORDER BY s.id DESC LIMIT ? OFFSET ?`, queryArgs...); err != nil {
		return ListData{}, err
	}
	result := ListData{Rows: service.schedules(rows), Filter: filter, Pagination: info, Jobs: service.catalog.Jobs()}
	result.PreviousURL, result.NextURL = listURL(filter, info.Previous), listURL(filter, info.Next)
	result.ReturnQuery = strings.TrimPrefix(listURL(filter, info.Page), "/schedules")
	return result, nil
}

func (service *Service) Find(ctx context.Context, id uint64) (Schedule, error) {
	var row scheduleRow
	if err := service.db.GetContext(ctx, &row, scheduleSelect+` WHERE s.id=?`, id); err != nil {
		return Schedule{}, err
	}
	return service.schedule(row), nil
}

func (service *Service) Occurrences(ctx context.Context, scheduleID uint64) ([]Occurrence, error) {
	var rows []occurrenceRow
	if err := service.db.SelectContext(ctx, &rows, `SELECT id,schedule_id,scheduled_for,identity_source,resolution_mode,status,attempt_count,retry_not_before,closed_at
		FROM schedule_occurrences WHERE schedule_id=? ORDER BY scheduled_for DESC LIMIT 25`, scheduleID); err != nil {
		return nil, err
	}
	result := make([]Occurrence, len(rows))
	for i, row := range rows {
		result[i] = occurrence(row)
	}
	return result, nil
}

func (service *Service) FindOccurrence(ctx context.Context, scheduleID, occurrenceID uint64) (Occurrence, []Attempt, error) {
	var row occurrenceRow
	if err := service.db.GetContext(ctx, &row, `SELECT id,schedule_id,scheduled_for,identity_source,resolution_mode,status,attempt_count,retry_not_before,closed_at
		FROM schedule_occurrences WHERE id=? AND schedule_id=?`, occurrenceID, scheduleID); err != nil {
		return Occurrence{}, nil, err
	}
	var rows []attemptRow
	if err := service.db.SelectContext(ctx, &rows, `SELECT a.attempt_no,a.ingestion_run_id run_id,r.status run_status,a.trigger_reference,a.submitted_at,r.finished_at
		FROM schedule_attempts a JOIN ingestion_runs r ON r.id=a.ingestion_run_id WHERE a.occurrence_id=? ORDER BY a.attempt_no`, occurrenceID); err != nil {
		return Occurrence{}, nil, err
	}
	attempts := make([]Attempt, len(rows))
	for i, value := range rows {
		attempts[i] = Attempt{value.AttemptNo, value.RunID, value.RunStatus, value.TriggerReference, formatTime(value.SubmittedAt), formatNullTime(value.FinishedAt)}
	}
	return occurrence(row), attempts, nil
}

func (service *Service) Create(ctx context.Context, form FormData, actor uint64, requesters ...securityctx.Requester) (domain.Schedule, error) {
	return service.domain.Create(ctx, domain.CreateInput{Definition: service.definition(form), Enabled: form.Enabled, ActorID: &actor, Requester: requester(requesters)})
}

func (service *Service) CreateMany(ctx context.Context, form BulkFormData, actor uint64, requesters ...securityctx.Requester) (BulkResultData, error) {
	cronExpression, timezone := strings.TrimSpace(form.CronExpression), strings.TrimSpace(form.Timezone)
	if timezone == "" {
		timezone = domain.DefaultTimezone
	}
	inputs := make([]domain.CreateInput, len(form.JobKeys))
	for index, jobKey := range form.JobKeys {
		job, found := service.catalog.Find(jobKey)
		if !found {
			return BulkResultData{}, fmt.Errorf("%w: unknown job %q", domain.ErrInvalidDefinition, jobKey)
		}
		inputs[index] = domain.CreateInput{Definition: service.definition(FormData{
			Name: job.Name, JobKey: job.Key, CronExpression: cronExpression, Timezone: timezone,
		}), Enabled: form.Enabled, ActorID: &actor, Requester: requester(requesters)}
	}
	created, err := service.domain.CreateMany(ctx, inputs)
	if err != nil {
		return BulkResultData{}, err
	}
	result := BulkResultData{Selected: len(form.JobKeys), Created: len(created.Created), CronExpression: cronExpression,
		Timezone: timezone, Enabled: form.Enabled, CreatedSchedules: make([]BulkSchedule, 0, len(created.Created))}
	for _, schedule := range created.Created {
		job, _ := service.catalog.Find(schedule.Definition.JobKey)
		result.CreatedSchedules = append(result.CreatedSchedules, BulkSchedule{ID: schedule.ID, JobName: job.Name, Enabled: schedule.Enabled})
	}
	existing := make(map[string][]BulkSchedule)
	for _, schedule := range created.Existing {
		job, _ := service.catalog.Find(schedule.Definition.JobKey)
		existing[schedule.Definition.JobKey] = append(existing[schedule.Definition.JobKey], BulkSchedule{
			ID: schedule.ID, JobName: job.Name, Enabled: schedule.Enabled,
		})
	}
	for _, jobKey := range form.JobKeys {
		matches := existing[jobKey]
		if len(matches) == 0 {
			continue
		}
		job, _ := service.catalog.Find(jobKey)
		result.SkippedJobs = append(result.SkippedJobs, BulkSkippedJob{JobName: job.Name, Existing: matches})
	}
	result.Skipped = len(result.SkippedJobs)
	return result, nil
}

func (service *Service) Update(ctx context.Context, id uint64, form FormData, actor uint64, requesters ...securityctx.Requester) (domain.Schedule, error) {
	return service.domain.Update(ctx, id, domain.UpdateInput{Definition: service.definition(form), ExpectedRevision: form.ExpectedRevision, ActorID: &actor, Requester: requester(requesters)})
}

func (service *Service) Enable(ctx context.Context, id, revision, actor uint64, requesters ...securityctx.Requester) (domain.Schedule, error) {
	return service.domain.Enable(ctx, id, revision, &actor, requester(requesters))
}
func (service *Service) Disable(ctx context.Context, id, revision, actor uint64, requesters ...securityctx.Requester) (domain.Schedule, error) {
	return service.domain.Disable(ctx, id, revision, &actor, requester(requesters))
}
func (service *Service) Archive(ctx context.Context, id, revision, actor uint64, requesters ...securityctx.Requester) (domain.Schedule, error) {
	return service.domain.Archive(ctx, id, revision, &actor, requester(requesters))
}

func (service *Service) BulkState(ctx context.Context, ids []uint64, action domain.BulkAction, actor uint64, requesters ...securityctx.Requester) (domain.BulkStateResult, error) {
	return service.domain.BulkState(ctx, ids, action, &actor, requester(requesters))
}

func requester(values []securityctx.Requester) *securityctx.Requester {
	if len(values) == 0 {
		return nil
	}
	return &values[0]
}

func (service *Service) definition(form FormData) domain.Definition {
	job, _ := service.catalog.Find(form.JobKey)
	policy := domain.PreviousCalendarDayPolicy()
	if job.DateStrategy == core.NoDate {
		policy = domain.LiveSnapshotPolicy()
	}
	return domain.Definition{Name: form.Name, JobKey: form.JobKey, CronExpression: form.CronExpression, Timezone: form.Timezone, Policy: policy}
}

func (service *Service) schedules(rows []scheduleRow) []Schedule {
	result := make([]Schedule, len(rows))
	for i, row := range rows {
		result[i] = service.schedule(row)
	}
	return result
}
func (service *Service) schedule(row scheduleRow) Schedule {
	job, _ := service.catalog.Find(row.JobKey)
	value := Schedule{ID: row.ID, Name: row.Name, JobKey: row.JobKey, JobName: job.Name, CronExpression: row.CronExpression, Timezone: row.Timezone,
		PolicyKind: row.PolicyKind, Enabled: row.Enabled, Archived: row.ArchivedAt.Valid, Revision: row.Revision, NextRunAt: formatNullTime(row.NextRunAt),
		SchedulerNotBefore: formatNullTime(row.SchedulerNotBefore), DeliveryBlockReason: row.DeliveryBlockReason.String,
		ValidationError: strings.TrimSpace(row.ValidationErrorClass.String + " " + row.ValidationErrorMessage.String), ArchivedAt: formatNullTime(row.ArchivedAt), CreatedAt: formatTime(row.CreatedAt),
		OccurrenceMode: row.ResolutionMode.String, OccurrenceStatus: row.OccurrenceStatus.String, OccurrenceScheduledFor: formatNullTime(row.ScheduledFor),
		AttemptCount: uint32(row.AttemptCount.Int64), RetryNotBefore: formatNullTime(row.RetryNotBefore), LatestAttemptNo: uint32(row.LatestAttemptNo.Int64), LatestRunStatus: row.LatestRunStatus.String}
	if row.OccurrenceID.Valid {
		id := uint64(row.OccurrenceID.Int64)
		value.OccurrenceID = &id
	}
	if row.LatestRunID.Valid {
		id := uint64(row.LatestRunID.Int64)
		value.LatestRunID = &id
	}
	if row.NextRunAt.Valid {
		if location, err := time.LoadLocation(row.Timezone); err == nil {
			value.NextRunLocal = row.NextRunAt.Time.In(location).Format("02 Jan 2006 15:04:05 MST")
		}
	}
	if row.BacklogSeconds.Valid && row.BacklogSeconds.Int64 > 0 {
		value.BacklogAge = compactDuration(time.Duration(row.BacklogSeconds.Int64) * time.Second)
	}
	value.StateLabel = scheduleState(value, row.Overdue)
	return value
}

func occurrence(row occurrenceRow) Occurrence {
	return Occurrence{row.ID, row.ScheduleID, formatTime(row.ScheduledFor), row.IdentitySource, row.ResolutionMode, row.Status, row.AttemptCount, formatNullTime(row.RetryNotBefore), formatNullTime(row.ClosedAt)}
}

func (service *Service) validateFilter(filter Filter) error {
	if filter.Job != "" {
		if _, found := service.catalog.Find(filter.Job); !found {
			return fmt.Errorf("invalid job filter")
		}
	}
	for label, value := range map[string]string{"enabled": filter.Enabled, "archived": filter.Archived} {
		if value != "" && value != "true" && value != "false" {
			return fmt.Errorf("invalid %s filter", label)
		}
	}
	return nil
}

func filterSQL(filter Filter) (string, []any) {
	clauses, args := []string{"1=1"}, []any{}
	if filter.Job != "" {
		clauses, args = append(clauses, "s.job_key=?"), append(args, filter.Job)
	}
	if filter.Enabled != "" {
		clauses, args = append(clauses, "s.enabled=?"), append(args, filter.Enabled == "true")
	}
	if filter.Archived == "true" {
		clauses = append(clauses, "s.archived_at IS NOT NULL")
	} else if filter.Archived == "false" {
		clauses = append(clauses, "s.archived_at IS NULL")
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func listURL(filter Filter, page int) string {
	if page == 0 {
		return ""
	}
	values := url.Values{"page": {strconv.Itoa(page)}}
	if filter.Job != "" {
		values.Set("job", filter.Job)
	}
	if filter.Enabled != "" {
		values.Set("enabled", filter.Enabled)
	}
	if filter.Archived != "" {
		values.Set("archived", filter.Archived)
	}
	return "/schedules?" + values.Encode()
}
func formatTime(value time.Time) string { return value.UTC().Format("02 Jan 2006 15:04:05 UTC") }
func formatNullTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return formatTime(value.Time)
}

func compactDuration(value time.Duration) string {
	if value >= 24*time.Hour {
		return fmt.Sprintf("%dd %dh", int(value/(24*time.Hour)), int(value%(24*time.Hour)/time.Hour))
	}
	if value >= time.Hour {
		return fmt.Sprintf("%dh %dm", int(value/time.Hour), int(value%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dm", max(int(value/time.Minute), 0))
}

func scheduleState(value Schedule, overdue bool) string {
	if value.Archived {
		return "Archived"
	}
	if !value.Enabled {
		return "Disabled"
	}
	if value.DeliveryBlockReason == "source_disabled" {
		return "Blocked — source disabled"
	}
	if value.DeliveryBlockReason == "source_configuration_required" {
		return "Blocked — source authentication required"
	}
	if value.DeliveryBlockReason == "job_busy" {
		return "Blocked — job busy"
	}
	if value.RetryNotBefore != "" {
		return "Retrying"
	}
	if value.OccurrenceMode == "live_coalesced" {
		return "Live catch-up"
	}
	if overdue && value.OccurrenceID != nil {
		return "Backlog"
	}
	if overdue {
		return "Due"
	}
	return "Enabled"
}
