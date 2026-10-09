//go:build integration

package ingestionrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestRuntimeSettingsUpdateIsAtomicOptimisticAndAudited(t *testing.T) {
	db := integrationdb.Open(t)
	repository := runtimeRepository(t, db)
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	actorA := integrationdb.User(t, db, fmt.Sprintf("runtimea%d", time.Now().UnixNano()), role.ID, true)
	actorB := integrationdb.User(t, db, fmt.Sprintf("runtimeb%d", time.Now().UnixNano()), role.ID, true)
	effective := integrationdb.User(t, db, fmt.Sprintf("runtimee%d", time.Now().UnixNano()), role.ID, true)
	adminA := integrationdb.Requester(actorA, role)
	adminA.Effective = securityctx.Identity{UserID: effective.ID, Username: effective.Username}
	adminB := integrationdb.Requester(actorB, role)
	if _, err := db.Exec(`DELETE FROM audit_logs WHERE action='ingestion.runtime_settings_updated'`); err != nil {
		t.Fatal(err)
	}
	defaults := RuntimeSettings{2, 4, 3}
	setRuntimeSettings(t, db, defaults)

	if _, err := repository.UpdateRuntimeSettings(context.Background(), defaults, RuntimeSettings{65, 4, 3}, adminA); err == nil {
		t.Fatal("out-of-range target accepted")
	}
	if changed, err := repository.UpdateRuntimeSettings(context.Background(), defaults, defaults, adminA); err != nil || changed {
		t.Fatalf("no-op changed=%v err=%v", changed, err)
	}
	if count := runtimeAuditCount(t, db); count != 0 {
		t.Fatalf("validation failure or no-op audited %d events", count)
	}

	target := RuntimeSettings{4, 8, 6}
	if changed, err := repository.UpdateRuntimeSettings(context.Background(), defaults, target, adminA); err != nil || !changed {
		t.Fatalf("update changed=%v err=%v", changed, err)
	}
	if got, err := repository.RuntimeSettings(context.Background()); err != nil || got != target {
		t.Fatalf("persisted=%+v err=%v", got, err)
	}
	var event struct {
		Actor     uint64  `db:"actor_user_id"`
		Effective uint64  `db:"effective_user_id"`
		Resource  *string `db:"resource_type"`
		Metadata  []byte  `db:"metadata"`
	}
	if err := db.Get(&event, `SELECT actor_user_id,effective_user_id,resource_type,metadata FROM audit_logs WHERE action='ingestion.runtime_settings_updated'`); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]map[string]int
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	wantMetadata := map[string]map[string]int{
		"from": {"max_running_jobs": 2, "fixed_member_concurrency": 4, "detail_concurrency": 3},
		"to":   {"max_running_jobs": 4, "fixed_member_concurrency": 8, "detail_concurrency": 6},
	}
	if event.Actor != actorA.ID || event.Effective != effective.ID || event.Resource != nil || fmt.Sprint(metadata) != fmt.Sprint(wantMetadata) {
		t.Fatalf("audit=%+v metadata=%s", event, event.Metadata)
	}

	// Admin A renders 4/8/6, admin B saves first, A's stale submission must not apply.
	if _, err := repository.UpdateRuntimeSettings(context.Background(), target, RuntimeSettings{3, 8, 6}, adminB); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateRuntimeSettings(context.Background(), target, RuntimeSettings{9, 9, 9}, adminA); !errors.Is(err, ErrRuntimeSettingsConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	if got, _ := repository.RuntimeSettings(context.Background()); got != (RuntimeSettings{3, 8, 6}) {
		t.Fatalf("stale submission applied: %+v", got)
	}
	if count := runtimeAuditCount(t, db); count != 2 {
		t.Fatalf("audit events=%d want 2 (A then B, none for conflict)", count)
	}
}

func TestClaimFollowsLiveMaxRunningJobs(t *testing.T) {
	db := integrationdb.Open(t)
	repository := runtimeRepository(t, db)
	baseline := runningBaseline(t, repository)
	ids := queueDistinctJobs(t, db, repository, 3)
	limit := func(max int) { setRuntimeSettings(t, db, RuntimeSettings{baseline + max, 4, 3}) }

	// A. Raising the limit admits more work without reconstructing anything.
	limit(1)
	first := claimOwned(t, repository, ids)
	if extra := claim(t, repository); extra != nil {
		t.Fatalf("claimed %d above limit", extra.ID)
	}
	limit(2)
	second := claimOwned(t, repository, ids)

	// B. Lowering the limit never touches running work and blocks new claims
	// until the running count falls below the current limit.
	limit(1)
	for _, run := range []*Run{first, second} {
		if stored, err := repository.Get(context.Background(), run.ID); err != nil || stored.Status != StatusRunning || stored.CancelRequested {
			t.Fatalf("running work changed by lower limit: %+v err=%v", stored, err)
		}
	}
	if extra := claim(t, repository); extra != nil {
		t.Fatalf("claimed %d above lowered limit", extra.ID)
	}
	if err := repository.Finish(context.Background(), first.ID, first.OwnerID, StatusSucceeded, SafeError{}); err != nil {
		t.Fatal(err)
	}
	if extra := claim(t, repository); extra != nil {
		t.Fatalf("claimed %d while running == limit", extra.ID)
	}
	if err := repository.Finish(context.Background(), second.ID, second.OwnerID, StatusSucceeded, SafeError{}); err != nil {
		t.Fatal(err)
	}
	claimOwned(t, repository, ids)
}

