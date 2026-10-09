//go:build integration

package coordinator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionexec"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

type gatedExecutor struct {
	entered chan uint64
	mu      sync.Mutex
	gates   map[uint64]chan struct{}
	causes  map[uint64]error
}

func (executor *gatedExecutor) gate(id uint64) chan struct{} {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.gates[id] == nil {
		executor.gates[id] = make(chan struct{})
	}
	return executor.gates[id]
}

func (executor *gatedExecutor) Execute(ctx context.Context, run ingestionrun.Run, _ string) ingestionexec.Result {
	executor.entered <- run.ID
	select {
	case <-executor.gate(run.ID):
		return ingestionexec.Result{Status: ingestionrun.StatusSucceeded, BusinessComplete: true}
	case <-ctx.Done():
		executor.mu.Lock()
		executor.causes[run.ID] = context.Cause(ctx)
		executor.mu.Unlock()
		return ingestionexec.Result{Status: ingestionrun.StatusCancelled, Cause: context.Cause(ctx),
			Error: ingestionrun.SafeError{Class: "cancelled", Message: "application shutdown cancelled the run", Step: "test"}}
	}
}

func (executor *gatedExecutor) cause(id uint64) error {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.causes[id]
}

func TestDispatcherFollowsLiveMaxRunningJobsWithoutRestart(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	fake := &gatedExecutor{entered: make(chan uint64, 8), gates: map[uint64]chan struct{}{}, causes: map[uint64]error{}}
	coordinator := &Coordinator{runs: runs, executor: fake, fixed: ingestionstore.NewFixedRepository(db), details: ingestionstore.NewDetailRepository(db),
		masters: ingestionstore.NewMasterRepository(db), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		local: map[uint64]context.CancelCauseFunc{}, parents: map[uint64]string{}}
	// Settle stale rows other suites left behind so Run's own sweep cannot move
	// the admission baseline mid-test.
	coordinator.recoverStaleSweep(context.Background())
	baseline, err := runs.RunningJobs(context.Background())
	if err != nil || baseline+2 > ingestionrun.MaxRuntimeLimit {
		t.Fatalf("baseline=%d err=%v", baseline, err)
	}
	ids := queueDistinctIntegrationJobs(t, db, runs, 3)
	limit := func(max int) {
		if _, err := db.Exec(`UPDATE ingestion_runtime_settings SET max_running_jobs=? WHERE id=1`, baseline+max); err != nil {
			t.Fatal(err)
		}
	}
	limit(1)

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { coordinator.Run(ctx); close(done) }()
	t.Cleanup(func() { stop(); <-done })

	first := awaitEntry(t, fake, ids)
	expectNoEntry(t, fake)
	expectRunning(t, runs, baseline+1)

	limit(2) // live raise: the same process admits a second execution
	second := awaitEntry(t, fake, ids)
	expectRunning(t, runs, baseline+2)

	limit(1) // live lower: both executions continue untouched
	expectNoEntry(t, fake)
	for _, id := range []uint64{first, second} {
		if cause := fake.cause(id); cause != nil {
			t.Fatalf("run %d cancelled by lowered limit: %v", id, cause)
		}
		if run, err := runs.Get(context.Background(), id); err != nil || run.Status != ingestionrun.StatusRunning || run.CancelRequested {
			t.Fatalf("run %d=%+v err=%v", id, run, err)
		}
	}

	close(fake.gate(first))
	awaitStatus(t, runs, first, ingestionrun.StatusSucceeded)
	expectNoEntry(t, fake) // running == lowered limit, still no admission

	close(fake.gate(second))
	awaitStatus(t, runs, second, ingestionrun.StatusSucceeded)
	third := awaitEntry(t, fake, ids)

	// Shutdown with an active execution: Run returns only after the execution
	// observed coordinator shutdown and was finished canonically.
	stop()
	<-done
	if cause := fake.cause(third); !errors.Is(cause, ingestionrun.ErrCoordinatorShutdown) {
		t.Fatalf("third cause=%v", cause)
	}
	if run, err := runs.Get(context.Background(), third); err != nil || run.Status != ingestionrun.StatusCancelled {
		t.Fatalf("third=%+v err=%v", run, err)
	}
	coordinator.mu.Lock()
	leaked := len(coordinator.local)
	coordinator.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("local cancellation registrations leaked: %d", leaked)
	}
}

func awaitEntry(t *testing.T, fake *gatedExecutor, ids []uint64) uint64 {
	t.Helper()
	select {
	case id := <-fake.entered:
		if !slices.Contains(ids, id) {
			t.Fatalf("executed foreign run %d", id)
		}
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("no execution admitted")
		return 0
	}
}

// expectNoEntry covers several dispatcher idle polls; callers pair it with a
// database running-count assertion.
func expectNoEntry(t *testing.T, fake *gatedExecutor) {
	t.Helper()
	select {
	case id := <-fake.entered:
		t.Fatalf("run %d admitted above the live limit", id)
	case <-time.After(time.Second):
	}
}

func expectRunning(t *testing.T, runs *ingestionrun.Repository, want int) {
	t.Helper()
	if running, err := runs.RunningJobs(context.Background()); err != nil || running != want {
		t.Fatalf("running=%d want=%d err=%v", running, want, err)
	}
}

func awaitStatus(t *testing.T, runs *ingestionrun.Repository, id uint64, want ingestionrun.Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := runs.Get(context.Background(), id)
		if err == nil && run.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d=%+v err=%v want status %s", id, run, err, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func queueDistinctIntegrationJobs(t *testing.T, db *sqlx.DB, runs *ingestionrun.Repository, count int) []uint64 {
	t.Helper()
	var queued int
	if err := db.Get(&queued, `SELECT COUNT(*) FROM ingestion_runs WHERE kind IN ('job','run_all_child') AND status='queued'`); err != nil || queued != 0 {
		t.Fatalf("disposable database has %d stray queued runs: %v", queued, err)
	}
	var original int
	if err := db.Get(&original, `SELECT max_running_jobs FROM ingestion_runtime_settings WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var ids []uint64
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE ingestion_runtime_settings SET max_running_jobs=? WHERE id=1`, original)
		for _, id := range ids {
			_, _ = db.Exec(`DELETE FROM ingestion_run_errors WHERE run_id=?`, id)
			_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id)
		}
	})
	catalog, _ := ingestion.NewCatalog()
	for _, job := range catalog.Jobs() {
		if len(ids) == count {
			break
		}
		if job.DateStrategy != ingestion.NoDate {
			continue
		}
		var enabled bool
		if err := db.Get(&enabled, `SELECT enabled FROM source_settings WHERE source_id=?`, job.Key); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id=?`, job.Key); err != nil {
			t.Fatal(err)
		}
		key := job.Key
		t.Cleanup(func() { _, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id=?`, enabled, key) })
		parameters, err := parametersForIntegrationJob(job)
		if err != nil {
			t.Fatal(err)
		}
		id, err := runs.Submit(context.Background(), job.Key, parameters, ingestionrun.TriggerDirect, t.Name(), nil)
		if err != nil {
			t.Fatalf("queue %s: %v", job.Key, err)
		}
		ids = append(ids, id)
	}
	if len(ids) != count {
		t.Fatalf("queued %d jobs, want %d", len(ids), count)
	}
	return ids
}
