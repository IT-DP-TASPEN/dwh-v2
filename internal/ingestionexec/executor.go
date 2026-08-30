package ingestionexec

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestiondiag"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
)

type Executor struct {
	client            *fincloud.Client
	fixed             *ingestionstore.FixedRepository
	detail            *ingestionstore.DetailRepository
	maintenance       *ingestionstore.MaintenanceRepository
	runs              *ingestionrun.Repository
	updateProgress    func(context.Context, uint64, string, ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error
	catalog           ingestion.Catalog
	fixedConcurrency  int
	detailConcurrency int
	now               func() time.Time
	logger            *slog.Logger
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
	rows            uint64
	segments        int
	checksum        [sha256.Size]byte
	layer           fixedLayer
	step, memberKey string
	item            string
	err             error
}

type journalTransactionType struct {
	ID          string
	Description string
}

type maintenanceDateError struct {
	class string
	cause error
}

const (
	detailOutstandingDefinitionKey = "eod_detail_outstanding_rekening_pinjaman"
	detailOutstandingMergedFile    = "DetailOutstandingRekeningPinjaman.csv"
	maintenanceSourceMerged        = "merged"
	maintenanceSourceSplit         = "split"
)

var (
	detailOutstandingSplitFile = regexp.MustCompile(`(?i)^DetailOutstandingRekeningPinjaman_(.+)\.csv$`)
	detailOutstandingBranches  = []string{"000", "001", "002", "003", "004", "005", "006", "007", "008"}
)

type maintenanceSourceSelection struct {
	mode            string
	logicalFileName string
	paths           []string
}

func (failure *maintenanceDateError) Error() string { return failure.cause.Error() }
func (failure *maintenanceDateError) Unwrap() error { return failure.cause }

func New(client *fincloud.Client, fixed *ingestionstore.FixedRepository, detail *ingestionstore.DetailRepository, maintenance *ingestionstore.MaintenanceRepository, runs *ingestionrun.Repository, catalog ingestion.Catalog, fixedConcurrency, detailConcurrency int, logger *slog.Logger) (*Executor, error) {
	if client == nil || fixed == nil || detail == nil || maintenance == nil || runs == nil || fixedConcurrency < 1 || detailConcurrency < 1 || logger == nil {
		return nil, fmt.Errorf("complete ingestion executor dependencies are required")
	}
	return &Executor{client: client, fixed: fixed, detail: detail, maintenance: maintenance, runs: runs, updateProgress: runs.UpdateProgress, catalog: catalog,
		fixedConcurrency: fixedConcurrency, detailConcurrency: detailConcurrency, now: time.Now, logger: logger}, nil
}

func (executor *Executor) Execute(ctx context.Context, run ingestionrun.Run, ownerID string) Result {
	recorder := newRunDiagnosticRecorder(executor.runs, executor.logger, run.ID, run.JobKey)
	ctx = ingestiondiag.WithRecorder(ctx, recorder.record, run.ID, run.JobKey)
	job, found := executor.catalog.Find(run.JobKey)
	if !found || run.Status != ingestionrun.StatusRunning || run.OwnerID != ownerID {
		result := failed("contract", "invalid executable run", "start", fmt.Errorf("invalid run contract"))
		recordTerminalFallback(ctx, recorder, result)
		return result
	}
	if err := run.Parameters.Validate(job); err != nil {
		result := failed("contract", "invalid executable parameters", "start", err)
		recordTerminalFallback(ctx, recorder, result)
		return result
	}
	var result Result
	switch job.Category {
	case ingestion.CategoryFixed:
		result = executor.executeFixed(ctx, run, job)
	case ingestion.CategoryDetail:
		result = executor.executeDetail(ctx, run, job)
	case ingestion.CategoryEOD, ingestion.CategoryCBR:
		result = executor.executeMaintenance(ctx, run, job)
	default:
		result = failed("contract", "unsupported job category", "start", fmt.Errorf("unsupported category %s", job.Category))
	}
	recordTerminalFallback(ctx, recorder, result)
	return result
}

func (executor *Executor) persistProgress(ctx context.Context, run ingestionrun.Run, progress ingestionrun.Progress, diagnostics *ingestionrun.MapperDiagnostics, enabled *bool) error {
	if !*enabled {
		return nil
	}
	err := executor.updateProgress(context.WithoutCancel(ctx), run.ID, run.OwnerID, progress, diagnostics)
	if err == nil || errors.Is(err, ingestionrun.ErrOwnershipLost) {
		return err
	}
	*enabled = false
	progressCtx := diagnosticScope(ctx, "persistence", "persist_run_progress", "persist_run_progress", "", "")
	recordProgressDegraded(progressCtx, err)
	executor.logger.Warn("progress persistence degraded; ingestion continues", "run_id", run.ID, "job_key", run.JobKey, "error", err)
	return nil
}

func (executor *Executor) executeFixed(ctx context.Context, run ingestionrun.Run, job ingestion.JobDefinition) Result {
	progressWrites := true
	definition := *job.Fixed
	var locations ingestion.FrozenLocations
	var accounts ingestion.FrozenAccountCodes
	var journalTransactionTypes []journalTransactionType
	var err error
	if definition.Key == "journal_transaction_report" {
		rows, fetchErr := executor.client.FetchJournalTransactionTypes(ctx)
		if fetchErr != nil {
			return sourceFailure(ctx, fetchErr, "enumerate_journal_transaction_types")
		}
		journalTransactionTypes, err = freezeJournalTransactionTypes(rows)
		if err != nil {
			return failed("source_contract", "journal transaction-type set is empty", "enumerate_journal_transaction_types", err)
		}
		logged := make([]string, min(20, len(journalTransactionTypes)))
		for index := range logged {
			logged[index] = boundedJournalTransactionTypeID(journalTransactionTypes[index].ID)
		}
		executor.logger.InfoContext(ctx, "journal transaction types frozen", "run_id", run.ID, "job_key", run.JobKey,
			"transaction_type_count", len(journalTransactionTypes), "transaction_type_ids", logged,
			"transaction_type_ids_omitted", len(journalTransactionTypes)-len(logged))
	}
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
	loadID, err := executor.fixed.BeginLoad(diagnosticScope(ctx, "persistence", "begin_fixed_load", "begin_fixed_load", "", ""), run.ID, definition, plan)
	if err != nil {
		if result, cancelled := cancellationFailure(ctx, "begin_fixed_load", err); cancelled {
			return result
		}
		return failed("persistence", "could not begin fixed report load", "begin_fixed_load", err)
	}
	progress := ingestionrun.Progress{Total: uint64(len(plan.Members)), Step: "fetch_members"}
	if err := executor.persistProgress(ctx, run, progress, nil, &progressWrites); err != nil {
		return ownershipFailure(err, "persist_run_progress")
	}
	var first *fixedMemberResult
	poolCtx, stopPool := context.WithCancel(ctx)
	defer stopPool()
	var progressFatal error
	runFixedPool(poolCtx, plan.Members, executor.fixedConcurrency,
		func(workCtx context.Context, descriptor ingestion.RequestDescriptor) fixedMemberResult {
			result := executor.fetchAndStageFixedMember(workCtx, definition, loadID, descriptor, journalTransactionTypes)
			result.memberKey = descriptor.MemberKey
			return result
		}, func(result fixedMemberResult) {
			progress.Started++
			if result.err != nil {
				if suppressCleanupCancellation(ctx, result.err) {
					if err := executor.persistProgress(ctx, run, progress, nil, &progressWrites); err != nil {
						progressFatal = err
						stopPool()
					}
					return
				}
				firstFailure := first == nil
				recordFixedMemberDiagnostic(ctx, result, firstFailure)
				progress.Failed++
				if firstFailure {
					copy := result
					first = &copy
				}
			} else {
				progress.Succeeded++
				progress.Rows += result.rows
			}
			if err := executor.persistProgress(ctx, run, progress, nil, &progressWrites); err != nil {
				progressFatal = err
				stopPool()
			}
		})
	if progressFatal != nil {
		return ownershipFailure(progressFatal, "persist_run_progress")
	}
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
	if err := executor.persistProgress(ctx, run, progress, nil, &progressWrites); err != nil {
		return ownershipFailure(err, "persist_run_progress")
	}
	if err := executor.fixed.Promote(diagnosticScope(ctx, "persistence", "promote_fixed_load", "promote_fixed_load", "", ""), run.ID, run.OwnerID, definition, loadID); err != nil {
		if result, cancelled := cancellationFailure(ctx, "promote_fixed_load", err); cancelled {
			return result
		}
		return failed("persistence", "fixed report promotion failed", "promote_fixed_load", err)
	}
	return Result{Status: ingestionrun.StatusSucceeded, BusinessComplete: true}
}

func (executor *Executor) fetchAndStageFixedMember(ctx context.Context, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, journalTransactionTypes []journalTransactionType) fixedMemberResult {
	result := executor.fetchFixedMemberSegments(ctx, definition, descriptor, journalTransactionTypes,
		func(stageCtx context.Context, segment ingestionstore.FixedSegment) error {
			return executor.fixed.StageMemberSegment(stageCtx, definition, loadID, descriptor, segment)
		})
	if result.err != nil {
		return result
	}
	persistCtx := diagnosticScope(ctx, "persistence", "finalize_fixed_member_candidate", "finalize_fixed_member_candidate", "", descriptor.MemberKey)
	if err := executor.fixed.FinalizeMemberCandidate(persistCtx, definition, loadID, descriptor, result.segments, result.rows, result.checksum); err != nil {
		return fixedMemberResult{layer: fixedLayerPersistence, step: "finalize_fixed_member_candidate", err: err}
	}
	return result
}

func (executor *Executor) fetchFixedMemberSegments(ctx context.Context, definition ingestion.FixedDefinition, descriptor ingestion.RequestDescriptor, journalTransactionTypes []journalTransactionType, stage func(context.Context, ingestionstore.FixedSegment) error) fixedMemberResult {
	chunks, err := ingestion.ChunkDateRange(descriptor.RequestedFrom, descriptor.RequestedTo, definition.MaxChunkDays)
	if err != nil {
		return fixedMemberResult{layer: fixedLayerContract, step: "plan_fixed_member", err: err}
	}
	transactionTypes := journalTransactionTypes
	if definition.Key != "journal_transaction_report" {
		transactionTypes = []journalTransactionType{{}}
	} else if len(transactionTypes) == 0 {
		return fixedMemberResult{layer: fixedLayerContract, step: "plan_fixed_member", err: fmt.Errorf("frozen journal transaction-type set is empty")}
	}
	segmentCount := len(chunks) * len(transactionTypes)
	memberHash := sha256.New()
	var rows uint64
	for chunkIndex, chunk := range chunks {
		for typeIndex, transactionType := range transactionTypes {
			item := ""
			if definition.Key == "journal_transaction_report" {
				item = fmt.Sprintf("journal chunk %d/%d, transaction type %d/%d (%s)", chunkIndex+1, len(chunks), typeIndex+1, len(transactionTypes), boundedJournalTransactionTypeID(transactionType.ID))
			}
			if err := ctx.Err(); err != nil {
				return fixedMemberResult{layer: fixedLayerSource, step: "download_report", item: item, err: err}
			}
			request, err := ingestion.BuildFixedRequestDescriptor(definition, ingestion.FixedDateRangeParams{From: chunk.From, To: chunk.To}, descriptor.SourceLocationID, descriptor.AccountCode, descriptor.MemberKey)
			if err == nil && definition.Key == "journal_transaction_report" {
				request, err = journalTransactionRequest(request, transactionType.ID)
			}
			if err != nil {
				return fixedMemberResult{layer: fixedLayerContract, step: "plan_fixed_member", item: item, err: err}
			}
			sourceCtx := diagnosticScope(ctx, "source", "download_report", "download_report", item, descriptor.MemberKey)
			content, err := executor.client.DownloadReport(sourceCtx, request.ReportName, request.Parameters...)
			if err != nil {
				return fixedMemberResult{layer: fixedLayerSource, step: "download_report", item: item, err: err}
			}
			parserCtx := diagnosticScope(ctx, "source_contract", "parse_fixed_csv", "parse_fixed_csv", item, descriptor.MemberKey)
			parsed, err := ingestion.ParseFixedCSV(parserCtx, definition, descriptor.SourceLocationID, content)
			if err != nil {
				return fixedMemberResult{layer: fixedLayerSourceContract, step: "parse_fixed_csv", item: item, err: err}
			}
			segment := ingestionstore.FixedSegment{Index: chunkIndex*len(transactionTypes) + typeIndex, AsOfDate: chunk.To, SourceRows: parsed}
			persistCtx := diagnosticScope(ctx, "persistence", "stage_fixed_member_segment", "stage_fixed_member_segment", item, descriptor.MemberKey)
			if err := stage(persistCtx, segment); err != nil {
				return fixedMemberResult{layer: fixedLayerPersistence, step: "stage_fixed_member_segment", item: item, err: err}
			}
			rows += uint64(len(parsed))
			for _, row := range parsed {
				ingestion.WriteFixedMemberChecksumPart(memberHash, row.SourceRowChecksum)
			}
		}
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], memberHash.Sum(nil))
	return fixedMemberResult{rows: rows, segments: segmentCount, checksum: checksum}
}

