package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/fincloudauth"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionexec"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

// runExecutor is the narrow execution seam the dispatcher depends on.
type runExecutor interface {
	Execute(context.Context, ingestionrun.Run, string) ingestionexec.Result
}

type Coordinator struct {
	runs     *ingestionrun.Repository
	executor runExecutor
	fixed    *ingestionstore.FixedRepository
	details  *ingestionstore.DetailRepository
	masters  *ingestionstore.MasterRepository
	catalog  ingestion.Catalog
	ownerID  string
	logger   *slog.Logger

	mu      sync.Mutex
	local   map[uint64]context.CancelCauseFunc
	parents map[uint64]string
}

const (
	ingestionHeartbeatInterval = 5 * time.Second
	ingestionLease             = 2 * time.Minute
	ingestionRecoveryInterval  = 40 * time.Second
	ingestionRecoveryBatch     = 256
)

func New(db *sqlx.DB, sessions *fincloud.SessionCoordinator, authProfiles *fincloudauth.Repository, logger *slog.Logger) (*Coordinator, error) {
	if db == nil || sessions == nil || authProfiles == nil || logger == nil {
		return nil, fmt.Errorf("database, Fincloud sessions, Auth Profiles, and logger are required")
	}
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		return nil, err
	}
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		return nil, err
	}
	ownerID, err := ingestionrun.NewOwnerID()
	if err != nil {
		return nil, err
	}
	fixed := ingestionstore.NewFixedRepository(db)
	details := ingestionstore.NewDetailRepository(db)
	masters := ingestionstore.NewMasterRepository(db)
	executor, err := ingestionexec.New(sessions, authProfiles, fixed, details, masters,
		ingestionstore.NewMaintenanceRepository(db), runs, catalog, logger)
	if err != nil {
		return nil, err
	}
	return &Coordinator{runs: runs, executor: executor, catalog: catalog, fixed: fixed, details: details, masters: masters, ownerID: ownerID, logger: logger,
		local: map[uint64]context.CancelCauseFunc{}, parents: map[uint64]string{}}, nil
}

func (coordinator *Coordinator) OwnerID() string { return coordinator.ownerID }

func (coordinator *Coordinator) Submit(ctx context.Context, jobKey string, parameters ingestionrun.Parameters, trigger ingestionrun.Trigger, reference string, requester *uint64) (uint64, error) {
	return coordinator.runs.Submit(ctx, jobKey, parameters, trigger, reference, requester)
}

func (coordinator *Coordinator) SubmitManual(ctx context.Context, jobKey string, parameters ingestionrun.Parameters, trigger ingestionrun.Trigger, reference string, requester securityctx.Requester) (uint64, error) {
	return coordinator.runs.SubmitManual(ctx, jobKey, parameters, trigger, reference, requester)
}

func (coordinator *Coordinator) SubmitInTx(ctx context.Context, tx *sqlx.Tx, jobKey string, parameters ingestionrun.Parameters, trigger ingestionrun.Trigger, reference string, requester *uint64) (uint64, error) {
	return coordinator.runs.SubmitInTx(ctx, tx, jobKey, parameters, trigger, reference, requester)
}

func (coordinator *Coordinator) SubmitRunAll(ctx context.Context, from, to ingestion.CalendarDate, trigger ingestionrun.Trigger, reference string, requester *uint64) (uint64, error) {
	id, err := coordinator.runs.CreateRunAll(ctx, from, to, trigger, reference, requester)
	if err == nil {
		coordinator.registerParent(ctx, id)
	}
	return id, err
}

func (coordinator *Coordinator) SubmitRunAllManual(ctx context.Context, from, to ingestion.CalendarDate, trigger ingestionrun.Trigger, reference string, requester securityctx.Requester) (uint64, error) {
	id, err := coordinator.runs.CreateRunAllManual(ctx, from, to, trigger, reference, requester)
	if err == nil {
		coordinator.registerParent(ctx, id)
	}
	return id, err
}

func (coordinator *Coordinator) registerParent(ctx context.Context, id uint64) {
	run, err := coordinator.runs.Get(ctx, id)
	if err != nil || run.Kind != ingestionrun.KindRunAllParent || run.Status != ingestionrun.StatusRunning || run.OwnerID == "" {
		coordinator.logger.Warn("register Run All parent ownership", "run_id", id, "error", err)
		return
	}
	coordinator.mu.Lock()
	coordinator.parents[id] = run.OwnerID
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) Cancel(ctx context.Context, runID uint64, reason string, requester securityctx.Requester) error {
	applied, err := coordinator.runs.RequestCancellation(ctx, runID, reason, requester)
	if err == nil && !applied {
		return ingestionrun.ErrTransition
	}
	return err
}

