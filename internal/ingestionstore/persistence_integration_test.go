//go:build integration

package ingestionstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	databasepkg "github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/shopspring/decimal"
)

// stageMemberFixture keeps existing integration fixtures compact while production uses
// only incremental segment staging and explicit finalization.
func stageMemberFixture(repository *FixedRepository, ctx context.Context, definition ingestion.FixedDefinition, loadID uint64, descriptor ingestion.RequestDescriptor, segments []FixedSegment) error {
	segments = append([]FixedSegment(nil), segments...)
	sort.Slice(segments, func(left, right int) bool { return segments[left].Index < segments[right].Index })
	hash := sha256.New()
	var rows uint64
	for _, segment := range segments {
		if err := repository.StageMemberSegment(ctx, definition, loadID, descriptor, segment); err != nil {
			return err
		}
		rows += uint64(len(segment.SourceRows))
		for _, row := range segment.SourceRows {
			ingestion.WriteFixedMemberChecksumPart(hash, row.SourceRowChecksum)
		}
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], hash.Sum(nil))
	return repository.FinalizeMemberCandidate(ctx, definition, loadID, descriptor, len(segments), rows, checksum)
}

func TestFixedSegmentsStageIncrementallyAndFinalizeExplicitly(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[5]
	from, _ := ingestion.ParseCalendarDate("2026-01-01")
	to, _ := ingestion.ParseCalendarDate("2026-03-31")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	loadID, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	member := plan.Members[0]
	first := FixedSegment{Index: 0, AsOfDate: from.AddDays(29), SourceRows: fixedRowsN(t, definition, 2, "first")}
	empty := FixedSegment{Index: 1, AsOfDate: from.AddDays(59)}
	last := FixedSegment{Index: 2, AsOfDate: to, SourceRows: fixedRowsN(t, definition, 1, "last")}
	if err := repository.StageMemberSegment(context.Background(), definition, loadID, member, first); err != nil {
		t.Fatal(err)
	}
	assertMember := func(wantStatus string, wantRows, wantSegments uint64) {
		t.Helper()
		var status string
		var rows, segments uint64
		if err := db.QueryRowx(`SELECT status,row_count,staged_segment_count FROM fixed_report_load_members WHERE load_id=? AND member_key=?`, loadID, member.MemberKey).Scan(&status, &rows, &segments); err != nil || status != wantStatus || rows != wantRows || segments != wantSegments {
			t.Fatalf("member status=%s rows=%d segments=%d error=%v", status, rows, segments, err)
		}
	}
	assertMember(fixedMemberPending, 2, 1)
	if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err == nil {
		t.Fatal("partially staged member promoted")
	}
	if err := repository.StageMemberSegment(context.Background(), definition, loadID, member, empty); err != nil {
		t.Fatal(err)
	}
	assertMember(fixedMemberPending, 2, 2)
	if err := repository.StageMemberSegment(context.Background(), definition, loadID, member, empty); err == nil {
		t.Fatal("duplicate empty segment staged")
	}
	outOfOrder := last
	outOfOrder.Index = 3
	if err := repository.StageMemberSegment(context.Background(), definition, loadID, member, outOfOrder); err == nil {
		t.Fatal("out-of-order segment staged")
	}
	assertMember(fixedMemberPending, 2, 2)
	if err := repository.StageMemberSegment(context.Background(), definition, loadID, member, last); err != nil {
		t.Fatal(err)
	}
	assertMember(fixedMemberPending, 3, 3)

	hash := sha256.New()
	for _, segment := range []FixedSegment{first, empty, last} {
		for _, row := range segment.SourceRows {
			ingestion.WriteFixedMemberChecksumPart(hash, row.SourceRowChecksum)
		}
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], hash.Sum(nil))
	if err := repository.FinalizeMemberCandidate(context.Background(), definition, loadID, member, 2, 3, checksum); err == nil {
		t.Fatal("wrong expected segment count finalized")
	}
	if err := repository.FinalizeMemberCandidate(context.Background(), definition, loadID, member, 3, 2, checksum); err == nil {
		t.Fatal("wrong row count finalized")
	}
	if err := repository.FinalizeMemberCandidate(context.Background(), definition, loadID, member, 3, 3, checksum); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeMemberCandidate(context.Background(), definition, loadID, member, 3, 3, checksum); err != nil {
		t.Fatalf("exact finalization replay failed: %v", err)
	}
	assertMember(fixedMemberSuccess, 3, 3)

	var original string
	if err := db.Get(&original, `SELECT source_row_checksum FROM stg_fincloud_fund_distribution_reports WHERE load_id=? ORDER BY row_ordinal LIMIT 1`, loadID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE stg_fincloud_fund_distribution_reports SET source_row_checksum=REPEAT('0',64) WHERE load_id=? ORDER BY row_ordinal LIMIT 1`, loadID); err != nil {
		t.Fatal(err)
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err == nil {
		t.Fatal("tampered staging passed independent Promote validation")
	}
	if _, err := db.Exec(`UPDATE stg_fincloud_fund_distribution_reports SET source_row_checksum=? WHERE load_id=? ORDER BY row_ordinal LIMIT 1`, original, loadID); err != nil {
		t.Fatal(err)
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err != nil {
		t.Fatal(err)
	}
}

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
	load1, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		rows := fixedRows(t, definition, member.SourceLocationID)
		if err := stageMemberFixture(repository, context.Background(), definition, load1, member, []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: rows}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, load1); err != nil {
		t.Fatal(err)
	}
	if err := stageMemberFixture(repository, context.Background(), definition, load1, plan.Members[0], []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, plan.Members[0].SourceLocationID)}}); err == nil {
		t.Fatal("published load accepted member replacement")
	}
	load2, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := stageMemberFixture(repository, context.Background(), definition, load2, plan.Members[0], []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, plan.Members[0].SourceLocationID)}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, load2); err == nil {
		t.Fatal("partial location set promoted")
	}
	var active uint64
	if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, date.String(), date.String()); err != nil || active != load1 {
		t.Fatalf("active load=%d want=%d error=%v", active, load1, err)
	}
	load3, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		if err := stageMemberFixture(repository, context.Background(), definition, load3, member, []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, member.SourceLocationID)}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE stg_fincloud_balance_sheet_reports SET source_row_checksum=REPEAT('0',64) WHERE load_id=? LIMIT 1`, load3); err != nil {
		t.Fatal(err)
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, load3); err == nil {
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
		loads[index], err = repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := stageMemberFixture(repository, context.Background(), definition, loads[index], plan.Members[0],
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
			errorsByLoad[index] = repository.promoteWithoutRunFence(context.Background(), definition, loads[index])
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

func TestFixedConcurrentStagingJoinsBeforeAtomicPromotion(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[2]
	date, _ := ingestion.ParseCalendarDate("2026-08-15")
	locations, _ := ingestion.FreezeLocations([]string{"000", "001", "002", "003"})
	plan, err := ingestion.BuildFixedSnapshotPlan(definition, ingestion.FixedSnapshotDateParams{Date: date}, locations)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	loadID, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsFound := make(chan error, len(plan.Members))
	for _, member := range plan.Members {
		rows := fixedRows(t, definition, member.SourceLocationID)
		go func(member ingestion.RequestDescriptor) {
			<-start
			errorsFound <- stageMemberFixture(repository, context.Background(), definition, loadID, member,
				[]FixedSegment{{Index: 0, AsOfDate: date, SourceRows: rows}})
		}(member)
	}
	close(start)
	for range plan.Members {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err != nil {
		t.Fatal(err)
	}
	var status string
	var active uint64
	var rows int
	if err := db.Get(&status, `SELECT status FROM fixed_report_loads WHERE id=?`, loadID); err != nil || status != fixedLoadPublished {
		t.Fatalf("status=%q error=%v", status, err)
	}
	if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, date.String(), date.String()); err != nil || active != loadID {
		t.Fatalf("active=%d want=%d error=%v", active, loadID, err)
	}
	if err := db.Get(&rows, `SELECT COUNT(*) FROM fincloud_balance_sheet_reports WHERE load_id=?`, loadID); err != nil || rows != len(plan.Members) {
		t.Fatalf("published rows=%d want=%d error=%v", rows, len(plan.Members), err)
	}

	for _, mode := range []string{"cancelled", "failed"} {
		candidate, beginErr := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if mode == "cancelled" {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = stageMemberFixture(repository, ctx, definition, candidate, plan.Members[0], []FixedSegment{{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, plan.Members[0].SourceLocationID)}})
		} else {
			err = stageMemberFixture(repository, context.Background(), definition, candidate, plan.Members[0], []FixedSegment{{Index: -1, AsOfDate: date, SourceRows: fixedRows(t, definition, plan.Members[0].SourceLocationID)}})
		}
		if err == nil || repository.promoteWithoutRunFence(context.Background(), definition, candidate) == nil {
			t.Fatalf("%s staging was publishable", mode)
		}
		if err := db.Get(&status, `SELECT status FROM fixed_report_loads WHERE id=?`, candidate); err != nil || status != fixedLoadPending {
			t.Fatalf("%s status=%q error=%v", mode, status, err)
		}
		if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, date.String(), date.String()); err != nil || active != loadID {
			t.Fatalf("%s changed publication to %d error=%v", mode, active, err)
		}
	}
}