func freezeJournalTransactionTypes(rows []fincloud.JournalTransactionType) ([]journalTransactionType, error) {
	seen := make(map[string]struct{}, len(rows))
	frozen := make([]journalTransactionType, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" || id == "%" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		frozen = append(frozen, journalTransactionType{ID: id, Description: row.Description})
	}
	if len(frozen) == 0 {
		return nil, fmt.Errorf("captured journal transaction-type set is empty")
	}
	slices.SortFunc(frozen, func(left, right journalTransactionType) int { return strings.Compare(left.ID, right.ID) })
	return frozen, nil
}

func journalTransactionRequest(request ingestion.RequestDescriptor, transactionTypeID string) (ingestion.RequestDescriptor, error) {
	id := strings.TrimSpace(transactionTypeID)
	if request.ReportName != "Journal Transaction csv" || len(request.Parameters) != 6 || id == "" || id == "%" {
		return ingestion.RequestDescriptor{}, fmt.Errorf("valid exact Journal transaction type is required")
	}
	request.Parameters = append([]string(nil), request.Parameters...)
	request.Parameters[1] = id
	return request, nil
}

func boundedJournalTransactionTypeID(id string) string {
	runes := []rune(id)
	if len(runes) <= 64 {
		return id
	}
	return string(runes[:64]) + "..."
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
		if result.err != nil {
			cancel()
		}
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
	progressWrites := true
	if err := executor.detail.PrepareRun(ctx, run.ID); err != nil {
		return failed("persistence", "could not prepare Detail staging", "prepare_detail_staging", err)
	}
	snapshotDate := jakartaSnapshotDate(executor.now())
	if err := executor.runs.FreezeSnapshotDate(ctx, run.ID, run.OwnerID, snapshotDate); err != nil {
		return failed("persistence", "could not freeze Detail execution date", "snapshot_date", err)
	}
	identifiers, err := executor.enumerateDetails(ctx, job.Key, snapshotDate)
	if err != nil {
		return sourceFailure(ctx, err, "enumerate_identifiers")
	}
	progress := ingestionrun.Progress{Total: uint64(len(identifiers)), Step: "fetch_details"}
	if err := executor.persistProgress(ctx, run, progress, nil, &progressWrites); err != nil {
		return ownershipFailure(err, "persist_run_progress")
	}
	outcome := runDetailPool(ctx, identifiers, executor.detailConcurrency,
		func(workCtx context.Context, identifier string) detailItemResult {
			return executor.fetchAndStageDetail(workCtx, run.ID, job.Key, identifier)
		},
		func(progress ingestionrun.Progress, diagnostics *ingestionrun.MapperDiagnostics) error {
			return executor.persistProgress(ctx, run, progress, diagnostics, &progressWrites)
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
	outcome.progress.Step = "publish_detail"
	var diagnostics *ingestionrun.MapperDiagnostics
	if outcome.diagnostics.TotalCount > 0 {
		diagnostics = &outcome.diagnostics
	}
	if err := executor.persistProgress(ctx, run, outcome.progress, diagnostics, &progressWrites); err != nil {
		return ownershipFailure(err, "persist_run_progress")
	}
	domain, err := detailDomain(job.Key)
	if err != nil {
		return failed("contract", "unsupported Detail job", "publish_detail", err)
	}
	publishCtx := diagnosticScope(ctx, "persistence", "publish_detail", "publish_"+job.Key, "", "")
	if err := executor.detail.Publish(publishCtx, run.ID, run.OwnerID, domain, outcome.progress.Total); err != nil {
		if result, cancelled := cancellationFailure(ctx, "publish_detail", err); cancelled {
			return result
		}
		recordPersistenceDiagnostic(publishCtx, err, true)
		return failed("persistence", "Detail current-state publication failed", "publish_detail", err)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := executor.detail.CleanupRun(cleanupCtx, run.ID); err != nil {
		executor.logger.Warn("clean Detail staging", "run_id", run.ID, "job_key", run.JobKey, "error", err)
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
	rows       uint64
	layer      detailLayer
	identifier string
	err        error
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
				result.identifier = identifier
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
			if suppressCleanupCancellation(ctx, result.err) {
				continue
			}
			outcome.progress.Failed++
			if result.layer == detailLayerMap {
				mapperCtx := diagnosticScope(ctx, "item_data", "map_detail", "map_detail", result.identifier, "")
				recordMapperDiagnostic(mapperCtx, result.err, result.identifier, false)
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
			} else {
				primary := outcome.fatal == nil
				if primary {
					outcome.fatal, outcome.fatalLayer = result.err, result.layer
				}
				recordDetailFatalDiagnostic(ctx, result, primary)
				cancel()
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
		step = "stage_detail"
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
		return failed("persistence", "Detail candidate staging failed", step, err)
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

func (executor *Executor) fetchAndStageDetail(ctx context.Context, runID uint64, jobKey, identifier string) detailItemResult {
	fetchedAt := executor.now().UTC()
	var record ingestion.DetailRecord
	var err error
	switch jobKey {
	case "cif_detail":
		value, fetchErr := executor.client.FetchCIFDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, identifier: identifier, err: fetchErr}
		}
		record, err = ingestion.MapCIFDetail(ctx, value, fetchedAt)
	case "saving_detail":
		value, fetchErr := executor.client.FetchSavingDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, identifier: identifier, err: fetchErr}
		}
		record, err = ingestion.MapSavingDetail(ctx, value, fetchedAt)
	case "time_deposit_detail":
		value, fetchErr := executor.client.FetchTimeDepositDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, identifier: identifier, err: fetchErr}
		}
		record, err = ingestion.MapTimeDepositDetail(ctx, value, fetchedAt)
	case "loan_detail":
		value, fetchErr := executor.client.FetchLoanDetail(ctx, identifier)
		if fetchErr != nil {
			return detailItemResult{layer: detailLayerFetch, identifier: identifier, err: fetchErr}
		}
		record, err = ingestion.MapLoanDetail(ctx, value, fetchedAt)
	}
	if err != nil {
		return detailItemResult{layer: detailLayerMap, identifier: identifier, err: err}
	}
	persistCtx := diagnosticScope(ctx, "persistence", "stage_detail", "stage_"+jobKey, identifier, "")
	err = executor.detail.Stage(persistCtx, runID, record)
	if err != nil {
		result := detailPersistenceFailure(err)
		result.identifier = identifier
		return result
	}
	rows := uint64(1)
	for _, children := range record.Children {
		rows += uint64(len(children))
	}
	return detailItemResult{rows: rows, identifier: identifier}
}

