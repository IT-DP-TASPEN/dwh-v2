//go:build integration

package ingestionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestFixedDatePublicationReplacesOverlapAndPreservesMultiplicity(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	repository := NewFixedRepository(db)
	_, a := publicationCandidate(t, db, definition, "2026-01-01", "2026-06-30", nil)
	publishCandidate(t, repository, definition, a)
	assertDatePublication(t, db, definition, "2026-01-01", "2026-06-30", func(string) uint64 { return a }, 2)
	_, b := publicationCandidate(t, db, definition, "2026-02-01", "2026-07-31", nil)
	publishCandidate(t, repository, definition, b)
	assertDatePublication(t, db, definition, "2026-01-01", "2026-07-31", func(date string) uint64 {
		if date < "2026-02-01" {
			return a
		}
		return b
	}, 2)
	var obsolete, intervalAuthority int
	if err := db.Get(&obsolete, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE load_id=? AND coverage_date BETWEEN '2026-02-01' AND '2026-06-30'`, a); err != nil || obsolete != 0 {
		t.Fatalf("obsolete overlap rows=%d error=%v", obsolete, err)
	}
	if err := db.Get(&intervalAuthority, `SELECT COUNT(*) FROM fixed_report_publications WHERE job_key=?`, definition.Key); err != nil || intervalAuthority != 0 {
		t.Fatalf("date report used interval authority: count=%d error=%v", intervalAuthority, err)
	}
}

func TestFixedOlderOverlapRejectsWholeCandidate(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	repository := NewFixedRepository(db)
	_, older := publicationCandidate(t, db, definition, "2026-01-01", "2026-06-30", nil)
	_, newer := publicationCandidate(t, db, definition, "2026-02-01", "2026-07-31", nil)
	publishCandidate(t, repository, definition, newer)
	if err := repository.promoteWithoutRunFence(context.Background(), definition, older); !errors.Is(err, ErrFixedStale) {
		t.Fatalf("older overlap error=%v", err)
	}
	assertDatePublication(t, db, definition, "2026-02-01", "2026-07-31", func(string) uint64 { return newer }, 2)
	var januaryRows, januaryAuthority int
	var status string
	if err := db.Get(&januaryRows, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE coverage_date<'2026-02-01'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&januaryAuthority, `SELECT COUNT(*) FROM fixed_report_date_publications WHERE job_key=? AND coverage_date<'2026-02-01'`, definition.Key); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&status, `SELECT status FROM fixed_report_loads WHERE id=?`, older); err != nil || status != fixedLoadPending || januaryRows != 0 || januaryAuthority != 0 {
		t.Fatalf("partial stale publication: rows=%d authority=%d status=%s error=%v", januaryRows, januaryAuthority, status, err)
	}
}

func TestFixedDateExactRerunAndAuthoritativeEmptySucceedAtomically(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	// Other integration cases intentionally retain run history; clear their admission
	// leases only in this disposable database before exercising the production fence.
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='abandoned' WHERE job_key=? AND status IN ('queued','running')`, definition.Key); err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	_, a := publicationCandidate(t, db, definition, "2026-03-01", "2026-03-31", nil)
	publishCandidate(t, repository, definition, a)
	_, b := publicationCandidate(t, db, definition, "2026-03-01", "2026-03-31", nil)
	publishCandidate(t, repository, definition, b)
	assertDatePublication(t, db, definition, "2026-03-01", "2026-03-31", func(string) uint64 { return b }, 2)
	var oldRows int
	if err := db.Get(&oldRows, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE load_id=?`, a); err != nil || oldRows != 0 {
		t.Fatalf("exact rerun old rows=%d error=%v", oldRows, err)
	}
	runID, empty := publicationCandidate(t, db, definition, "2026-03-10", "2026-03-10", func(_ ingestion.RequestDescriptor, _ int) bool { return true })
	owner := strings.Repeat("e", 64)
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='running',owner_id=? WHERE id=?`, owner, runID); err != nil {
		t.Fatal(err)
	}
	if err := repository.Promote(context.Background(), runID, owner, definition, empty); err != nil {
		t.Fatal(err)
	}
	assertDatePublication(t, db, definition, "2026-03-10", "2026-03-10", func(string) uint64 { return empty }, 0)
	var status string
	if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, runID); err != nil || status != "succeeded" {
		t.Fatalf("empty publication run=%s error=%v", status, err)
	}
	var emptySegmentRows uint64
	if err := db.Get(&emptySegmentRows, `SELECT row_count FROM fixed_report_load_segments WHERE load_id=?`, empty); err != nil || emptySegmentRows != 0 {
		t.Fatalf("empty segment rows=%d error=%v", emptySegmentRows, err)
	}
}