func TestCoAConcurrentStagingUsesPendingMemberInvariant(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[4]
	from, _ := ingestion.ParseCalendarDate("2026-08-19")
	to, _ := ingestion.ParseCalendarDate("2026-08-20")
	accountCodes := make([]string, 64)
	for index := range accountCodes {
		accountCodes[index] = fmt.Sprintf("synthetic-account-%04d", index)
	}
	accounts, err := ingestion.FreezeAccountCodes(accountCodes)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, accounts)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	for iteration := range 3 {
		loadID, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
		if err != nil {
			t.Fatal(err)
		}
		rowsByMember := make([][]ingestion.FixedCSVRow, len(plan.Members))
		wantRows := 0
		for index := range plan.Members {
			count := 5 + index%7
			if index == 0 {
				count = 256
			} else if index < 8 {
				count = 0
			}
			rowsByMember[index] = fixedRowsN(t, definition, count, fmt.Sprintf("iteration-%d-member-%d", iteration, index))
			wantRows += count
		}
		for index := range 8 {
			if err := stageMemberFixture(repository, context.Background(), definition, loadID, plan.Members[index], []FixedSegment{{Index: 0, AsOfDate: to, SourceRows: rowsByMember[index]}}); err != nil {
				t.Fatal(err)
			}
		}
		stageFixedConcurrently(t, repository, definition, loadID, to, plan.Members[8:], rowsByMember[8:], 4)

		var successfulMembers, stagedRows int
		if err := db.Get(&successfulMembers, `SELECT COUNT(*) FROM fixed_report_load_members WHERE load_id=? AND status=?`, loadID, fixedMemberSuccess); err != nil || successfulMembers != len(plan.Members) {
			t.Fatalf("successful members=%d want=%d error=%v", successfulMembers, len(plan.Members), err)
		}
		if err := db.Get(&stagedRows, `SELECT COUNT(*) FROM stg_fincloud_coa_movement_reports WHERE load_id=?`, loadID); err != nil || stagedRows != wantRows {
			t.Fatalf("staged rows=%d want=%d error=%v", stagedRows, wantRows, err)
		}
		if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err != nil {
			t.Fatal(err)
		}
		var active uint64
		if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, from.String(), to.String()); err != nil || active != loadID {
			t.Fatalf("active load=%d want=%d error=%v", active, loadID, err)
		}
		if err := db.Get(&stagedRows, `SELECT COUNT(*) FROM fincloud_coa_movement_reports WHERE load_id=?`, loadID); err != nil || stagedRows != wantRows {
			t.Fatalf("published rows=%d want=%d error=%v", stagedRows, wantRows, err)
		}
	}
}