func detailDomain(jobKey string) (ingestion.DetailDomain, error) {
	switch jobKey {
	case "cif_detail":
		return ingestion.DetailCIF, nil
	case "saving_detail":
		return ingestion.DetailSaving, nil
	case "time_deposit_detail":
		return ingestion.DetailTimeDeposit, nil
	case "loan_detail":
		return ingestion.DetailLoan, nil
	default:
		return "", fmt.Errorf("unsupported Detail job %q", jobKey)
	}
}

func detailPersistenceFailure(err error) detailItemResult {
	return detailItemResult{layer: detailLayerPersist, err: err}
}

func (executor *Executor) executeMaintenance(ctx context.Context, run ingestionrun.Run, job ingestion.JobDefinition) Result {
	progressWrites := true
	series, err := ingestionrun.DecodeMaintenanceSeries(run.Parameters)
	if err != nil {
		return failed("contract", "invalid maintenance date series", "plan", err)
	}
	progress := ingestionrun.Progress{Total: uint64(len(series.Dates)), Step: "maintenance_dates"}
	if err := executor.persistProgress(ctx, run, progress, nil, &progressWrites); err != nil {
		return ownershipFailure(err, "persist_run_progress")
	}
	var first error
	firstClass := ""
	for _, requested := range series.Dates {
		if err := ctx.Err(); err != nil {
			if result, cancelled := cancellationFailure(ctx, "maintenance_dates", err); cancelled {
				return result
			}
			return sourceFailure(ctx, err, "maintenance_dates")
		}
		progress.Started++
		rows, err := executor.fetchAndSaveMaintenance(ctx, run, *job.Maintenance, requested)
		if err != nil {
			if errors.Is(err, ingestionrun.ErrOwnershipLost) {
				return ownershipFailure(err, "persist_maintenance")
			}
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return sourceFailure(ctx, err, "maintenance_dates")
			}
			progress.Failed++
			class := maintenanceErrorClass(err)
			primary := first == nil
			if first == nil {
				first = err
				firstClass = class
			}
			if class == "source" {
				sourceCtx := diagnosticScope(ctx, "source", "maintenance_dates", "maintenance_dates", requested.String(), "")
				recordSourceDiagnostic(sourceCtx, err, true, false)
				if progressErr := executor.persistProgress(ctx, run, progress, nil, &progressWrites); progressErr != nil {
					return ownershipFailure(progressErr, "persist_run_progress")
				}
				return sourceFailure(ctx, err, "maintenance_dates")
			}
			if class == "source_contract" {
				parserCtx := diagnosticScope(ctx, "source_contract", "parse_maintenance_csv", "parse_maintenance_csv", requested.String(), "")
				recordParserDiagnostic(parserCtx, err, primary)
			} else if class == "persistence" {
				persistCtx := diagnosticScope(ctx, "persistence", "persist_maintenance", "persist_maintenance", requested.String(), "")
				recordPersistenceDiagnostic(persistCtx, err, primary)
			}
		} else {
			progress.Succeeded++
			progress.Rows += rows
		}
		if progressErr := executor.persistProgress(ctx, run, progress, nil, &progressWrites); progressErr != nil {
			return ownershipFailure(progressErr, "persist_run_progress")
		}
	}
	if first != nil {
		return failed(firstClass, maintenanceFailureMessage(firstClass), "maintenance_dates", first)
	}
	return Result{Status: ingestionrun.StatusSucceeded, BusinessComplete: true}
}

