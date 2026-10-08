//go:build integration

package fixedcoverage_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/fixedcoverage"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/jmoiron/sqlx"
)

func TestHistoricalCoverageBackfillAndCutover(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	t.Cleanup(func() { resetHistorical(t, db, true) })
	definitions := ingestion.FixedDefinitions()
	cif := definitions[0]

	t.Run("all_seven_mappings_and_durable_cutover", func(t *testing.T) {
		resetHistorical(t, db, false)
		for _, def := range definitions {
			if def.PublicationMode != ingestion.DateAddressable {
				continue
			}
			raw := "2026-08-12"
			if def.Key == "vault_mutation_report" {
				raw = "2026 -08-1200:15:00"
			}
			if def.Key == "teller_mutation_report" {
				raw = "2026-08-12 00:15:00"
			}
			seedHistoricalRow(t, db, def, raw, nil, "2026-08-01")
		}
		if !errors.Is(fixedcoverage.RequireReady(ctx, db), fixedcoverage.ErrNotReady) {
			t.Fatal("legacy installation prematurely ready")
		}
		if err := fixedcoverage.Backfill(ctx, db, 1); err != nil {
			t.Fatal(err)
		}
		if err := fixedcoverage.RequireReady(ctx, db); err != nil {
			t.Fatal(err)
		}
		for _, def := range definitions {
			if def.PublicationMode != ingestion.DateAddressable {
				continue
			}
			table, _ := ingestion.FixedTableName(def.Key)
			var date string
			if err := db.Get(&date, "SELECT DATE_FORMAT(coverage_date,'%Y-%m-%d') FROM `"+table+"`"); err != nil || date != "2026-08-12" {
				t.Fatalf("%s mapping=%q error=%v", def.Key, date, err)
			}
		}
		var complete, dates int
		mustGet(t, db, &complete, `SELECT COUNT(*) FROM fixed_report_coverage_backfill WHERE complete=TRUE`)
		mustGet(t, db, &dates, `SELECT COUNT(*) FROM fixed_report_date_publications`)
		if complete != 7 || dates != 0 {
			t.Fatalf("complete=%d fabricated historical authority rows=%d", complete, dates)
		}
		if err := fixedcoverage.Backfill(ctx, db, 1); err != nil {
			t.Fatalf("ready replay not idempotent: %v", err)
		}
	})

	t.Run("invalid_row_rolls_back_batch_and_resumes", func(t *testing.T) {
		resetHistorical(t, db, false)
		first := seedHistoricalRow(t, db, cif, "2026-08-12", nil, "2026-08-01")
		bad := seedHistoricalRow(t, db, cif, "2026-02-30", nil, "2026-08-01")
		last := seedHistoricalRow(t, db, cif, "2026-08-13", nil, "2026-08-01")
		if _, err := fixedcoverage.BackfillBatch(ctx, db, cif, 2); err == nil {
			t.Fatal("invalid legacy date silently skipped")
		}
		var watermark uint64
		var normalized int
		mustGet(t, db, &watermark, `SELECT last_id FROM fixed_report_coverage_backfill WHERE job_key=?`, cif.Key)
		mustGet(t, db, &normalized, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE coverage_date IS NOT NULL`)
		if watermark != 0 || normalized != 0 {
			t.Fatalf("failed batch mutated watermark=%d normalized=%d", watermark, normalized)
		}
		if done, err := fixedcoverage.BackfillBatch(ctx, db, cif, 1); done || err != nil {
			t.Fatalf("bounded first batch done=%t error=%v", done, err)
		}
		mustGet(t, db, &watermark, `SELECT last_id FROM fixed_report_coverage_backfill WHERE job_key=?`, cif.Key)
		if watermark != first {
			t.Fatalf("first batch watermark=%d want=%d", watermark, first)
		}
		if _, err := fixedcoverage.BackfillBatch(ctx, db, cif, 1); err == nil {
			t.Fatal("resumed malformed row accepted")
		}
		mustGet(t, db, &watermark, `SELECT last_id FROM fixed_report_coverage_backfill WHERE job_key=?`, cif.Key)
		if watermark != first {
			t.Fatalf("failed resume advanced watermark=%d", watermark)
		}
		mustExec(t, db, `UPDATE fincloud_cif_opening_reports SET register_date='2026-08-12' WHERE id=?`, bad)
		if err := fixedcoverage.Backfill(ctx, db, 1); err != nil {
			t.Fatal(err)
		}
		mustGet(t, db, &watermark, `SELECT last_id FROM fixed_report_coverage_backfill WHERE job_key=?`, cif.Key)
		mustGet(t, db, &normalized, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE coverage_date IS NOT NULL`)
		if watermark != last || normalized != 3 {
			t.Fatalf("resume watermark=%d want=%d normalized=%d", watermark, last, normalized)
		}
		if err := fixedcoverage.Backfill(ctx, db, 1); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("existing_coverage_must_match_valid_source", func(t *testing.T) {
		for _, item := range []struct{ raw, coverage string }{
			{"2026-08-12", "2026-08-13"}, {"bad", "2026-08-12"}, {"", "2026-08-12"},
		} {
			resetHistorical(t, db, false)
			seedHistoricalRow(t, db, cif, item.raw, item.coverage, "2026-08-01")
			if err := fixedcoverage.Backfill(ctx, db, 1); err == nil {
				t.Fatalf("unsafe preexisting date accepted raw=%q coverage=%q", item.raw, item.coverage)
			}
			if !errors.Is(fixedcoverage.RequireReady(ctx, db), fixedcoverage.ErrNotReady) {
				t.Fatal("malformed historical coverage activated model")
			}
			var watermark uint64
			mustGet(t, db, &watermark, `SELECT last_id FROM fixed_report_coverage_backfill WHERE job_key=?`, cif.Key)
			if watermark != 0 {
				t.Fatal("invalid preexisting coverage advanced progress")
			}
		}
	})

	t.Run("pending_model_blocks_new_loads", func(t *testing.T) {
		resetHistorical(t, db, false)
		run := seedHistoricalRun(t, db, cif.Key, "succeeded", "")
		date := historicalDate(t, "2026-08-12")
		plan, err := ingestion.BuildFixedPlan(cif, ingestion.FixedDateRangeParams{From: date, To: date}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ingestionstore.NewFixedRepository(db).BeginLoad(ctx, run, cif, plan); !errors.Is(err, fixedcoverage.ErrNotReady) {
			t.Fatalf("unprepared BeginLoad error=%v", err)
		}
		var count int
		mustGet(t, db, &count, `SELECT COUNT(*) FROM fixed_report_loads`)
		if count != 0 {
			t.Fatal("pending model created candidate")
		}
	})

	t.Run("queued_or_running_fixed_run_blocks_backfill", func(t *testing.T) {
		for _, status := range []string{"queued", "running"} {
			resetHistorical(t, db, false)
			seedHistoricalRow(t, db, cif, "2026-08-12", nil, "2026-08-01")
			seedHistoricalRun(t, db, cif.Key, status, "backfill-test-owner")
			if err := fixedcoverage.Backfill(ctx, db, 1); err == nil {
				t.Fatalf("%s Fixed execution did not block preparation", status)
			}
			var normalized int
			mustGet(t, db, &normalized, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE coverage_date IS NOT NULL`)
			if normalized != 0 || !errors.Is(fixedcoverage.RequireReady(ctx, db), fixedcoverage.ErrNotReady) {
				t.Fatal("active run allowed partial preparation/cutover")
			}
		}
	})

	t.Run("authoritative_publication_removes_all_backfilled_legacy_duplicates", func(t *testing.T) {
		resetHistorical(t, db, false)
		seedHistoricalRow(t, db, cif, "2026-08-12", nil, "2026-08-01")
		seedHistoricalRow(t, db, cif, "2026-08-12", nil, "2026-08-12")
		if err := fixedcoverage.Backfill(ctx, db, 1); err != nil {
			t.Fatal(err)
		}
		var before int
		mustGet(t, db, &before, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE coverage_date='2026-08-12'`)
		if before != 2 {
			t.Fatal("backfill deduplicated legacy source rows")
		}
		owner := strings.Repeat("b", 64)
		run := seedHistoricalRun(t, db, cif.Key, "running", owner)
		date := historicalDate(t, "2026-08-12")
		plan, err := ingestion.BuildFixedPlan(cif, ingestion.FixedDateRangeParams{From: date, To: date}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
		if err != nil {
			t.Fatal(err)
		}
		repository := ingestionstore.NewFixedRepository(db)
		load, err := repository.BeginLoad(ctx, run, cif, plan)
		if err != nil {
			t.Fatal(err)
		}
		values := make([]string, len(cif.RequiredHeaders))
		for i, header := range cif.RequiredHeaders {
			if header == "Register Date" {
				values[i] = date.String()
			}
		}
		rows, err := ingestion.ParseFixedCSV(ctx, cif, "", strings.Join(cif.RequiredHeaders, "|")+"\n"+strings.Join(values, "|")+"\n")
		if err != nil {
			t.Fatal(err)
		}
		segment := ingestionstore.FixedSegment{Index: 0, SourcePeriodFrom: date, SourcePeriodTo: date, AsOfDate: date, SourceRows: rows}
		if err := repository.StageMemberSegment(ctx, cif, load, plan.Members[0], segment); err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		ingestion.WriteFixedMemberChecksumPart(hash, rows[0].SourceRowChecksum)
		var checksum [sha256.Size]byte
		copy(checksum[:], hash.Sum(nil))
		if err := repository.FinalizeMemberCandidate(ctx, cif, load, plan.Members[0], 1, 1, checksum); err != nil {
			t.Fatal(err)
		}
		if err := repository.Promote(ctx, run, owner, cif, load); err != nil {
			t.Fatal(err)
		}
		var count, history, members int
		var finalLoad, activeLoad uint64
		var status string
		mustGet(t, db, &count, `SELECT COUNT(*) FROM fincloud_cif_opening_reports`)
		mustGet(t, db, &finalLoad, `SELECT load_id FROM fincloud_cif_opening_reports`)
		mustGet(t, db, &activeLoad, `SELECT active_load_id FROM fixed_report_date_publications WHERE job_key=? AND coverage_date=?`, cif.Key, date.String())
		mustGet(t, db, &history, `SELECT COUNT(*) FROM fixed_report_loads`)
		mustGet(t, db, &members, `SELECT COUNT(*) FROM fixed_report_load_members`)
		mustGet(t, db, &status, `SELECT status FROM ingestion_runs WHERE id=?`, run)
		if count != 1 || finalLoad != load || activeLoad != load || history != 3 || members != 3 || status != "succeeded" {
			t.Fatalf("final=%d final_load=%d authority=%d history=%d members=%d run=%s", count, finalLoad, activeLoad, history, members, status)
		}
	})
}

func resetHistorical(t *testing.T, db *sqlx.DB, ready bool) {
	t.Helper()
	for _, def := range ingestion.FixedDefinitions() {
		table, _ := ingestion.FixedTableName(def.Key)
		mustExec(t, db, "DELETE FROM `stg_"+table+"`")
		mustExec(t, db, "DELETE FROM `"+table+"`")
	}
	for _, table := range []string{"fixed_report_date_publications", "fixed_report_publications", "fixed_report_load_segments", "fixed_report_load_members", "fixed_report_loads"} {
		mustExec(t, db, "DELETE FROM `"+table+"`")
	}
	mustExec(t, db, `UPDATE ingestion_runs SET status='failed' WHERE job_key IN (SELECT job_key FROM fixed_report_publication_locks) AND status IN ('queued','running')`)
	mustExec(t, db, `UPDATE fixed_report_coverage_state SET ready=?,ready_at=NULL WHERE id=1`, ready)
	mustExec(t, db, `UPDATE fixed_report_coverage_backfill SET last_id=0,complete=?`, ready)
}

func seedHistoricalRun(t *testing.T, db *sqlx.DB, key, status, owner string) uint64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO ingestion_runs (kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,owner_id) VALUES ('job',?,?,'fixed_range_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?)`, key, status, owner)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return uint64(id)
}

func seedHistoricalRow(t *testing.T, db *sqlx.DB, def ingestion.FixedDefinition, raw string, coverage any, from string) uint64 {
	t.Helper()
	run := seedHistoricalRun(t, db, def.Key, "succeeded", "")
	result, err := db.Exec(`INSERT INTO fixed_report_loads (ingestion_run_id,job_key,period_from,period_to,status,expected_member_count,manifest_checksum) VALUES (?, ?,?,'2026-09-30','published',1,UNHEX(REPEAT('00',32)))`, run, def.Key, from)
	if err != nil {
		t.Fatal(err)
	}
	load, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO fixed_report_load_members (load_id,member_key,status,row_count,member_checksum) VALUES (?,'legacy','success',1,UNHEX(REPEAT('00',32)))`, load)
	table, _ := ingestion.FixedTableName(def.Key)
	columns := []string{"load_id", "row_ordinal", "source_segment_index", "source_row_number", "source_row_checksum", "period_from", "period_to", "as_of_date", "coverage_date"}
	args := []any{load, 1, 0, 2, strings.Repeat("0", 64), from, "2026-09-30", "2026-08-12", coverage}
	if def.SourceLocationID {
		columns, args = append(columns, "source_location_id"), append(args, "008")
	}
	if !def.SnapshotDate {
		columns, args = append(columns, ingestion.FixedColumnName(def.CoverageDateHeader)), append(args, raw)
	}
	result, err = db.Exec("INSERT INTO `"+table+"` (`"+strings.Join(columns, "`,`")+"`) VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return uint64(id)
}

func historicalDate(t *testing.T, value string) ingestion.CalendarDate {
	t.Helper()
	date, err := ingestion.ParseCalendarDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return date
}

func mustExec(t *testing.T, db *sqlx.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(fmt.Errorf("historical fixture SQL: %w", err))
	}
}

func mustGet(t *testing.T, db *sqlx.DB, destination any, query string, args ...any) {
	t.Helper()
	if err := db.Get(destination, query, args...); err != nil {
		t.Fatal(err)
	}
}
