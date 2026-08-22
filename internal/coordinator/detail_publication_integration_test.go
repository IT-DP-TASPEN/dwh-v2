//go:build integration

package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDetailPublicationCrashBeforeRunAllReconciliationCompletesOnce(t *testing.T) {
	db := integrationdb.Open(t)
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE`); err != nil {
		t.Fatal(err)
	}
	catalog, _ := ingestion.NewCatalog()
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	date, _ := ingestion.ParseCalendarDate("2026-08-22")
	parentID, err := runs.CreateRunAll(context.Background(), date, date, 3, ingestionrun.TriggerDirect, "detail-publication-crash", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM fincloud_loan_details WHERE account_no='RUN-ALL-CRASH'`)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=?`, parentID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, parentID)
	})
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='succeeded',finished_at=CURRENT_TIMESTAMP(6)
		WHERE parent_run_id=? AND job_key<>'loan_detail'`, parentID); err != nil {
		t.Fatal(err)
	}
	if changed, err := runs.ReconcileOneParent(context.Background()); err != nil || !changed {
		t.Fatalf("queue Detail child changed=%v error=%v", changed, err)
	}
	owner, _ := ingestionrun.NewOwnerID()
	child, err := runs.Claim(context.Background(), owner)
	if err != nil || child == nil || child.JobKey != "loan_detail" {
		t.Fatalf("claimed child=%+v error=%v", child, err)
	}
	progress := ingestionrun.Progress{Total: 1, Started: 1, Succeeded: 1, Rows: 4, Step: "publish_detail"}
	if err := runs.UpdateProgress(context.Background(), child.ID, owner, progress, nil); err != nil {
		t.Fatal(err)
	}
	record, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailLoan,
		[]byte(`{"id":"RUN-ALL-CRASH","nocif":"CIF","outstandingpinjaman":"1","biayapencairan":[{"namabiaya":"fee"}],"jadwalangsuran":[{"angsuranke":1}],"historybayar":[{"angsuranke":1}]}`), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	details := ingestionstore.NewDetailRepository(db)
	if err := details.Stage(context.Background(), child.ID, record); err != nil {
		t.Fatal(err)
	}
	if err := details.Publish(context.Background(), child.ID, owner, ingestion.DetailLoan, 1); err != nil {
		t.Fatal(err)
	}

	restarted, _ := ingestionrun.NewRepository(db, catalog)
	if changed, err := restarted.ReconcileOneParent(context.Background()); err != nil || !changed {
		t.Fatalf("restart reconciliation changed=%v error=%v", changed, err)
	}
	var parentStatus string
	if err := db.Get(&parentStatus, `SELECT status FROM ingestion_runs WHERE id=?`, parentID); err != nil || parentStatus != "completed" {
		t.Fatalf("parent status=%q error=%v", parentStatus, err)
	}
	if changed, err := restarted.ReconcileOneParent(context.Background()); err != nil || changed {
		t.Fatalf("repeated reconciliation changed=%v error=%v", changed, err)
	}
	var loanChildren int
	if err := db.Get(&loanChildren, `SELECT COUNT(*) FROM ingestion_runs WHERE parent_run_id=? AND job_key='loan_detail' AND status='succeeded'`, parentID); err != nil || loanChildren != 1 {
		t.Fatalf("successful loan children=%d error=%v", loanChildren, err)
	}
}