func TestCoAMemberDuplicateCompletionIsRejected(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	definition := ingestion.FixedDefinitions()[4]
	from, _ := ingestion.ParseCalendarDate("2026-08-19")
	to, _ := ingestion.ParseCalendarDate("2026-08-20")
	accounts, _ := ingestion.FreezeAccountCodes([]string{"synthetic-account"})
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, accounts)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(db)
	loadID, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	stage := func(rows []ingestion.FixedCSVRow) error {
		return stageMemberFixture(repository, context.Background(), definition, loadID, plan.Members[0], []FixedSegment{{Index: 0, AsOfDate: to, SourceRows: rows}})
	}
	if err := stage(fixedRowsN(t, definition, 7, "first")); err != nil {
		t.Fatal(err)
	}
	if err := stage(fixedRowsN(t, definition, 5, "replacement")); err == nil {
		t.Fatal("completed member accepted replacement")
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, rows := range [][]ingestion.FixedCSVRow{fixedRowsN(t, definition, 5, "concurrent-a"), fixedRowsN(t, definition, 9, "concurrent-b")} {
		go func(rows []ingestion.FixedCSVRow) {
			<-start
			results <- stage(rows)
		}(rows)
	}
	close(start)
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("completed member accepted concurrent replacement")
		}
	}
	var memberRows, stagedRows int
	if err := db.Get(&memberRows, `SELECT row_count FROM fixed_report_load_members WHERE load_id=? AND member_key=?`, loadID, plan.Members[0].MemberKey); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&stagedRows, `SELECT COUNT(*) FROM stg_fincloud_coa_movement_reports WHERE load_id=? AND member_key=?`, loadID, plan.Members[0].MemberKey); err != nil || stagedRows != memberRows || stagedRows != 7 {
		t.Fatalf("staged rows=%d member rows=%d error=%v", stagedRows, memberRows, err)
	}
	if err := repository.promoteWithoutRunFence(context.Background(), definition, loadID); err != nil {
		t.Fatal(err)
	}

	failedLoadID, err := repository.BeginLoad(context.Background(), fixedRunID(t, db.DB, definition.Key), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	duplicateRows := fixedRowsN(t, definition, 2, "duplicate")
	duplicateRows[1].SourceRowNumber = duplicateRows[0].SourceRowNumber
	if err := stageMemberFixture(repository, context.Background(), definition, failedLoadID, plan.Members[0], []FixedSegment{{Index: 0, AsOfDate: to, SourceRows: duplicateRows}}); err == nil {
		t.Fatal("duplicate source rows staged")
	}
	var status string
	if err := db.Get(&status, `SELECT status FROM fixed_report_load_members WHERE load_id=? AND member_key=?`, failedLoadID, plan.Members[0].MemberKey); err != nil || status != fixedMemberPending {
		t.Fatalf("failed member status=%q error=%v", status, err)
	}
	if err := db.Get(&stagedRows, `SELECT COUNT(*) FROM stg_fincloud_coa_movement_reports WHERE load_id=?`, failedLoadID); err != nil || stagedRows != 0 {
		t.Fatalf("failed member committed rows=%d error=%v", stagedRows, err)
	}
	if _, err := db.Exec(`UPDATE fixed_report_load_members SET status='unexpected' WHERE load_id=? AND member_key=?`, failedLoadID, plan.Members[0].MemberKey); err != nil {
		t.Fatal(err)
	}
	if err := stageMemberFixture(repository, context.Background(), definition, failedLoadID, plan.Members[0], []FixedSegment{{Index: 0, AsOfDate: to, SourceRows: fixedRowsN(t, definition, 1, "unexpected")}}); err == nil || !strings.Contains(err.Error(), "cannot stage") {
		t.Fatalf("unexpected member status error=%v", err)
	}
}

