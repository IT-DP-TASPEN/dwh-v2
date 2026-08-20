package ingestionexec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
)

type Executor struct {
	client            *fincloud.Client
	fixed             *ingestionstore.FixedRepository
	detail            *ingestionstore.DetailRepository
	maintenance       *ingestionstore.MaintenanceRepository
	runs              *ingestionrun.Repository
	catalog           ingestion.Catalog
	fixedConcurrency  int
	detailConcurrency int
	now               func() time.Time
}

type Result struct {
	Status           ingestionrun.Status
	Error            ingestionrun.SafeError
	Cause            error
	BusinessComplete bool
}

type fixedLayer uint8

const (
	fixedLayerNone fixedLayer = iota
	fixedLayerContract
	fixedLayerSource
	fixedLayerSourceContract
	fixedLayerPersistence
)

type fixedMemberResult struct {
	rows  uint64
	layer fixedLayer
	step  string
	err   error
}

func New(client *fincloud.Client, fixed *ingestionstore.FixedRepository, detail *ingestionstore.DetailRepository, maintenance *ingestionstore.MaintenanceRepository, runs *ingestionrun.Repository, catalog ingestion.Catalog, fixedConcurrency, detailConcurrency int) (*Executor, error) {
	if client == nil || fixed == nil || detail == nil || maintenance == nil || runs == nil || fixedConcurrency < 1 || detailConcurrency < 1 {
		return nil, fmt.Errorf("complete ingestion executor dependencies are required")
	}
	return &Executor{client: client, fixed: fixed, detail: detail, maintenance: maintenance, runs: runs, catalog: catalog,
		fixedConcurrency: fixedConcurrency, detailConcurrency: detailConcurrency, now: time.Now}, nil
}

func (executor *Executor) Execute(ctx context.Context, run ingestionrun.Run, ownerID string) Result {
	job, found := executor.catalog.Find(run.JobKey)
	if !found || run.Status != ingestionrun.StatusRunning || run.OwnerID != ownerID {
		return failed("contract", "invalid executable run", "start", fmt.Errorf("invalid run contract"))
	}
	if err := run.Parameters.Validate(job); err != nil {
		return failed("contract", "invalid executable parameters", "start", err)
	}
	switch job.Category {
	case ingestion.CategoryFixed:
		return executor.executeFixed(ctx, run, job)
	case ingestion.CategoryDetail:
		return executor.executeDetail(ctx, run, job)
	case ingestion.CategoryEOD, ingestion.CategoryCBR:
		return executor.executeMaintenance(ctx, run, job)
	default:
		return failed("contract", "unsupported job category", "start", fmt.Errorf("unsupported category %s", job.Category))
	}
}