func (coordinator *Coordinator) RuntimeSettings(ctx context.Context) (ingestionrun.RuntimeSettings, error) {
	return coordinator.runs.RuntimeSettings(ctx)
}

func (coordinator *Coordinator) UpdateRuntimeSettings(ctx context.Context, expected, target ingestionrun.RuntimeSettings, requester securityctx.Requester) (bool, error) {
	return coordinator.runs.UpdateRuntimeSettings(ctx, expected, target, requester)
}

func (coordinator *Coordinator) RunningJobs(ctx context.Context) (int, error) {
	return coordinator.runs.RunningJobs(ctx)
}

func (coordinator *Coordinator) RecoverAbandoned(ctx context.Context, runID uint64, expectedOwner string, expectedHeartbeat time.Time, reason string, requester securityctx.Requester) error {
	return coordinator.runs.RecoverAbandoned(ctx, runID, expectedOwner, expectedHeartbeat, reason, requester)
}

// Run starts one claim dispatcher plus background maintenance. Admission
// capacity is decided by Claim against the live database limit, so there is no
// process-local worker ceiling and no pool of idle pollers.
func (coordinator *Coordinator) Run(ctx context.Context) {
	executionCtx, stopExecution := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopExecution(ingestionrun.ErrCoordinatorShutdown)
	var wait, active sync.WaitGroup
	wait.Add(6)
	go func() { defer wait.Done(); coordinator.dispatch(ctx, executionCtx, &active) }()
	go func() { defer wait.Done(); coordinator.recoverStale(ctx) }()
	go func() { defer wait.Done(); coordinator.reconcile(ctx) }()
	go func() { defer wait.Done(); coordinator.cleanupDetailStaging(ctx) }()
	go func() { defer wait.Done(); coordinator.cleanupFixedStaging(ctx) }()
	go func() { defer wait.Done(); coordinator.cleanupMasterStaging(ctx) }()
	<-ctx.Done()
	stopExecution(ingestionrun.ErrCoordinatorShutdown)
	// The dispatcher is the only caller of active.Add; once wait returns no new
	// execution can start, so waiting on active cannot race an Add.
	wait.Wait()
	active.Wait()
}