func stageFixedConcurrently(t *testing.T, repository *FixedRepository, definition ingestion.FixedDefinition, loadID uint64, asOfDate ingestion.CalendarDate, members []ingestion.RequestDescriptor, rows [][]ingestion.FixedCSVRow, concurrency int) {
	t.Helper()
	type item struct{ index int }
	jobs := make(chan item)
	results := make(chan error, len(members))
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				results <- stageMemberFixture(repository, context.Background(), definition, loadID, members[job.index], []FixedSegment{{Index: 0, AsOfDate: asOfDate, SourceRows: rows[job.index]}})
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range members {
			jobs <- item{index: index}
		}
	}()
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetailCurrentStatePublicationDeletionEmptyAndFailure(t *testing.T) {
	db := integrationdb.Open(t)
	resetDetail(t, db.DB)
	repository := NewDetailRepository(db)
	for _, domain := range []ingestion.DetailDomain{ingestion.DetailCIF, ingestion.DetailSaving, ingestion.DetailTimeDeposit, ingestion.DetailLoan} {
		t.Run(string(domain), func(t *testing.T) {
			specification, _ := detailSpecification(domain)
			runID, owner := detailRun(t, db.DB, specification.jobKey, 3)
			stageDetailSet(t, repository, runID, domain, map[string]int{"A": 1, "B": 1, "C": 1})
			if err := repository.Publish(context.Background(), runID, owner, domain, 3); err != nil {
				t.Fatal(err)
			}
			if err := repository.Publish(context.Background(), runID, owner, domain, 3); err != nil {
				t.Fatalf("committed publication was not recognized after ambiguous acknowledgement: %v", err)
			}
			assertDetailKeys(t, db.DB, specification, "A,B,C")
			var staged int
			if err := db.Get(&staged, "SELECT COUNT(*) FROM `"+specification.stage+"` WHERE ingestion_run_id=?", runID); err != nil || staged != 3 {
				t.Fatalf("post-commit staging=%d error=%v", staged, err)
			}
			if err := repository.CleanupRun(context.Background(), runID); err != nil {
				t.Fatal(err)
			}
			if err := db.Get(&staged, "SELECT COUNT(*) FROM `"+specification.stage+"` WHERE ingestion_run_id=?", runID); err != nil || staged != 0 {
				t.Fatalf("cleaned staging=%d error=%v", staged, err)
			}

			fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			key := detailIdentifier(specification.fields)
			if _, err := db.Exec("UPDATE `"+specification.table+"` SET created_at=?,updated_at=? WHERE `"+key+"`='C'", fixed, fixed); err != nil {
				t.Fatal(err)
			}
			for _, child := range specification.children {
				if _, err := db.Exec("UPDATE `"+child.table+"` SET created_at=?,updated_at=? WHERE account_no='C'", fixed, fixed); err != nil {
					t.Fatal(err)
				}
			}

			secondID, secondOwner := detailRun(t, db.DB, specification.jobKey, 3)
			stageDetailSet(t, repository, secondID, domain, map[string]int{"B": 2, "C": 1, "D": 1})
			if err := repository.Publish(context.Background(), secondID, secondOwner, domain, 3); err != nil {
				t.Fatal(err)
			}
			assertDetailKeys(t, db.DB, specification, "B,C,D")
			var timestamps struct {
				Created time.Time `db:"created_at"`
				Updated time.Time `db:"updated_at"`
				Fetched time.Time `db:"last_fetched_at"`
			}
			if err := db.Get(&timestamps, "SELECT created_at,updated_at,last_fetched_at FROM `"+specification.table+"` WHERE `"+key+"`='C'"); err != nil || !timestamps.Created.Equal(fixed) || !timestamps.Updated.Equal(fixed) || !timestamps.Fetched.After(fixed) {
				t.Fatalf("unchanged parent timestamps=%+v error=%v", timestamps, err)
			}
			for _, child := range specification.children {
				var preserved int
				if err := db.Get(&preserved, "SELECT COUNT(*) FROM `"+child.table+"` WHERE account_no='C' AND created_at=? AND updated_at=?", fixed, fixed); err != nil || preserved != 2 {
					t.Fatalf("unchanged %s timestamps=%d error=%v", child.table, preserved, err)
				}
			}
			if _, err := repository.CleanupTerminal(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			if err := db.Get(&staged, "SELECT COUNT(*) FROM `"+specification.stage+"` WHERE ingestion_run_id=?", secondID); err != nil || staged != 0 {
				t.Fatalf("terminal staging=%d error=%v", staged, err)
			}

			partialID, partialOwner := detailRun(t, db.DB, specification.jobKey, 2)
			stageDetailSet(t, repository, partialID, domain, map[string]int{"B": 3})
			if err := repository.Publish(context.Background(), partialID, partialOwner, domain, 2); err == nil {
				t.Fatal("partial candidate published")
			}
			assertDetailKeys(t, db.DB, specification, "B,C,D")
			if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, partialID); err != nil {
				t.Fatal(err)
			}
			cancelledID, cancelledOwner := detailRun(t, db.DB, specification.jobKey, 1)
			stageDetailSet(t, repository, cancelledID, domain, map[string]int{"B": 3})
			if _, err := db.Exec(`UPDATE ingestion_runs SET cancel_requested_at=CURRENT_TIMESTAMP(6),cancel_reason='operator' WHERE id=?`, cancelledID); err != nil {
				t.Fatal(err)
			}
			if err := repository.Publish(context.Background(), cancelledID, cancelledOwner, domain, 1); err == nil {
				t.Fatal("cancelled candidate published")
			}
			assertDetailKeys(t, db.DB, specification, "B,C,D")
			if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, cancelledID); err != nil {
				t.Fatal(err)
			}

			emptyID, emptyOwner := detailRun(t, db.DB, specification.jobKey, 0)
			if err := repository.Publish(context.Background(), emptyID, emptyOwner, domain, 0); err != nil {
				t.Fatal(err)
			}
			assertDetailKeys(t, db.DB, specification, "")
		})
	}
}