func (executor *Executor) executeFixed(ctx context.Context, run ingestionrun.Run, job ingestion.JobDefinition) Result {
	definition := *job.Fixed
	var locations ingestion.FrozenLocations
	var accounts ingestion.FrozenAccountCodes
	var err error
	if definition.LocationStrategy == ingestion.PerLocation {
		rows, fetchErr := executor.client.FetchAccessibleLocations(ctx)
		if fetchErr != nil {
			return sourceFailure(ctx, fetchErr, "enumerate_locations")
		}
		values := make([]string, len(rows))
		for index := range rows {
			values[index] = rows[index].ID
		}
		locations, err = ingestion.FreezeLocations(values)
		if err != nil {
			return failed("source_contract", "accessible location set is empty", "enumerate_locations", err)
		}
	}
	if definition.AccountCodeStrategy == ingestion.AllAccountCodes {
		rows, fetchErr := executor.client.FetchAccountCodes(ctx)
		if fetchErr != nil {
			return sourceFailure(ctx, fetchErr, "enumerate_account_codes")
		}
		values := make([]string, len(rows))
		for index := range rows {
			values[index] = rows[index].ID
		}
		accounts, err = ingestion.FreezeAccountCodes(values)
		if err != nil {
			return failed("source_contract", "account-code set is empty", "enumerate_account_codes", err)
		}
	}
	var plan ingestion.FixedPlan
	if job.DateStrategy == ingestion.SingleDate {
		series, decodeErr := ingestionrun.DecodeDateSeries(run.Parameters)
		if decodeErr != nil {
			return failed("contract", "invalid fixed date series", "plan", decodeErr)
		}
		plan, err = ingestion.BuildFixedDateSeriesPlan(definition, series.Dates, locations)
	} else {
		rangeValue, decodeErr := ingestionrun.DecodeRange(run.Parameters)
		if decodeErr != nil {
			return failed("contract", "invalid fixed range", "plan", decodeErr)
		}
		plan, err = ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: rangeValue.From, To: rangeValue.To}, locations, accounts)
	}
	if err != nil {
		return failed("contract", "could not build fixed execution plan", "plan", err)
	}
	loadID, err := executor.fixed.BeginLoad(ctx, run.ID, definition, plan)
	if err != nil {
		if result, cancelled := cancellationFailure(ctx, "begin_fixed_load", err); cancelled {
			return result
		}
		return failed("persistence", "could not begin fixed report load", "begin_fixed_load", err)
	}
	progress := ingestionrun.Progress{Total: uint64(len(plan.Members)), Step: "fetch_members"}
	_ = executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil)
	var first *fixedMemberResult
	runFixedPool(ctx, plan.Members, executor.fixedConcurrency,
		func(workCtx context.Context, descriptor ingestion.RequestDescriptor) fixedMemberResult {
			return executor.fetchAndStageFixedMember(workCtx, definition, loadID, descriptor)
		}, func(result fixedMemberResult) {
			progress.Started++
			if result.err != nil {
				progress.Failed++
				if first == nil {
					copy := result
					first = &copy
				}
			} else {
				progress.Succeeded++
				progress.Rows += result.rows
			}
			_ = executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil)
		})
	if ctx.Err() != nil {
		if result, cancelled := cancellationFailure(ctx, "fetch_members", ctx.Err()); cancelled {
			return result
		}
		return sourceFailure(ctx, ctx.Err(), "fetch_members")
	}
	if first != nil {
		return fixedFailure(ctx, *first)
	}
	progress.Step = "promote"
	_ = executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil)
	if err := executor.fixed.Promote(ctx, definition, loadID); err != nil {
		if result, cancelled := cancellationFailure(ctx, "promote_fixed_load", err); cancelled {
			return result
		}
		return failed("persistence", "fixed report promotion failed", "promote_fixed_load", err)
	}
	return Result{Status: ingestionrun.StatusSucceeded, BusinessComplete: true}
}

func (executor *Executor) fetchAndStageFixedMember(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor) fixedMemberResult {
	chunks, err := ingestion.ChunkDateRange(descriptor.RequestedFrom, descriptor.RequestedTo, definition.MaxChunkDays)
	if err != nil {
		return fixedMemberResult{layer: fixedLayerContract, step: "plan_fixed_member", err: err}
	}
	segments := make([]ingestionstore.FixedSegment, 0, len(chunks))
	var rows uint64
	for index, chunk := range chunks {
		request, err := ingestion.BuildFixedRequestDescriptor(definition, ingestion.FixedDateRangeParams{From: chunk.From, To: chunk.To}, descriptor.SourceLocationID, descriptor.AccountCode, descriptor.MemberKey)
		if err != nil {
			return fixedMemberResult{layer: fixedLayerContract, step: "plan_fixed_member", err: err}
		}
		content, err := executor.client.DownloadReport(ctx, request.ReportName, request.Parameters...)
		if err != nil {
			return fixedMemberResult{layer: fixedLayerSource, step: "download_report", err: err}
		}
		parsed, err := ingestion.ParseFixedCSV(ctx, definition, descriptor.SourceLocationID, content)
		if err != nil {
			return fixedMemberResult{layer: fixedLayerSourceContract, step: "parse_fixed_csv", err: err}
		}
		rows += uint64(len(parsed))
		segments = append(segments, ingestionstore.FixedSegment{Index: index, AsOfDate: chunk.To, SourceRows: parsed})
	}
	if err := executor.fixed.StageMember(ctx, definition, loadID, descriptor, segments); err != nil {
		return fixedMemberResult{layer: fixedLayerPersistence, step: "stage_fixed_member", err: err}
	}
	return fixedMemberResult{rows: rows}
}

func runFixedPool(ctx context.Context, descriptors []ingestion.RequestDescriptor, concurrency int,
	work func(context.Context, ingestion.RequestDescriptor) fixedMemberResult, consume func(fixedMemberResult),
) {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan ingestion.RequestDescriptor)
	results := make(chan fixedMemberResult, concurrency)
	workers := min(concurrency, len(descriptors))
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for descriptor := range jobs {
				result := work(workCtx, descriptor)
				results <- result
				if result.err != nil {
					cancel()
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, descriptor := range descriptors {
			select {
			case jobs <- descriptor:
			case <-workCtx.Done():
				return
			}
		}
	}()
	go func() { wait.Wait(); close(results) }()
	for result := range results {
		consume(result)
	}
}

