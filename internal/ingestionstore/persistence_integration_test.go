//go:build integration

package ingestionstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/shopspring/decimal"
)

func TestFixedCompleteSetPromotionAndStaleOrdering(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[2]
	date, _ := ingestion.ParseCalendarDate("2026-08-12")
	locations, _ := ingestion.FreezeLocations([]string{"000", "008"})
	plan, err := ingestion.BuildFixedSnapshotPlan(definition, ingestion.FixedSnapshotDateParams{Date: date}, locations)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	load1, err := repository.BeginLoad(context.Background(), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		rows := fixedRows(t, definition, member.SourceLocationID)
		if err := repository.StageMember(context.Background(), definition, load1, member.MemberKey, []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: rows}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.Promote(context.Background(), definition, load1); err != nil {
		t.Fatal(err)
	}
	if err := repository.StageMember(context.Background(), definition, load1, plan.Members[0].MemberKey, []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, plan.Members[0].SourceLocationID)}}); err == nil {
		t.Fatal("published load accepted member replacement")
	}
	load2, err := repository.BeginLoad(context.Background(), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.StageMember(context.Background(), definition, load2, plan.Members[0].MemberKey, []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, plan.Members[0].SourceLocationID)}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Promote(context.Background(), definition, load2); err == nil {
		t.Fatal("partial location set promoted")
	}
	var active uint64
	if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, date.String(), date.String()); err != nil || active != load1 {
		t.Fatalf("active load=%d want=%d error=%v", active, load1, err)
	}
	load3, err := repository.BeginLoad(context.Background(), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		if err := repository.StageMember(context.Background(), definition, load3, member.MemberKey, []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, member.SourceLocationID)}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE stg_fincloud_balance_sheet_reports SET source_row_checksum=REPEAT('0',64) WHERE load_id=? LIMIT 1`, load3); err != nil {
		t.Fatal(err)
	}
	if err := repository.Promote(context.Background(), definition, load3); err == nil {
		t.Fatal("tampered staged member promoted")
	}
}

func TestFixedFirstPublicationRaceUsesMonotonicLoadID(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[0]
	from, _ := ingestion.ParseCalendarDate("2026-08-11")
	to, _ := ingestion.ParseCalendarDate("2026-08-12")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	loads := make([]uint64, 2)
	for index := range loads {
		loads[index], err = repository.BeginLoad(context.Background(), definition, plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.StageMember(context.Background(), definition, loads[index], ingestion.SingleFixedMemberKey,
			[]FixedSegment{{Index: 0, AsOfDate: to, SourceRows: fixedRows(t, definition, "")}}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsByLoad := make([]error, 2)
	done := make(chan int, 2)
	for index := range loads {
		go func(index int) {
			<-start
			errorsByLoad[index] = repository.Promote(context.Background(), definition, loads[index])
			done <- index
		}(index)
	}
	close(start)
	<-done
	<-done
	if errorsByLoad[1] != nil {
		t.Fatalf("newer load failed: %v", errorsByLoad[1])
	}
	var active uint64
	if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, from.String(), to.String()); err != nil || active != loads[1] {
		t.Fatalf("race active load=%d want=%d errors=%v", active, loads[1], errorsByLoad)
	}
}

func TestDetailSnapshotsAndDecimalGuard(t *testing.T) {
	db := integrationdb.Open(t)
	for _, table := range []string{"fincloud_loan_disbursement_fees", "fincloud_loan_repayment_schedule", "fincloud_loan_payment_history", "fincloud_loan_details"} {
		if _, err := db.Exec("DELETE FROM `" + table + "`"); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewDetailRepository(db)
	for _, value := range []string{"2026-08-11", "2026-08-12"} {
		date, _ := ingestion.ParseCalendarDate(value)
		record, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailLoan,
			[]byte(`{"id":"LN-1","nocif":"CIF-1","outstandingpinjaman":"1234567890.123456","jadwalangsuran":[{"angsuranke":1,"angsuran":"10.250000"}]}`), date, time.Now().UTC())
		if err != nil || repository.SaveLoanSnapshot(context.Background(), record) != nil {
			t.Fatalf("save detail: map=%v", err)
		}
	}
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM fincloud_loan_details WHERE account_no='LN-1'`); err != nil || count != 2 {
		t.Fatalf("snapshot count=%d error=%v", count, err)
	}
	date, _ := ingestion.ParseCalendarDate("2026-08-12")
	invalid, _ := ingestion.MapDetailPayload(context.Background(), ingestion.DetailLoan,
		[]byte(`{"id":"LN-1","nocif":"CIF-1","outstandingpinjaman":"1.1234567"}`), date, time.Now().UTC())
	if err := repository.SaveLoanSnapshot(context.Background(), invalid); err == nil {
		t.Fatal("excess decimal scale accepted")
	}
	rollbackRecord, _ := ingestion.MapDetailPayload(context.Background(), ingestion.DetailLoan,
		[]byte(`{"id":"LN-1","nocif":"CIF-1","outstandingpinjaman":"999.000000","jadwalangsuran":[{"angsuranke":1,"angsuran":"1.000000"}]}`), date, time.Now().UTC())
	rollbackRecord.Children["jadwalangsuran"][0].Fields["installment_amount"] = decimal.RequireFromString("1.1234567")
	if err := repository.SaveLoanSnapshot(context.Background(), rollbackRecord); err == nil {
		t.Fatal("invalid child decimal accepted")
	}
	var outstanding string
	if err := db.Get(&outstanding, `SELECT CAST(outstanding_principal AS CHAR) FROM fincloud_loan_details WHERE as_of_date=? AND account_no='LN-1'`, date.String()); err != nil || outstanding != "1234567890.123456" {
		t.Fatalf("failed child changed parent: outstanding=%s error=%v", outstanding, err)
	}
}