func TestFixedProfitLossExactReplacementAllowsDifferentIntervals(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[3]
	repository := NewFixedRepository(db)
	_, a := publicationCandidate(t, db, definition, "2026-01-01", "2026-06-30", nil)
	publishCandidate(t, repository, definition, a)
	_, b := publicationCandidate(t, db, definition, "2026-01-01", "2026-06-30", nil)
	publishCandidate(t, repository, definition, b)
	_, c := publicationCandidate(t, db, definition, "2026-02-01", "2026-07-31", nil)
	publishCandidate(t, repository, definition, c)
	var oldRows, bRows, cRows, intervalAuthorities, dateAuthorities int
	for _, check := range []struct {
		load  uint64
		count *int
	}{{a, &oldRows}, {b, &bRows}, {c, &cRows}} {
		if err := db.Get(check.count, `SELECT COUNT(*) FROM fincloud_profit_loss_statements WHERE load_id=?`, check.load); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Get(&intervalAuthorities, `SELECT COUNT(*) FROM fixed_report_publications WHERE job_key=? AND active_load_id IN (?,?)`, definition.Key, b, c); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&dateAuthorities, `SELECT COUNT(*) FROM fixed_report_date_publications WHERE job_key=?`, definition.Key); err != nil {
		t.Fatal(err)
	}
	if oldRows != 0 || bRows != 4 || cRows != 4 || intervalAuthorities != 2 || dateAuthorities != 0 {
		t.Fatalf("P&L final rows=%d/%d/%d interval authorities=%d date authorities=%d", oldRows, bRows, cRows, intervalAuthorities, dateAuthorities)
	}
	var segments, invalidRequests int
	if err := db.QueryRowx(`SELECT COUNT(*),COALESCE(SUM(source_period_from<>'2026-01-01' OR source_period_to<>'2026-06-30' OR as_of_date<>'2026-06-30'),0) FROM fixed_report_load_segments WHERE load_id=?`, b).Scan(&segments, &invalidRequests); err != nil || segments != 2 || invalidRequests != 0 {
		t.Fatalf("P&L exact provenance segments=%d invalid=%d error=%v", segments, invalidRequests, err)
	}
}

func TestFixedSegmentsPersistActualIntervalsEmptyRowsAndJournalVariants(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	for _, definition := range []ingestion.FixedDefinition{ingestion.FixedDefinitions()[0], ingestion.FixedDefinitions()[1]} {
		_, loadID := publicationCandidate(t, db, definition, "2026-08-01", "2026-09-30", func(_ ingestion.RequestDescriptor, index int) bool { return index == 1 })
		var segments []struct {
			From    string `db:"source_period_from"`
			To      string `db:"source_period_to"`
			AsOf    string `db:"as_of_date"`
			Rows    uint64 `db:"row_count"`
			Variant string `db:"request_variant"`
		}
		if err := db.Select(&segments, `SELECT DATE_FORMAT(source_period_from,'%Y-%m-%d') source_period_from,
			DATE_FORMAT(source_period_to,'%Y-%m-%d') source_period_to,DATE_FORMAT(as_of_date,'%Y-%m-%d') as_of_date,row_count,COALESCE(request_variant,'') request_variant
			FROM fixed_report_load_segments WHERE load_id=? ORDER BY segment_index`, loadID); err != nil {
			t.Fatal(err)
		}
		want := [][2]string{{"2026-08-01", "2026-08-30"}, {"2026-08-31", "2026-09-29"}, {"2026-09-30", "2026-09-30"}}
		if len(segments) != 3 {
			t.Fatalf("%s segments=%+v", definition.Key, segments)
		}
		for index, segment := range segments {
			variant := ""
			if definition.Key == "journal_transaction_report" {
				variant = "exact-type-id"
			}
			if segment.From != want[index][0] || segment.To != want[index][1] || segment.AsOf != segment.To || segment.Variant != variant || (index == 1 && segment.Rows != 0) {
				t.Fatalf("%s segment[%d]=%+v", definition.Key, index, segment)
			}
		}
	}
}

