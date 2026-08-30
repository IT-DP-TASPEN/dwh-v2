//go:build integration

package ingestionstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDetailStagingNoLongerLocksRunProgressRow(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	owner := strings.Repeat("1", 64)
	runID := insertOwnedStoreRun(t, db, "saving_detail", owner)
	tx, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO stg_fincloud_saving_details
		(ingestion_run_id,as_of_date,account_no,cif_no,beginning_balance,balance,raw_payload,raw_checksum,last_fetched_at)
		VALUES (?,'2026-08-26','NO-FK-LOCK','CIF',0,0,JSON_OBJECT(),REPEAT('0',64),UTC_TIMESTAMP(6))`, runID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runs.UpdateProgress(ctx, runID, owner, ingestionrun.Progress{Total: 1, Step: "fetch_details"}, nil)
	}()
	if err := <-done; err != nil {
		t.Fatalf("staging insert still blocked progress row: %v", err)
	}
}

func TestStaleDetailWorkerCannotPublishAfterNewRunSucceeds(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	details := NewDetailRepository(db)
	oldOwner := strings.Repeat("2", 64)
	oldRun := insertOwnedStoreRun(t, db, "saving_detail", oldOwner)
	if err := details.Stage(context.Background(), oldRun, mapDetailRecord(t, ingestion.DetailSaving, "FENCED-ACCOUNT", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET heartbeat_at=UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE WHERE id=?`, oldRun); err != nil {
		t.Fatal(err)
	}
	if recovery, err := runs.RecoverOneStale(context.Background(), 2*time.Minute, strings.Repeat("3", 64)); err != nil || recovery == nil || recovery.RunID != oldRun {
		t.Fatalf("recovery=%+v error=%v", recovery, err)
	}
	newOwner := strings.Repeat("4", 64)
	newRun := insertOwnedStoreRun(t, db, "saving_detail", newOwner)
	if err := details.Stage(context.Background(), newRun, mapDetailRecord(t, ingestion.DetailSaving, "FENCED-ACCOUNT", 2)); err != nil {
		t.Fatal(err)
	}
	if err := details.Publish(context.Background(), newRun, newOwner, ingestion.DetailSaving, 1); err != nil {
		t.Fatal(err)
	}
	if err := details.Publish(context.Background(), oldRun, oldOwner, ingestion.DetailSaving, 1); err == nil {
		t.Fatal("stale worker published after ownership recovery")
	}
	var status string
	if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, newRun); err != nil || status != "succeeded" {
		t.Fatalf("new run status=%q error=%v", status, err)
	}
	var payload string
	if err := db.Get(&payload, `SELECT CAST(raw_payload AS CHAR) FROM fincloud_saving_details WHERE account_no='FENCED-ACCOUNT'`); err != nil || !strings.Contains(payload, `"version": 2`) {
		t.Fatalf("authoritative payload=%s error=%v", payload, err)
	}
}

func TestDetailCleanupReclaimsRecoveredAndOrphanStagingWithoutRunFK(t *testing.T) {
	db := integrationdb.Open(t)
	owner := strings.Repeat("5", 64)
	runID := insertOwnedStoreRun(t, db, "saving_detail", owner)
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='abandoned',finished_at=UTC_TIMESTAMP(6) WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	for id, account := range map[uint64]string{runID: "RECOVERED-STAGE", 9223372036854775807: "ORPHAN-STAGE"} {
		if _, err := db.Exec(`INSERT INTO stg_fincloud_saving_details
			(ingestion_run_id,as_of_date,account_no,cif_no,beginning_balance,balance,raw_payload,raw_checksum,last_fetched_at)
			VALUES (?,'2026-08-26',?,'CIF',0,0,JSON_OBJECT(),REPEAT('0',64),UTC_TIMESTAMP(6))`, id, account); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewDetailRepository(db)
	deleted, err := repository.CleanupTerminal(context.Background(), 100)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d error=%v", deleted, err)
	}
	var remaining int
	if err := db.Get(&remaining, `SELECT COUNT(*) FROM stg_fincloud_saving_details WHERE account_no IN ('RECOVERED-STAGE','ORPHAN-STAGE')`); err != nil || remaining != 0 {
		t.Fatalf("remaining=%d error=%v", remaining, err)
	}
}

func TestFixedPublicationRollsBackWhenFinalOwnershipFenceMisses(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[0]
	date, _ := ingestion.ParseCalendarDate("2026-08-26")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: date, To: date}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("6", 64)
	runID := insertOwnedStoreRun(t, db, definition.Key, owner)
	repository := NewFixedRepository(db)
	loadID, err := repository.BeginLoad(context.Background(), runID, definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		if err := stageMemberFixture(repository, context.Background(), definition, loadID, member,
			[]FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, "")}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='abandoned',finished_at=UTC_TIMESTAMP(6) WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if err := repository.Promote(context.Background(), runID, owner, definition, loadID); err == nil {
		t.Fatal("stale fixed worker published")
	}
	var publications, rows int
	if err := db.Get(&publications, `SELECT COUNT(*) FROM fixed_report_publications WHERE active_load_id=?`, loadID); err != nil {
		t.Fatal(err)
	}
	storage, _ := fixedStorageFor(definition)
	if err := db.Get(&rows, "SELECT COUNT(*) FROM `"+storage.finalTable+"` WHERE load_id=?", loadID); err != nil || publications != 0 || rows != 0 {
		t.Fatalf("rolled-back fixed publication: publications=%d rows=%d error=%v", publications, rows, err)
	}
}

