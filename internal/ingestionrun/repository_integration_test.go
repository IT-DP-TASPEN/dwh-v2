//go:build integration

package ingestionrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestMySQLCanonicalParametersAndAllRunAllChildren(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, err := NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	type sourceState struct {
		Key     string `db:"source_id"`
		Enabled bool   `db:"enabled"`
	}
	var states []sourceState
	if err := db.Select(&states, `SELECT source_id,enabled FROM source_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, state := range states {
			_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id=?`, state.Enabled, state.Key)
		}
	})
	from, _ := ingestion.ParseCalendarDate("2026-06-01")
	to, _ := ingestion.ParseCalendarDate("2026-06-03")
	tests := []struct {
		job, transformed string
		parameters       Parameters
	}{
		{job: "cif_opening_report", transformed: `{ "to":"2026-06-03", "from":"2026-06-01" }`},
		{job: "balance_sheet_report", transformed: `{ "dates":["2026-06-01", "2026-06-02", "2026-06-03"] }`},
		{job: "eod_cif_opening_report_full", transformed: `{ "lookback_days":3, "dates":["2026-06-01","2026-06-02","2026-06-03"] }`},
		{job: "saving_detail", transformed: `{ }`},
	}
	tests[0].parameters, _ = NewRangeExecution(tests[0].job, from, to)
	tests[1].parameters, _ = NewDateSeriesExecution(tests[1].job, from, to)
	tests[2].parameters, _ = NewMaintenanceSeriesExecution(tests[2].job, from, to, 3)
	tests[3].parameters, _ = NewLiveSnapshotExecution(tests[3].job)
	for _, test := range tests {
		runID, err := repository.Submit(context.Background(), test.job, test.parameters, TriggerDirect, "mysql-json-roundtrip", nil)
		if err != nil {
			t.Fatalf("submit %s: %v", test.job, err)
		}
		if _, err := db.Exec(`UPDATE ingestion_runs SET parameters_json=? WHERE id=?`, test.transformed, runID); err != nil {
			t.Fatal(err)
		}
		run, err := repository.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		job, _ := catalog.Find(test.job)
		if err := run.Parameters.Validate(job); err != nil {
			t.Fatalf("%s failed after MySQL JSON round-trip: %v; json=%s", test.job, err, run.Parameters.JSON)
		}
		if test.parameters.Kind != DetailLiveSnapshotV1 && bytes.Equal(run.Parameters.JSON, test.parameters.JSON) {
			t.Fatalf("%s test did not reproduce MySQL JSON transformation", test.job)
		}
		if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
	}

	parentID, err := repository.CreateRunAll(context.Background(), from, to, 3, TriggerDirect, "mysql-run-all-roundtrip", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=?`, parentID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, parentID)
	})
	owner, _ := NewOwnerID()
	jobs := catalog.Jobs()
	for index, job := range jobs {
		changed, err := repository.ReconcileOneParent(context.Background())
		if err != nil || !changed {
			t.Fatalf("activate child %d changed=%v error=%v", index+1, changed, err)
		}
		run, err := repository.Claim(context.Background(), owner)
		if err != nil || run == nil {
			t.Fatalf("claim child %d run=%+v error=%v", index+1, run, err)
		}
		if run.Kind != KindRunAllChild || run.ChildPosition == nil || int(*run.ChildPosition) != index+1 || run.JobKey != job.Key {
			t.Fatalf("child %d order/run mismatch: %+v want=%s", index+1, run, job.Key)
		}
		if err := run.Parameters.Validate(job); err != nil {
			t.Fatalf("child %d %s MySQL parameters: %v; json=%s", index+1, job.Key, err, run.Parameters.JSON)
		}
		if err := repository.Finish(context.Background(), run.ID, owner, StatusSucceeded, SafeError{}); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := repository.ReconcileOneParent(context.Background()); err != nil || !changed {
		t.Fatalf("finish Run All parent changed=%v error=%v", changed, err)
	}
	parent, err := repository.Get(context.Background(), parentID)
	if err != nil || parent.Status != StatusCompleted {
		t.Fatalf("Run All parent=%+v error=%v", parent, err)
	}
}

func TestMapperDiagnosticsShareProgressFlushAndSurviveCancellation(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, _ := NewRepository(db, catalog)
	var enabled bool
	if err := db.Get(&enabled, `SELECT enabled FROM source_settings WHERE source_id='saving_detail'`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id='saving_detail'`)
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id='saving_detail'`, enabled)
	})
	parameters, _ := NewLiveSnapshotExecution("saving_detail")
	runID, err := repository.Submit(context.Background(), "saving_detail", parameters, TriggerDirect, "mapper-diagnostics", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID) })
	owner, _ := NewOwnerID()
	run, err := repository.Claim(context.Background(), owner)
	if err != nil || run == nil || run.ID != runID {
		t.Fatalf("claim=%+v error=%v", run, err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET cancel_requested_at=UTC_TIMESTAMP(6),cancel_reason='operator' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	var diagnostics MapperDiagnostics
	if err := diagnostics.Add(savingMapperError(t, "diagnostic-secret", "1").Metadata()); err != nil {
		t.Fatal(err)
	}
	progress := Progress{Total: 2, Started: 1, Failed: 1, Step: "fetch_details"}
	if err := repository.UpdateProgress(context.Background(), runID, owner, progress, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var cancellationPreserved int
	if err := db.Get(&cancellationPreserved, `SELECT cancel_requested_at IS NOT NULL AND cancel_reason='operator' FROM ingestion_runs WHERE id=?`, runID); err != nil || cancellationPreserved != 1 {
		t.Fatalf("progress flush overwrote cancellation state: preserved=%d error=%v", cancellationPreserved, err)
	}
	if err := repository.Finish(context.Background(), runID, owner, StatusCancelled, SafeError{Class: "cancelled", Message: "operator cancelled", Step: "fetch_detail"}); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Get(context.Background(), runID)
	if err != nil || stored.MapperDiagnostics == nil || stored.MapperDiagnostics.TotalCount != 1 || stored.MapperDiagnostics.Groups[0].Field != "beginning_balance" {
		t.Fatalf("cancelled run diagnostics=%+v error=%v", stored.MapperDiagnostics, err)
	}
}

func TestDurableQueueRunAllAndTerminalCAS(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, fmt.Sprintf("runactor%d", time.Now().UnixNano()), adminRole.ID, true)
	requester := integrationdb.Requester(actor, adminRole)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM audit_logs WHERE actor_user_id=?`, actor.ID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, actor.ID)
	})
	from, _ := ingestion.ParseCalendarDate("2026-06-01")
	to, _ := ingestion.ParseCalendarDate("2026-06-03")
	var previousEnabled bool
	if err := db.Get(&previousEnabled, `SELECT enabled FROM source_settings WHERE source_id='cif_opening_report'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id='cif_opening_report'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id='cif_opening_report'`, previousEnabled)
	})
	directParameters, _ := NewRangeExecution("cif_opening_report", from, to)
	directID, err := repository.Submit(context.Background(), "cif_opening_report", directParameters, TriggerDirect, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Submit(context.Background(), "cif_opening_report", directParameters, TriggerDirect, "", nil); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("duplicate submit error=%v", err)
	}
	var journalEnabled bool
	if err := db.Get(&journalEnabled, `SELECT enabled FROM source_settings WHERE source_id='journal_transaction_report'`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id='journal_transaction_report'`)
	journalParameters, _ := NewRangeExecution("journal_transaction_report", from, to)
	journalID, err := repository.Submit(context.Background(), "journal_transaction_report", journalParameters, TriggerDirect, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RequestCancellation(context.Background(), journalID, "must roll back", securityctx.Requester{}); err == nil {
		t.Fatal("cancellation without durable actor attribution succeeded")
	}
	journal, _ := repository.Get(context.Background(), journalID)
	if journal.Status != StatusQueued || journal.CancelRequested {
		t.Fatalf("failed cancellation audit did not roll back: %+v", journal)
	}
	if err := repository.RequestCancellation(context.Background(), journalID, "operator cancelled", requester); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, journalID)
		_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id='journal_transaction_report'`, journalEnabled)
	})
	parentID, err := repository.CreateRunAll(context.Background(), from, to, 3, TriggerDirect, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=?`, parentID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id IN (?,?)`, parentID, directID)
	})
	var children int
	if err := db.Get(&children, `SELECT COUNT(*) FROM ingestion_runs WHERE parent_run_id=?`, parentID); err != nil || children != 36 {
		t.Fatalf("Run All children=%d error=%v", children, err)
	}
	var detailJSON string
	if err := db.Get(&detailJSON, `SELECT CAST(parameters_json AS CHAR) FROM ingestion_runs WHERE parent_run_id=? AND job_key='cif_detail'`, parentID); err != nil || detailJSON != "{}" {
		t.Fatalf("detail params=%q error=%v", detailJSON, err)
	}
	changed, err := repository.ReconcileOneParent(context.Background())
	if err != nil || !changed {
		t.Fatalf("first reconciliation changed=%v error=%v", changed, err)
	}
	var skipped int
	if err := db.Get(&skipped, `SELECT COUNT(*) FROM ingestion_runs WHERE parent_run_id=? AND job_key='cif_opening_report' AND status='skipped' AND skip_reason='job_busy'`, parentID); err != nil || skipped != 1 {
		t.Fatalf("busy child skipped=%d error=%v", skipped, err)
	}
	if changed, err = repository.ReconcileOneParent(context.Background()); err != nil || !changed {
		t.Fatalf("second reconciliation changed=%v error=%v", changed, err)
	}

	owner, _ := NewOwnerID()
	first, err := repository.Claim(context.Background(), owner)
	if err != nil || first == nil || first.ID != directID {
		t.Fatalf("first claim=%+v error=%v", first, err)
	}
	if err := repository.Finish(context.Background(), first.ID, owner, StatusSucceeded, SafeError{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.RequestCancellation(context.Background(), first.ID, "late", requester); err != nil {
		t.Fatal(err)
	}
	finished, _ := repository.Get(context.Background(), first.ID)
	if finished.Status != StatusSucceeded || finished.CancelRequested {
		t.Fatalf("late cancellation rewrote completed run=%+v", finished)
	}

	second, err := repository.Claim(context.Background(), owner)
	if err != nil || second == nil || second.Kind != KindRunAllChild || second.HeartbeatAt == nil {
		t.Fatalf("second claim=%+v error=%v", second, err)
	}
	var parentOwner *string
	if err := db.Get(&parentOwner, `SELECT owner_id FROM ingestion_runs WHERE id=?`, parentID); err != nil || parentOwner != nil {
		t.Fatalf("Run All parent owner=%v error=%v", parentOwner, err)
	}
	if err := repository.RecoverAbandoned(context.Background(), second.ID, owner, *second.HeartbeatAt, "must roll back", securityctx.Requester{}); err == nil {
		t.Fatal("recovery without durable actor attribution succeeded")
	}
	stillRunning, _ := repository.Get(context.Background(), second.ID)
	if stillRunning.Status != StatusRunning {
		t.Fatalf("failed recovery audit did not roll back: %s", stillRunning.Status)
	}
	if err := repository.RecoverAbandoned(context.Background(), second.ID, owner, *second.HeartbeatAt, "operator verified process loss", requester); err != nil {
		t.Fatal(err)
	}
	if err := repository.Finish(context.Background(), second.ID, owner, StatusSucceeded, SafeError{}); !errors.Is(err, ErrTransition) {
		t.Fatalf("worker overwrote abandoned run: %v", err)
	}
	if err := repository.RequestCancellation(context.Background(), parentID, "test cleanup", requester); err != nil {
		t.Fatal(err)
	}
	if changed, err = repository.ReconcileOneParent(context.Background()); err != nil || !changed {
		t.Fatalf("cancelled parent reconciliation changed=%v error=%v", changed, err)
	}
	parent, _ := repository.Get(context.Background(), parentID)
	if parent.Status != StatusCancelled {
		t.Fatalf("parent status=%s", parent.Status)
	}
	var audited int
	if err := db.Get(&audited, `SELECT COUNT(*) FROM audit_logs WHERE actor_user_id=? AND resource_type='ingestion_run'
		AND action IN ('ingestion.cancellation_requested','ingestion.abandoned_recovered')`, actor.ID); err != nil || audited != 3 {
		t.Fatalf("durable run audit rows=%d error=%v", audited, err)
	}
}
