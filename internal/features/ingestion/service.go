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
	rows, total, err := service.repository.ListRuns(ctx, filter, RunPageSize, max(page-1, 0)*RunPageSize)
	if err != nil {
		return RunPage{}, err
	}
	pageInfo := pagination.New(page, RunPageSize, total)
	if pageInfo.Offset() != max(page-1, 0)*RunPageSize {
		rows, _, err = service.repository.ListRuns(ctx, filter, RunPageSize, pageInfo.Offset())
		if err != nil {
			return RunPage{}, err
		}
	}
	result := RunPage{Rows: service.views(rows), Filter: filter, Pagination: pageInfo, Jobs: service.catalog.Jobs(),
		Statuses: []string{"planned", "queued", "running", "succeeded", "failed", "skipped", "cancelled", "abandoned", "completed", "completed_with_skips"},
		Kinds:    []string{"job", "run_all_parent", "run_all_child"}, Triggers: []string{"direct", "scheduler", "run_all"}}
	result.PreviousURL, result.NextURL = runPageURL(filter, pageInfo.Previous), runPageURL(filter, pageInfo.Next)
	return result, nil
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
	view := RunView{ID: row.ID, Kind: row.Kind, KindLabel: strings.ReplaceAll(row.Kind, "_", " "), JobKey: row.JobKey.String,
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