func fixedFailure(ctx context.Context, result fixedMemberResult) Result {
	if cancelledResult, cancelled := cancellationFailure(ctx, result.step, result.err); cancelled {
		return cancelledResult
	}
	switch result.layer {
	case fixedLayerContract:
		return failed("contract", "invalid fixed member plan", result.step, result.err)
	case fixedLayerSource:
		return sourceFailure(ctx, result.err, result.step)
	case fixedLayerSourceContract:
		return failed("source_contract", "fixed report CSV parsing failed", result.step, result.err)
	case fixedLayerPersistence:
		return failed("persistence", "fixed report staging failed", result.step, result.err)
	default:
		return failed("contract", "unknown fixed failure layer", "fetch_members", result.err)
	}
}

func (executor *Executor) executeDetail(ctx context.Context, run ingestionrun.Run, job ingestion.JobDefinition) Result {
	snapshotDate := jakartaSnapshotDate(executor.now())
	if err := executor.runs.FreezeSnapshotDate(ctx, run.ID, run.OwnerID, snapshotDate); err != nil {
		return failed("persistence", "could not freeze detail snapshot date", "snapshot_date", err)
	}
	identifiers, err := executor.enumerateDetails(ctx, job.Key, snapshotDate)
	if err != nil {
		return sourceFailure(ctx, err, "enumerate_identifiers")
	}
	progress := ingestionrun.Progress{Total: uint64(len(identifiers)), Step: "fetch_details"}
	if err := executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil); err != nil {
		return failed("persistence", "could not persist detail run progress", "persist_run_progress", err)
	}
	outcome := runDetailPool(ctx, identifiers, executor.detailConcurrency,
		func(workCtx context.Context, identifier string) detailItemResult {
			return executor.fetchAndSaveDetail(workCtx, job.Key, identifier, snapshotDate)
		},
		func(progress ingestionrun.Progress, diagnostics *ingestionrun.MapperDiagnostics) error {
			return executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, diagnostics)
		},
	)
	if outcome.progress.Started < outcome.progress.Total && ctx.Err() != nil {
		if result, cancelled := cancellationFailure(ctx, "fetch_detail", ctx.Err()); cancelled {
			return result
		}
		return sourceFailure(ctx, ctx.Err(), "fetch_detail")
	}
	if outcome.fatal != nil {
		return detailFatalFailure(ctx, outcome.fatalLayer, outcome.fatal)
	}
	if outcome.firstLocal != nil {
		return failed("item_data", "one or more detail identifiers failed", "map_detail", outcome.firstLocal)
	}
	return Result{Status: ingestionrun.StatusSucceeded, BusinessComplete: true}
}

type detailLayer uint8

const (
	detailLayerNone detailLayer = iota
	detailLayerFetch
	detailLayerMap
	detailLayerPersist
	detailLayerRunProgress
)

type detailItemResult struct {
	rows  uint64
	layer detailLayer
	err   error
}

type detailPoolOutcome struct {
	progress    ingestionrun.Progress
	diagnostics ingestionrun.MapperDiagnostics
	firstLocal  error
	fatal       error
	fatalLayer  detailLayer
}