func (executor *Executor) fetchAndSaveMaintenance(ctx context.Context, run ingestionrun.Run, definition ingestion.MaintenanceDefinition, requested ingestion.CalendarDate) (uint64, error) {
	if requested.IsZero() {
		return 0, &maintenanceDateError{class: "contract", cause: fmt.Errorf("maintenance requested date is required")}
	}
	folderKind := "daily"
	if definition.Kind == ingestion.MaintenanceCBR {
		folderKind = "cbr"
	}
	folder := folderKind + "/" + strings.ReplaceAll(requested.String(), "-", "")
	sourceCtx := diagnosticScope(ctx, "source", "list_maintenance_reports", "list_maintenance_reports", requested.String(), "")
	files, err := executor.client.ListMaintenanceReportFiles(sourceCtx, folder)
	if err != nil {
		var source *fincloud.Error
		if !errors.As(err, &source) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return 0, &maintenanceDateError{class: "source_contract", cause: err}
		}
		return 0, err
	}
	expectedRoot := path.Join("/app/report", folder) + "/"
	for _, sourcePath := range files {
		if !strings.HasPrefix(path.Clean(sourcePath), expectedRoot) {
			return 0, &maintenanceDateError{class: "source_contract", cause: fmt.Errorf("maintenance report path does not match requested date %s", requested)}
		}
	}
	selection, err := selectMaintenanceSources(definition, files)
	if err != nil {
		return 0, &maintenanceDateError{class: "source_contract", cause: err}
	}
	if len(selection.paths) == 0 {
		return 0, &maintenanceDateError{class: "date_local", cause: fmt.Errorf("registered maintenance file was not found for requested date %s", requested)}
	}
	if definition.Key == detailOutstandingDefinitionKey && executor.logger != nil {
		names := make([]string, len(selection.paths))
		for index, sourcePath := range selection.paths {
			names[index] = path.Base(sourcePath)
		}
		executor.logger.InfoContext(ctx, "maintenance source selected", "job_key", definition.Key, "requested_date", requested.String(),
			"source_mode", selection.mode, "source_files", names)
	}
	var combined ingestion.ParsedMaintenanceCSV
	logicalRowNumber := 2
	for _, sourcePath := range selection.paths {
		fileName := path.Base(sourcePath)
		downloadCtx := diagnosticScope(ctx, "source", "download_maintenance_report", "download_maintenance_report", requested.String(), "")
		content, err := executor.client.DownloadMaintenanceReport(downloadCtx, fileName, path.Dir(sourcePath))
		if err != nil {
			if selection.mode == maintenanceSourceSplit {
				err = fmt.Errorf("download split maintenance file %s: %w", fileName, err)
			}
			return 0, err
		}
		parsed, err := ingestion.ParseMaintenanceCSV(diagnosticScope(ctx, "source_contract", "parse_maintenance_csv", "parse_maintenance_csv", requested.String(), ""), definition, requested, content)
		if err != nil {
			if selection.mode == maintenanceSourceSplit {
				err = fmt.Errorf("parse split maintenance file %s: %w", fileName, err)
			}
			return 0, &maintenanceDateError{class: "source_contract", cause: err}
		}
		if parsed.AsOfDate != requested {
			return 0, &maintenanceDateError{class: "source_contract", cause: fmt.Errorf("maintenance report date does not match requested date %s", requested)}
		}
		if err := appendMaintenanceSource(&combined, parsed, fileName, selection.mode, &logicalRowNumber); err != nil {
			return 0, &maintenanceDateError{class: "source_contract", cause: err}
		}
	}
	persistCtx := diagnosticScope(ctx, "persistence", "persist_maintenance", "persist_maintenance", requested.String(), "")
	if err := executor.maintenance.SaveSnapshot(persistCtx, run.ID, run.OwnerID, ingestionstore.MaintenanceSnapshot{
		RequestedDate: requested, FileName: selection.logicalFileName, Parsed: combined,
	}); err != nil {
		return 0, &maintenanceDateError{class: "persistence", cause: err}
	}
	return uint64(len(combined.Rows)), nil
}