func TestConcurrentClaimsNeverExceedLiveLimit(t *testing.T) {
	db := integrationdb.Open(t)
	repository := runtimeRepository(t, db)
	baseline := runningBaseline(t, repository)
	ids := queueDistinctJobs(t, db, repository, 6)
	setRuntimeSettings(t, db, RuntimeSettings{baseline + 2, 4, 3})
	var wait sync.WaitGroup
	var mu sync.Mutex
	var claimed []uint64
	start := make(chan struct{})
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			owner, _ := NewOwnerID()
			run, err := repository.Claim(context.Background(), owner)
			if err != nil {
				t.Error(err)
				return
			}
			if run != nil {
				mu.Lock()
				claimed = append(claimed, run.ID)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wait.Wait()
	if len(claimed) != 2 {
		t.Fatalf("concurrent claims=%v want exactly 2", claimed)
	}
	for _, id := range claimed {
		if !slices.Contains(ids, id) {
			t.Fatalf("claimed foreign run %d", id)
		}
	}
	if running, err := repository.RunningJobs(context.Background()); err != nil || running != baseline+2 {
		t.Fatalf("running=%d err=%v", running, err)
	}
}

func runtimeRepository(t *testing.T, db *sqlx.DB) *Repository {
	t.Helper()
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func setRuntimeSettings(t *testing.T, db *sqlx.DB, settings RuntimeSettings) {
	t.Helper()
	var original RuntimeSettings
	if err := db.QueryRow(`SELECT max_running_jobs,fixed_member_concurrency,detail_concurrency FROM ingestion_runtime_settings WHERE id=1`).
		Scan(&original.MaxRunningJobs, &original.FixedMemberConcurrency, &original.DetailConcurrency); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE ingestion_runtime_settings SET max_running_jobs=?,fixed_member_concurrency=?,detail_concurrency=? WHERE id=1`,
			original.MaxRunningJobs, original.FixedMemberConcurrency, original.DetailConcurrency)
	})
	if _, err := db.Exec(`UPDATE ingestion_runtime_settings SET max_running_jobs=?,fixed_member_concurrency=?,detail_concurrency=? WHERE id=1`,
		settings.MaxRunningJobs, settings.FixedMemberConcurrency, settings.DetailConcurrency); err != nil {
		t.Fatal(err)
	}
}

func runtimeAuditCount(t *testing.T, db *sqlx.DB) int {
	t.Helper()
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM audit_logs WHERE action='ingestion.runtime_settings_updated'`); err != nil {
		t.Fatal(err)
	}
	return count
}

// runningBaseline makes admission assertions relative to running rows other
// suites may have left in the disposable database; queued strays would be
// claimed ahead of ours, so they are rejected outright.
func runningBaseline(t *testing.T, repository *Repository) int {
	t.Helper()
	var queued int
	if err := repository.db.Get(&queued, `SELECT COUNT(*) FROM ingestion_runs WHERE kind IN ('job','run_all_child') AND status='queued'`); err != nil || queued != 0 {
		t.Fatalf("disposable database has %d stray queued runs: %v", queued, err)
	}
	running, err := repository.RunningJobs(context.Background())
	if err != nil || running+3 > MaxRuntimeLimit {
		t.Fatalf("baseline running=%d err=%v", running, err)
	}
	return running
}

func queueDistinctJobs(t *testing.T, db *sqlx.DB, repository *Repository, count int) []uint64 {
	t.Helper()
	var ids []uint64
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id)
		}
	})
	for _, job := range repository.catalog.Jobs() {
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
		parameters, err := NewLiveSnapshotExecution(job.Key)
		if err != nil {
			t.Fatal(err)
		}
		id, err := repository.Submit(context.Background(), job.Key, parameters, TriggerDirect, t.Name(), nil)
		if err != nil {
			t.Fatalf("queue %s: %v", job.Key, err)
		}
		ids = append(ids, id)
	}
	if len(ids) != count {
		t.Fatalf("queued %d distinct jobs, want %d", len(ids), count)
	}
	return ids
}

func claim(t *testing.T, repository *Repository) *Run {
	t.Helper()
	owner, err := NewOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.Claim(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func claimOwned(t *testing.T, repository *Repository, ids []uint64) *Run {
	t.Helper()
	run := claim(t, repository)
	if run == nil || !slices.Contains(ids, run.ID) {
		t.Fatalf("claim=%+v want one of %v", run, ids)
	}
	return run
}
