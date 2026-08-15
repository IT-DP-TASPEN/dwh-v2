package ingestion

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/shopspring/decimal"
)

func TestCalendarDatePreservesJakartaCivilDate(t *testing.T) {
	jakarta, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	value := time.Date(2026, 8, 12, 0, 0, 0, 0, jakarta)
	if value.UTC().Format(dateLayout) != "2026-08-11" {
		t.Fatal("test setup does not cross UTC date")
	}
	if got := CalendarDateFromTime(value).String(); got != "2026-08-12" {
		t.Fatalf("calendar date = %s", got)
	}
}

func TestPreviousCalendarDayJakartaIsPolicyNotCatalogAssignment(t *testing.T) {
	now := time.Date(2026, 8, 12, 17, 30, 0, 0, time.UTC) // 2026-08-13 00:30 Jakarta
	if got := ResolvePreviousCalendarDayJakarta(now).String(); got != "2026-08-12" {
		t.Fatalf("previous Jakarta date = %s", got)
	}
	if _, found := reflect.TypeOf(JobDefinition{}).FieldByName("SchedulePolicy"); found {
		t.Fatal("catalog guessed job-specific schedule assignment")
	}
}

func TestCanonicalCatalog(t *testing.T) {
	catalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[JobCategory]int{}
	fixtureGaps := 0
	for _, job := range catalog.Jobs() {
		counts[job.Category]++
		if job.Maintenance != nil {
			if job.Maintenance.SchemaMode != DynamicAdditive {
				t.Fatalf("%s is not dynamic-additive", job.Key)
			}
			if job.Maintenance.FixtureGapAccepted {
				fixtureGaps++
				if !job.Active {
					t.Fatalf("fixture-gap job %s is inactive", job.Key)
				}
			}
		}
	}
	if len(catalog.Jobs()) != 36 || counts[CategoryFixed] != 8 || counts[CategoryEOD] != 17 || counts[CategoryCBR] != 7 || counts[CategoryDetail] != 4 || fixtureGaps != 5 {
		t.Fatalf("catalog counts jobs=%d categories=%v fixture gaps=%d", len(catalog.Jobs()), counts, fixtureGaps)
	}
	wantStrategies := map[string][2]string{
		"cif_opening_report":         {string(SingleRequestAllLocationsEmpty), string(NoAccountCodeStrategy)},
		"journal_transaction_report": {string(SingleRequestAllLocationsEmpty), string(NoAccountCodeStrategy)},
		"balance_sheet_report":       {string(PerLocation), string(NoAccountCodeStrategy)},
		"profit_loss_statement":      {string(PerLocation), string(NoAccountCodeStrategy)},
		"coa_movement_report":        {string(SingleRequestAllLocationsEmpty), string(AllAccountCodes)},
		"fund_distribution_report":   {string(SingleRequestAllLocationsEmpty), string(NoAccountCodeStrategy)},
		"vault_mutation_report":      {string(SingleRequestAllLocationsEmpty), string(NoAccountCodeStrategy)},
		"teller_mutation_report":     {string(SingleRequestAllLocationsEmpty), string(NoAccountCodeStrategy)},
	}
	for key, want := range wantStrategies {
		job, _ := catalog.Find(key)
		got := [2]string{string(job.Fixed.LocationStrategy), string(job.Fixed.AccountCodeStrategy)}
		if got != want {
			t.Fatalf("%s strategies = %v, want %v", key, got, want)
		}
	}
}