func appendMaintenanceSource(combined *ingestion.ParsedMaintenanceCSV, parsed ingestion.ParsedMaintenanceCSV, fileName, mode string, logicalRowNumber *int) error {
	if combined.Columns == nil {
		*combined = parsed
		combined.Rows = nil
	} else if !slices.Equal(combined.Columns, parsed.Columns) {
		return fmt.Errorf("DetailOutstandingRekeningPinjaman split header mismatch in %s", fileName)
	}
	for _, row := range parsed.Rows {
		row.SourceFileName = fileName
		if mode == maintenanceSourceSplit {
			row.SourceRowNumber = *logicalRowNumber
			*logicalRowNumber = *logicalRowNumber + 1
		}
		combined.Rows = append(combined.Rows, row)
	}
	return nil
}

func selectMaintenanceSources(definition ingestion.MaintenanceDefinition, files []string) (maintenanceSourceSelection, error) {
	if definition.Key == detailOutstandingDefinitionKey {
		return selectDetailOutstandingSources(definition, files)
	}
	for _, sourcePath := range files {
		disposition, candidate := ingestion.ClassifyMaintenanceFile(definition.Kind, path.Base(sourcePath))
		if disposition == ingestion.MaintenanceRegistered && candidate != nil && candidate.Key == definition.Key {
			return maintenanceSourceSelection{mode: maintenanceSourceMerged, logicalFileName: path.Base(sourcePath), paths: []string{sourcePath}}, nil
		}
	}
	return maintenanceSourceSelection{}, nil
}