func TestCIFVerticalExtensionsPublishAsOneExactSet(t *testing.T) {
	db := integrationdb.Open(t)
	resetDetail(t, db.DB)
	repository := NewDetailRepository(db)
	fetched := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)

	firstID, firstOwner := detailRun(t, db.DB, "cif_detail", 1)
	first, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailCIF, []byte(`{
		"id":"C-1","namanasabah":"name","perorangan_noktp":"","dataktp_nik":"fallback",
		"perorangan_tempatlahir":"city","dataktp_nama":"ktp name","dataktp_berlakuseumurhidup":false,
		"datapekerjaan_penghasilanbersihperbulan":"1000.25","perusahaan_nonpwp":"NPWP",
		"datakyc_sumberdana":"salary","profilresiko_profil":"low"
	}`), fetched)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Stage(context.Background(), firstID, first); err != nil {
		t.Fatal(err)
	}
	if err := repository.PrepareRun(context.Background(), firstID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"stg_fincloud_cif_details", "stg_fincloud_cif_ktp"} {
		var count int
		if err := db.Get(&count, "SELECT COUNT(*) FROM `"+table+"` WHERE ingestion_run_id=?", firstID); err != nil || count != 0 {
			t.Fatalf("prepared %s count=%d error=%v", table, count, err)
		}
	}
	if err := repository.Stage(context.Background(), firstID, first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Publish(context.Background(), firstID, firstOwner, ingestion.DetailCIF, 1); err != nil {
		t.Fatal(err)
	}
	var ktpNo string
	if err := db.Get(&ktpNo, `SELECT ktp_no FROM fincloud_cifs WHERE cif_no='C-1'`); err != nil || ktpNo != "" {
		t.Fatalf("ktp_no=%q error=%v", ktpNo, err)
	}
	for _, table := range []string{
		"fincloud_cif_personal_profiles", "fincloud_cif_ktp", "fincloud_cif_employment", "fincloud_cif_company",
		"fincloud_cif_kyc", "fincloud_cif_regulatory",
	} {
		var count int
		if err := db.Get(&count, "SELECT COUNT(*) FROM `"+table+"` WHERE cif_no='C-1'"); err != nil || count != 1 {
			t.Fatalf("%s count=%d error=%v", table, count, err)
		}
	}
	var addressCount int
	if err := db.Get(&addressCount, `SELECT COUNT(*) FROM fincloud_cif_addresses WHERE cif_no='C-1'`); err != nil || addressCount != 0 {
		t.Fatalf("meaningless address section count=%d error=%v", addressCount, err)
	}
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := db.Exec(`UPDATE fincloud_cif_regulatory SET created_at=?,updated_at=? WHERE cif_no='C-1'`, fixed, fixed); err != nil {
		t.Fatal(err)
	}

	secondID, secondOwner := detailRun(t, db.DB, "cif_detail", 1)
	second, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailCIF, []byte(`{
		"id":"C-1","namanasabah":"name","perorangan_noktp":"","perorangan_tempatlahir":"new city",
		"profilresiko_profil":"low"
	}`), fetched.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Stage(context.Background(), secondID, second); err != nil {
		t.Fatal(err)
	}
	if err := repository.Publish(context.Background(), secondID, secondOwner, ingestion.DetailCIF, 1); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"fincloud_cif_ktp", "fincloud_cif_employment", "fincloud_cif_company", "fincloud_cif_kyc"} {
		var count int
		if err := db.Get(&count, "SELECT COUNT(*) FROM `"+table+"` WHERE cif_no='C-1'"); err != nil || count != 0 {
			t.Fatalf("stale %s count=%d error=%v", table, count, err)
		}
	}
	var birthPlace string
	if err := db.Get(&birthPlace, `SELECT birth_place FROM fincloud_cif_personal_profiles WHERE cif_no='C-1'`); err != nil || birthPlace != "new city" {
		t.Fatalf("birth_place=%q error=%v", birthPlace, err)
	}
	var timestamps struct {
		Created time.Time `db:"created_at"`
		Updated time.Time `db:"updated_at"`
	}
	if err := db.Get(&timestamps, `SELECT created_at,updated_at FROM fincloud_cif_regulatory WHERE cif_no='C-1'`); err != nil || !timestamps.Created.Equal(fixed) || !timestamps.Updated.Equal(fixed) {
		t.Fatalf("unchanged regulatory timestamps=%+v error=%v", timestamps, err)
	}

	emptyID, emptyOwner := detailRun(t, db.DB, "cif_detail", 0)
	if err := repository.Publish(context.Background(), emptyID, emptyOwner, ingestion.DetailCIF, 0); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"fincloud_cifs", "fincloud_cif_personal_profiles", "fincloud_cif_regulatory"} {
		var count int
		if err := db.Get(&count, "SELECT COUNT(*) FROM `"+table+"`"); err != nil || count != 0 {
			t.Fatalf("empty publication %s count=%d error=%v", table, count, err)
		}
	}
}

func TestExpandedSavingLoanAndChildFieldsPersist(t *testing.T) {
	db := integrationdb.Open(t)
	resetDetail(t, db.DB)
	repository := NewDetailRepository(db)
	fetched := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)

	savingID, savingOwner := detailRun(t, db.DB, "saving_detail", 1)
	saving, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailSaving,
		[]byte(`{"norekening":"S-1","nocif":"C-1","nama":"product","namarek":"display","saldoawal":"1","saldoakhir":"2","produk_bnpl":false}`), fetched)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Stage(context.Background(), savingID, saving); err != nil {
		t.Fatal(err)
	}
	if err := repository.Publish(context.Background(), savingID, savingOwner, ingestion.DetailSaving, 1); err != nil {
		t.Fatal(err)
	}
	var savingRow struct {
		AccountName string `db:"account_name"`
		ProductBNPL string `db:"product_bnpl"`
	}
	if err := db.Get(&savingRow, `SELECT account_name,product_bnpl FROM fincloud_saving_details WHERE account_no='S-1'`); err != nil || savingRow.AccountName != "display" || savingRow.ProductBNPL != "false" {
		t.Fatalf("saving row=%+v error=%v", savingRow, err)
	}

	loanID, loanOwner := detailRun(t, db.DB, "loan_detail", 1)
	loan, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailLoan, []byte(`{
		"id":"L-1","nocif":"C-1","channeling":{"nopengajuan":"APP-1","premiajk":"12.25","lifeinsurancecompany":"insurer","collateralpolicenumber":"POL-1","collateraltype":"vehicle","collateralvalue":"100.50"},
		"jadwalangsuran":[{"bunga_flat":"1.25","pokok_flat":"2.50","pinjaman_flat":"3.75","statusbayar":"paid"}],
		"historybayar":[{"bayar_dendapelunasan":"4.25","nominaldwp":"5.50","otor":"checker"}]
	}`), fetched)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Stage(context.Background(), loanID, loan); err != nil {
		t.Fatal(err)
	}
	if err := repository.Publish(context.Background(), loanID, loanOwner, ingestion.DetailLoan, 1); err != nil {
		t.Fatal(err)
	}
	var application string
	var premium, collateral string
	if err := db.QueryRow(`SELECT application_number,CAST(insurance_premium AS CHAR),CAST(collateral_value AS CHAR) FROM fincloud_loan_details WHERE account_no='L-1'`).Scan(&application, &premium, &collateral); err != nil || application != "APP-1" || !decimal.RequireFromString(premium).Equal(decimal.RequireFromString("12.25")) || !decimal.RequireFromString(collateral).Equal(decimal.RequireFromString("100.5")) {
		t.Fatalf("loan application=%q premium=%q collateral=%q error=%v", application, premium, collateral, err)
	}
	var status, authorizer string
	if err := db.Get(&status, `SELECT payment_status FROM fincloud_loan_repayment_schedule WHERE account_no='L-1' AND item_index=0`); err != nil || status != "paid" {
		t.Fatalf("payment status=%q error=%v", status, err)
	}
	if err := db.Get(&authorizer, `SELECT authorizer FROM fincloud_loan_payment_history WHERE account_no='L-1' AND item_index=0`); err != nil || authorizer != "checker" {
		t.Fatalf("authorizer=%q error=%v", authorizer, err)
	}
}