func TestFixedCoverageReplacementUsesMigrationIndex(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	_, loadID := publicationCandidate(t, db, definition, "2026-01-01", "2026-06-30", nil)
	publishCandidate(t, NewFixedRepository(db), definition, loadID)
	for _, other := range []ingestion.FixedDefinition{ingestion.FixedDefinitions()[1], ingestion.FixedDefinitions()[4], ingestion.FixedDefinitions()[2]} {
		from, to := "2026-03-01", "2026-04-30"
		if other.SnapshotDate {
			from, to = "2026-03-15", "2026-03-15"
		}
		_, candidate := publicationCandidate(t, db, other, from, to, nil)
		publishCandidate(t, NewFixedRepository(db), other, candidate)
	}
	for _, table := range []string{"fincloud_cif_opening_reports", "fincloud_journal_transaction_reports", "fincloud_coa_movement_reports", "fincloud_balance_sheet_reports"} {
		for _, query := range []string{
			"EXPLAIN SELECT id FROM `" + table + "` FORCE INDEX (idx_fixed_coverage_date) WHERE coverage_date BETWEEN '2026-03-01' AND '2026-03-31'",
			"EXPLAIN DELETE target FROM `" + table + "` target FORCE INDEX (idx_fixed_coverage_date) WHERE coverage_date BETWEEN '2026-03-01' AND '2026-03-31'",
		} {
			rows, err := db.Queryx(query)
			if err != nil {
				t.Fatal(err)
			}
			if !rows.Next() {
				rows.Close()
				t.Fatal("EXPLAIN returned no plan")
			}
			plan := map[string]any{}
			if err := rows.MapScan(plan); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
			key, _ := plan["key"].([]byte)
			access, _ := plan["type"].([]byte)
			if string(key) != "idx_fixed_coverage_date" || string(access) == "ALL" {
				t.Fatalf("coverage query plan=%v", plan)
			}
		}
	}
}