func TestPurePlanUsesFrozenDeterministicInternalDimensions(t *testing.T) {
	locations, err := FreezeLocations([]string{" 008 ", "000", "008", " "})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(locations.Values(), []string{"000", "008"}) {
		t.Fatalf("locations = %v", locations)
	}
	accounts, err := FreezeAccountCodes([]string{"2.1", " 1.2 ", "2.1", ""})
	if err != nil {
		t.Fatal(err)
	}
	from, _ := ParseCalendarDate("2026-08-01")
	to, _ := ParseCalendarDate("2026-08-12")
	definitions := FixedDefinitions()
	balance, err := BuildFixedSnapshotPlan(definitions[2], FixedSnapshotDateParams{Date: to}, locations)
	if err != nil {
		t.Fatal(err)
	}
	locationValues := locations.Values()
	locationValues[0] = "MUTATED"
	if len(balance.Members) != 2 || balance.Members[0].Parameters[0] != "000" || balance.Members[0].SourceLocationID != "000" || !balance.RequireAllMembers {
		t.Fatalf("balance plan = %+v", balance)
	}
	if balance.ReplacementScope.JobKey != "balance_sheet_report" || balance.ReplacementScope.From != to || balance.ReplacementScope.To != to {
		t.Fatalf("replacement scope = %+v", balance.ReplacementScope)
	}
	coa, err := BuildFixedPlan(definitions[4], FixedDateRangeParams{From: from, To: to}, FrozenLocations{}, accounts)
	if err != nil {
		t.Fatal(err)
	}
	accountValues := accounts.Values()
	accountValues[0] = "MUTATED"
	if len(coa.Members) != 2 || coa.Members[0].Parameters[0] != "1.2" || coa.Members[0].Parameters[3] != "" || coa.Members[0].AccountCode != "1.2" {
		t.Fatalf("CoA plan = %+v", coa)
	}
	for _, definition := range definitions {
		for _, field := range []string{"Branch", "Location", "AccountCode"} {
			if _, found := reflect.TypeOf(FixedDateRangeParams{}).FieldByName(field); found {
				t.Fatalf("public params expose %s", field)
			}
		}
		_ = definition
	}
}

func TestBalanceDateSeriesFreezesDateLocationMembers(t *testing.T) {
	from, _ := ParseCalendarDate("2026-06-01")
	to, _ := ParseCalendarDate("2026-06-02")
	locations, _ := FreezeLocations([]string{"008", "000"})
	plan, err := BuildFixedDateSeriesPlan(FixedDefinitions()[2], []CalendarDate{from, to}, locations)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Members) != 4 || plan.Range.From != from || plan.Range.To != to {
		t.Fatalf("balance series plan=%+v", plan)
	}
	seen := map[string]bool{}
	for _, member := range plan.Members {
		if len(member.MemberKey) != 64 || seen[member.MemberKey] || member.Parameters[0] != member.SourceLocationID || len(member.Parameters) != 2 {
			t.Fatalf("invalid balance descriptor=%+v", member)
		}
		seen[member.MemberKey] = true
	}
}

