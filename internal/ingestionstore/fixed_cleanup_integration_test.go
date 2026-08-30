//go:build integration

package ingestionstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	databasepkg "github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

type fixedCleanupFixture struct {
	RunID, LoadID uint64
	Owner         string
	Definition    ingestion.FixedDefinition
	Plan          ingestion.FixedPlan
	Storage       fixedStorage
}

func TestFixedCleanupPreservesSuccessfulPublicationAndHistory(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	runIDs := []uint64{}
	t.Cleanup(func() { cleanupFixedCleanupFixtures(t, db, runIDs) })
	repository := NewFixedRepository(db)
	fixture := newFixedCleanupFixture(t, db, repository, ingestion.FixedDefinitions()[0])
	runIDs = append(runIDs, fixture.RunID)

	if err := repository.Promote(context.Background(), fixture.RunID, fixture.Owner, fixture.Definition, fixture.LoadID); err != nil {
		t.Fatal(err)
	}
	var beforeCount int
	var beforeChecksums string
	if err := db.QueryRowx("SELECT COUNT(*),COALESCE(GROUP_CONCAT(source_row_checksum ORDER BY row_ordinal),'') FROM `"+fixture.Storage.finalTable+"` WHERE load_id=?", fixture.LoadID).Scan(&beforeCount, &beforeChecksums); err != nil {
		t.Fatal(err)
	}
	result, err := repository.CleanupTerminal(context.Background(), 100)
	if err != nil || result.Candidates != 1 || result.Loads != 1 || result.Rows != 1 {
		t.Fatalf("cleanup=%+v error=%v", result, err)
	}

	assertFixedCleanupRows(t, db, fixture.Storage.stagingTable, fixture.LoadID, 0)
	var afterCount int
	var afterChecksums string
	if err := db.QueryRowx("SELECT COUNT(*),COALESCE(GROUP_CONCAT(source_row_checksum ORDER BY row_ordinal),'') FROM `"+fixture.Storage.finalTable+"` WHERE load_id=?", fixture.LoadID).Scan(&afterCount, &afterChecksums); err != nil || afterCount != beforeCount || afterChecksums != beforeChecksums {
		t.Fatalf("final rows before=%d/%q after=%d/%q error=%v", beforeCount, beforeChecksums, afterCount, afterChecksums, err)
	}
	assertFixedCleanupHistory(t, db, fixture, "succeeded")
	var active uint64
	if err := db.Get(&active, `SELECT active_load_id FROM fixed_report_publications WHERE active_load_id=?`, fixture.LoadID); err != nil || active != fixture.LoadID {
		t.Fatalf("active load=%d error=%v", active, err)
	}
}

func TestFixedCleanupTerminalAndLifecycleBoundary(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	runIDs := []uint64{}
	t.Cleanup(func() { cleanupFixedCleanupFixtures(t, db, runIDs) })
	repository := NewFixedRepository(db)
	definition := ingestion.FixedDefinitions()[5]
	fixtures := map[string]fixedCleanupFixture{}
	for _, status := range []string{"failed", "cancelled", "abandoned"} {
		fixture := newFixedCleanupFixture(t, db, repository, definition)
		runIDs = append(runIDs, fixture.RunID)
		setFixedCleanupRunStatus(t, db, fixture.RunID, status)
		fixtures[status] = fixture
	}
	running := newFixedCleanupFixture(t, db, repository, definition)
	runIDs = append(runIDs, running.RunID)

	result, err := repository.CleanupTerminal(context.Background(), 100)
	if err != nil || result.Loads != 3 || result.Rows != 3 {
		t.Fatalf("terminal cleanup=%+v error=%v", result, err)
	}
	for status, fixture := range fixtures {
		assertFixedCleanupRows(t, db, fixture.Storage.stagingTable, fixture.LoadID, 0)
		assertFixedCleanupHistory(t, db, fixture, status)
	}
	assertFixedCleanupRows(t, db, running.Storage.stagingTable, running.LoadID, 1)
	assertFixedCleanupHistory(t, db, running, "running")

	setFixedCleanupRunStatus(t, db, running.RunID, "failed")
	result, err = repository.CleanupTerminal(context.Background(), 100)
	if err != nil || result.Loads != 1 || result.Rows != 1 {
		t.Fatalf("later terminal cleanup=%+v error=%v", result, err)
	}
	assertFixedCleanupRows(t, db, running.Storage.stagingTable, running.LoadID, 0)
	assertFixedCleanupHistory(t, db, running, "failed")
}

