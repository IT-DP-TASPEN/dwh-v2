//go:build integration

package ingestionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestMasterExactSetPublicationVerificationAndForeignKeys(t *testing.T) {
	db := integrationdb.Open(t)
	repository := NewMasterRepository(db)
	_, _ = db.Exec(`DELETE FROM fincloud_reference_categories WHERE domain='cif'`)
	_, _ = db.Exec(`DELETE FROM fincloud_marketing_master`)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM stg_fincloud_reference_items`)
		_, _ = db.Exec(`DELETE FROM stg_fincloud_reference_categories`)
		_, _ = db.Exec(`DELETE FROM stg_fincloud_marketing_master`)
		_, _ = db.Exec(`DELETE FROM fincloud_reference_categories WHERE domain='cif'`)
		_, _ = db.Exec(`DELETE FROM fincloud_marketing_master`)
	})

	reference, err := ingestion.NormalizeReference(ingestion.ReferenceCIF, map[string]json.RawMessage{
		"group": json.RawMessage(`[{"id":"","descr":"Choose"},{"id":"A","descr":"First"},{"id":"A","descr":"Second"}]`),
		"empty": json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, owner := masterRun(t, db, "cif_reference_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID) })
	fetched := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	if err := repository.StageReference(context.Background(), runID, reference, fetched); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishReference(context.Background(), runID, owner, "cif_reference_master", reference); err != nil {
		t.Fatal(err)
	}
	var categories, items, duplicateCodes int
	if err := db.QueryRowx(`SELECT (SELECT COUNT(*) FROM fincloud_reference_categories WHERE domain='cif'),(SELECT COUNT(*) FROM fincloud_reference_items WHERE domain='cif'),(SELECT COUNT(*) FROM fincloud_reference_items WHERE domain='cif' AND category_key='group' AND code='A')`).Scan(&categories, &items, &duplicateCodes); err != nil || categories != 2 || items != 2 || duplicateCodes != 2 {
		t.Fatalf("reference counts categories=%d items=%d duplicates=%d error=%v", categories, items, duplicateCodes, err)
	}
	var originalUpdated time.Time
	if err := db.Get(&originalUpdated, `SELECT updated_at FROM fincloud_reference_categories WHERE domain='cif' AND category_key='group'`); err != nil {
		t.Fatal(err)
	}
	sameRun, sameOwner := masterRun(t, db, "cif_reference_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, sameRun) })
	refetched := fetched.Add(30 * time.Minute)
	if err := repository.StageReference(context.Background(), sameRun, reference, refetched); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishReference(context.Background(), sameRun, sameOwner, "cif_reference_master", reference); err != nil {
		t.Fatal(err)
	}
	var unchanged struct {
		Updated time.Time `db:"updated"`
		Fetched time.Time `db:"fetched"`
	}
	if err := db.Get(&unchanged, `SELECT updated_at updated,last_fetched_at fetched FROM fincloud_reference_categories WHERE domain='cif' AND category_key='group'`); err != nil || !unchanged.Updated.Equal(originalUpdated) || !unchanged.Fetched.Equal(refetched) {
		t.Fatalf("unchanged timestamps=%+v original=%s error=%v", unchanged, originalUpdated, err)
	}
	changed, _ := ingestion.NormalizeReference(ingestion.ReferenceCIF, map[string]json.RawMessage{
		"group": json.RawMessage(`[{"id":"","descr":"Choose"},{"id":"A","descr":"Updated"},{"id":"A","descr":"Second"}]`),
		"empty": json.RawMessage(`[]`),
		"added": json.RawMessage(`["X"]`),
	})
	changedRun, changedOwner := masterRun(t, db, "cif_reference_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, changedRun) })
	if err := repository.StageReference(context.Background(), changedRun, changed, fetched.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishReference(context.Background(), changedRun, strings.Repeat("b", 64), "cif_reference_master", changed); err == nil {
		t.Fatal("late previous owner published reference candidate")
	}
	if err := repository.PublishReference(context.Background(), changedRun, changedOwner, "cif_reference_master", changed); err != nil {
		t.Fatal(err)
	}
	var changedDescription string
	if err := db.QueryRowx(`SELECT
		(SELECT COUNT(*) FROM fincloud_reference_categories WHERE domain='cif'),
		(SELECT COUNT(*) FROM fincloud_reference_items WHERE domain='cif'),
		(SELECT description FROM fincloud_reference_items WHERE domain='cif' AND category_key='group' AND source_ordinal=1)`).Scan(&categories, &items, &changedDescription); err != nil || categories != 3 || items != 3 || changedDescription != "Updated" {
		t.Fatalf("changed reference categories=%d items=%d description=%q error=%v", categories, items, changedDescription, err)
	}

	tamperRun, tamperOwner := masterRun(t, db, "cif_reference_master")
	t.Cleanup(func() {
		_ = repository.CleanupRun(context.Background(), tamperRun)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, tamperRun)
	})
	if err := repository.StageReference(context.Background(), tamperRun, reference, fetched.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE stg_fincloud_reference_items SET code='tampered' WHERE ingestion_run_id=? LIMIT 1`, tamperRun); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishReference(context.Background(), tamperRun, tamperOwner, "cif_reference_master", reference); err == nil {
		t.Fatal("tampered reference candidate published")
	}
	if err := db.Get(&items, `SELECT COUNT(*) FROM fincloud_reference_items WHERE domain='cif'`); err != nil || items != 3 {
		t.Fatalf("tamper changed current rows=%d error=%v", items, err)
	}
	if err := repository.CleanupRun(context.Background(), tamperRun); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, tamperRun); err != nil {
		t.Fatal(err)
	}
	cancelRun, cancelOwner := masterRun(t, db, "cif_reference_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, cancelRun) })
	if err := repository.StageReference(context.Background(), cancelRun, reference, fetched.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET cancel_requested_at=UTC_TIMESTAMP(6) WHERE id=?`, cancelRun); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishReference(context.Background(), cancelRun, cancelOwner, "cif_reference_master", reference); err == nil {
		t.Fatal("cancelled reference candidate published")
	}
	if err := repository.CleanupRun(context.Background(), cancelRun); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, cancelRun); err != nil {
		t.Fatal(err)
	}

	empty, _ := ingestion.NormalizeReference(ingestion.ReferenceCIF, map[string]json.RawMessage{})
	emptyRun, emptyOwner := masterRun(t, db, "cif_reference_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, emptyRun) })
	if err := repository.StageReference(context.Background(), emptyRun, empty, fetched.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishReference(context.Background(), emptyRun, emptyOwner, "cif_reference_master", empty); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowx(`SELECT
		(SELECT COUNT(*) FROM fincloud_reference_categories WHERE domain='cif'),
		(SELECT COUNT(*) FROM fincloud_reference_items WHERE domain='cif')`).Scan(&categories, &items); err != nil || categories != 0 || items != 0 {
		t.Fatalf("valid empty left categories=%d items=%d error=%v", categories, items, err)
	}

	marketing, _ := ingestion.NormalizeMarketing([]json.RawMessage{json.RawMessage(`{"id":"001","nama_marketing":"Same","locationname":"HQ","aktif":"1","status_dokumen":"ok","tgltransaksi":"raw"}`), json.RawMessage(`{"id":"002","nama_marketing":"Same","locationname":"Branch","aktif":"1","status_dokumen":"ok","tgltransaksi":"raw"}`)})
	marketingRun, marketingOwner := masterRun(t, db, "marketing_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, marketingRun) })
	if err := repository.StageMarketing(context.Background(), marketingRun, marketing, fetched); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishMarketing(context.Background(), marketingRun, marketingOwner, "marketing_master", marketing); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := db.Select(&ids, `SELECT marketing_id FROM fincloud_marketing_master ORDER BY BINARY marketing_id`); err != nil || strings.Join(ids, ",") != "001,002" {
		t.Fatalf("Marketing IDs=%v error=%v", ids, err)
	}
	emptyMarketing, _ := ingestion.NormalizeMarketing([]json.RawMessage{})
	emptyMarketingRun, emptyMarketingOwner := masterRun(t, db, "marketing_master")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, emptyMarketingRun) })
	if err := repository.StageMarketing(context.Background(), emptyMarketingRun, emptyMarketing, fetched.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishMarketing(context.Background(), emptyMarketingRun, emptyMarketingOwner, "marketing_master", emptyMarketing); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&items, `SELECT COUNT(*) FROM fincloud_marketing_master`); err != nil || items != 0 {
		t.Fatalf("valid empty Marketing left rows=%d error=%v", items, err)
	}

	var stagingFKs int
	if err := db.Get(&stagingFKs, `SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME IN ('stg_fincloud_reference_categories','stg_fincloud_reference_items','stg_fincloud_marketing_master')`); err != nil || stagingFKs != 0 {
		t.Fatalf("staging FKs=%d error=%v", stagingFKs, err)
	}
	var deleteRule string
	if err := db.Get(&deleteRule, `SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME='fk_fincloud_reference_items_category'`); err != nil || deleteRule != "CASCADE" {
		t.Fatalf("current FK delete rule=%q error=%v", deleteRule, err)
	}
}

func masterRun(t *testing.T, db *sqlx.DB, jobKey string) (uint64, string) {
	t.Helper()
	owner := strings.Repeat("a", 64)
	parameters, err := ingestionrun.NewLiveSnapshotExecution(jobKey)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO ingestion_runs (kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,owner_id,claimed_at,heartbeat_at,started_at) VALUES ('job',?,'running',?,?,?,?, 'direct',?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, jobKey, parameters.Kind, parameters.Version, parameters.JSON, parameters.Checksum[:], fmt.Sprintf("master-test-%d", time.Now().UnixNano()), owner)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return uint64(id), owner
}