func TestRecoveredFixedRunUsesFreshLoadWithoutSegmentResume(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	definition := ingestion.FixedDefinitions()[0]
	date, _ := ingestion.ParseCalendarDate("2026-08-26")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: date, To: date}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	var wasEnabled bool
	if err := db.Get(&wasEnabled, `SELECT enabled FROM source_settings WHERE source_id=?`, definition.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id=?`, definition.Key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id=?`, wasEnabled, definition.Key)
	})

	oldOwner := strings.Repeat("8", 64)
	oldRun := insertOwnedStoreRun(t, db, definition.Key, oldOwner)
	repository := NewFixedRepository(db)
	oldLoad, err := repository.BeginLoad(context.Background(), oldRun, definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	segment := FixedSegment{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, "")}
	if err := repository.StageMemberSegment(context.Background(), definition, oldLoad, plan.Members[0], segment); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET heartbeat_at=UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE WHERE id=?`, oldRun); err != nil {
		t.Fatal(err)
	}
	if recovered, err := runs.RecoverOneStale(context.Background(), 2*time.Minute, strings.Repeat("9", 64)); err != nil || recovered == nil || recovered.RunID != oldRun {
		t.Fatalf("recovery=%+v error=%v", recovered, err)
	}

	parameters, err := ingestionrun.NewRangeExecution(definition.Key, date, date)
	if err != nil {
		t.Fatal(err)
	}
	newRunID, err := runs.Submit(context.Background(), definition.Key, parameters, ingestionrun.TriggerDirect, "fresh-after-recovery", nil)
	if err != nil {
		t.Fatal(err)
	}
	newOwner := strings.Repeat("a", 64)
	newRun, err := runs.Claim(context.Background(), newOwner)
	if err != nil || newRun == nil || newRun.ID != newRunID {
		t.Fatalf("claim=%+v error=%v", newRun, err)
	}
	newLoad, err := repository.BeginLoad(context.Background(), newRunID, definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	if newLoad == oldLoad {
		t.Fatal("recovered execution reused partial Fixed load")
	}
	var oldSegments, newSegments uint64
	if err := db.Get(&oldSegments, `SELECT staged_segment_count FROM fixed_report_load_members WHERE load_id=?`, oldLoad); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&newSegments, `SELECT staged_segment_count FROM fixed_report_load_members WHERE load_id=?`, newLoad); err != nil || oldSegments != 1 || newSegments != 0 {
		t.Fatalf("old segments=%d new segments=%d error=%v", oldSegments, newSegments, err)
	}
	result, err := repository.CleanupTerminal(context.Background(), 100)
	if err != nil || result.Rows != 1 {
		t.Fatalf("cleanup=%+v error=%v", result, err)
	}
	storage, _ := fixedStorageFor(definition)
	var oldRows, newRows int
	if err := db.Get(&oldRows, "SELECT COUNT(*) FROM `"+storage.stagingTable+"` WHERE load_id=?", oldLoad); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&newRows, "SELECT COUNT(*) FROM `"+storage.stagingTable+"` WHERE load_id=?", newLoad); err != nil || oldRows != 0 || newRows != 0 {
		t.Fatalf("old staging=%d new staging=%d error=%v", oldRows, newRows, err)
	}
	t.Cleanup(func() {
		resetFixed(t, db.DB)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, newRunID)
	})
}

func TestMaintenanceSnapshotRollsBackWhenFinalOwnershipFenceMisses(t *testing.T) {
	db := integrationdb.Open(t)
	definition := findMaintenance(t, "cbr_customer")
	date, _ := ingestion.ParseCalendarDate("2026-08-26")
	oldSnapshot, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "Customer\nold\n")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewMaintenanceRepository(db)
	baseline := MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: oldSnapshot}
	if err := repository.saveSnapshotWithoutRunFence(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
		_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
		_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	})
	owner := strings.Repeat("7", 64)
	runID := insertOwnedStoreRun(t, db, definition.Key, owner)
	newSnapshot, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "Customer\nnew\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='abandoned',finished_at=UTC_TIMESTAMP(6) WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	err = repository.SaveSnapshot(context.Background(), runID, owner, MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: newSnapshot})
	if err == nil {
		t.Fatal("stale maintenance worker published")
	}
	column, _ := quoteIdentifier(oldSnapshot.Columns[0].PhysicalName)
	var value string
	if err := db.Get(&value, "SELECT "+column+" FROM `"+definition.TableName+"` WHERE as_of_date=?", date.String()); err != nil || value != "old" {
		t.Fatalf("maintenance snapshot changed to %q error=%v", value, err)
	}
}

func insertOwnedStoreRun(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, job, owner string) uint64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,
		 owner_id,claimed_at,heartbeat_at,started_at)
		VALUES ('job',?,'running','detail_live_snapshot_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,
		 UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, job, owner)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM stg_fincloud_saving_details WHERE ingestion_run_id=?`, id)
		_, _ = db.Exec(`DELETE FROM fincloud_saving_details WHERE account_no='FENCED-ACCOUNT'`)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id)
	})
	return uint64(id)
}