func (coordinator *Coordinator) cleanupMasterStaging(ctx context.Context) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		for cleanupCtx.Err() == nil {
			deleted, err := coordinator.masters.CleanupTerminal(cleanupCtx, 100)
			if err != nil {
				if ctx.Err() == nil {
					coordinator.logger.Warn("clean terminal Master staging", "error", err)
				}
				return
			}
			if deleted == 0 {
				return
			}
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func (coordinator *Coordinator) cleanupFixedStaging(ctx context.Context) {
	cleanup := func() {
		started := time.Now()
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		total := ingestionstore.FixedCleanupResult{RowsByTable: map[string]int64{}}
		for cleanupCtx.Err() == nil {
			result, err := coordinator.fixed.CleanupTerminal(cleanupCtx, 100)
			total.Candidates += result.Candidates
			total.Loads += result.Loads
			total.Rows += result.Rows
			for table, rows := range result.RowsByTable {
				total.RowsByTable[table] += rows
			}
			if err != nil {
				if ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
					coordinator.logger.Warn("clean terminal Fixed staging", "candidate_loads", total.Candidates,
						"loads_cleaned", total.Loads, "rows_deleted", total.Rows,
						"duration_ms", time.Since(started).Milliseconds(), "diagnostic", ingestionstore.TechnicalDiagnostic(err))
				}
				return
			}
			if result.Candidates == 0 {
				break
			}
		}
		if total.Candidates > 0 {
			coordinator.logger.Info("cleaned terminal Fixed staging", "candidate_loads", total.Candidates,
				"loads_cleaned", total.Loads, "rows_deleted", total.Rows, "rows_by_table", total.RowsByTable,
				"duration_ms", time.Since(started).Milliseconds())
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func (coordinator *Coordinator) cleanupDetailStaging(ctx context.Context) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		for cleanupCtx.Err() == nil {
			deleted, err := coordinator.details.CleanupTerminal(cleanupCtx, 100)
			if err != nil {
				if ctx.Err() == nil {
					coordinator.logger.Warn("clean terminal Detail staging", "error", err)
				}
				return
			}
			if deleted == 0 {
				return
			}
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func (coordinator *Coordinator) dispatch(ctx, executionCtx context.Context, active *sync.WaitGroup) {
	for ctx.Err() == nil {
		attemptOwner, ownerErr := ingestionrun.NewOwnerID()
		if ownerErr != nil {
			coordinator.logger.Error("create ingestion owner", "error", ownerErr)
			wait(ctx, time.Second)
			continue
		}
		run, err := coordinator.runs.Claim(ctx, attemptOwner)
		if err != nil {
			if ctx.Err() == nil {
				coordinator.logger.Error("claim ingestion run", "error", err)
			}
			wait(ctx, 500*time.Millisecond)
			continue
		}
		if run == nil {
			wait(ctx, 250*time.Millisecond)
			continue
		}
		active.Add(1)
		go func() { defer active.Done(); coordinator.executeClaimedRun(ctx, executionCtx, *run, attemptOwner) }()
	}
}

func (coordinator *Coordinator) executeClaimedRun(ctx, executionCtx context.Context, run ingestionrun.Run, attemptOwner string) {
	started := time.Now()
	logger := coordinator.logger.With("run_id", run.ID, "job_key", run.JobKey)
	job, _ := coordinator.catalog.Find(run.JobKey)
	startAttributes := []any{"event", "ingestion.run.started", "category", job.Category, "kind", run.Kind, "trigger", run.Trigger}
	if run.ParentRunID != nil {
		startAttributes = append(startAttributes, "parent_run_id", *run.ParentRunID)
	}
	logger.Info("ingestion run started", startAttributes...)
	runCtx, cancel := context.WithCancelCause(executionCtx)
	coordinator.mu.Lock()
	coordinator.local[run.ID] = cancel
	coordinator.mu.Unlock()
	heartbeatDone := make(chan struct{})
	go coordinator.heartbeat(runCtx, cancel, run, heartbeatDone)
	result := coordinator.executor.Execute(runCtx, run, attemptOwner)
	coordinator.mu.Lock()
	delete(coordinator.local, run.ID)
	coordinator.mu.Unlock()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err := coordinator.runs.Finish(finishCtx, run.ID, attemptOwner, result.Status, result.Error)
	finishCancel()
	if errors.Is(err, ingestionrun.ErrTransition) && result.Status == ingestionrun.StatusSucceeded {
		finishCtx, finishCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = coordinator.runs.Finish(finishCtx, run.ID, attemptOwner, ingestionrun.StatusCancelled,
			ingestionrun.SafeError{Class: "cancelled", Message: "run cancellation requested", Step: "finish"})
		finishCancel()
	}
	cancel(nil)
	<-heartbeatDone
	if run.ParentRunID != nil {
		coordinator.reconcileOwnedParent(ctx, *run.ParentRunID)
	}
	if err != nil && !errors.Is(err, ingestionrun.ErrTransition) {
		logger.Error("finish ingestion run", "error", err)
	}
	coordinator.logRunCompleted(ctx, logger, run, job.Category, result, time.Since(started))
}

// logRunCompleted emits the single terminal lifecycle record for a run attempt.
// Status and progress come from the persisted row, so publication-owned
// finishes, cancellation fallbacks, and stale recovery all report the
// canonical outcome. It only reads; it never affects Finish.
func (coordinator *Coordinator) logRunCompleted(ctx context.Context, logger *slog.Logger, run ingestionrun.Run, category ingestion.JobCategory, result ingestionexec.Result, duration time.Duration) {
	status, progress := result.Status, run.Progress
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	persisted, err := coordinator.runs.Get(readCtx, run.ID)
	cancel()
	if err == nil {
		progress = persisted.Progress
		if ingestionrun.IsTerminal(persisted.Status) {
			status = persisted.Status
		}
	}
	attributes := []any{"event", "ingestion.run.completed", "category", category, "status", status, "duration_ms", duration.Milliseconds(),
		"rows", progress.Rows, "total", progress.Total, "succeeded", progress.Succeeded, "failed", progress.Failed}
	level := slog.LevelInfo
	if status != ingestionrun.StatusSucceeded && status != ingestionrun.StatusCancelled {
		level = slog.LevelWarn
		if status == ingestionrun.StatusFailed {
			level = slog.LevelError
		}
		attributes = append(attributes, "error_class", result.Error.Class, "error_step", result.Error.Step)
		if causeType := fincloud.SafeCauseClass(result.Cause); causeType != "" {
			attributes = append(attributes, "cause_type", causeType)
		}
	} else if result.Cause != nil {
		attributes = append(attributes, "error_class", result.Error.Class, "error_step", result.Error.Step)
	}
	logger.Log(ctx, level, "ingestion run completed", attributes...)
}

func (coordinator *Coordinator) heartbeat(ctx context.Context, cancel context.CancelCauseFunc, run ingestionrun.Run, done chan<- struct{}) {
	defer close(done)
	lastProof := time.Now()
	ticker := time.NewTicker(ingestionHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, heartbeatCancel := context.WithTimeout(ctx, 5*time.Second)
			state, err := coordinator.runs.Heartbeat(heartbeatCtx, run.ID, run.OwnerID)
			heartbeatCancel()
			if err != nil {
				coordinator.logger.Warn("heartbeat ingestion run", "run_id", run.ID, "error", err)
				if time.Since(lastProof) >= ingestionLease {
					cancel(ingestionrun.ErrLeaseUnproven)
					return
				}
				continue
			}
			if !state.Owned {
				cancel(ingestionrun.ErrOwnershipLost)
				return
			}
			lastProof = time.Now()
			if state.CancelRequested {
				cancel(ingestionrun.ErrCancellationRequested)
				return
			}
		}
	}
}

func (coordinator *Coordinator) recoverStale(ctx context.Context) {
	coordinator.recoverStaleSweep(ctx)
	ticker := time.NewTicker(ingestionRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			coordinator.recoverStaleSweep(ctx)
		}
	}
}

func (coordinator *Coordinator) recoverStaleSweep(ctx context.Context) int {
	defer coordinator.reconcileOwnedParents(ctx)
	recovered := 0
	for range ingestionRecoveryBatch {
		owner, err := ingestionrun.NewOwnerID()
		if err != nil {
			coordinator.logger.Error("create recovery owner", "error", err)
			return recovered
		}
		recoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		found, err := coordinator.runs.RecoverOneStale(recoveryCtx, ingestionLease, owner)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				coordinator.logger.Error("recover stale ingestion run", "error", err)
			}
			return recovered
		}
		if found == nil {
			return recovered
		}
		recovered++
		coordinator.mu.Lock()
		if found.Kind == ingestionrun.KindRunAllParent {
			coordinator.parents[found.RunID] = found.NewOwner
		}
		if stop := coordinator.local[found.RunID]; stop != nil {
			stop(ingestionrun.ErrOwnershipLost)
		}
		coordinator.mu.Unlock()
		jobKey := found.JobKey
		if jobKey == "" {
			jobKey = "run_all_parent"
		}
		diagnosticCtx, diagnosticCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = coordinator.runs.AppendTechnicalEvent(diagnosticCtx, ingestionrun.TechnicalEvent{
			RunID: found.RunID, Severity: "warning", EventKind: "recovery", Recovered: boolPointer(true),
			Class: "ownership", Step: "recover_stale", Operation: "recover_stale", JobKey: jobKey,
			ErrorMessage: "Stale execution ownership recovered automatically.",
		})
		diagnosticCancel()
		if err != nil {
			coordinator.logger.Warn("persist stale recovery diagnostic", "run_id", found.RunID, "error", err)
		}
	}
	return recovered
}