func TestMaintenanceDynamicAdditiveRetry(t *testing.T) {
	db := integrationdb.Open(t)
	definition := findMaintenance(t, "cbr_customer")
	_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
	_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
	_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	t.Cleanup(func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
		_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
		_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	})
	date, _ := ingestion.ParseCalendarDate("2026-08-12")
	parsed, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "One|Two\na|b\n")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewMaintenanceRepository(db)
	if err := repository.SaveSnapshot(context.Background(), MaintenanceSnapshot{RequestedDate: date, MatchedDate: date, FileName: "cbrcustomer.csv", Parsed: parsed}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER phase3_fail_cbr_customer BEFORE INSERT ON `" + definition.TableName + "` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced load failure'"); err != nil {
		t.Fatal(err)
	}
	broken, _ := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "One|Two|Three\nc|d|e\n")
	if err := repository.SaveSnapshot(context.Background(), MaintenanceSnapshot{RequestedDate: date, MatchedDate: date, FileName: "cbrcustomer.csv", Parsed: broken}); err == nil {
		t.Fatal("broken load succeeded")
	}
	if _, err := db.Exec(`DROP TRIGGER phase3_fail_cbr_customer`); err != nil {
		t.Fatal(err)
	}
	var newColumn int
	if err := db.Get(&newColumn, `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='three'`, definition.TableName); err != nil || newColumn != 1 {
		t.Fatalf("additive DDL did not survive failed load: count=%d error=%v", newColumn, err)
	}
	valid, _ := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "One|Two|Three\nc|d|e\n")
	if err := repository.SaveSnapshot(context.Background(), MaintenanceSnapshot{RequestedDate: date, MatchedDate: date, FileName: "cbrcustomer.csv", Parsed: valid}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardPinnedLockConnection(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	connection, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var firstID int64
	if err := connection.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	lockName := "phase3-discard-proof"
	var acquired int
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&acquired); err != nil || acquired != 1 {
		t.Fatalf("acquire=%d error=%v", acquired, err)
	}
	if err := discardPinnedConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := connection.PingContext(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("discarded connection ping error=%v", err)
	}
	second, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var secondID int64
	if err := second.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatalf("discarded connection %d returned to pool", firstID)
	}
	var free int
	if err := second.QueryRowContext(ctx, "SELECT IS_FREE_LOCK(?)", lockName).Scan(&free); err != nil || free != 1 {
		t.Fatalf("lock not released by physical close: free=%d error=%v", free, err)
	}
}

func TestConcurrentMaintenanceSchemaEvolutionSerializes(t *testing.T) {
	db := integrationdb.Open(t)
	definition := findMaintenance(t, "cbr_customer")
	_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
	_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
	_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	t.Cleanup(func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
		_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
		_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	})
	date, _ := ingestion.ParseCalendarDate("2026-08-12")
	repository := NewMaintenanceRepository(db)
	start := make(chan struct{})
	errorsByHeader := make(chan error, 2)
	for _, header := range []string{"Alpha", "Beta"} {
		go func(header string) {
			parsed, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, header+"\nvalue\n")
			if err == nil {
				<-start
				err = repository.SaveSnapshot(context.Background(), MaintenanceSnapshot{RequestedDate: date, MatchedDate: date, FileName: "cbrcustomer.csv", Parsed: parsed})
			}
			errorsByHeader <- err
		}(header)
	}
	close(start)
	for range 2 {
		if err := <-errorsByHeader; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME IN ('alpha','beta')`, definition.TableName); err != nil || count != 2 {
		t.Fatalf("concurrent schema columns=%d error=%v", count, err)
	}
}

func fixedRows(t *testing.T, definition ingestion.FixedDefinition, location string) []ingestion.FixedCSVRow {
	t.Helper()
	values := make([]string, len(definition.RequiredHeaders))
	content := strings.Join(definition.RequiredHeaders, "|") + "\n" + strings.Join(values, "|") + "\n"
	rows, err := ingestion.ParseFixedCSV(context.Background(), definition, location, content)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func findMaintenance(t *testing.T, key string) ingestion.MaintenanceDefinition {
	t.Helper()
	for _, definition := range ingestion.MaintenanceDefinitions() {
		if definition.Key == key {
			return definition
		}
	}
	t.Fatalf("missing maintenance %s", key)
	return ingestion.MaintenanceDefinition{}
}

func resetFixed(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"stg_fincloud_cif_opening_reports", "stg_fincloud_journal_transaction_reports", "stg_fincloud_balance_sheet_reports", "stg_fincloud_profit_loss_statements",
		"stg_fincloud_coa_movement_reports", "stg_fincloud_fund_distribution_reports", "stg_fincloud_vault_mutation_reports", "stg_fincloud_teller_mutation_reports",
		"fincloud_cif_opening_reports", "fincloud_journal_transaction_reports", "fincloud_balance_sheet_reports", "fincloud_profit_loss_statements",
		"fincloud_coa_movement_reports", "fincloud_fund_distribution_reports", "fincloud_vault_mutation_reports", "fincloud_teller_mutation_reports",
		"fixed_report_publications", "fixed_report_load_members", "fixed_report_loads",
	} {
		if _, err := db.Exec("DELETE FROM `" + table + "`"); err != nil {
			t.Fatal(err)
		}
	}
}