func TestDetailStageRejectsInvalidDecimalWithoutChangingPublishedState(t *testing.T) {
	db := integrationdb.Open(t)
	resetDetail(t, db.DB)
	repository := NewDetailRepository(db)
	runID, _ := detailRun(t, db.DB, "loan_detail", 1)
	invalid, _ := ingestion.MapDetailPayload(context.Background(), ingestion.DetailLoan,
		[]byte(`{"id":"LN-1","nocif":"CIF-1","outstandingpinjaman":"1.1234567"}`), time.Now().UTC())
	if err := repository.Stage(context.Background(), runID, invalid); err == nil {
		t.Fatal("excess decimal scale accepted")
	}
	valid := mapDetailRecord(t, ingestion.DetailLoan, "LN-1", 1)
	valid.Children["jadwalangsuran"][0].Fields["installment_amount"] = decimal.RequireFromString("1.1234567")
	if err := repository.Stage(context.Background(), runID, valid); err == nil {
		t.Fatal("invalid child decimal accepted")
	}
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM fincloud_loan_details`); err != nil || count != 0 {
		t.Fatalf("failed stage changed final state: count=%d error=%v", count, err)
	}
}

func TestDetailPublicationFailureRollsBackFinalStateAndRunSuccess(t *testing.T) {
	db := integrationdb.Open(t)
	resetDetail(t, db.DB)
	repository := NewDetailRepository(db)
	firstID, firstOwner := detailRun(t, db.DB, "loan_detail", 1)
	stageDetailSet(t, repository, firstID, ingestion.DetailLoan, map[string]int{"A": 1})
	if err := repository.Publish(context.Background(), firstID, firstOwner, ingestion.DetailLoan, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS fail_detail_publication`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_detail_publication BEFORE INSERT ON fincloud_loan_details
		FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced publication failure'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TRIGGER IF EXISTS fail_detail_publication`) })
	secondID, secondOwner := detailRun(t, db.DB, "loan_detail", 1)
	stageDetailSet(t, repository, secondID, ingestion.DetailLoan, map[string]int{"B": 1})
	if err := repository.Publish(context.Background(), secondID, secondOwner, ingestion.DetailLoan, 1); err == nil {
		t.Fatal("forced publication failure succeeded")
	}
	specification, _ := detailSpecification(ingestion.DetailLoan)
	assertDetailKeys(t, db.DB, specification, "A")
	var status string
	if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, secondID); err != nil || status != "running" {
		t.Fatalf("failed publication run status=%q error=%v", status, err)
	}
}

func TestGroupedDetailDecimalsPersistExactly(t *testing.T) {
	db := integrationdb.Open(t)
	resetDetail(t, db.DB)
	repository := NewDetailRepository(db)
	fetched := time.Date(2026, 8, 14, 1, 2, 3, 456000000, time.UTC)
	samples := []struct {
		domain  ingestion.DetailDomain
		jobKey  string
		payload string
	}{
		{domain: ingestion.DetailSaving, jobKey: "saving_detail", payload: `{"norekening":"GROUPED-S","nocif":"SAFE","saldoawal":"1.25","saldoakhir":"2.50","mutasidebit":"1,234.56","mutasikredit":"2,345.67"}`},
		{domain: ingestion.DetailTimeDeposit, jobKey: "time_deposit_detail", payload: `{"id":"GROUPED-T","nocif":"SAFE","nominal":"1,234,567.89","accrueinterest":"12,345.67","produk_sukubunga":"5.25","mutasideposito":[{"nominal":"98,765.43","sukubunga":"5.25"}]}`},
		{domain: ingestion.DetailLoan, jobKey: "loan_detail", payload: `{"id":"GROUPED-L","nocif":"SAFE","outstandingpinjaman":"1,234,567.89","tunggakanpokok":"12,345.67","tunggakanbunga":"2,345.67","dendatunggakan":"1.25","produk_sukubunga":"5.50"}`},
	}
	for _, sample := range samples {
		runID, owner := detailRun(t, db.DB, sample.jobKey, 1)
		record, err := ingestion.MapDetailPayload(context.Background(), sample.domain, []byte(sample.payload), fetched)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.Stage(context.Background(), runID, record); err != nil {
			t.Fatal(err)
		}
		if err := repository.Publish(context.Background(), runID, owner, sample.domain, 1); err != nil {
			t.Fatal(err)
		}
	}
	assertDecimal := func(query, want string) {
		t.Helper()
		var got string
		if err := db.Get(&got, query); err != nil || !decimal.RequireFromString(got).Equal(decimal.RequireFromString(want)) {
			t.Fatalf("decimal=%q want=%s error=%v", got, want, err)
		}
	}
	assertDecimal(`SELECT CAST(debit_mutation AS CHAR) FROM fincloud_saving_details WHERE account_no='GROUPED-S'`, "1234.56")
	assertDecimal(`SELECT CAST(credit_mutation AS CHAR) FROM fincloud_saving_details WHERE account_no='GROUPED-S'`, "2345.67")
	assertDecimal(`SELECT CAST(nominal AS CHAR) FROM fincloud_time_deposit_details WHERE account_no='GROUPED-T'`, "1234567.89")
	assertDecimal(`SELECT CAST(nominal AS CHAR) FROM fincloud_time_deposit_mutations WHERE account_no='GROUPED-T' AND item_index=0`, "98765.43")
	assertDecimal(`SELECT CAST(outstanding_principal AS CHAR) FROM fincloud_loan_details WHERE account_no='GROUPED-L'`, "1234567.89")
}