// publicationCandidate preserves repeated payloads: replacement operates on dates,
// never on a business key or checksum. Empty segments have independent provenance.
func publicationCandidate(t *testing.T, db *sqlx.DB, definition ingestion.FixedDefinition, fromValue, toValue string, empty func(ingestion.RequestDescriptor, int) bool) (uint64, uint64) {
	t.Helper()
	from, _ := ingestion.ParseCalendarDate(fromValue)
	to, _ := ingestion.ParseCalendarDate(toValue)
	locations, _ := ingestion.FreezeLocations([]string{"000", "008"})
	accounts, _ := ingestion.FreezeAccountCodes([]string{"10101"})
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, locations, accounts)
	if definition.SnapshotDate {
		var dates []ingestion.CalendarDate
		for date := from; ; date = date.AddDays(1) {
			dates = append(dates, date)
			if date == to {
				break
			}
		}
		plan, err = ingestion.BuildFixedDateSeriesPlan(definition, dates, locations)
	}
	if err != nil {
		t.Fatal(err)
	}
	if definition.Key == "journal_transaction_report" {
		plan.SourceVariants = []string{"exact-type-id"}
	}
	runID := fixedRunID(t, db.DB, definition.Key)
	repository := NewFixedRepository(db)
	loadID, err := repository.BeginLoad(context.Background(), runID, definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		chunks, err := ingestion.FixedSourceChunks(definition, member.RequestedFrom, member.RequestedTo)
		if err != nil {
			t.Fatal(err)
		}
		var segments []FixedSegment
		for index, chunk := range chunks {
			segment := FixedSegment{Index: index, SourcePeriodFrom: chunk.From, SourcePeriodTo: chunk.To, AsOfDate: chunk.To}
			if definition.Key == "journal_transaction_report" {
				segment.RequestVariant = "exact-type-id"
			}
			if empty == nil || !empty(member, index) {
				var content strings.Builder
				content.WriteString(strings.Join(definition.RequiredHeaders, "|") + "\n")
				for date := chunk.From; date.String() <= chunk.To.String(); date = date.AddDays(1) {
					values := make([]string, len(definition.RequiredHeaders))
					values[0] = "same-source-payload"
					for i, header := range definition.RequiredHeaders {
						if header == definition.CoverageDateHeader {
							values[i] = date.String()
						}
					}
					row := strings.Join(values, "|") + "\n"
					content.WriteString(row)
					content.WriteString(row)
					if definition.PublicationMode == ingestion.IntervalResult || date == chunk.To {
						break
					}
				}
				segment.SourceRows, err = ingestion.ParseFixedCSV(context.Background(), definition, member.SourceLocationID, content.String())
				if err != nil {
					t.Fatal(err)
				}
			}
			segments = append(segments, segment)
		}
		if err := stageMemberFixture(repository, context.Background(), definition, loadID, member, segments); err != nil {
			t.Fatal(err)
		}
	}
	return runID, loadID
}

func publishCandidate(t *testing.T, repository *FixedRepository, definition ingestion.FixedDefinition, loadID uint64) {
	t.Helper()
	if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err != nil {
		t.Fatal(err)
	}
}

func assertDatePublication(t *testing.T, db *sqlx.DB, definition ingestion.FixedDefinition, fromValue, toValue string, expected func(string) uint64, expectedRows int) {
	t.Helper()
	storage, _ := fixedStorageFor(definition)
	from, _ := ingestion.ParseCalendarDate(fromValue)
	to, _ := ingestion.ParseCalendarDate(toValue)
	for date := from; date.String() <= to.String(); date = date.AddDays(1) {
		var authority uint64
		if err := db.Get(&authority, `SELECT active_load_id FROM fixed_report_date_publications WHERE job_key=? AND coverage_date=?`, definition.Key, date.String()); err != nil || authority != expected(date.String()) {
			t.Fatalf("authority %s=%d want=%d error=%v", date, authority, expected(date.String()), err)
		}
		var count int
		var min, max uint64
		query := fmt.Sprintf("SELECT COUNT(*),COALESCE(MIN(load_id),0),COALESCE(MAX(load_id),0) FROM `%s` WHERE coverage_date=?", storage.finalTable)
		if err := db.QueryRowx(query, date.String()).Scan(&count, &min, &max); err != nil || count != expectedRows || (count > 0 && (min != authority || max != authority)) {
			t.Fatalf("final %s count=%d loads=%d/%d authority=%d error=%v", date, count, min, max, authority, err)
		}
		if date == to {
			break
		}
	}
}

func TestFixedBalanceSheetDateSeriesReplacesSnapshotsAndPublishesEmptyDates(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[2]
	repository := NewFixedRepository(db)
	_, a := publicationCandidate(t, db, definition, "2026-03-01", "2026-03-03", func(member ingestion.RequestDescriptor, _ int) bool {
		return member.RequestedFrom.String() == "2026-03-03" || (member.RequestedFrom.String() == "2026-03-02" && member.SourceLocationID == "008")
	})
	publishCandidate(t, repository, definition, a)
	for index, count := range []int{4, 2, 0} {
		date := fmt.Sprintf("2026-03-%02d", index+1)
		assertDatePublication(t, db, definition, date, date, func(string) uint64 { return a }, count)
	}
	_, b := publicationCandidate(t, db, definition, "2026-03-02", "2026-03-04", nil)
	publishCandidate(t, repository, definition, b)
	assertDatePublication(t, db, definition, "2026-03-01", "2026-03-04", func(date string) uint64 {
		if date == "2026-03-01" {
			return a
		}
		return b
	}, 4)
	var locations int
	if err := db.Get(&locations, `SELECT COUNT(DISTINCT source_location_id) FROM fincloud_balance_sheet_reports WHERE coverage_date='2026-03-03'`); err != nil || locations != 2 {
		t.Fatalf("snapshot locations=%d error=%v", locations, err)
	}
}