func TestFixedCleanupCoversCatalogAndContinuesBacklog(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	runIDs := []uint64{}
	t.Cleanup(func() { cleanupFixedCleanupFixtures(t, db, runIDs) })
	repository := NewFixedRepository(db)
	fixtures := make([]fixedCleanupFixture, 0, len(ingestion.FixedDefinitions()))
	for _, definition := range ingestion.FixedDefinitions() {
		fixture := newFixedCleanupFixture(t, db, repository, definition)
		runIDs = append(runIDs, fixture.RunID)
		setFixedCleanupRunStatus(t, db, fixture.RunID, "failed")
		fixtures = append(fixtures, fixture)
	}

	wantBatches := []int{3, 3, 2}
	for index, want := range wantBatches {
		result, err := repository.CleanupTerminal(context.Background(), 3)
		if err != nil || result.Candidates != want || result.Loads != want || result.Rows != int64(want) {
			t.Fatalf("batch %d cleanup=%+v want=%d error=%v", index, result, want, err)
		}
	}
	for _, fixture := range fixtures {
		assertFixedCleanupRows(t, db, fixture.Storage.stagingTable, fixture.LoadID, 0)
		assertFixedCleanupHistory(t, db, fixture, "failed")
	}
}

func TestFixedCleanupRechecksTerminalStateAtDeleteBoundary(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	runIDs := []uint64{}
	t.Cleanup(func() { cleanupFixedCleanupFixtures(t, db, runIDs) })
	repository := NewFixedRepository(db)
	fixture := newFixedCleanupFixture(t, db, repository, ingestion.FixedDefinitions()[6])
	runIDs = append(runIDs, fixture.RunID)
	setFixedCleanupRunStatus(t, db, fixture.RunID, "failed")
	storages, _ := fixedStorages()
	candidates, err := repository.fixedCleanupCandidates(context.Background(), storages, 100)
	if err != nil || len(candidates) != 1 || candidates[0].LoadID != fixture.LoadID {
		t.Fatalf("candidates=%+v error=%v", candidates, err)
	}
	setFixedCleanupRunStatus(t, db, fixture.RunID, "running")
	deleted, err := repository.cleanupFixedCandidate(context.Background(), candidates[0])
	if err != nil || deleted != 0 {
		t.Fatalf("live delete rows=%d error=%v", deleted, err)
	}
	assertFixedCleanupRows(t, db, fixture.Storage.stagingTable, fixture.LoadID, 1)

	setFixedCleanupRunStatus(t, db, fixture.RunID, "failed")
	result, err := repository.CleanupTerminal(context.Background(), 100)
	if err != nil || result.Loads != 1 {
		t.Fatalf("terminal cleanup=%+v error=%v", result, err)
	}
	assertFixedCleanupRows(t, db, fixture.Storage.stagingTable, fixture.LoadID, 0)
}

func TestFixedCleanupRetriesLockTimeoutAndLaterSweepRecovers(t *testing.T) {
	db := integrationdb.Open(t)
	resetFixed(t, db.DB)
	runIDs := []uint64{}
	t.Cleanup(func() { cleanupFixedCleanupFixtures(t, db, runIDs) })
	cleanupDB, err := databasepkg.Open(context.Background(), integrationdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	cleanupDB.SetMaxOpenConns(1)
	cleanupDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = cleanupDB.Close() })
	if _, err := cleanupDB.Exec(`SET SESSION innodb_lock_wait_timeout=1`); err != nil {
		t.Fatal(err)
	}
	repository := NewFixedRepository(cleanupDB)

	transient := newFixedCleanupFixture(t, db, NewFixedRepository(db), ingestion.FixedDefinitions()[7])
	runIDs = append(runIDs, transient.RunID)
	setFixedCleanupRunStatus(t, db, transient.RunID, "failed")
	blocker := lockFixedCleanupRow(t, db, transient)
	transientBlocker := blocker
	t.Cleanup(func() { _ = transientBlocker.Rollback() })
	before := mysqlSessionRollbacks(t, cleanupDB)
	done := make(chan struct {
		result FixedCleanupResult
		err    error
	}, 1)
	go func() {
		result, cleanupErr := repository.CleanupTerminal(context.Background(), 100)
		done <- struct {
			result FixedCleanupResult
			err    error
		}{result, cleanupErr}
	}()
	time.Sleep(1500 * time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	completed := <-done
	if completed.err != nil || completed.result.Loads != 1 || mysqlSessionRollbacks(t, cleanupDB) <= before {
		t.Fatalf("transient cleanup=%+v rollbacks_before=%d error=%v", completed.result, before, completed.err)
	}

	exhausted := newFixedCleanupFixture(t, db, NewFixedRepository(db), ingestion.FixedDefinitions()[7])
	runIDs = append(runIDs, exhausted.RunID)
	setFixedCleanupRunStatus(t, db, exhausted.RunID, "failed")
	following := newFixedCleanupFixture(t, db, NewFixedRepository(db), ingestion.FixedDefinitions()[7])
	runIDs = append(runIDs, following.RunID)
	setFixedCleanupRunStatus(t, db, following.RunID, "failed")
	blocker = lockFixedCleanupRow(t, db, exhausted)
	exhaustedBlocker := blocker
	t.Cleanup(func() { _ = exhaustedBlocker.Rollback() })
	result, cleanupErr := repository.CleanupTerminal(context.Background(), 100)
	diagnostic := TechnicalDiagnostic(cleanupErr)
	if cleanupErr == nil || result.Candidates != 2 || result.Loads != 1 || diagnostic.TxAttempt != databasepkg.ReplaySafeMaxAttempts {
		t.Fatalf("exhausted cleanup=%+v diagnostic=%+v error=%v", result, diagnostic, cleanupErr)
	}
	assertFixedCleanupRows(t, db, exhausted.Storage.stagingTable, exhausted.LoadID, 1)
	assertFixedCleanupRows(t, db, following.Storage.stagingTable, following.LoadID, 0)
	assertFixedCleanupHistory(t, db, exhausted, "failed")
	assertFixedCleanupHistory(t, db, following, "failed")
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	result, err = repository.CleanupTerminal(context.Background(), 100)
	if err != nil || result.Loads != 1 {
		t.Fatalf("later sweep cleanup=%+v error=%v", result, err)
	}
	assertFixedCleanupRows(t, db, exhausted.Storage.stagingTable, exhausted.LoadID, 0)
}