func selectDetailOutstandingSources(definition ingestion.MaintenanceDefinition, files []string) (maintenanceSourceSelection, error) {
	var merged, splitLooking []string
	canonical := make(map[string]string, len(detailOutstandingBranches))
	duplicateBranches := make(map[string][]string)
	for _, sourcePath := range files {
		fileName := path.Base(sourcePath)
		if definition.FilePattern.MatchString(fileName) {
			merged = append(merged, sourcePath)
			continue
		}
		match := detailOutstandingSplitFile.FindStringSubmatch(fileName)
		if match == nil {
			continue
		}
		splitLooking = append(splitLooking, sourcePath)
		branch := match[1]
		if !slices.Contains(detailOutstandingBranches, branch) {
			continue
		}
		if previous, exists := canonical[branch]; exists {
			duplicateBranches[branch] = []string{previous, sourcePath}
			continue
		}
		canonical[branch] = sourcePath
	}
	if len(merged) > 0 && len(splitLooking) > 0 {
		return maintenanceSourceSelection{}, fmt.Errorf("invalid DetailOutstandingRekeningPinjaman source layout: merged and split files coexist")
	}
	if len(merged) > 1 {
		return maintenanceSourceSelection{}, fmt.Errorf("duplicate DetailOutstandingRekeningPinjaman merged source: %s and %s", merged[0], merged[1])
	}
	for _, branch := range detailOutstandingBranches {
		if duplicates := duplicateBranches[branch]; len(duplicates) > 0 {
			return maintenanceSourceSelection{}, fmt.Errorf("duplicate DetailOutstandingRekeningPinjaman split branch %s: %s and %s", branch, duplicates[0], duplicates[1])
		}
	}
	if len(merged) == 1 {
		return maintenanceSourceSelection{mode: maintenanceSourceMerged, logicalFileName: detailOutstandingMergedFile, paths: merged}, nil
	}
	if len(canonical) == 0 {
		return maintenanceSourceSelection{}, nil
	}
	missing := make([]string, 0, len(detailOutstandingBranches)-len(canonical))
	paths := make([]string, 0, len(detailOutstandingBranches))
	for _, branch := range detailOutstandingBranches {
		sourcePath, exists := canonical[branch]
		if !exists {
			missing = append(missing, branch)
			continue
		}
		paths = append(paths, sourcePath)
	}
	if len(missing) > 0 {
		return maintenanceSourceSelection{}, fmt.Errorf("incomplete DetailOutstandingRekeningPinjaman split source; missing branches: %s", strings.Join(missing, ", "))
	}
	return maintenanceSourceSelection{mode: maintenanceSourceSplit, logicalFileName: detailOutstandingMergedFile, paths: paths}, nil
}