func TestFixedSourceDateContractRejectsEntireSegmentInMySQL(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	from, _ := ingestion.ParseCalendarDate("2026-08-01")
	to, _ := ingestion.ParseCalendarDate("2026-09-30")
	accounts, _ := ingestion.FreezeAccountCodes([]string{"10101"})
	repository := NewFixedRepository(db)
	for _, definition := range ingestion.FixedDefinitions() {
		if definition.CoverageDateHeader == "" {
			continue
		}
		t.Run(definition.Key, func(t *testing.T) {
			plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, accounts)
			if err != nil {
				t.Fatal(err)
			}
			if definition.Key == "journal_transaction_report" {
				plan.SourceVariants = []string{"exact-type-id"}
			}
			format := func(value string) string {
				date, err := time.Parse("2006-01-02", value)
				if err != nil {
					t.Fatal(err)
				}
				return date.Format(definition.CoverageDateLayout)
			}
			valid := format("2026-08-01")
			for _, bad := range []string{"", strings.Replace(valid, "08-01", "02-30", 1), "08/01/2026", format("2026-07-31"), format("2026-08-31")} {
				loadID, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
				if err != nil {
					t.Fatal(err)
				}
				var csv strings.Builder
				csv.WriteString(strings.Join(definition.RequiredHeaders, "|") + "\n")
				for _, date := range []string{valid, bad} {
					values := make([]string, len(definition.RequiredHeaders))
					for index, header := range definition.RequiredHeaders {
						if header == definition.CoverageDateHeader {
							values[index] = date
						}
					}
					csv.WriteString(strings.Join(values, "|") + "\n")
				}
				rows, err := ingestion.ParseFixedCSV(context.Background(), definition, "", csv.String())
				if err != nil {
					t.Fatal(err)
				}
				segment := FixedSegment{Index: 0, SourcePeriodFrom: from, SourcePeriodTo: from.AddDays(29), AsOfDate: from.AddDays(29), SourceRows: rows}
				if definition.Key == "journal_transaction_report" {
					segment.RequestVariant = "exact-type-id"
				}
				if err := repository.StageMemberSegment(context.Background(), definition, loadID, plan.Members[0], segment); err == nil {
					t.Fatalf("invalid date %q staged", bad)
				}
				if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err == nil {
					t.Fatal("invalid segment published")
				}
				storage, _ := fixedStorageFor(definition)
				for _, table := range []string{storage.stagingTable, storage.finalTable, "fixed_report_load_segments"} {
					var count int
					if err := db.Get(&count, "SELECT COUNT(*) FROM `"+table+"` WHERE load_id=?", loadID); err != nil || count != 0 {
						t.Fatalf("invalid date wrote %s: rows=%d error=%v", table, count, err)
					}
				}
				var publications int
				var status string
				if err := db.Get(&publications, `SELECT COUNT(*) FROM fixed_report_date_publications WHERE active_load_id=?`, loadID); err != nil || publications != 0 {
					t.Fatalf("invalid date authority=%d error=%v", publications, err)
				}
				if err := db.Get(&status, `SELECT status FROM fixed_report_load_members WHERE load_id=?`, loadID); err != nil || status != fixedMemberPending {
					t.Fatalf("invalid member status=%s error=%v", status, err)
				}
			}
		})
	}
}

