//go:build integration

package dwhschema

import (
	"context"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestOpaqueSourceKeysUseExactDatabaseSemantics(t *testing.T) {
	db := integrationdb.Open(t)
	defer db.Exec(`DELETE FROM source_settings WHERE source_id IN ('phase3_case_probe','Phase3_case_probe')`)
	if _, err := db.Exec(`INSERT INTO source_settings (source_id,enabled) VALUES ('phase3_case_probe',TRUE),('Phase3_case_probe',TRUE)`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM source_settings WHERE BINARY source_id IN (BINARY 'phase3_case_probe',BINARY 'Phase3_case_probe')`); err != nil || count != 2 {
		t.Fatalf("exact source key count=%d error=%v", count, err)
	}
}

func TestRuntimeSchemaCompatibility(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	if err := VerifyRuntime(ctx, db); err != nil {
		t.Fatalf("canonical schema rejected: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM source_settings WHERE source_id='cif_detail'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`INSERT INTO source_settings (source_id,enabled,updated_by_user_id) VALUES ('cif_detail',TRUE,NULL) ON DUPLICATE KEY UPDATE source_id=VALUES(source_id)`)
	})
	if err := VerifyRuntime(ctx, db); err == nil || !strings.Contains(err.Error(), "source settings") {
		t.Fatalf("missing source key error=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO source_settings (source_id,enabled,updated_by_user_id) VALUES ('cif_detail',TRUE,NULL)`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`DELETE FROM ingestion_runtime_settings WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`INSERT INTO ingestion_runtime_settings (id,max_running_jobs,fixed_member_concurrency,detail_concurrency) VALUES (1,2,4,3) ON DUPLICATE KEY UPDATE id=VALUES(id)`)
	})
	if err := VerifyRuntime(ctx, db); err == nil || !strings.Contains(err.Error(), "runtime settings") {
		t.Fatalf("missing runtime settings error=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO ingestion_runtime_settings (id,max_running_jobs,fixed_member_concurrency,detail_concurrency) VALUES (1,2,4,3)`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`ALTER TABLE schedule_occurrences DROP INDEX uq_schedule_occurrences_active`); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_, _ = db.Exec(`ALTER TABLE schedule_occurrences ADD UNIQUE KEY uq_schedule_occurrences_active (active_schedule_id)`)
		}
	})
	if err := VerifyRuntime(ctx, db); err == nil || !strings.Contains(err.Error(), "uq_schedule_occurrences_active") {
		t.Fatalf("missing safety index error=%v", err)
	}
	if _, err := db.Exec(`ALTER TABLE schedule_occurrences ADD UNIQUE KEY uq_schedule_occurrences_active (active_schedule_id)`); err != nil {
		t.Fatal(err)
	}
	restored = true
	if err := VerifyRuntime(ctx, db); err != nil {
		t.Fatalf("restored schema rejected: %v", err)
	}
}

func TestRuntimeSchemaRefusesIncompleteLineageWithoutMigrating(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id,is_applied,tstamp) VALUES (?,FALSE,NOW())`, CurrentVersion); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_, _ = db.Exec(`INSERT INTO goose_db_version (version_id,is_applied,tstamp) VALUES (?,TRUE,NOW())`, CurrentVersion)
		}
	})
	var before int
	if err := db.Get(&before, `SELECT COUNT(*) FROM goose_db_version`); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntime(ctx, db); err == nil {
		t.Fatal("incomplete Goose lineage accepted")
	}
	var after int
	if err := db.Get(&after, `SELECT COUNT(*) FROM goose_db_version`); err != nil || after != before {
		t.Fatalf("readiness changed Goose history: before=%d after=%d error=%v", before, after, err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id,is_applied,tstamp) VALUES (?,TRUE,NOW())`, CurrentVersion); err != nil {
		t.Fatal(err)
	}
	restored = true
}