func TestReplaySafeTransactionRecoversFromRealMySQL1213(t *testing.T) {
	db := integrationdb.Open(t)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ingestion_deadlock_probe (id INT PRIMARY KEY, value INT NOT NULL) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE ingestion_deadlock_probe`) })
	if _, err := db.Exec(`INSERT INTO ingestion_deadlock_probe VALUES (1,0),(2,0) ON DUPLICATE KEY UPDATE value=0`); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errorsFound := make(chan error, 2)
	var attempts [2]atomic.Int32
	for index := range 2 {
		go func(index int) {
			first, second := index+1, 2-index
			errorsFound <- retryReplaySafeTx(context.Background(), db, "deadlock_probe", func(tx *sqlx.Tx) error {
				attempt := attempts[index].Add(1)
				if _, err := tx.Exec(`UPDATE ingestion_deadlock_probe SET value=value+1 WHERE id=?`, first); err != nil {
					return err
				}
				if attempt == 1 {
					ready <- struct{}{}
					<-release
				}
				if _, err := tx.Exec(`UPDATE ingestion_deadlock_probe SET value=value+1 WHERE id=?`, second); err != nil {
					return err
				}
				return nil
			})
		}(index)
	}
	<-ready
	<-ready
	close(release)
	for range 2 {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
	}
	if total := attempts[0].Load() + attempts[1].Load(); total != 3 {
		t.Fatalf("transaction attempts=%d want=3 (%d,%d)", total, attempts[0].Load(), attempts[1].Load())
	}
	var values []int
	if err := db.Select(&values, `SELECT value FROM ingestion_deadlock_probe ORDER BY id`); err != nil || !reflect.DeepEqual(values, []int{2, 2}) {
		t.Fatalf("deadlock retry leaked partial state: values=%v error=%v", values, err)
	}
}

func TestReplaySafeTransactionRecoversFromRealMySQL1205WithoutPartialLeak(t *testing.T) {
	db := integrationdb.Open(t)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ingestion_lock_timeout_probe (id INT PRIMARY KEY, value INT NOT NULL) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE ingestion_lock_timeout_probe`) })
	if _, err := db.Exec(`INSERT INTO ingestion_lock_timeout_probe VALUES (1,0),(2,0) ON DUPLICATE KEY UPDATE value=0`); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(`UPDATE ingestion_lock_timeout_probe SET value=value WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `SET SESSION innodb_lock_wait_timeout=1`); err != nil {
		t.Fatal(err)
	}
	timedOut := make(chan struct{})
	done := make(chan error, 1)
	var attempts, sourceFetches atomic.Int32
	sourceFetches.Add(1) // fetched/prepared data stays outside the retry closure
	go func() {
		_, retryErr := databasepkg.RetryReplaySafeConnTx(context.Background(), connection, func(tx *sql.Tx) error {
			attempts.Add(1)
			if _, err := tx.Exec(`UPDATE ingestion_lock_timeout_probe SET value=value+1 WHERE id=2`); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE ingestion_lock_timeout_probe SET value=value+1 WHERE id=1`)
			var mysqlError *mysql.MySQLError
			if errors.As(err, &mysqlError) && mysqlError.Number == 1205 {
				select {
				case <-timedOut:
				default:
					close(timedOut)
				}
			}
			return err
		})
		done <- retryErr
	}()
	<-timedOut
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var values []int
	if err := db.Select(&values, `SELECT value FROM ingestion_lock_timeout_probe ORDER BY id`); err != nil || !reflect.DeepEqual(values, []int{1, 1}) {
		t.Fatalf("1205 retry leaked partial state: values=%v error=%v", values, err)
	}
	if attempts.Load() != 2 || sourceFetches.Load() != 1 {
		t.Fatalf("attempts=%d source_fetches=%d", attempts.Load(), sourceFetches.Load())
	}
}

