package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
)

type Service struct {
	repository *Repository
	runs       *ingestionrun.Repository
	catalog    core.Catalog
}

func NewService(repository *Repository) (*Service, error) {
	catalog, err := core.NewCatalog()
	if err != nil {
		return nil, err
	}
	runs, err := ingestionrun.NewRepository(repository.db, catalog)
	if err != nil {
		return nil, err
	}
	return &Service{repository: repository, runs: runs, catalog: catalog}, nil
}

func (service *Service) ListRuns(ctx context.Context, filter RunFilter, page int) (RunPage, error) {
	if err := service.validateFilter(filter); err != nil {
		return RunPage{}, err
	}
	grouped := filter.Kind == "" && filter.Trigger == ""
	offset := max(page-1, 0) * RunPageSize
	var total int64
	var items []RunListItem
	var err error
	if grouped {
		entities, entityTotal, queryErr := service.repository.ListRunEntities(ctx, filter, RunPageSize, offset)
		total, err = entityTotal, queryErr
		if err == nil {
			items, err = service.runListItems(ctx, entities)
		}
	} else {
		var rows []runRow
		rows, total, err = service.repository.ListRuns(ctx, filter, RunPageSize, offset)
		if err == nil {
			items, err = service.physicalRunItems(ctx, rows)
		}
	}
	if err != nil {
		return RunPage{}, err
	}
	pageInfo := pagination.New(page, RunPageSize, total)
	if pageInfo.Offset() != offset {
		return service.ListRuns(ctx, filter, pageInfo.Page)
	}
	result := RunPage{Rows: items, Filter: filter, Pagination: pageInfo, Jobs: service.catalog.Jobs(),
		Statuses: []string{"planned", "queued", "running", "succeeded", "failed", "skipped", "cancelled", "abandoned", "completed", "completed_with_skips"},
		Kinds:    append([]RunKindOption(nil), runKindOptions...),
		Triggers: []string{"direct", "scheduler", "run_all"}}
	result.PreviousURL, result.NextURL = runPageURL(filter, pageInfo.Previous), runPageURL(filter, pageInfo.Next)
	return result, nil
}

func (service *Service) physicalRunItems(ctx context.Context, rows []runRow) ([]RunListItem, error) {
	views := service.views(rows)
	parentIDs := make([]uint64, 0, len(views))
	for _, view := range views {
		if view.Kind == string(ingestionrun.KindRunAllParent) {
			parentIDs = append(parentIDs, view.ID)
		}
	}
	summaries, err := service.repository.runAllSummaries(ctx, parentIDs)
	if err != nil {
		return nil, err
	}
	items := make([]RunListItem, len(views))
	for index := range views {
		if views[index].Kind == string(ingestionrun.KindRunAllParent) {
			summary := summaries[views[index].ID]
			views[index].RunAllSummary = &summary
		}
		items[index].RunView = views[index]
	}
	return items, nil
}

func (service *Service) runListItems(ctx context.Context, entities []runListEntityRow) ([]RunListItem, error) {
	runIDs := make([]uint64, 0, len(entities))
	waveTimes := make([]time.Time, 0, len(entities))
	for _, entity := range entities {
		if entity.EntityKind == "run" {
			runIDs = append(runIDs, entity.RunID)
		} else if entity.EntityKind == "scheduler_wave" && entity.ScheduledFor.Valid {
			waveTimes = append(waveTimes, entity.ScheduledFor.Time.UTC())
		} else {
			return nil, fmt.Errorf("invalid run list entity %q", entity.EntityKind)
		}
	}
	runs, err := service.repository.runsByIDs(ctx, runIDs)
	if err != nil {
		return nil, err
	}
	runItems, err := service.physicalRunItems(ctx, runs)
	if err != nil {
		return nil, err
	}
	runsByID := make(map[uint64]RunView, len(runItems))
	for _, item := range runItems {
		runsByID[item.ID] = item.RunView
	}
	summaries, err := service.repository.schedulerWaveSummaries(ctx, waveTimes)
	if err != nil {
		return nil, err
	}
	summariesByTime := make(map[int64]schedulerWaveSummaryRow, len(summaries))
	for _, summary := range summaries {
		summariesByTime[summary.ScheduledFor.UnixMicro()] = summary
	}
	items := make([]RunListItem, 0, len(entities))
	for _, entity := range entities {
		if entity.EntityKind == "run" {
			id := entity.RunID
			view, found := runsByID[id]
			if !found {
				return nil, fmt.Errorf("hydrate run list entity %d: missing run", id)
			}
			items = append(items, RunListItem{RunView: view})
			continue
		}
		scheduledFor := entity.ScheduledFor.Time.UTC()
		summary, found := summariesByTime[scheduledFor.UnixMicro()]
		if !found {
			return nil, fmt.Errorf("hydrate scheduler wave %s: missing summary", scheduledFor.Format(time.RFC3339Nano))
		}
		items = append(items, RunListItem{SchedulerWave: schedulerWaveView(scheduledFor, entity.ActivityAt, summary)})
	}
	return items, nil
}

