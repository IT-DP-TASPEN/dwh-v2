//go:build integration

package ingestionrun

import (
	"context"
	"errors"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

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
	if err := repository.RequestCancellation(context.Background(), first.ID, "late"); err != nil {
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
	if err := repository.RecoverAbandoned(context.Background(), second.ID, owner, *second.HeartbeatAt, "operator verified process loss"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Finish(context.Background(), second.ID, owner, StatusSucceeded, SafeError{}); !errors.Is(err, ErrTransition) {
		t.Fatalf("worker overwrote abandoned run: %v", err)
	}
	if err := repository.RequestCancellation(context.Background(), parentID, "test cleanup"); err != nil {
		t.Fatal(err)
	}
	if changed, err = repository.ReconcileOneParent(context.Background()); err != nil || !changed {
		t.Fatalf("cancelled parent reconciliation changed=%v error=%v", changed, err)
	}
	parent, _ := repository.Get(context.Background(), parentID)
	if parent.Status != StatusCancelled {
		t.Fatalf("parent status=%s", parent.Status)
	}
}
