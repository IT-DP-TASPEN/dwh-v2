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
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionexec"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Coordinator struct {
	runs     *ingestionrun.Repository
	executor *ingestionexec.Executor
	details  *ingestionstore.DetailRepository
	ownerID  string
	workers  int
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

func New(ctx context.Context, db *sqlx.DB, client *fincloud.Client, logger *slog.Logger) (*Coordinator, error) {
	if db == nil || client == nil || logger == nil {
		return nil, fmt.Errorf("database, Fincloud client, and logger are required")
	}
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		return nil, err
	}
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		return nil, err
	}
	settings, err := runs.RuntimeSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load ingestion runtime settings: %w", err)
	}
	ownerID, err := ingestionrun.NewOwnerID()
	if err != nil {
		return nil, err
	}
	details := ingestionstore.NewDetailRepository(db)
	executor, err := ingestionexec.New(client, ingestionstore.NewFixedRepository(db), details,
		ingestionstore.NewMaintenanceRepository(db), runs, catalog, settings.FixedMemberConcurrency, settings.DetailConcurrency, logger)
	if err != nil {
		return nil, err
	}
	return &Coordinator{runs: runs, executor: executor, details: details, ownerID: ownerID, workers: settings.MaxRunningJobs, logger: logger,
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
	return coordinator.runs.RequestCancellation(ctx, runID, reason, requester)
}

func (coordinator *Coordinator) RecoverAbandoned(ctx context.Context, runID uint64, expectedOwner string, expectedHeartbeat time.Time, reason string, requester securityctx.Requester) error {
	return coordinator.runs.RecoverAbandoned(ctx, runID, expectedOwner, expectedHeartbeat, reason, requester)
}

func (coordinator *Coordinator) Run(ctx context.Context) {
	executionCtx, stopExecution := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopExecution(ingestionrun.ErrCoordinatorShutdown)
	var wait sync.WaitGroup
	for range coordinator.workers {
		wait.Add(1)
		go func() { defer wait.Done(); coordinator.worker(ctx, executionCtx) }()
	}
	wait.Add(3)
	go func() { defer wait.Done(); coordinator.recoverStale(ctx) }()
	go func() { defer wait.Done(); coordinator.reconcile(ctx) }()
	go func() { defer wait.Done(); coordinator.cleanupDetailStaging(ctx) }()
	<-ctx.Done()
	stopExecution(ingestionrun.ErrCoordinatorShutdown)
	wait.Wait()
}

func (coordinator *Coordinator) cleanupDetailStaging(ctx context.Context) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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

func (coordinator *Coordinator) worker(ctx, executionCtx context.Context) {
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
		runCtx, cancel := context.WithCancelCause(executionCtx)
		coordinator.mu.Lock()
		coordinator.local[run.ID] = cancel
		coordinator.mu.Unlock()
		heartbeatDone := make(chan struct{})
		go coordinator.heartbeat(runCtx, cancel, *run, heartbeatDone)
		result := coordinator.executor.Execute(runCtx, *run, attemptOwner)
		coordinator.mu.Lock()
		delete(coordinator.local, run.ID)
		coordinator.mu.Unlock()
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = coordinator.runs.Finish(finishCtx, run.ID, attemptOwner, result.Status, result.Error)
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
			coordinator.logger.Error("finish ingestion run", "run_id", run.ID, "job_key", run.JobKey, "error", err)
		}
		if result.Cause != nil {
			attributes := []any{"run_id", run.ID, "job_key", run.JobKey, "class", result.Error.Class, "error", result.Cause}
			if causeType := fincloud.SafeCauseClass(result.Cause); causeType != "" {
				attributes = append(attributes, "cause_type", causeType)
			}
			coordinator.logger.Warn("ingestion run completed with error", attributes...)
		}
	}
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
