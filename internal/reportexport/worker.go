package reportexport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ibldzn/go-admin/internal/reporting"
)

type WorkerConfig struct {
	Concurrency       int
	ExportTimeout     time.Duration
	HeartbeatInterval time.Duration
	StaleAfter        time.Duration
	Retention         time.Duration
	CleanupInterval   time.Duration
	OrphanGrace       time.Duration
}

type Worker struct {
	repository    *Repository
	reports       *reporting.Repository
	pools         *reporting.PoolManager
	storage       *Storage
	engine        reporting.QueryEngine
	config        WorkerConfig
	owner         string
	logger        *slog.Logger
	heartbeatFunc func(context.Context, uint64, string, uint32, time.Time) (bool, error)
}

func NewWorker(repository *Repository, reports *reporting.Repository, pools *reporting.PoolManager, storage *Storage, config WorkerConfig, logger *slog.Logger) (*Worker, error) {
	if repository == nil || reports == nil || pools == nil || storage == nil || config.Concurrency < 1 || config.ExportTimeout <= 0 || config.HeartbeatInterval <= 0 || config.StaleAfter <= config.HeartbeatInterval || config.Retention <= 0 || config.CleanupInterval <= 0 || config.OrphanGrace <= config.ExportTimeout {
		return nil, fmt.Errorf("export worker dependencies and bounds are required")
	}
	ownerBytes := make([]byte, 32)
	if _, err := rand.Read(ownerBytes); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{repository: repository, reports: reports, pools: pools, storage: storage, config: config, owner: hex.EncodeToString(ownerBytes), logger: logger, heartbeatFunc: repository.Heartbeat}, nil
}

func (worker *Worker) OwnerID() string { return worker.owner }

func (worker *Worker) Run(ctx context.Context) {
	var group sync.WaitGroup
	for index := 0; index < worker.config.Concurrency; index++ {
		group.Add(1)
		go func() { defer group.Done(); worker.claimLoop(ctx) }()
	}
	group.Add(2)
	go func() { defer group.Done(); worker.staleLoop(ctx) }()
	go func() { defer group.Done(); worker.cleanupLoop(ctx) }()
	group.Wait()
}

func (worker *Worker) claimLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		job, err := worker.repository.Claim(ctx, worker.owner, time.Now().UTC())
		if err != nil && !errors.Is(err, context.Canceled) {
			worker.logger.ErrorContext(ctx, "claim report export", "error", err)
		}
		if job != nil {
			worker.execute(ctx, *job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (worker *Worker) execute(parent context.Context, job Job) {
	timeoutContext, timeoutCancel := context.WithTimeout(parent, worker.config.ExportTimeout)
	attemptContext, cancelAttempt := context.WithCancelCause(timeoutContext)
	heartbeatDone := make(chan struct{})
	go worker.heartbeat(attemptContext, cancelAttempt, job, heartbeatDone)
	var workspace, published string
	stage := "eligibility"
	err := func() error {
		eligible, err := worker.repository.EligibleForExecution(attemptContext, job.SubmittedByUserID, job.ReportID, job.DatasourceID)
		if err != nil {
			return err
		}
		if !eligible {
			return fmt.Errorf("access revoked")
		}
		var snapshot Snapshot
		if err := json.Unmarshal(job.ParametersJSON, &snapshot); err != nil || snapshot.Version != 1 {
			return fmt.Errorf("invalid export parameter snapshot")
		}
		datasource, err := worker.reports.FindDatasource(attemptContext, job.DatasourceID)
		if err != nil {
			return err
		}
		if datasource.Status != reporting.StatusActive {
			return reporting.ErrInactive
		}
		database, err := worker.pools.Database(attemptContext, datasource, false)
		if err != nil {
			return err
		}
		workspace, err = worker.storage.Workspace(job.ID, job.Attempt)
		if err != nil {
			return err
		}
		stage = "workbook"
		sink, err := NewWorkbookSink(attemptContext, workspace, job.ReportName, WorkbookConfig{Progress: func(rows uint64, part uint32) error {
			progressContext, cancel := context.WithTimeout(attemptContext, worker.heartbeatTimeout())
			defer cancel()
			owned, progressErr := worker.repository.Progress(progressContext, job.ID, worker.owner, job.Attempt, rows, part)
			if progressErr != nil {
				worker.logger.WarnContext(attemptContext, "update report export progress", "job_id", job.ID, "error", progressErr)
				return nil
			}
			if !owned {
				return reporting.ErrClaimLost
			}
			return nil
		}})
		if err != nil {
			return err
		}
		defer sink.Abort()
		stage = "query"
		normalized, err := reporting.NormalizeSnapshotParameters(snapshot.Parameters, snapshot.Input)
		if err != nil {
			return err
		}
		if err := worker.engine.StreamNormalized(attemptContext, database, job.SQLText, snapshot.Parameters, normalized, sink); err != nil {
			return err
		}
		stage = "workbook"
		artifactPath, artifactName, artifactType, parts, rows, err := sink.Finish()
		if err != nil {
			return err
		}
		stage = "publish"
		relative, size, err := worker.storage.Publish(job.ID, job.Attempt, artifactPath)
		if err != nil {
			return err
		}
		published = relative
		artifact := Artifact{RelativePath: relative, Name: artifactName, Type: artifactType, Size: size, Parts: parts, Rows: rows}
		owned, err := worker.repository.Succeed(attemptContext, job.ID, worker.owner, job.Attempt, artifact, time.Now().UTC().Add(worker.config.Retention), time.Now().UTC())
		if err != nil {
			return err
		}
		if !owned {
			return reporting.ErrClaimLost
		}
		return nil
	}()
	cancelAttempt(err)
	<-heartbeatDone
	timeoutCancel()
	if workspace != "" {
		_ = worker.storage.RemoveWorkspace(workspace)
	}
	if err == nil {
		return
	}
	if published != "" {
		_ = worker.storage.Remove(published)
	}
	cause := context.Cause(attemptContext)
	if errors.Is(err, reporting.ErrClaimLost) || errors.Is(cause, reporting.ErrClaimLost) {
		worker.logger.Info("report export claim lost", "job_id", job.ID, "attempt", job.Attempt)
		return
	}
	class, message := safeFailure(stage, err, cause)
	finishContext, finishCancel := context.WithTimeout(context.WithoutCancel(parent), worker.heartbeatTimeout())
	defer finishCancel()
	if _, finishErr := worker.repository.Fail(finishContext, job.ID, worker.owner, job.Attempt, class, message, time.Now().UTC()); finishErr != nil {
		worker.logger.Error("finish failed report export", "job_id", job.ID, "attempt", job.Attempt, "error", finishErr)
	}
	worker.logger.Error("report export failed", "job_id", job.ID, "attempt", job.Attempt, "stage", stage, "error", err)
}

func (worker *Worker) heartbeat(ctx context.Context, cancel context.CancelCauseFunc, job Job, done chan<- struct{}) {
	defer close(done)
	lastProof := time.Now()
	ticker := time.NewTicker(worker.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatContext, heartbeatCancel := context.WithTimeout(ctx, worker.heartbeatTimeout())
			owned, err := worker.heartbeatFunc(heartbeatContext, job.ID, worker.owner, job.Attempt, time.Now().UTC())
			heartbeatCancel()
			if err == nil && !owned {
				cancel(reporting.ErrClaimLost)
				return
			}
			if err == nil {
				lastProof = time.Now()
				continue
			}
			worker.logger.WarnContext(ctx, "heartbeat report export", "job_id", job.ID, "attempt", job.Attempt, "error", err)
			if time.Since(lastProof) >= worker.config.StaleAfter {
				cancel(reporting.ErrLeaseUnproven)
				return
			}
		}
	}
}

func (worker *Worker) heartbeatTimeout() time.Duration {
	value := worker.config.StaleAfter / 3
	if value > 5*time.Second {
		value = 5 * time.Second
	}
	return value
}

func (worker *Worker) staleLoop(ctx context.Context) {
	interval := worker.config.StaleAfter / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := worker.repository.RequeueStale(ctx, now.UTC().Add(-worker.config.StaleAfter)); err != nil {
				worker.logger.ErrorContext(ctx, "requeue stale report exports", "error", err)
			}
		}
	}
}