func newFixedCleanupFixture(t *testing.T, db *sqlx.DB, repository *FixedRepository, definition ingestion.FixedDefinition) fixedCleanupFixture {
	t.Helper()
	date, _ := ingestion.ParseCalendarDate("2026-08-27")
	locations, _ := ingestion.FreezeLocations([]string{"008"})
	accounts, _ := ingestion.FreezeAccountCodes([]string{"10101"})
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: date, To: date}, locations, accounts)
	if err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("c", 64)
	runResult, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,
		 owner_id,claimed_at,heartbeat_at,started_at)
		VALUES ('job',?,'running','fixed_range_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,
		 UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, definition.Key, owner)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runResult.LastInsertId()
	loadID, err := repository.BeginLoad(context.Background(), uint64(runID), definition, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plan.Members {
		segment := FixedSegment{Index: 0, AsOfDate: date, SourceRows: fixedRows(t, definition, member.SourceLocationID)}
		if err := stageMemberFixture(repository, context.Background(), definition, loadID, member, []FixedSegment{segment}); err != nil {
			t.Fatal(err)
		}
	}
	storage, _ := fixedStorageFor(definition)
	return fixedCleanupFixture{RunID: uint64(runID), LoadID: loadID, Owner: owner, Definition: definition, Plan: plan, Storage: storage}
}

func cleanupFixedCleanupFixtures(t *testing.T, db *sqlx.DB, runIDs []uint64) {
	t.Helper()
	resetFixed(t, db.DB)
	for _, runID := range runIDs {
		if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID); err != nil {
			t.Errorf("delete cleanup run %d: %v", runID, err)
		}
	}
}

func setFixedCleanupRunStatus(t *testing.T, db *sqlx.DB, runID uint64, status string) {
	t.Helper()
	finished := "UTC_TIMESTAMP(6)"
	if status == "running" {
		finished = "NULL"
	}
	query := fmt.Sprintf("UPDATE ingestion_runs SET status=?,finished_at=%s WHERE id=?", finished)
	if _, err := db.Exec(query, status, runID); err != nil {
		t.Fatal(err)
	}
}

func assertFixedCleanupRows(t *testing.T, db *sqlx.DB, table string, loadID uint64, want int) {
	t.Helper()
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM `"+table+"` WHERE load_id=?", loadID); err != nil || count != want {
		t.Fatalf("%s load %d rows=%d want=%d error=%v", table, loadID, count, want, err)
	}
}

func assertFixedCleanupHistory(t *testing.T, db *sqlx.DB, fixture fixedCleanupFixture, wantStatus string) {
	t.Helper()
	var status string
	if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, fixture.RunID); err != nil || status != wantStatus {
		t.Fatalf("run %d status=%q want=%q error=%v", fixture.RunID, status, wantStatus, err)
	}
	var loads, members int
	if err := db.Get(&loads, `SELECT COUNT(*) FROM fixed_report_loads WHERE id=? AND ingestion_run_id=?`, fixture.LoadID, fixture.RunID); err != nil || loads != 1 {
		t.Fatalf("load history=%d error=%v", loads, err)
	}
	if err := db.Get(&members, `SELECT COUNT(*) FROM fixed_report_load_members WHERE load_id=?`, fixture.LoadID); err != nil || members != len(fixture.Plan.Members) {
		t.Fatalf("member history=%d want=%d error=%v", members, len(fixture.Plan.Members), err)
	}
}

func lockFixedCleanupRow(t *testing.T, db *sqlx.DB, fixture fixedCleanupFixture) *sqlx.Tx {
	t.Helper()
	tx, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var loadID uint64
	if err := tx.Get(&loadID, "SELECT load_id FROM `"+fixture.Storage.stagingTable+"` WHERE load_id=? LIMIT 1 FOR UPDATE", fixture.LoadID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	return tx
}

func mysqlSessionRollbacks(t *testing.T, db *sqlx.DB) uint64 {
	t.Helper()
	var name string
	var count uint64
	if err := db.QueryRowx(`SHOW SESSION STATUS LIKE 'Com_rollback'`).Scan(&name, &count); err != nil {
		t.Fatal(err)
	}
	return count
}
