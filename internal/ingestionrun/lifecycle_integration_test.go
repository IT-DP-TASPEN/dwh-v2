//go:build integration

package ingestionrun

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDBTimeRecoveryReleasesJobAndFencesOldWorker(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, _ := NewRepository(db, catalog)
	var wasEnabled bool
	if err := db.Get(&wasEnabled, `SELECT enabled FROM source_settings WHERE source_id='saving_detail'`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id='saving_detail'`)
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id='saving_detail'`, wasEnabled)
	})
	owner := strings.Repeat("a", 64)
	runID := insertStaleRun(t, db, "saving_detail", KindJob, owner)
	replacement := strings.Repeat("b", 64)
	recovered, err := repository.RecoverOneStale(context.Background(), 2*time.Minute, replacement)
	if err != nil || recovered == nil || recovered.RunID != runID || recovered.PreviousOwner != owner {
		t.Fatalf("recovery=%+v error=%v", recovered, err)
	}
	if err := repository.Finish(context.Background(), runID, owner, StatusSucceeded, SafeError{}); !errors.Is(err, ErrTransition) {
		t.Fatalf("old worker terminal write error=%v", err)
	}
	if duplicate, err := repository.RecoverOneStale(context.Background(), 2*time.Minute, strings.Repeat("0", 64)); err != nil || duplicate != nil {
		t.Fatalf("duplicate recovery was not idempotent: recovery=%+v error=%v", duplicate, err)
	}
	var status Status
	if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, runID); err != nil || status != StatusAbandoned {
		t.Fatalf("status=%s error=%v", status, err)
	}
	parameters, _ := NewLiveSnapshotExecution("saving_detail")
	newID, err := repository.Submit(context.Background(), "saving_detail", parameters, TriggerDirect, "after-recovery", nil)
	if err != nil || newID == runID {
		t.Fatalf("logical job remained busy: id=%d error=%v", newID, err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, newID) })
}

func TestRecoveryCASMissesWhenHeartbeatRenewsAfterCandidateRead(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, _ := NewRepository(db, catalog)
	owner := strings.Repeat("c", 64)
	runID := insertStaleRun(t, db, "loan_detail", KindJob, owner)
	blocker, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var marker uint64
	if err := blocker.Get(&marker, `SELECT id FROM ingestion_runs WHERE id=? FOR UPDATE`, runID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		recovery *Recovery
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		recovery, err := repository.RecoverOneStale(context.Background(), 2*time.Minute, strings.Repeat("d", 64))
		done <- outcome{recovery: recovery, err: err}
	}()
	time.Sleep(150 * time.Millisecond)
	if _, err := blocker.Exec(`UPDATE ingestion_runs SET heartbeat_at=UTC_TIMESTAMP(6) WHERE id=? AND owner_id=?`, runID, owner); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.recovery != nil {
		t.Fatalf("renewed heartbeat was recovered: recovery=%+v error=%v", result.recovery, result.err)
	}
	var running bool
	if err := db.Get(&running, `SELECT status='running' AND owner_id=? FROM ingestion_runs WHERE id=?`, owner, runID); err != nil || !running {
		t.Fatalf("renewed ownership lost: running=%v error=%v", running, err)
	}
}

