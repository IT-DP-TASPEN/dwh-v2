package ingestion

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/fincloud"
)

func TestLegacySanitizedFixedAndMaintenanceFixturesRemainCompatible(t *testing.T) {
	root := legacyFixtureRoot(t)
	fixedFiles := map[string]string{
		"cif_opening_report": "cif_opening_report.csv", "journal_transaction_report": "journal_transaction_report.csv",
		"balance_sheet_report": "balance_sheet_report.csv", "profit_loss_statement": "profit_loss_statement.csv",
		"coa_movement_report": "coa_movement_report.csv", "fund_distribution_report": "fund_distribution_report.csv",
		"vault_mutation_report": "vault_mutation_report.csv", "teller_mutation_report": "teller_mutation_report.csv",
	}
	for _, definition := range FixedDefinitions() {
		content, err := os.ReadFile(filepath.Join(root, "csv_samples", fixedFiles[definition.Key]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseFixedCSV(context.Background(), definition, "000", string(content)); err != nil {
			t.Fatalf("%s sanitized fixture: %v", definition.Key, err)
		}
	}
	maintenanceFiles := map[string]string{
		"eod_cif_opening_report_full":               "eod_files_cif_opening_report_full_csv.csv",
		"eod_detail_outstanding_rekening_pinjaman":  "eod_files_detail_outstanding_rekening_pinjaman_csv.csv",
		"eod_laporan_pelunasan_pinjaman_sebelum_jt": "eod_files_laporan_pelunasan_pinjaman_sebelum_jt_csv.csv",
		"eod_laporan_pembayaran_angsuran":           "eod_files_laporan_pembayaran_angsuran_csv.csv",
		"eod_laporan_pencairan_pinjaman":            "eod_files_laporan_pencairan_pinjaman_csv.csv",
		"eod_laporan_pinjaman_akan_jatuh_tempo":     "eod_files_laporan_pinjaman_akan_jatuh_tempo_csv.csv",
		"eod_loan_write_off_report":                 "eod_files_loan_write_off_report_csv.csv",
		"eod_savings_account_api_transaction":       "eod_files_savings_account_api_transaction_csv.csv",
		"eod_savings_account_closing_report":        "eod_files_savings_account_closing_report_csv.csv",
		"eod_savings_account_opening_report":        "eod_files_savings_account_opening_report_csv.csv",
		"eod_savings_account_balance_report":        "eod_savings_account_balance_report.csv",
		"eod_loan_will_due_report":                  "eod_loan_will_due_report.csv",
		"cbr_balance_sheet":                         "cbr_files_cbr_balance_sheet_csv.csv", "cbr_arrears": "cbr_files_cbrarrears_csv.csv",
		"cbr_collateral": "cbr_files_cbrcollateral_csv.csv", "cbr_customer": "cbr_files_cbrcustomer_csv.csv",
		"cbr_loan": "cbr_files_cbrloan_csv.csv", "cbr_savings": "cbr_files_cbrsavings_csv.csv",
		"cbr_time_deposit": "cbr_files_cbrtimedeposit_csv.csv",
	}
	asOf, _ := ParseCalendarDate("2026-08-12")
	for _, definition := range MaintenanceDefinitions() {
		fixture, exists := maintenanceFiles[definition.Key]
		if !exists {
			if !definition.FixtureGapAccepted {
				t.Fatalf("%s missing accepted fixture-gap classification", definition.Key)
			}
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, "csv_samples", fixture))
		if err != nil {
			t.Fatal(err)
		}
		reader := csv.NewReader(strings.NewReader(string(content)))
		reader.Comma = '|'
		headers, err := reader.Read()
		if err != nil {
			t.Fatalf("%s sanitized fixture header: %v", definition.Key, err)
		}
		columns, err := NormalizeMaintenanceHeaders(headers)
		if err != nil {
			t.Fatalf("%s sanitized fixture headers: %v", definition.Key, err)
		}
		if _, err := reader.Read(); err == io.EOF {
			columnNames := map[string]struct{}{}
			for _, column := range columns {
				columnNames[column.PhysicalName] = struct{}{}
			}
			for _, key := range definition.BusinessKeyColumns {
				if _, found := columnNames[key]; !found {
					t.Fatalf("%s missing key %s", definition.Key, key)
				}
			}
			continue
		}
		if _, err := ParseMaintenanceCSV(context.Background(), definition, asOf, string(content)); err != nil {
			t.Fatalf("%s sanitized fixture: %v", definition.Key, err)
		}
	}
}

func TestLegacySanitizedDetailFixturesDecodeAndMap(t *testing.T) {
	root := legacyFixtureRoot(t)
	tests := []struct {
		file   string
		domain DetailDomain
		typed  any
	}{
		{"cif_detail_sample.json", DetailCIF, &fincloud.DetailEnvelope[fincloud.CIFDetail]{}},
		{"saving_detail_sample.json", DetailSaving, &fincloud.DetailEnvelope[fincloud.SavingDetail]{}},
		{"time_deposit_detail_sample.json", DetailTimeDeposit, &fincloud.DetailEnvelope[fincloud.TimeDepositDetail]{}},
		{"loan_detail_sample.json", DetailLoan, &fincloud.DetailEnvelope[fincloud.LoanDetail]{}},
	}
	for _, test := range tests {
		data, err := os.ReadFile(filepath.Join(root, test.file))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, test.typed); err != nil {
			t.Fatalf("%s typed decode: %v", test.file, err)
		}
		var rawEnvelope struct {
			Data struct {
				Result json.RawMessage `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &rawEnvelope); err != nil {
			t.Fatal(err)
		}
		if len(rawEnvelope.Data.Result) == 0 {
			t.Fatalf("%s has no data.result", test.file)
		}
	}
}

func legacyFixtureRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "dwh-data-fetcher", "internal", "fetcher", "testdata")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("read-only legacy fixtures unavailable: %v", err)
	}
	return root
}