func schedulerWaveView(scheduledFor, activityAt time.Time, summary schedulerWaveSummaryRow) *SchedulerWaveView {
	key := scheduledFor.UTC().Format(time.RFC3339Nano)
	values := url.Values{"scheduled_for": {key}}
	view := &SchedulerWaveView{ScheduledFor: scheduledFor.UTC(), ScheduledForLabel: formatTime(scheduledFor), ScheduledForKey: key,
		URL: "/runs/scheduler-wave?" + values.Encode(), DOMID: strconv.FormatInt(scheduledFor.UnixMicro(), 10), ActivityAt: formatTime(activityAt),
		Total: summary.Total, Resolved: summary.Resolved, Unresolved: summary.Unresolved, Discarded: summary.Discarded,
		Rejected: summary.Rejected, Attempts: summary.Attempts}
	view.Summary = schedulerWaveSummary(view)
	return view
}

func schedulerWaveSummary(wave *SchedulerWaveView) string {
	parts := []string{plural(wave.Total, "occurrence")}
	for _, value := range []struct {
		count uint64
		label string
	}{{wave.Resolved, "resolved"}, {wave.Unresolved, "unresolved"}, {wave.Discarded, "discarded"}, {wave.Rejected, "rejected invalid"}} {
		if value.count != 0 {
			parts = append(parts, fmt.Sprintf("%d %s", value.count, value.label))
		}
	}
	parts = append(parts, plural(wave.Attempts, "attempt"))
	return strings.Join(parts, " · ")
}