func TestFixedPromotionRejectsTamperedCoverageAndSourceSegment(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	repository := NewFixedRepository(db)
	for _, mutation := range []string{
		`UPDATE stg_fincloud_cif_opening_reports SET coverage_date='2026-08-02' WHERE load_id=?`,
		`UPDATE fixed_report_load_segments SET source_period_from='2026-08-02' WHERE load_id=?`,
		`UPDATE fixed_report_load_segments SET row_count=row_count+1 WHERE load_id=?`,
	} {
		_, loadID := publicationCandidate(t, db, definition, "2026-08-01", "2026-08-01", nil)
		if _, err := db.Exec(mutation, loadID); err != nil {
			t.Fatal(err)
		}
		if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err == nil {
			t.Fatalf("tampered contract published: %s", mutation)
		}
		var rows, publications int
		if err := db.Get(&rows, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE load_id=?`, loadID); err != nil {
			t.Fatal(err)
		}
		if err := db.Get(&publications, `SELECT COUNT(*) FROM fixed_report_date_publications WHERE active_load_id=?`, loadID); err != nil || rows != 0 || publications != 0 {
			t.Fatalf("tampered candidate escaped: rows=%d authorities=%d error=%v", rows, publications, err)
		}
	}
}

func TestFixedCoAMovementConsumerSeesOnlyAuthoritativeSourceDates(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[4]
	repository := NewFixedRepository(db)
	_, a := publicationCandidate(t, db, definition, "2026-03-01", "2026-03-31", nil)
	publishCandidate(t, repository, definition, a)
	_, b := publicationCandidate(t, db, definition, "2026-03-15", "2026-04-15", nil)
	publishCandidate(t, repository, definition, b)
	var count int
	var min, max uint64
	// The installed OJK consumer uses this source-date shape without a metadata join.
	if err := db.QueryRowx("SELECT COUNT(*),MIN(load_id),MAX(load_id) FROM fincloud_coa_movement_reports WHERE `date` BETWEEN '2026-03-15' AND '2026-03-31'").Scan(&count, &min, &max); err != nil || count != 34 || min != b || max != b {
		t.Fatalf("normal CoA query: rows=%d loads=%d/%d error=%v", count, min, max, err)
	}
	if err := db.Get(&count, `SELECT COUNT(*) FROM fincloud_coa_movement_reports WHERE load_id=?`, a); err != nil || count != 28 {
		t.Fatalf("retained prior dates=%d error=%v", count, err)
	}
}

func TestFixedPublicationAcceptsMySQLMaximumCalendarDate(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	_, loadID := publicationCandidate(t, db, definition, "9999-12-31", "9999-12-31", nil)
	publishCandidate(t, NewFixedRepository(db), definition, loadID)
	assertDatePublication(t, db, definition, "9999-12-31", "9999-12-31", func(string) uint64 { return loadID }, 2)
}

func TestFixedSucceededRunCannotAcknowledgeUnpublishedCandidate(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	t.Cleanup(func() { resetFixed(t, db.DB) })
	definition := ingestion.FixedDefinitions()[0]
	// The fixture run is already terminal. It cannot fence this pending load.
	runID, loadID := publicationCandidate(t, db, definition, "2026-03-10", "2026-03-10", nil)
	if err := NewFixedRepository(db).Promote(context.Background(), runID, strings.Repeat("e", 64), definition, loadID); err == nil {
		t.Fatal("succeeded run concealed an unpublished candidate's failed ownership fence")
	}
	var rows, publications int
	var status string
	if err := db.Get(&rows, `SELECT COUNT(*) FROM fincloud_cif_opening_reports WHERE load_id=?`, loadID); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&publications, `SELECT COUNT(*) FROM fixed_report_date_publications WHERE active_load_id=?`, loadID); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&status, `SELECT status FROM fixed_report_loads WHERE id=?`, loadID); err != nil || rows != 0 || publications != 0 || status != fixedLoadPending {
		t.Fatalf("failed fence leaked rows=%d publications=%d status=%s error=%v", rows, publications, status, err)
	}
}