func TestHeartbeatRenewsOwnershipAndTransientFailureDoesNotReleaseJob(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, _ := NewRepository(db, catalog)
	owner := strings.Repeat("8", 64)
	var wasEnabled bool
	if err := db.Get(&wasEnabled, `SELECT enabled FROM source_settings WHERE source_id='cif_detail'`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id='cif_detail'`)
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id='cif_detail'`, wasEnabled)
	})
	runID := insertStaleRun(t, db, "cif_detail", KindJob, owner)
	if _, err := db.Exec(`UPDATE ingestion_runs SET heartbeat_at=UTC_TIMESTAMP(6)-INTERVAL 1 MINUTE WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	before, _ := repository.Get(context.Background(), runID)
	heartbeat, err := repository.Heartbeat(context.Background(), runID, owner)
	if err != nil || !heartbeat.Owned {
		t.Fatalf("heartbeat=%+v error=%v", heartbeat, err)
	}
	after, _ := repository.Get(context.Background(), runID)
	if before.HeartbeatAt == nil || after.HeartbeatAt == nil || !after.HeartbeatAt.After(*before.HeartbeatAt) {
		t.Fatalf("heartbeat did not advance: before=%v after=%v", before.HeartbeatAt, after.HeartbeatAt)
	}
	blocker, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var marker uint64
	if err := blocker.Get(&marker, `SELECT id FROM ingestion_runs WHERE id=? FOR UPDATE`, runID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, heartbeatErr := repository.Heartbeat(ctx, runID, owner)
	cancel()
	if heartbeatErr == nil {
		_ = blocker.Rollback()
		t.Fatal("blocked heartbeat unexpectedly succeeded")
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	parameters, _ := NewLiveSnapshotExecution("cif_detail")
	if _, err := repository.Submit(context.Background(), "cif_detail", parameters, TriggerDirect, "heartbeat-contention", nil); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("transient heartbeat failure released job: %v", err)
	}
	if recovery, err := repository.RecoverOneStale(context.Background(), 2*time.Minute, strings.Repeat("9", 64)); err != nil || recovery != nil {
		t.Fatalf("fresh ownership recovered: recovery=%+v error=%v", recovery, err)
	}
}

func TestRunAllParentTakeoverAdoptsChildrenAndFencesOldOwner(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, _ := NewRepository(db, catalog)
	date, _ := ingestion.ParseCalendarDate("2026-08-26")
	parentID, err := repository.CreateRunAll(context.Background(), date, date, TriggerDirect, "parent-takeover", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=?`, parentID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, parentID)
	})
	parent, _ := repository.Get(context.Background(), parentID)
	oldOwner := parent.OwnerID
	childOwner := strings.Repeat("e", 64)
	if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=? AND child_position=36`, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='succeeded',finished_at=UTC_TIMESTAMP(6) WHERE parent_run_id=? AND child_position=1`, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='running',owner_id=?,claimed_at=UTC_TIMESTAMP(6),heartbeat_at=UTC_TIMESTAMP(6),started_at=UTC_TIMESTAMP(6)
		WHERE parent_run_id=? AND child_position=2`, childOwner, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET heartbeat_at=UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE WHERE id=?`, parentID); err != nil {
		t.Fatal(err)
	}
	newOwner := strings.Repeat("f", 64)
	recovery, err := repository.RecoverOneStale(context.Background(), 2*time.Minute, newOwner)
	if err != nil || recovery == nil || recovery.RunID != parentID || recovery.NewOwner != newOwner {
		t.Fatalf("parent recovery=%+v error=%v", recovery, err)
	}
	var childCount, succeeded int
	if err := db.QueryRowx(`SELECT COUNT(*),SUM(status='succeeded') FROM ingestion_runs WHERE parent_run_id=?`, parentID).Scan(&childCount, &succeeded); err != nil || childCount != 36 || succeeded != 1 {
		t.Fatalf("children=%d succeeded=%d error=%v", childCount, succeeded, err)
	}
	if _, err := repository.ReconcileParent(context.Background(), parentID, oldOwner); !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("old parent owner reconciled: %v", err)
	}
	if changed, err := repository.ReconcileParent(context.Background(), parentID, newOwner); err != nil || changed {
		t.Fatalf("active child reconciliation changed=%v error=%v", changed, err)
	}
	if err := repository.Finish(context.Background(), childRunID(t, db, parentID, 2), childOwner, StatusSucceeded, SafeError{}); err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.ReconcileParent(context.Background(), parentID, newOwner); err != nil || !changed {
		t.Fatalf("adopted parent did not resume: changed=%v error=%v", changed, err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='succeeded',finished_at=UTC_TIMESTAMP(6) WHERE parent_run_id=? AND status IN ('planned','queued')`, parentID); err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.ReconcileParent(context.Background(), parentID, newOwner); err != nil || !changed {
		t.Fatalf("adopted parent did not finish: changed=%v error=%v", changed, err)
	}
	var parentStatus Status
	if err := db.Get(&parentStatus, `SELECT status FROM ingestion_runs WHERE id=?`, parentID); err != nil || parentStatus != StatusCompleted {
		t.Fatalf("parent status=%s error=%v", parentStatus, err)
	}
}

func insertStaleRun(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, job string, kind Kind, owner string) uint64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,
		 owner_id,claimed_at,heartbeat_at,started_at)
		VALUES (?,?,'running','detail_live_snapshot_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,
		 UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE,UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE,UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE)`, kind, job, owner)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id) })
	return uint64(id)
}

func childRunID(t *testing.T, db interface {
	Get(any, string, ...any) error
}, parentID uint64, position int) uint64 {
	t.Helper()
	var id uint64
	if err := db.Get(&id, `SELECT id FROM ingestion_runs WHERE parent_run_id=? AND child_position=?`, parentID, position); err != nil {
		t.Fatal(err)
	}
	return id
}