func plural(count uint64, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func (service *Service) SchedulerWave(ctx context.Context, scheduledFor time.Time) (SchedulerWaveDetail, error) {
	rows, err := service.repository.SchedulerWave(ctx, scheduledFor)
	if err != nil {
		return SchedulerWaveDetail{}, err
	}
	detail := SchedulerWaveDetail{ScheduledFor: formatTime(scheduledFor)}
	for _, row := range rows {
		if len(detail.Occurrences) == 0 || detail.Occurrences[len(detail.Occurrences)-1].OccurrenceID != row.OccurrenceID {
			job, _ := service.catalog.Find(row.JobKey)
			detail.Occurrences = append(detail.Occurrences, SchedulerOccurrenceView{ScheduleID: row.ScheduleID, OccurrenceID: row.OccurrenceID,
				ScheduleName: row.ScheduleName, JobName: job.Name, Status: presentOccurrenceStatus(row.OccurrenceStatus)})
		}
		if !row.RunID.Valid {
			continue
		}
		jobKey := row.RunJobKey.String
		if jobKey == "" {
			jobKey = row.JobKey
		}
		job, _ := service.catalog.Find(jobKey)
		occurrence := &detail.Occurrences[len(detail.Occurrences)-1]
		occurrence.Attempts = append(occurrence.Attempts, SchedulerAttemptView{RunID: uint64(row.RunID.Int64), AttemptNo: uint32(row.AttemptNo.Int64),
			JobName: job.Name, Status: PresentStatus(row.RunStatus.String), CreatedAt: formatTime(row.RunCreatedAt.Time)})
	}
	return detail, nil
}

func (service *Service) RunAllChildren(ctx context.Context, parentID uint64) (RunChildren, error) {
	rows, err := service.repository.RunAllChildren(ctx, parentID)
	if err != nil {
		return RunChildren{}, err
	}
	return RunChildren{ParentID: parentID, Rows: service.views(rows)}, nil
}

func (service *Service) FindRun(ctx context.Context, id uint64) (RunDetail, error) {
	row, err := service.repository.FindRun(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	detail := RunDetail{Run: service.view(row)}
	detail.MapperDiagnostics, err = ingestionrun.DecodeMapperDiagnostics(row.MapperDiagnostics)
	if err != nil {
		return RunDetail{}, err
	}
	events, err := service.runs.TechnicalEvents(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	detail.TechnicalErrors = technicalEventViews(events)
	if row.Kind == string(ingestionrun.KindRunAllParent) {
		children, err := service.repository.Children(ctx, id)
		if err != nil {
			return RunDetail{}, err
		}
		detail.Children = service.views(children)
	}
	detail.Scheduler, err = service.repository.SchedulerProvenance(ctx, id)
	detail.Polling = !detail.Run.Terminal
	return detail, err
}

func technicalEventViews(events []ingestionrun.TechnicalEvent) []TechnicalEventView {
	result := make([]TechnicalEventView, len(events))
	for index, event := range events {
		view := TechnicalEventView{ID: event.ID, AffectedItems: event.OccurrenceCount, OccurredAt: formatTechnicalTime(event.OccurredAt),
			LastOccurredAt: formatTechnicalTime(event.LastOccurredAt), Severity: event.Severity, EventKind: event.EventKind, Terminal: event.Terminal,
			Recovered: event.Recovered, Class: event.Class, Step: event.Step, Operation: event.Operation, JobKey: event.JobKey,
			ItemIdentifier: event.ItemIdentifier, MemberKey: event.MemberKey, Attempt: event.Attempt, ErrorType: event.ErrorType,
			ErrorMessage: event.ErrorMessage, AggregationScope: event.AggregationScope, CapturedExamples: len(event.Samples), Samples: event.Samples}
		if event.OccurrenceCount > uint64(len(event.Samples)) {
			view.OmittedExamples = event.OccurrenceCount - uint64(len(event.Samples))
		}
		if event.Recovered != nil {
			if *event.Recovered {
				view.RecoveryState = "recovered"
			} else {
				view.RecoveryState = "not recovered"
			}
		}
		var pretty []byte
		if json.Valid(event.Details) {
			var value any
			_ = json.Unmarshal(event.Details, &value)
			omitRenderedResponseBody(value)
			pretty, _ = json.MarshalIndent(value, "", "  ")
		}
		view.Details = string(pretty)
		var body struct {
			Source struct {
				Response *struct {
					Body struct {
						Encoding string `json:"body_encoding"`
						Body     string `json:"body"`
					} `json:"body"`
				} `json:"response"`
			} `json:"source"`
		}
		if json.Unmarshal(event.Details, &body) == nil && body.Source.Response != nil {
			view.BodyEncoding, view.Body = body.Source.Response.Body.Encoding, body.Source.Response.Body.Body
		}
		result[index] = view
	}
	return result
}

func omitRenderedResponseBody(value any) {
	root, _ := value.(map[string]any)
	source, _ := root["source"].(map[string]any)
	response, _ := source["response"].(map[string]any)
	body, _ := response["body"].(map[string]any)
	if text, _ := body["body"].(string); text != "" {
		body["body"] = "[shown separately below]"
	}
}

func formatTechnicalTime(value time.Time) string {
	return value.UTC().Format("02 Jan 2006 15:04:05.000000 UTC")
}

func (service *Service) OverviewRuns(ctx context.Context) (RunOverview, error) {
	value, err := service.repository.RunOverview(ctx)
	if err != nil {
		return RunOverview{}, err
	}
	return RunOverview{Queued: value.Queued, Running: value.Running, RecentProblems: service.views(value.Problems), RecentSuccesses: service.views(value.Successes)}, nil
}

func (service *Service) OverviewSources(ctx context.Context) (SourceOverview, error) {
	jobs := service.catalog.Jobs()
	keys := make([]string, len(jobs))
	for index, job := range jobs {
		keys[index] = job.Key
	}
	return service.repository.SourceOverview(ctx, keys)
}
func (service *Service) OverviewSchedules(ctx context.Context) (ScheduleOverview, error) {
	return service.repository.ScheduleOverview(ctx)
}
func (service *Service) ActiveRunID(ctx context.Context, jobKey string) (uint64, bool, error) {
	return service.repository.ActiveRunID(ctx, jobKey)
}

func (service *Service) views(rows []runRow) []RunView {
	result := make([]RunView, len(rows))
	for index, row := range rows {
		result[index] = service.view(row)
	}
	return result
}

func (service *Service) view(row runRow) RunView {
	job, _ := service.catalog.Find(row.JobKey.String)
	view := RunView{ID: row.ID, Kind: row.Kind, KindLabel: presentRunKind(row.Kind), JobKey: row.JobKey.String,
		JobName: job.Name, Status: PresentStatus(row.Status), Trigger: row.TriggerType, TriggerReference: row.TriggerReference.String,
		RequestedBy: row.RequestedByUsername.String, SkipReason: row.SkipReason.String, CancelRequested: row.CancelRequestedAt.Valid,
		CancelReason: row.CancelReason.String, OwnerID: row.OwnerID.String, ProgressTotal: row.ProgressTotal, ProgressStarted: row.ProgressStarted,
		ProgressSucceeded: row.ProgressSucceeded, ProgressFailed: row.ProgressFailed, Rows: row.RowsProcessed, CurrentStep: row.CurrentStep.String,
		ErrorClass: row.ErrorClass.String, ErrorMessage: row.ErrorMessage.String, ErrorStep: row.ErrorStep.String,
		CreatedAt: formatTime(row.CreatedAt), StartedAt: formatNullTime(row.StartedAt), FinishedAt: formatNullTime(row.FinishedAt)}
	if row.ParentRunID.Valid {
		id := uint64(row.ParentRunID.Int64)
		view.ParentRunID = &id
	}
	if row.ChildPosition.Valid {
		view.ChildPosition = uint16(row.ChildPosition.Int64)
	}
	if row.HeartbeatAt.Valid {
		view.HeartbeatAt = formatTime(row.HeartbeatAt.Time)
		view.HeartbeatEvidence = row.HeartbeatAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if row.SnapshotDate.Valid {
		view.SnapshotDate = row.SnapshotDate.Time.Format("2006-01-02")
	}
	view.Parameters = parameterFields(row.parameters())
	view.Terminal = ingestionrun.IsTerminal(ingestionrun.Status(row.Status))
	view.ProgressUnit = progressUnit(job)
	return view
}

func parameterFields(parameters ingestionrun.Parameters) []ParameterField {
	switch parameters.Kind {
	case ingestionrun.FixedRangeV1, ingestionrun.RunAllRangeV1:
		value, err := ingestionrun.DecodeRange(parameters)
		if err == nil {
			return []ParameterField{{"From", value.From.String()}, {"To", value.To.String()}}
		}
	case ingestionrun.FixedDateSeriesV1:
		value, err := ingestionrun.DecodeDateSeries(parameters)
		if err == nil && len(value.Dates) > 0 {
			return []ParameterField{{"From", value.Dates[0].String()}, {"To", value.Dates[len(value.Dates)-1].String()}, {"Dates", strconv.Itoa(len(value.Dates))}}
		}
	case ingestionrun.MaintenanceDateSeriesV2:
		value, err := ingestionrun.DecodeMaintenanceSeries(parameters)
		if err == nil && len(value.Dates) > 0 {
			return []ParameterField{{"From", value.Dates[0].String()}, {"To", value.Dates[len(value.Dates)-1].String()}, {"Dates", strconv.Itoa(len(value.Dates))}}
		}
	case ingestionrun.DetailLiveSnapshotV1:
		return []ParameterField{{"Mode", "Current-state synchronization"}}
	}
	return []ParameterField{{"Parameters", "Unavailable"}}
}

func progressUnit(job core.JobDefinition) string {
	if job.Category == core.CategoryDetail {
		return "identifiers"
	}
	if job.Category == core.CategoryEOD || job.Category == core.CategoryCBR {
		return "requested dates"
	}
	return "members"
}

func (service *Service) validateFilter(filter RunFilter) error {
	if filter.Job != "" {
		if _, found := service.catalog.Find(filter.Job); !found {
			return fmt.Errorf("invalid job filter")
		}
	}
	if err := validateChoice("status", filter.Status, []string{"planned", "queued", "running", "succeeded", "failed", "skipped", "cancelled", "abandoned", "completed", "completed_with_skips"}); err != nil {
		return err
	}
	if err := validateChoice("kind", filter.Kind, []string{"job", "run_all_parent", "run_all_child"}); err != nil {
		return err
	}
	return validateChoice("trigger", filter.Trigger, []string{"direct", "scheduler", "run_all"})
}

func validateChoice(label, value string, choices []string) error {
	if value == "" {
		return nil
	}
	for _, choice := range choices {
		if value == choice {
			return nil
		}
	}
	return fmt.Errorf("invalid %s filter", label)
}

func runPageURL(filter RunFilter, page int) string {
	if page == 0 {
		return ""
	}
	values := url.Values{"page": {strconv.Itoa(page)}}
	for key, value := range map[string]string{"job": filter.Job, "status": filter.Status, "kind": filter.Kind, "trigger": filter.Trigger} {
		if value != "" {
			values.Set(key, value)
		}
	}
	return "/runs?" + values.Encode()
}

func formatTime(value time.Time) string { return value.UTC().Format("02 Jan 2006 15:04:05 UTC") }
func formatNullTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return formatTime(value.Time)
}