func runConcurrentSaves(t *testing.T, saves []func() error) {
	t.Helper()
	start := make(chan struct{})
	errorsFound := make(chan error, len(saves))
	for _, save := range saves {
		go func() {
			<-start
			errorsFound <- save()
		}()
	}
	close(start)
	for range saves {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
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
	if err := repository.saveSnapshotWithoutRunFence(context.Background(), MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: parsed}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER phase3_fail_cbr_customer BEFORE INSERT ON `" + definition.TableName + "` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced load failure'"); err != nil {
		t.Fatal(err)
	}
	broken, _ := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "One|Two|Three\nc|d|e\n")
	if err := repository.saveSnapshotWithoutRunFence(context.Background(), MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: broken}); err == nil {
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
	if err := repository.saveSnapshotWithoutRunFence(context.Background(), MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: valid}); err != nil {
		t.Fatal(err)
	}
	otherDate, _ := ingestion.ParseCalendarDate("2026-08-11")
	mismatched, _ := ingestion.ParseMaintenanceCSV(context.Background(), definition, otherDate, "One|Two|Three\nc|d|e\n")
	if err := repository.saveSnapshotWithoutRunFence(context.Background(), MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: mismatched}); err == nil {
		t.Fatal("mismatched maintenance source date succeeded")
	}
	empty, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, date, "One|Two|Three\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.saveSnapshotWithoutRunFence(context.Background(), MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: empty}); err != nil {
		t.Fatalf("valid empty maintenance snapshot: %v", err)
	}
	var rows int
	if err := db.Get(&rows, "SELECT COUNT(*) FROM `"+definition.TableName+"` WHERE as_of_date=?", date.String()); err != nil || rows != 0 {
		t.Fatalf("empty maintenance publication rows=%d error=%v", rows, err)
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
				err = repository.saveSnapshotWithoutRunFence(context.Background(), MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: parsed})
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

func fixedRowsN(t *testing.T, definition ingestion.FixedDefinition, count int, marker string) []ingestion.FixedCSVRow {
	t.Helper()
	var content strings.Builder
	content.WriteString(strings.Join(definition.RequiredHeaders, "|"))
	content.WriteByte('\n')
	values := make([]string, len(definition.RequiredHeaders))
	for index := range count {
		values[0] = fmt.Sprintf("%s-%d", marker, index)
		content.WriteString(strings.Join(values, "|"))
		content.WriteByte('\n')
	}
	rows, err := ingestion.ParseFixedCSV(context.Background(), definition, "", content.String())
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

func resetDetail(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, specification := range detailSpecifications {
		if _, err := db.Exec("DELETE FROM `" + specification.stage + "`"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DELETE FROM `" + specification.table + "`"); err != nil {
			t.Fatal(err)
		}
	}
}

func detailRun(t *testing.T, db *sql.DB, jobKey string, total uint64) (uint64, string) {
	t.Helper()
	owner := strings.Repeat("d", 64)
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,owner_id,
		 claimed_at,heartbeat_at,started_at,progress_total,progress_started,progress_succeeded,progress_failed,current_step)
		VALUES ('job',?,'running','detail_live_snapshot_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,
		 CURRENT_TIMESTAMP(6),CURRENT_TIMESTAMP(6),CURRENT_TIMESTAMP(6),?,?,?,0,'publish_detail')`, jobKey, owner, total, total, total)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id) })
	return uint64(id), owner
}

func stageDetailSet(t *testing.T, repository *DetailRepository, runID uint64, domain ingestion.DetailDomain, versions map[string]int) {
	t.Helper()
	errorsFound := make(chan error, len(versions))
	for identifier, version := range versions {
		record := mapDetailRecord(t, domain, identifier, version)
		go func() { errorsFound <- repository.Stage(context.Background(), runID, record) }()
	}
	for range versions {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
	}
}

func mapDetailRecord(t *testing.T, domain ingestion.DetailDomain, identifier string, version int) ingestion.DetailRecord {
	t.Helper()
	var payload string
	switch domain {
	case ingestion.DetailCIF:
		payload = fmt.Sprintf(`{"id":"%s","namanasabah":"name-%d"}`, identifier, version)
	case ingestion.DetailSaving:
		payload = fmt.Sprintf(`{"norekening":"%s","nocif":"CIF-%s","saldoawal":"%d","saldoakhir":"%d"}`, identifier, identifier, version, version)
	case ingestion.DetailTimeDeposit:
		payload = fmt.Sprintf(`{"id":"%s","nocif":"CIF-%s","nominal":"%d","mutasideposito":[{"nominal":"%d","referensi":"%s-%d-a"},{"nominal":"%d","referensi":"%s-%d-b"}]}`, identifier, identifier, version, version, identifier, version, version+1, identifier, version)
	case ingestion.DetailLoan:
		payload = fmt.Sprintf(`{"id":"%s","nocif":"CIF-%s","outstandingpinjaman":"%d","biayapencairan":[{"namabiaya":"%s-%d-a"},{"namabiaya":"%s-%d-b"}],"jadwalangsuran":[{"angsuranke":%d},{"angsuranke":%d}],"historybayar":[{"angsuranke":1,"nojurnal":"%s-%d-a"},{"angsuranke":2,"nojurnal":"%s-%d-b"}]}`, identifier, identifier, version, identifier, version, identifier, version, version*10+1, version*10+2, identifier, version, identifier, version)
	default:
		t.Fatalf("unsupported Detail domain %s", domain)
	}
	record, err := ingestion.MapDetailPayload(context.Background(), domain, []byte(payload), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertDetailKeys(t *testing.T, db *sql.DB, specification detailSpec, want string) {
	t.Helper()
	key := detailIdentifier(specification.fields)
	var got string
	if err := db.QueryRow("SELECT COALESCE(GROUP_CONCAT(`" + key + "` ORDER BY `" + key + "` SEPARATOR ','),'') FROM `" + specification.table + "`").Scan(&got); err != nil || got != want {
		t.Fatalf("%s keys=%q want=%q error=%v", specification.table, got, want, err)
	}
}

func fixedRunID(t *testing.T, db *sql.DB, jobKey string) uint64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,finished_at)
		VALUES ('job',?,'succeeded','fixed_range_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',CURRENT_TIMESTAMP(6))`, jobKey)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return uint64(id)
}