func TestFixedManifestCanonicalGolden(t *testing.T) {
	definition := FixedDefinitions()[2]
	date, _ := ParseCalendarDate("2026-08-12")
	locations, _ := FreezeLocations([]string{"008", "000"})
	plan, err := BuildFixedSnapshotPlan(definition, FixedSnapshotDateParams{Date: date}, locations)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := FixedManifestChecksum(definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	const want = "00c5ca8db9364afbcec149515f30f98353d879c33d8e254dad2f0a30cc109e3b"
	if got := hex.EncodeToString(checksum[:]); got != want {
		t.Fatalf("manifest checksum = %s", got)
	}
}

func TestFixedCSVStrictHeaderBOMZeroRowsAndProvenance(t *testing.T) {
	for _, fixedDefinition := range FixedDefinitions() {
		headerOnly := "\uFEFF" + strings.Join(fixedDefinition.RequiredHeaders, "|") + "\n"
		rows, err := ParseFixedCSV(context.Background(), fixedDefinition, "", headerOnly)
		if err != nil || len(rows) != 0 {
			t.Fatalf("%s header-only rows=%d error=%v", fixedDefinition.Key, len(rows), err)
		}
	}
	definition := FixedDefinitions()[2]
	content := "\uFEFF" + strings.Join(definition.RequiredHeaders, "|") + "\n"
	rows, err := ParseFixedCSV(context.Background(), definition, "008", content)
	if err != nil || len(rows) != 0 {
		t.Fatalf("header-only result rows=%d error=%v", len(rows), err)
	}
	_, err = ParseFixedCSV(context.Background(), definition, "008", "\uFEFF")
	if err == nil {
		t.Fatal("BOM-only CSV accepted")
	}
	content += "|100|Assets|1|2|3|4\n"
	rows, err = ParseFixedCSV(context.Background(), definition, "008", content)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SourceLocationID != "008" || rows[0].Values["Branch"] != "" || rows[0].SourceRowChecksum == "" {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestInclusiveThirtyDayChunking(t *testing.T) {
	from, _ := ParseCalendarDate("2026-01-01")
	to, _ := ParseCalendarDate("2026-02-01")
	chunks, err := ChunkDateRange(from, to, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].To.String() != "2026-01-30" || chunks[1].From.String() != "2026-01-31" || chunks[1].To.String() != "2026-02-01" {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestFixedRequestPositionalLayouts(t *testing.T) {
	from, _ := ParseCalendarDate("2026-08-11")
	to, _ := ParseCalendarDate("2026-08-12")
	locations, _ := FreezeLocations([]string{"008"})
	accounts, _ := FreezeAccountCodes([]string{"1.2"})
	want := map[string][]string{
		"cif_opening_report":         {"", "2026-08-11", "2026-08-12"},
		"journal_transaction_report": {"", "%", "2026-08-11", "2026-08-12", "", ""},
		"balance_sheet_report":       {"008", "2026-08-12"},
		"profit_loss_statement":      {"008", "2026-08-11", "2026-08-12"},
		"coa_movement_report":        {"1.2", "2026-08-11", "2026-08-12", ""},
		"fund_distribution_report":   {"", "", "", "", "2026-08-11", "2026-08-12"},
		"vault_mutation_report":      {"", "2026-08-11", "2026-08-12"},
		"teller_mutation_report":     {"", "ALL", "2026-08-11", "2026-08-12"},
	}
	for _, definition := range FixedDefinitions() {
		var plan FixedPlan
		var err error
		if definition.SnapshotDate {
			plan, err = BuildFixedSnapshotPlan(definition, FixedSnapshotDateParams{Date: to}, locations)
		} else {
			plan, err = BuildFixedPlan(definition, FixedDateRangeParams{From: from, To: to}, locations, accounts)
		}
		if err != nil {
			t.Fatalf("%s: %v", definition.Key, err)
		}
		if len(plan.Members) != 1 || !reflect.DeepEqual(plan.Members[0].Parameters, want[definition.Key]) {
			t.Fatalf("%s parameters=%v want=%v", definition.Key, plan.Members[0].Parameters, want[definition.Key])
		}
	}
}

func TestDetailMapperPreservesRawPayloadSnapshotAndChildren(t *testing.T) {
	asOf, _ := ParseCalendarDate("2026-08-12")
	fetched := time.Date(2026, 8, 12, 8, 9, 10, 123456000, time.UTC)
	raw := json.RawMessage(` { "id":"LN-1", "nocif":"CIF-1", "outstandingpinjaman":"1234567890.123456", "biayapencairan":[{"namabiaya":"x","jumlah_biaya":"1.25"}], "jadwalangsuran":[{"angsuranke":1}], "historybayar":[{"angsuranke":"1"}] } `)
	record, err := MapDetailPayload(context.Background(), DetailLoan, raw, asOf, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if record.Identifier != "LN-1" || record.AsOfDate.String() != "2026-08-12" || !record.LastFetchedAt.Equal(fetched) || record.RawChecksum == "" || len(record.Children) != 3 {
		t.Fatalf("record = %+v", record)
	}
	for name, children := range record.Children {
		if len(children) != 1 || children[0].Identifier != "LN-1" || children[0].AsOfDate != asOf || children[0].ItemIndex != 0 || children[0].RawItemChecksum == "" {
			t.Fatalf("%s children = %+v", name, children)
		}
	}
	if got := record.Fields["outstanding_principal"].(decimal.Decimal).String(); got != "1234567890.123456" {
		t.Fatalf("mapped decimal = %s", got)
	}
	var scalar fincloud.Scalar
	if err := json.Unmarshal([]byte(`1234567890.123456`), &scalar); err != nil {
		t.Fatal(err)
	}
	decimalValue, err := scalar.Decimal()
	if err != nil || decimalValue.String() != "1234567890.123456" {
		t.Fatalf("decimal=%s error=%v", decimalValue, err)
	}
}

func TestDetailMapperUsesStrictGroupedDecimalsForAllDomains(t *testing.T) {
	date, _ := ParseCalendarDate("2026-08-12")
	fetched := time.Date(2026, 8, 12, 8, 9, 10, 123456000, time.UTC)
	tests := []struct {
		name     string
		domain   DetailDomain
		payload  string
		fields   map[string]string
		children map[string]map[string]string
	}{
		{name: "saving", domain: DetailSaving,
			payload: `{"norekening":"S-1","nocif":"C-1","saldoawal":"1.25","saldoakhir":"2.50","mutasidebit":"1,234.56","mutasikredit":"2,345.67"}`,
			fields:  map[string]string{"beginning_balance": "1.25", "balance": "2.5", "debit_mutation": "1234.56", "credit_mutation": "2345.67"}},
		{name: "time deposit", domain: DetailTimeDeposit,
			payload:  `{"id":"T-1","nocif":"C-1","nominal":"1,234,567.89","accrueinterest":"12,345.67","produk_sukubunga":"5.25","mutasideposito":[{"nominal":"98,765.43","sukubunga":"5.25"}]}`,
			fields:   map[string]string{"nominal": "1234567.89", "accrued_interest": "12345.67", "product_interest_rate": "5.25"},
			children: map[string]map[string]string{"mutasideposito": {"nominal": "98765.43", "interest_rate": "5.25"}}},
		{name: "loan", domain: DetailLoan,
			payload: `{"id":"L-1","nocif":"C-1","outstandingpinjaman":"1,234,567.89","tunggakanpokok":"12,345.67","tunggakanbunga":"2,345.67","dendatunggakan":"1.25","produk_sukubunga":"5.50"}`,
			fields:  map[string]string{"outstanding_principal": "1234567.89", "principal_arrears": "12345.67", "interest_arrears": "2345.67", "penalty_arrears": "1.25", "product_interest_rate": "5.5"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, err := MapDetailPayload(context.Background(), test.domain, json.RawMessage(test.payload), date, fetched)
			if err != nil {
				t.Fatal(err)
			}
			for field, want := range test.fields {
				got, ok := record.Fields[field].(decimal.Decimal)
				if !ok || !got.Equal(decimal.RequireFromString(want)) {
					t.Fatalf("%s=%v want=%s", field, record.Fields[field], want)
				}
			}
			for child, fields := range test.children {
				for field, want := range fields {
					got, ok := record.Children[child][0].Fields[field].(decimal.Decimal)
					if !ok || !got.Equal(decimal.RequireFromString(want)) {
						t.Fatalf("%s.%s=%v want=%s", child, field, record.Children[child][0].Fields[field], want)
					}
				}
			}
		})
	}

	_, err := MapDetailPayload(context.Background(), DetailSaving,
		json.RawMessage(`{"norekening":"S-2","nocif":"C-2","saldoawal":"1","saldoakhir":"2","mutasidebit":"1,234"}`), date, fetched)
	var mapper *MapperError
	if !errors.As(err, &mapper) || mapper.Metadata().Field() != "debit_mutation" || mapper.Metadata().Category() != "decimal" || mapper.Metadata().Reason() != "invalid_value" {
		t.Fatalf("unsafe or missing mapper metadata: %#v error=%v", mapper, err)
	}
}

func TestWrappedDateAndDatetimeParsingRejectsInvalidValues(t *testing.T) {
	date, err := ParseWrappedFincloudDate(&fincloud.WrappedDateTime{Date: "2026-08-12 00:00:00.000000", Timezone: "Asia/Jakarta"})
	if err != nil || date.String() != "2026-08-12" {
		t.Fatalf("date=%s error=%v", date, err)
	}
	timestamp, err := ParseFincloudDateTime("2026-08-12 01:02:03.123456")
	if err != nil || timestamp.Nanosecond() != 123456000 {
		t.Fatalf("timestamp=%s error=%v", timestamp, err)
	}
	if _, err := ParseFincloudDate("2026-08-12garbage"); err == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestMaintenanceDefinitionsMatchersSchemaAndNormalization(t *testing.T) {
	if !DynamicAdditivePolicy.CreateTableOnFirstLoad || !DynamicAdditivePolicy.ReuseKnownColumns || !DynamicAdditivePolicy.AddMissingColumns || DynamicAdditivePolicy.AddColumnSQLType != "TEXT NULL" || DynamicAdditivePolicy.AllowDrop || DynamicAdditivePolicy.AllowRename || DynamicAdditivePolicy.AllowTypeMutation {
		t.Fatalf("dynamic additive policy = %+v", DynamicAdditivePolicy)
	}
	definitions := MaintenanceDefinitions()
	if len(definitions) != 24 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	for _, definition := range definitions {
		if definition.SchemaMode != DynamicAdditive {
			t.Fatalf("%s schema mode = %s", definition.Key, definition.SchemaMode)
		}
	}
	disposition, definition := ClassifyMaintenanceFile(MaintenanceCBR, "/tmp/CBRLOAN.CSV")
	if disposition != MaintenanceRegistered || definition.Key != "cbr_loan" {
		t.Fatalf("classification=%s definition=%+v", disposition, definition)
	}
	if disposition, _ := ClassifyMaintenanceFile(MaintenanceCBR, "Done.csv"); disposition != MaintenanceDone {
		t.Fatalf("Done=%s", disposition)
	}
	if disposition, _ := ClassifyMaintenanceFile(MaintenanceCBR, "SIPENDAR20260812.csv"); disposition != MaintenanceSIPENDAR {
		t.Fatalf("SIPENDAR=%s", disposition)
	}
	columns, err := NormalizeMaintenanceHeaders([]string{"AccountNo", strings.Repeat("VeryLongHeader", 8)})
	if err != nil {
		t.Fatal(err)
	}
	if columns[0].PhysicalName != "account_no" || len(columns[1].PhysicalName) != 64 {
		t.Fatalf("columns=%+v", columns)
	}
	if _, err := NormalizeMaintenanceHeaders([]string{"as of date"}); err == nil {
		t.Fatal("reserved column accepted")
	}
	if _, err := NormalizeMaintenanceHeaders([]string{"Account No", "account_no"}); err == nil {
		t.Fatal("normalized collision accepted")
	}
}

func TestMaintenanceParseIdentityAndCancellation(t *testing.T) {
	definition := MaintenanceDefinitions()[0]
	asOf, _ := ParseCalendarDate("2026-08-12")
	parsed, err := ParseMaintenanceCSV(context.Background(), definition, asOf, "CIF No|Name\n001|Masked\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Rows) != 1 || parsed.Rows[0].BusinessKeyHash == "" || parsed.Rows[0].RowChecksum == "" {
		t.Fatalf("parsed=%+v", parsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ParseMaintenanceCSV(ctx, definition, asOf, "CIF No|Name\n001|Masked\n"); err == nil {
		t.Fatal("cancelled parse succeeded")
	}
}

func TestMaintenanceLookbackAndBundleResolution(t *testing.T) {
	requested, _ := ParseCalendarDate("2026-08-12")
	dates, err := MaintenanceCandidateDates(MaintenanceParams{RequestedDate: requested, LookbackDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{dates[0].String(), dates[1].String(), dates[2].String(), dates[3].String()}; !reflect.DeepEqual(got, []string{"2026-08-12", "2026-08-11", "2026-08-10", "2026-08-09"}) {
		t.Fatalf("dates=%v", got)
	}
	if _, err := MaintenanceCandidateDates(MaintenanceParams{RequestedDate: requested, LookbackDays: 4}); err == nil {
		t.Fatal("lookback beyond three previous days accepted")
	}
	bundle, err := ResolveMaintenanceBundle(MaintenanceCBR, requested, map[string]string{
		"/reports/cbrloan.csv": "Loan No\n1\n", "/reports/Done.csv": "done", "/reports/SIPENDAR20260812.csv": "x", "/reports/extra.csv": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 1 || bundle.Files["cbr_loan"].FileName != "cbrloan.csv" || len(bundle.Resolutions) != 4 {
		t.Fatalf("bundle=%+v", bundle)
	}
}
