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

	mu    sync.Mutex
	local map[uint64]context.CancelCauseFunc
}

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
	return &Coordinator{runs: runs, executor: executor, details: details, ownerID: ownerID, workers: settings.MaxRunningJobs, logger: logger, local: map[uint64]context.CancelCauseFunc{}}, nil
}

func (coordinator *Coordinator) OwnerID() string { return coordinator.ownerID }

func (coordinator *Coordinator) Submit(ctx context.Context, jobKey string, parameters ingestionrun.Parameters, trigger ingestionrun.Trigger, reference string, requester *uint64) (uint64, error) {
	return coordinator.runs.Submit(ctx, jobKey, parameters, trigger, reference, requester)
}

func (coordinator *Coordinator) SubmitInTx(ctx context.Context, tx *sqlx.Tx, jobKey string, parameters ingestionrun.Parameters, trigger ingestionrun.Trigger, reference string, requester *uint64) (uint64, error) {
	return coordinator.runs.SubmitInTx(ctx, tx, jobKey, parameters, trigger, reference, requester)
}

func (coordinator *Coordinator) SubmitRunAll(ctx context.Context, from, to ingestion.CalendarDate, trigger ingestionrun.Trigger, reference string, requester *uint64) (uint64, error) {
	return coordinator.runs.CreateRunAll(ctx, from, to, trigger, reference, requester)
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
	go func() { defer wait.Done(); coordinator.supervise(ctx) }()
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
		if err := coordinator.details.CleanupTerminal(cleanupCtx, 100); err != nil && ctx.Err() == nil {
			coordinator.logger.Warn("clean terminal Detail staging", "error", err)
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
		run, err := coordinator.runs.Claim(ctx, coordinator.ownerID)
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
		result := coordinator.executor.Execute(runCtx, *run, coordinator.ownerID)
		coordinator.mu.Lock()
		delete(coordinator.local, run.ID)
		coordinator.mu.Unlock()
		cancel(nil)
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = coordinator.runs.Finish(finishCtx, run.ID, coordinator.ownerID, result.Status, result.Error)
		finishCancel()
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

func (coordinator *Coordinator) supervise(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := coordinator.runs.HeartbeatAndCancellations(ctx, coordinator.ownerID)
			if err != nil {
				coordinator.logger.Error("supervise ingestion runs", "error", err)
				continue
			}
			coordinator.mu.Lock()
			for _, id := range ids {
				if cancel := coordinator.local[id]; cancel != nil {
					cancel(ingestionrun.ErrCancellationRequested)
				}
			}
			coordinator.mu.Unlock()
		}
	}
}

func (coordinator *Coordinator) reconcile(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				changed, err := coordinator.runs.ReconcileOneParent(ctx)
				if err != nil {
					coordinator.logger.Error("reconcile Run All", "error", err)
					break
				}
				if !changed {
					break
				}
			}
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