func maintenanceErrorClass(err error) string {
	var failure *maintenanceDateError
	if errors.As(err, &failure) {
		return failure.class
	}
	if sourceWide(err) {
		return "source"
	}
	return "date_local"
}

func maintenanceFailureMessage(class string) string {
	switch class {
	case "source_contract":
		return "maintenance report contract validation failed"
	case "persistence":
		return "maintenance snapshot persistence failed"
	case "contract":
		return "invalid maintenance execution contract"
	default:
		return "one or more maintenance dates failed"
	}
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
	case errors.Is(cause, ingestionrun.ErrOwnershipLost), errors.Is(cause, ingestionrun.ErrLeaseUnproven),
		errors.Is(primary, ingestionrun.ErrOwnershipLost), errors.Is(primary, ingestionrun.ErrLeaseUnproven):
		return ownershipFailure(errors.Join(primary, cause), step), true
	default:
		return Result{}, false
	}
}

func ownershipFailure(err error, step string) Result {
	return failed("ownership", "execution ownership was lost", step, err)
}

func suppressCleanupCancellation(ctx context.Context, err error) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	cause := context.Cause(ctx)
	return ctx.Err() == nil || errors.Is(cause, ingestionrun.ErrCancellationRequested) || errors.Is(cause, ingestionrun.ErrCoordinatorShutdown)
}

func failed(class, message, step string, cause error) Result {
	return Result{Status: ingestionrun.StatusFailed, Error: ingestionrun.SafeError{Class: class, Message: message, Step: step}, Cause: cause}
}

func cancelled(message, step string, cause error) Result {
	return Result{Status: ingestionrun.StatusCancelled, Error: ingestionrun.SafeError{Class: "cancelled", Message: message, Step: step}, Cause: cause}
}
