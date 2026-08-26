//go:build integration

package dwhschema

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDetailCurrentStateSchemaIdentityAndCascades(t *testing.T) {
	db := integrationdb.Open(t)
	for _, table := range []string{
		"fincloud_cifs", "fincloud_saving_details", "fincloud_time_deposit_details", "fincloud_time_deposit_mutations",
		"fincloud_loan_details", "fincloud_loan_disbursement_fees", "fincloud_loan_repayment_schedule", "fincloud_loan_payment_history",
		"stg_fincloud_cif_details", "stg_fincloud_saving_details", "stg_fincloud_time_deposit_details", "stg_fincloud_time_deposit_mutations",
		"stg_fincloud_loan_details", "stg_fincloud_loan_disbursement_fees", "stg_fincloud_loan_repayment_schedule", "stg_fincloud_loan_payment_history",
	} {
		var dated int
		if err := db.Get(&dated, `SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='as_of_date'`, table); err != nil || dated != 0 {
			t.Fatalf("%s as_of_date columns=%d error=%v", table, dated, err)
		}
		var rowFormat string
		if err := db.Get(&rowFormat, `SELECT ROW_FORMAT FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, table); err != nil || !strings.EqualFold(rowFormat, "Dynamic") {
			t.Fatalf("%s row format=%q error=%v", table, rowFormat, err)
		}
	}
	extensions := []string{"personal_profiles", "ktp", "addresses", "employment", "company", "kyc", "regulatory"}
	for _, suffix := range extensions {
		finalTable, stageTable := "fincloud_cif_"+suffix, "stg_fincloud_cif_"+suffix
		for _, table := range []string{finalTable, stageTable} {
			var rowFormat string
			if err := db.Get(&rowFormat, `SELECT ROW_FORMAT FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, table); err != nil || !strings.EqualFold(rowFormat, "Dynamic") {
				t.Fatalf("%s row format=%q error=%v", table, rowFormat, err)
			}
		}
		var finalTimestamps, stageTimestamps int
		if err := db.Get(&finalTimestamps, `SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME IN ('created_at','updated_at')`, finalTable); err != nil || finalTimestamps != 2 {
			t.Fatalf("%s timestamp columns=%d error=%v", finalTable, finalTimestamps, err)
		}
		if err := db.Get(&stageTimestamps, `SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME IN ('created_at','updated_at')`, stageTable); err != nil || stageTimestamps != 0 {
			t.Fatalf("%s timestamp columns=%d error=%v", stageTable, stageTimestamps, err)
		}
		var timestamps []struct {
			Name    string `db:"COLUMN_NAME"`
			Default string `db:"COLUMN_DEFAULT"`
			Extra   string `db:"EXTRA"`
		}
		if err := db.Select(&timestamps, `SELECT COLUMN_NAME,COALESCE(COLUMN_DEFAULT,'') AS COLUMN_DEFAULT,EXTRA FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME IN ('created_at','updated_at') ORDER BY COLUMN_NAME`, finalTable); err != nil ||
			len(timestamps) != 2 || !strings.EqualFold(timestamps[0].Default, "CURRENT_TIMESTAMP(6)") ||
			!strings.EqualFold(timestamps[1].Default, "CURRENT_TIMESTAMP(6)") || !strings.Contains(strings.ToLower(timestamps[1].Extra), "on update current_timestamp(6)") {
			t.Fatalf("%s timestamp defaults=%+v error=%v", finalTable, timestamps, err)
		}
		for _, constraint := range []string{"fk_fincloud_cif_" + suffix + "_parent", "fk_stg_fincloud_cif_" + suffix + "_parent"} {
			var rule string
			if err := db.Get(&rule, `SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
				WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME=?`, constraint); err != nil || rule != "CASCADE" {
				t.Fatalf("%s delete rule=%q error=%v", constraint, rule, err)
			}
		}
	}
	for _, column := range []struct{ table, name, columnType string }{
		{"fincloud_loan_details", "application_number", "varchar(128)"},
		{"fincloud_loan_details", "insurance_premium", "decimal(24,6)"},
		{"fincloud_loan_details", "collateral_value", "decimal(24,6)"},
		{"fincloud_saving_details", "product_credit_interest_rate", "decimal(20,2)"},
	} {
		var got string
		if err := db.Get(&got, `SELECT COLUMN_TYPE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?`, column.table, column.name); err != nil || !strings.EqualFold(got, column.columnType) {
			t.Fatalf("%s.%s type=%q want=%q error=%v", column.table, column.name, got, column.columnType, err)
		}
	}
	for _, constraint := range []string{
		"fk_stg_fincloud_cif_details_run", "fk_stg_fincloud_saving_details_run",
		"fk_stg_fincloud_time_deposit_details_run", "fk_stg_fincloud_loan_details_run",
	} {
		var count int
		if err := db.Get(&count, `SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME=?`, constraint); err != nil || count != 0 {
			t.Fatalf("removed run-level staging constraint %s count=%d error=%v", constraint, count, err)
		}
	}
	for _, constraint := range []string{
		"fk_stg_fincloud_time_deposit_mutations_parent", "fk_stg_fincloud_loan_fees_parent",
		"fk_stg_fincloud_loan_schedule_parent", "fk_stg_fincloud_loan_history_parent",
	} {
		var rule string
		if err := db.Get(&rule, `SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME=?`, constraint); err != nil || rule != "CASCADE" {
			t.Fatalf("retained staging child constraint %s rule=%q error=%v", constraint, rule, err)
		}
	}
}

func TestDetailExpandedSchemaMaximumWidthInserts(t *testing.T) {
	db := integrationdb.Open(t)
	var version string
	if err := db.Get(&version, `SELECT VERSION()`); err != nil {
		t.Fatal(err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil || major != 8 || minor < 4 {
		t.Fatalf("MySQL 8.4+ required, got %q", version)
	}
	if _, err := db.Exec(`INSERT INTO fincloud_cifs
		(cif_no,customer_name,raw_payload,raw_checksum,last_fetched_at)
		VALUES ('MAX-WIDTH','name',JSON_OBJECT(),REPEAT('0',64),CURRENT_TIMESTAMP(6))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM fincloud_cifs WHERE cif_no='MAX-WIDTH'`) })
	for _, table := range []string{
		"fincloud_cif_personal_profiles", "fincloud_cif_ktp", "fincloud_cif_addresses", "fincloud_cif_employment",
		"fincloud_cif_company", "fincloud_cif_kyc", "fincloud_cif_regulatory",
	} {
		var columns []struct {
			Name   string `db:"COLUMN_NAME"`
			Length int    `db:"CHARACTER_MAXIMUM_LENGTH"`
		}
		if err := db.Select(&columns, `SELECT COLUMN_NAME,CHARACTER_MAXIMUM_LENGTH FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND DATA_TYPE='varchar' AND COLUMN_NAME<>'cif_no'
			ORDER BY ORDINAL_POSITION`, table); err != nil {
			t.Fatal(err)
		}
		names, placeholders := []string{"`cif_no`"}, []string{"?"}
		arguments := []any{"MAX-WIDTH"}
		for _, column := range columns {
			names = append(names, "`"+column.Name+"`")
			placeholders = append(placeholders, "?")
			arguments = append(arguments, strings.Repeat("🧪", column.Length))
		}
		if _, err := db.Exec("INSERT INTO `"+table+"` ("+strings.Join(names, ",")+") VALUES ("+strings.Join(placeholders, ",")+")", arguments...); err != nil {
			t.Fatalf("maximum-width insert into %s: %v", table, err)
		}
	}
}

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