func (worker *Worker) cleanupLoop(ctx context.Context) {
	worker.cleanup(ctx, time.Now().UTC())
	ticker := time.NewTicker(worker.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			worker.cleanup(ctx, now.UTC())
		}
	}
}

func (worker *Worker) cleanup(ctx context.Context, now time.Time) {
	expired, err := worker.repository.ExpiredArtifacts(ctx, now)
	if err != nil {
		worker.logger.ErrorContext(ctx, "list expired report artifacts", "error", err)
		return
	}
	for _, job := range expired {
		if job.ArtifactPath == nil {
			continue
		}
		if err := worker.storage.Remove(*job.ArtifactPath); err != nil {
			worker.logger.ErrorContext(ctx, "remove expired report artifact", "job_id", job.ID, "error", err)
			continue
		}
		if err := worker.repository.MarkArtifactDeleted(ctx, job.ID, now); err != nil {
			worker.logger.ErrorContext(ctx, "mark report artifact deleted", "job_id", job.ID, "error", err)
		}
	}
	referenced, err := worker.repository.ReferencedArtifacts(ctx)
	if err != nil {
		worker.logger.ErrorContext(ctx, "list referenced report artifacts", "error", err)
		return
	}
	if err := worker.storage.ReconcileFinal(referenced, now.Add(-worker.config.OrphanGrace)); err != nil && !errors.Is(err, os.ErrNotExist) {
		worker.logger.ErrorContext(ctx, "reconcile report artifacts", "error", err)
	}
	if err := worker.storage.CleanupWorkspaces(now.Add(-worker.config.OrphanGrace)); err != nil && !errors.Is(err, os.ErrNotExist) {
		worker.logger.ErrorContext(ctx, "clean report workspaces", "error", err)
	}
}

func safeFailure(stage string, err, cause error) (string, string) {
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "Export exceeded the configured time limit."
	}
	if errors.Is(cause, reporting.ErrLeaseUnproven) {
		return "lease_unproven", "Export stopped because worker ownership could not be verified."
	}
	if stage == "eligibility" {
		return "access_revoked", "Export authorization or report availability changed before execution."
	}
	if stage == "query" {
		return "query_failed", "Report export failed while reading the datasource."
	}
	if stage == "workbook" {
		return "xlsx_failed", "The result could not be represented safely as XLSX."
	}
	return "artifact_failed", "The report export artifact could not be published."
}