func runDetailPool(ctx context.Context, identifiers []string, concurrency int, work func(context.Context, string) detailItemResult,
	flush func(ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error,
) detailPoolOutcome {
	outcome := detailPoolOutcome{progress: ingestionrun.Progress{Total: uint64(len(identifiers)), Step: "fetch_details"}}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	results := make(chan detailItemResult, concurrency)
	workers := min(concurrency, len(identifiers))
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for identifier := range jobs {
				result := work(workCtx, identifier)
				if result.err != nil && result.layer != detailLayerMap {
					cancel()
				}
				results <- result
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, identifier := range identifiers {
			select {
			case jobs <- identifier:
			case <-workCtx.Done():
				return
			}
		}
	}()
	go func() { wait.Wait(); close(results) }()
	flushEnabled := true
	for result := range results {
		outcome.progress.Started++
		if result.err != nil {
			if errors.Is(result.err, context.Canceled) && ctx.Err() == nil {
				continue
			}
			outcome.progress.Failed++
			if result.layer == detailLayerMap {
				if outcome.firstLocal == nil {
					outcome.firstLocal = result.err
				}
				var mapper *ingestion.MapperError
				if errors.As(result.err, &mapper) {
					if err := outcome.diagnostics.Add(mapper.Metadata()); err != nil && outcome.fatal == nil {
						outcome.fatal, outcome.fatalLayer = err, detailLayerRunProgress
						flushEnabled = false
						cancel()
					}
				}
			} else if outcome.fatal == nil {
				outcome.fatal, outcome.fatalLayer = result.err, result.layer
			}
		} else {
			outcome.progress.Succeeded++
			outcome.progress.Rows += result.rows
		}
		if flushEnabled {
			var diagnostics *ingestionrun.MapperDiagnostics
			if outcome.diagnostics.TotalCount > 0 {
				diagnostics = &outcome.diagnostics
			}
			if err := flush(outcome.progress, diagnostics); err != nil {
				if outcome.fatal == nil {
					outcome.fatal, outcome.fatalLayer = err, detailLayerRunProgress
				}
				flushEnabled = false
				cancel()
			}
		}
	}
	return outcome
}

func detailFatalFailure(ctx context.Context, layer detailLayer, err error) Result {
	step := "detail"
	switch layer {
	case detailLayerFetch:
		step = "fetch_detail"
	case detailLayerPersist:
		step = "persist_detail"
	case detailLayerRunProgress:
		step = "persist_run_progress"
	}
	if result, cancelled := cancellationFailure(ctx, step, err); cancelled {
		return result
	}
	switch layer {
	case detailLayerFetch:
		return sourceFailure(ctx, err, step)
	case detailLayerPersist:
		return failed("persistence", "detail snapshot persistence failed", step, err)
	case detailLayerRunProgress:
		return failed("persistence", "could not persist detail run progress", step, err)
	default:
		return failed("contract", "unknown detail failure layer", step, err)
	}
}

func (executor *Executor) enumerateDetails(ctx context.Context, jobKey string, snapshotDate ingestion.CalendarDate) ([]string, error) {
	switch jobKey {
	case "cif_detail":
		return executor.client.FetchCIFNumbers(ctx, snapshotDate.String())
	case "saving_detail":
		return executor.client.FetchSavingAccounts(ctx)
	case "time_deposit_detail":
		return executor.client.FetchTimeDepositAccounts(ctx)
	case "loan_detail":
		return executor.client.FetchLoanAccounts(ctx)
	default:
		return nil, fmt.Errorf("unsupported detail job %s", jobKey)
	}
}

func (executor *Executor) fetchAndSaveDetail(ctx context.Context, jobKey, identifier string, snapshotDate ingestion.CalendarDate) detailItemResult {
	fetchedAt := executor.now().UTC()
	var record ingestion.DetailRecord
	var err error
	switch jobKey {
	case "cif_detail":
		value, fetchErr := executor.client.FetchCIFDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, err: fetchErr}
		}
		record, err = ingestion.MapCIFDetail(ctx, value, snapshotDate, fetchedAt)
	case "saving_detail":
		value, fetchErr := executor.client.FetchSavingDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, err: fetchErr}
		}
		record, err = ingestion.MapSavingDetail(ctx, value, snapshotDate, fetchedAt)
	case "time_deposit_detail":
		value, fetchErr := executor.client.FetchTimeDepositDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, err: fetchErr}
		}
		record, err = ingestion.MapTimeDepositDetail(ctx, value, snapshotDate, fetchedAt)
	case "loan_detail":
		value, fetchErr := executor.client.FetchLoanDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, err: fetchErr}
		}
		record, err = ingestion.MapLoanDetail(ctx, value, snapshotDate, fetchedAt)
	}
	if err != nil {
		return detailItemResult{layer: detailLayerMap, err: err}
	}
	switch jobKey {
	case "cif_detail":
		err = executor.detail.SaveCIFSnapshot(ctx, record)
	case "saving_detail":
		err = executor.detail.SaveSavingSnapshot(ctx, record)
	case "time_deposit_detail":
		err = executor.detail.SaveTimeDepositSnapshot(ctx, record)
	case "loan_detail":
		err = executor.detail.SaveLoanSnapshot(ctx, record)
	}
	if err != nil {
		return detailPersistenceFailure(err)
	}
	rows := uint64(1)
	for _, children := range record.Children {
		rows += uint64(len(children))
	}
	return detailItemResult{rows: rows}
}

func detailPersistenceFailure(err error) detailItemResult {
	return detailItemResult{layer: detailLayerPersist, err: err}
}