func boolPointer(value bool) *bool { return &value }

func (coordinator *Coordinator) reconcile(ctx context.Context) {
	ticker := time.NewTicker(ingestionHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			coordinator.reconcileOwnedParents(ctx)
		}
	}
}

func (coordinator *Coordinator) reconcileOwnedParents(ctx context.Context) {
	coordinator.mu.Lock()
	parents := make(map[uint64]string, len(coordinator.parents))
	for id, owner := range coordinator.parents {
		parents[id] = owner
	}
	coordinator.mu.Unlock()
	for id, owner := range parents {
		coordinator.reconcileParent(ctx, id, owner)
	}
}

func (coordinator *Coordinator) reconcileOwnedParent(ctx context.Context, id uint64) {
	coordinator.mu.Lock()
	owner := coordinator.parents[id]
	coordinator.mu.Unlock()
	if owner != "" {
		coordinator.reconcileParent(ctx, id, owner)
	}
}

func (coordinator *Coordinator) reconcileParent(ctx context.Context, id uint64, owner string) {
	for ctx.Err() == nil {
		changed, err := coordinator.runs.ReconcileParent(ctx, id, owner)
		if errors.Is(err, ingestionrun.ErrOwnershipLost) {
			coordinator.mu.Lock()
			if coordinator.parents[id] == owner {
				delete(coordinator.parents, id)
			}
			coordinator.mu.Unlock()
			return
		}
		if err != nil {
			coordinator.logger.Error("reconcile Run All", "run_id", id, "error", err)
			return
		}
		if !changed {
			return
		}
	}
}

func wait(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