func (executor *Executor) executeMaintenance(ctx context.Context, run ingestionrun.Run, job ingestion.JobDefinition) Result {
	series, err := ingestionrun.DecodeMaintenanceSeries(run.Parameters)
	if err != nil {
		return failed("contract", "invalid maintenance date series", "plan", err)
	}
	progress := ingestionrun.Progress{Total: uint64(len(series.Dates)), Step: "maintenance_dates"}
	_ = executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil)
	var first error
	for _, requested := range series.Dates {
		if err := ctx.Err(); err != nil {
			if result, cancelled := cancellationFailure(ctx, "maintenance_dates", err); cancelled {
				return result
			}
			return sourceFailure(ctx, err, "maintenance_dates")
		}
		progress.Started++
		rows, err := executor.fetchAndSaveMaintenance(ctx, *job.Maintenance, requested, series.LookbackDays)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return sourceFailure(ctx, err, "maintenance_dates")
			}
			progress.Failed++
			if first == nil {
				first = err
			}
			if sourceWide(err) {
				_ = executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil)
				return sourceFailure(ctx, err, "maintenance_dates")
			}
		} else {
			progress.Succeeded++
			progress.Rows += rows
		}
		_ = executor.runs.UpdateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, nil)
	}
	if first != nil {
		return failed("date_local", "one or more maintenance dates failed", "maintenance_dates", first)
	}
	return Result{Status: ingestionrun.StatusSucceeded, BusinessComplete: true}
}

func (executor *Executor) fetchAndSaveMaintenance(ctx context.Context, definition ingestion.MaintenanceDefinition, requested ingestion.CalendarDate, lookback int) (uint64, error) {
	candidates, err := ingestion.MaintenanceCandidateDates(ingestion.MaintenanceParams{RequestedDate: requested, LookbackDays: lookback})
	if err != nil {
		return 0, err
	}
	folderKind := "daily"
	if definition.Kind == ingestion.MaintenanceCBR {
		folderKind = "cbr"
	}
	for _, matched := range candidates {
		folder := folderKind + "/" + strings.ReplaceAll(matched.String(), "-", "")
		files, err := executor.client.ListMaintenanceReportFiles(ctx, folder)
		if err != nil {
			return 0, err
		}
		for _, sourcePath := range files {
			disposition, candidate := ingestion.ClassifyMaintenanceFile(definition.Kind, path.Base(sourcePath))
			if disposition != ingestion.MaintenanceRegistered || candidate == nil || candidate.Key != definition.Key {
				continue
			}
			content, err := executor.client.DownloadMaintenanceReport(ctx, path.Base(sourcePath), path.Dir(sourcePath))
			if err != nil {
				return 0, err
			}
			parsed, err := ingestion.ParseMaintenanceCSV(ctx, definition, matched, content)
			if err != nil {
				return 0, err
			}
			err = executor.maintenance.SaveSnapshot(ctx, ingestionstore.MaintenanceSnapshot{RequestedDate: requested, MatchedDate: matched, FileName: path.Base(sourcePath), Parsed: parsed})
			return uint64(len(parsed.Rows)), err
		}
	}
	return 0, fmt.Errorf("registered maintenance file was not found within lookback")
}

func sourceWide(err error) bool {
	var source *fincloud.Error
	if !errors.As(err, &source) {
		return false
	}
	if source.Kind == fincloud.ErrorAuthentication || source.Kind == fincloud.ErrorUnauthorized || source.Kind == fincloud.ErrorMalformed {
		return true
	}
	return source.HTTPStatus == 0 || source.HTTPStatus == http.StatusForbidden || source.HTTPStatus == http.StatusRequestTimeout ||
		source.HTTPStatus == http.StatusTooManyRequests || source.HTTPStatus >= 500 || source.HTTPStatus >= 400
}

func jakartaSnapshotDate(now time.Time) ingestion.CalendarDate {
	return ingestion.CalendarDateFromTime(now.In(time.FixedZone("Asia/Jakarta", 7*60*60)))
}

func sourceFailure(ctx context.Context, err error, step string) Result {
	if result, cancelled := cancellationFailure(ctx, step, err); cancelled {
		return result
	}
	return failed("source", "Fincloud source operation failed", step, err)
}

func cancellationFailure(ctx context.Context, step string, primary error) (Result, bool) {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, ingestionrun.ErrCancellationRequested):
		return cancelled("run cancellation requested", step, errors.Join(primary, cause)), true
	case errors.Is(cause, ingestionrun.ErrCoordinatorShutdown):
		return cancelled("application shutdown cancelled the run", step, errors.Join(primary, cause)), true
	default:
		return Result{}, false
	}
}

func failed(class, message, step string, cause error) Result {
	return Result{Status: ingestionrun.StatusFailed, Error: ingestionrun.SafeError{Class: class, Message: message, Step: step}, Cause: cause}
}

func cancelled(message, step string, cause error) Result {
	return Result{Status: ingestionrun.StatusCancelled, Error: ingestionrun.SafeError{Class: "cancelled", Message: message, Step: step}, Cause: cause}
}
