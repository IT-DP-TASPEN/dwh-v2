//go:build integration

package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestRetryUntilSuccessAndChronologicalCursor(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	schedule := createDueSchedule(t, db, service, "retry", "eod_detail_outstanding_rekening_pinjaman", due)

	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("submit first changed=%v error=%v", changed, err)
	}
	first := latestAttempt(t, db, schedule.ID)
	assertMaintenanceAttemptDate(t, db, first, "2026-08-09")
	finishRun(t, db, first.RunID, ingestionrun.StatusFailed, due.Add(time.Hour))
	clearThrottle(t, db, schedule.ID)
	restarted := newIntegrationService(t, db)
	if changed, err := restarted.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("record first backoff changed=%v error=%v", changed, err)
	}
	assertCursor(t, db, schedule.ID, due)
	assertRetry(t, db, schedule.ID, due.Add(time.Hour+time.Minute))
	expireThrottle(t, db, schedule.ID)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("submit second changed=%v error=%v", changed, err)
	}
	second := latestAttempt(t, db, schedule.ID)
	if second.AttemptNo != 2 {
		t.Fatalf("second attempt=%d", second.AttemptNo)
	}
	assertMaintenanceAttemptDate(t, db, second, "2026-08-09")
	finishRun(t, db, second.RunID, ingestionrun.StatusFailed, due.Add(2*time.Hour))
	clearThrottle(t, db, schedule.ID)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("record second backoff changed=%v error=%v", changed, err)
	}
	assertCursor(t, db, schedule.ID, due)
	assertRetry(t, db, schedule.ID, due.Add(2*time.Hour+2*time.Minute))
	expireThrottle(t, db, schedule.ID)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("submit third changed=%v error=%v", changed, err)
	}
	third := latestAttempt(t, db, schedule.ID)
	assertMaintenanceAttemptDate(t, db, third, "2026-08-09")
	finishRun(t, db, third.RunID, ingestionrun.StatusFailed, due.Add(3*time.Hour))
	clearThrottle(t, db, schedule.ID)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("record third backoff changed=%v error=%v", changed, err)
	}
	assertCursor(t, db, schedule.ID, due)
	var occurrences int
	if err := db.Get(&occurrences, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id=?`, schedule.ID); err != nil || occurrences != 1 {
		t.Fatalf("head-of-line occurrences=%d error=%v", occurrences, err)
	}
	expireThrottle(t, db, schedule.ID)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("submit fourth changed=%v error=%v", changed, err)
	}
	fourth := latestAttempt(t, db, schedule.ID)
	if fourth.AttemptNo != 4 {
		t.Fatalf("fourth attempt=%d", fourth.AttemptNo)
	}
	assertMaintenanceAttemptDate(t, db, fourth, "2026-08-09")
	finishRun(t, db, fourth.RunID, ingestionrun.StatusSucceeded, due.Add(4*time.Hour))
	clearThrottle(t, db, schedule.ID)
	restarted = newIntegrationService(t, db) // Simulate crash after run success, before cursor reconciliation.
	if changed, err := restarted.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("resolve success changed=%v error=%v", changed, err)
	}
	assertCursor(t, db, schedule.ID, due.AddDate(0, 0, 1))
	var attempts int
	if err := db.Get(&attempts, `SELECT COUNT(*) FROM schedule_attempts WHERE occurrence_id=?`, fourth.OccurrenceID); err != nil || attempts != 4 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
	assertCanonicalLink(t, db, schedule.ID, fourth)
}

func TestNoDateCoalescesAndAdvancesFromSuccessfulFinish(t *testing.T) {
	db, service := integrationService(t)
	for index, test := range []struct{ name, job string }{{"detail", "cif_detail"}, {"master", "cif_reference_master"}} {
		due := time.Date(2026, 8, 1+index, 1, 0, 0, 0, time.UTC)
		schedule := createDueSchedule(t, db, service, test.name, test.job, due)
		_, _ = service.process(context.Background(), schedule.ID)
		first := latestAttempt(t, db, schedule.ID)
		finishRun(t, db, first.RunID, ingestionrun.StatusCancelled, due.Add(time.Hour))
		clearThrottle(t, db, schedule.ID)
		_, _ = service.process(context.Background(), schedule.ID)
		expireThrottle(t, db, schedule.ID)
		_, _ = service.process(context.Background(), schedule.ID)
		second := latestAttempt(t, db, schedule.ID)
		finished := time.Date(2026, 8, 14, 3, 4, 5, 123456000, time.UTC)
		finishRun(t, db, second.RunID, ingestionrun.StatusSucceeded, finished)
		clearThrottle(t, db, schedule.ID)
		if _, err := service.process(context.Background(), schedule.ID); err != nil {
			t.Fatal(err)
		}
		assertCursor(t, db, schedule.ID, time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC))
		var occurrences int
		if err := db.Get(&occurrences, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id=?`, schedule.ID); err != nil || occurrences != 1 {
			t.Fatalf("%s coalesced occurrences=%d error=%v", test.name, occurrences, err)
		}
		var mode string
		if err := db.Get(&mode, `SELECT resolution_mode FROM schedule_occurrences WHERE schedule_id=?`, schedule.ID); err != nil || mode != "live_coalesced" {
			t.Fatalf("%s resolution mode=%q error=%v", test.name, mode, err)
		}
	}
}

func TestDetailPublicationCrashBeforeSchedulerResolutionAdvancesOnce(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	schedule := createDueSchedule(t, db, service, "detail-publication-crash", "saving_detail", due)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("submit changed=%v error=%v", changed, err)
	}
	attempt := latestAttempt(t, db, schedule.ID)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	owner, _ := ingestionrun.NewOwnerID()
	run, err := runs.Claim(context.Background(), owner)
	if err != nil || run == nil || run.ID != attempt.RunID {
		t.Fatalf("claimed run=%+v error=%v", run, err)
	}
	progress := ingestionrun.Progress{Total: 1, Started: 1, Succeeded: 1, Rows: 1, Step: "publish_detail"}
	if err := runs.UpdateProgress(context.Background(), run.ID, owner, progress, nil); err != nil {
		t.Fatal(err)
	}
	record, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailSaving,
		[]byte(`{"norekening":"SCHEDULER-CRASH","nocif":"CIF","saldoawal":"1","saldoakhir":"1"}`), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	details := ingestionstore.NewDetailRepository(db)
	if err := details.Stage(context.Background(), run.ID, record); err != nil {
		t.Fatal(err)
	}
	if err := details.Publish(context.Background(), run.ID, owner, ingestion.DetailSaving, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM fincloud_saving_details WHERE account_no='SCHEDULER-CRASH'`) })
	clearThrottle(t, db, schedule.ID)
	restarted := newIntegrationService(t, db)
	if changed, err := restarted.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("restart resolution changed=%v error=%v", changed, err)
	}
	var occurrenceStatus string
	if err := db.Get(&occurrenceStatus, `SELECT status FROM schedule_occurrences WHERE id=?`, attempt.OccurrenceID); err != nil || occurrenceStatus != "resolved" {
		t.Fatalf("occurrence status=%q error=%v", occurrenceStatus, err)
	}
	var attempts, linkedRuns int
	if err := db.Get(&attempts, `SELECT COUNT(*) FROM schedule_attempts WHERE occurrence_id=?`, attempt.OccurrenceID); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
	if err := db.Get(&linkedRuns, `SELECT COUNT(*) FROM ingestion_runs WHERE id=? AND status='succeeded'`, attempt.RunID); err != nil || linkedRuns != 1 {
		t.Fatalf("linked succeeded runs=%d error=%v", linkedRuns, err)
	}
	if changed, err := restarted.process(context.Background(), schedule.ID); err != nil || changed {
		t.Fatalf("repeated resolution changed=%v error=%v", changed, err)
	}
}

func TestSchedulerParametersRoundTripMySQLForEveryCanonicalKind(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	for index, jobKey := range []string{"cif_opening_report", "balance_sheet_report", "eod_cif_opening_report_full", "saving_detail"} {
		schedule := createDueSchedule(t, db, service, fmt.Sprintf("parameter-kind-%d", index), jobKey, due)
		if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
			t.Fatalf("submit %s changed=%v error=%v", jobKey, changed, err)
		}
		attempt := latestAttempt(t, db, schedule.ID)
		run, err := runs.Get(context.Background(), attempt.RunID)
		if err != nil {
			t.Fatal(err)
		}
		job, _ := catalog.Find(jobKey)
		if err := run.Parameters.Validate(job); err != nil {
			t.Fatalf("scheduled %s parameters failed after MySQL JSON round-trip: %v; json=%s", jobKey, err, run.Parameters.JSON)
		}
	}
}

func TestCancelledAndAbandonedAttemptsRemainUnresolved(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	for index, test := range []struct {
		job    string
		status ingestionrun.Status
	}{
		{"fund_distribution_report", ingestionrun.StatusCancelled},
		{"vault_mutation_report", ingestionrun.StatusAbandoned},
	} {
		occurrence := due.AddDate(0, 0, index)
		schedule := createDueSchedule(t, db, service, string(test.status), test.job, occurrence)
		_, _ = service.process(context.Background(), schedule.ID)
		attempt := latestAttempt(t, db, schedule.ID)
		finished := occurrence.Add(time.Hour)
		finishRun(t, db, attempt.RunID, test.status, finished)
		clearThrottle(t, db, schedule.ID)
		if _, err := service.process(context.Background(), schedule.ID); err != nil {
			t.Fatal(err)
		}
		assertCursor(t, db, schedule.ID, occurrence)
		assertRetry(t, db, schedule.ID, finished.Add(time.Minute))
	}
}

func TestDisableUpdateEnableFencesOldAttempt(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	schedule := createDueSchedule(t, db, service, "stale", "cif_opening_report", due)
	_, _ = service.process(context.Background(), schedule.ID)
	oldAttempt := latestAttempt(t, db, schedule.ID)

	disabled, err := service.Disable(context.Background(), schedule.ID, schedule.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(context.Background(), schedule.ID, UpdateInput{Definition: Definition{
		Name: "replacement", JobKey: "journal_transaction_report", CronExpression: "0 2 * * *", Timezone: "UTC", Policy: PreviousCalendarDayPolicy(),
	}, ExpectedRevision: disabled.Revision})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := service.Enable(context.Background(), schedule.ID, updated.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	newCursor := *enabled.NextRunAt
	finishRun(t, db, oldAttempt.RunID, ingestionrun.StatusSucceeded, due.Add(time.Hour))
	if got, err := service.Get(context.Background(), schedule.ID); err != nil || got.Definition.JobKey != "journal_transaction_report" || !got.NextRunAt.Equal(newCursor) {
		t.Fatalf("stale result changed replacement schedule=%+v error=%v", got, err)
	}
	var status string
	if err := db.Get(&status, `SELECT status FROM schedule_occurrences WHERE id=?`, oldAttempt.OccurrenceID); err != nil || status != "discarded" {
		t.Fatalf("old occurrence status=%q error=%v", status, err)
	}
}

func TestSemanticEditCannotDiscardBacklogButNameEditPreservesIt(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	schedule := createDueSchedule(t, db, service, "edit", "cif_opening_report", due)
	_, _ = service.process(context.Background(), schedule.ID)
	_, err := service.Update(context.Background(), schedule.ID, UpdateInput{Definition: Definition{
		Name: "semantic", JobKey: "journal_transaction_report", CronExpression: "0 1 * * *", Timezone: "UTC", Policy: PreviousCalendarDayPolicy(),
	}, ExpectedRevision: schedule.Revision})
	if !errors.Is(err, ErrBacklog) {
		t.Fatalf("semantic backlog edit error=%v", err)
	}
	updated, err := service.Update(context.Background(), schedule.ID, UpdateInput{Definition: Definition{
		Name: "renamed", JobKey: schedule.Definition.JobKey, CronExpression: schedule.Definition.CronExpression,
		Timezone: schedule.Definition.Timezone, Policy: schedule.Definition.Policy,
	}, ExpectedRevision: schedule.Revision})
	if err != nil || updated.Definition.Name != "renamed" || updated.Revision != schedule.Revision+1 || !updated.NextRunAt.Equal(due) {
		t.Fatalf("name edit=%+v error=%v", updated, err)
	}
}

func TestCorruptDefinitionUsesCursorFallbackAndRevisionedDisable(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	schedule := createDueSchedule(t, db, service, "corrupt", "cif_opening_report", due)
	if _, err := db.Exec(`UPDATE schedules SET cron_expression='not a cron' WHERE id=?`, schedule.ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("corrupt changed=%v error=%v", changed, err)
	}
	var row struct {
		Enabled bool         `db:"enabled"`
		Next    sql.NullTime `db:"next_run_at"`
		Rev     uint64       `db:"revision"`
	}
	if err := db.Get(&row, `SELECT enabled,next_run_at,revision FROM schedules WHERE id=?`, schedule.ID); err != nil || row.Enabled || row.Next.Valid || row.Rev != schedule.Revision+1 {
		t.Fatalf("disabled=%+v error=%v", row, err)
	}
	var occurrence struct {
		ScheduledFor   time.Time `db:"scheduled_for"`
		Identity       string    `db:"identity_source"`
		Mode, Status   string
		RejectRevision uint64 `db:"rejection_revision"`
	}
	if err := db.Get(&occurrence, `SELECT scheduled_for,identity_source,resolution_mode mode,status,rejection_revision
		FROM schedule_occurrences WHERE schedule_id=?`, schedule.ID); err != nil || !occurrence.ScheduledFor.Equal(due) ||
		occurrence.Identity != "persisted_cursor_fallback" || occurrence.Mode != "invalid" || occurrence.Status != "rejected_invalid" || occurrence.RejectRevision != schedule.Revision {
		t.Fatalf("fallback occurrence=%+v error=%v", occurrence, err)
	}
}

func TestBusyDisabledFairnessAndConcurrentAttemptAllocation(t *testing.T) {
	db, service := integrationService(t)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	busy := createDueSchedule(t, db, service, "busy", "cif_opening_report", due)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	date, _ := ingestion.ParseCalendarDate("2026-08-09")
	parameters, _ := ingestionrun.NewRangeExecution("cif_opening_report", date, date)
	manualID, err := runs.Submit(context.Background(), "cif_opening_report", parameters, ingestionrun.TriggerDirect, "busy-control", nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := service.process(context.Background(), busy.ID); err != nil || !changed {
		t.Fatalf("busy delivery changed=%v error=%v", changed, err)
	}
	assertAttemptCount(t, db, busy.ID, 0)
	assertCursor(t, db, busy.ID, due)
	var reason string
	if err := db.Get(&reason, `SELECT delivery_block_reason FROM schedules WHERE id=?`, busy.ID); err != nil || reason != "job_busy" {
		t.Fatalf("busy reason=%q error=%v", reason, err)
	}
	finishRun(t, db, manualID, ingestionrun.StatusFailed, due)

	disabled := createDueSchedule(t, db, service, "disabled", "journal_transaction_report", due)
	if _, err := db.Exec(`UPDATE source_settings SET enabled=FALSE WHERE source_id='journal_transaction_report'`); err != nil {
		t.Fatal(err)
	}
	_, _ = service.process(context.Background(), disabled.ID)
	assertAttemptCount(t, db, disabled.ID, 0)
	assertCursor(t, db, disabled.ID, due)
	if err := db.Get(&reason, `SELECT delivery_block_reason FROM schedules WHERE id=?`, disabled.ID); err != nil || reason != "source_disabled" {
		t.Fatalf("disabled reason=%q error=%v", reason, err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id='journal_transaction_report'`); err != nil {
		t.Fatal(err)
	}
	expireThrottle(t, db, disabled.ID)

	third := createDueSchedule(t, db, service, "third", "fund_distribution_report", due)
	if err := service.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertAttemptCount(t, db, disabled.ID, 1)
	assertAttemptCount(t, db, third.ID, 1)

	concurrent := createDueSchedule(t, db, service, "concurrent", "vault_mutation_report", due)
	otherInstance := newIntegrationService(t, db)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for _, instance := range []*Service{service, otherInstance} {
		wait.Add(1)
		go func(instance *Service) {
			defer wait.Done()
			_, err := instance.process(context.Background(), concurrent.ID)
			errorsSeen <- err
		}(instance)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAttemptCount(t, db, concurrent.ID, 1)
	var unresolved int
	if err := db.Get(&unresolved, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id=? AND status='unresolved'`, concurrent.ID); err != nil || unresolved != 1 {
		t.Fatalf("concurrent unresolved occurrences=%d error=%v", unresolved, err)
	}
}

func TestUTCSessionAndMicrosecondRoundTrip(t *testing.T) {
	db, _ := integrationService(t)
	connections := make([]*sqlx.Conn, 3)
	connectionIDs := map[uint64]bool{}
	for index := range connections {
		connection, err := db.Connx(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = connection
		defer connection.Close()
		var zone string
		var connectionID uint64
		if err := connection.QueryRowxContext(context.Background(), `SELECT @@session.time_zone,CONNECTION_ID()`).Scan(&zone, &connectionID); err != nil || zone != "+00:00" {
			t.Fatalf("connection %d timezone=%q error=%v", index, zone, err)
		}
		connectionIDs[connectionID] = true
	}
	if len(connectionIDs) != len(connections) {
		t.Fatalf("pooled connections not distinct: %v", connectionIDs)
	}
	want := time.Date(2026, 8, 14, 1, 2, 3, 456789000, time.UTC)
	var got time.Time
	if err := connections[0].QueryRowxContext(context.Background(), `SELECT CAST(? AS DATETIME(6))`, want).Scan(&got); err != nil || !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("roundtrip=%s location=%v error=%v", got, got.Location(), err)
	}
}

func TestCreateManyCreatesOrdinarySchedulesAndSkipsCurrentDuplicates(t *testing.T) {
	db, service := integrationService(t)
	ctx := context.Background()
	cron, timezone := "0 1 * * *", DefaultTimezone
	jobs := []string{"cif_opening_report", "journal_transaction_report", "cif_detail"}
	inputs := make([]CreateInput, len(jobs))
	for index, job := range jobs {
		inputs[index] = bulkCreateInput(job, cron, timezone, false, nil)
	}
	result, err := service.CreateMany(ctx, inputs)
	if err != nil || len(result.Created) != 3 || len(result.Existing) != 0 {
		t.Fatalf("initial bulk result=%+v error=%v", result, err)
	}
	var ordinary struct {
		Rows, Crons, Timezones, Names int
	}
	if err := db.Get(&ordinary, `SELECT COUNT(*) rows,COUNT(DISTINCT cron_expression) crons,
		COUNT(DISTINCT timezone) timezones,COUNT(DISTINCT name) names FROM schedules WHERE id IN (?,?,?)`,
		result.Created[0].ID, result.Created[1].ID, result.Created[2].ID); err != nil || ordinary.Rows != 3 || ordinary.Crons != 1 || ordinary.Timezones != 1 {
		t.Fatalf("ordinary schedules=%+v error=%v", ordinary, err)
	}

	alternate, err := service.CreateMany(ctx, []CreateInput{bulkCreateInput(jobs[0], "0 13 * * *", timezone, false, nil)})
	if err != nil || len(alternate.Created) != 1 || len(alternate.Existing) != 0 || alternate.Created[0].Definition.CronExpression != "0 13 * * *" {
		t.Fatalf("alternate result=%+v error=%v", alternate, err)
	}

	duplicate, err := service.CreateMany(ctx, []CreateInput{bulkCreateInput(jobs[0], cron, timezone, true, nil)})
	if err != nil || len(duplicate.Created) != 0 || len(duplicate.Existing) != 1 || duplicate.Existing[0].Enabled {
		t.Fatalf("disabled duplicate result=%+v error=%v", duplicate, err)
	}

	mixed, err := service.CreateMany(ctx, []CreateInput{
		bulkCreateInput(jobs[0], cron, timezone, true, nil),
		bulkCreateInput(jobs[1], cron, timezone, true, nil),
		bulkCreateInput("saving_detail", cron, timezone, true, nil),
	})
	if err != nil || len(mixed.Created) != 1 || len(mixed.Existing) != 2 || mixed.Created[0].Definition.JobKey != "saving_detail" {
		t.Fatalf("mixed result=%+v error=%v", mixed, err)
	}
	allExisting, err := service.CreateMany(ctx, []CreateInput{
		bulkCreateInput(jobs[0], cron, timezone, false, nil),
		bulkCreateInput(jobs[1], cron, timezone, false, nil),
		bulkCreateInput("saving_detail", cron, timezone, false, nil),
	})
	if err != nil || len(allExisting.Created) != 0 || len(allExisting.Existing) != 3 {
		t.Fatalf("all-existing result=%+v error=%v", allExisting, err)
	}

	archivedSeed, err := service.CreateMany(ctx, []CreateInput{bulkCreateInput("loan_detail", "0 3 * * *", timezone, false, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Archive(ctx, archivedSeed.Created[0].ID, archivedSeed.Created[0].Revision, nil); err != nil {
		t.Fatal(err)
	}
	afterArchive, err := service.CreateMany(ctx, []CreateInput{bulkCreateInput("loan_detail", "0 3 * * *", timezone, false, nil)})
	if err != nil || len(afterArchive.Created) != 1 || len(afterArchive.Existing) != 0 {
		t.Fatalf("archived replacement=%+v error=%v", afterArchive, err)
	}

	var before int
	if err := db.Get(&before, `SELECT COUNT(*) FROM schedules`); err != nil {
		t.Fatal(err)
	}
	invalidRequests := [][]CreateInput{
		nil,
		{bulkCreateInput("unknown_job", cron, timezone, false, nil)},
		{bulkCreateInput(jobs[0], "not a cron", timezone, false, nil)},
		{bulkCreateInput(jobs[0], cron, "Not/A_Timezone", false, nil)},
		{bulkCreateInput(jobs[0], cron, timezone, false, nil), bulkCreateInput(jobs[0], cron, timezone, false, nil)},
		{bulkCreateInput(jobs[0], cron, timezone, false, nil), bulkCreateInput(jobs[1], "0 2 * * *", timezone, false, nil)},
	}
	for index, request := range invalidRequests {
		if _, err := service.CreateMany(ctx, request); !errors.Is(err, ErrInvalidDefinition) {
			t.Fatalf("invalid request %d error=%v", index, err)
		}
	}
	var after int
	if err := db.Get(&after, `SELECT COUNT(*) FROM schedules`); err != nil || after != before {
		t.Fatalf("validation persisted rows before=%d after=%d error=%v", before, after, err)
	}
}

func TestCreateManyRollsBackPersistenceFailure(t *testing.T) {
	db, service := integrationService(t)
	missingActor := uint64(9_000_000_000)
	_, err := service.CreateMany(context.Background(), []CreateInput{
		bulkCreateInput("cif_opening_report", "0 4 * * *", DefaultTimezone, false, nil),
		bulkCreateInput("journal_transaction_report", "0 4 * * *", DefaultTimezone, false, &missingActor),
	})
	if err == nil {
		t.Fatal("persistence failure accepted")
	}
	var count int
	if queryErr := db.Get(&count, `SELECT COUNT(*) FROM schedules WHERE cron_expression='0 4 * * *' AND timezone=?`, DefaultTimezone); queryErr != nil || count != 0 {
		t.Fatalf("partial bulk rows=%d error=%v create_error=%v", count, queryErr, err)
	}
}

func TestCreateManySerializesIdenticalConcurrentRequestsAndUsesNormalRuntime(t *testing.T) {
	db, first := integrationService(t)
	second := newIntegrationService(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	type outcome struct {
		result CreateManyResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, service := range []*Service{first, second} {
		go func(service *Service) {
			<-start
			result, err := service.CreateMany(ctx, []CreateInput{
				bulkCreateInput("vault_mutation_report", "0 5 * * *", DefaultTimezone, true, nil),
			})
			outcomes <- outcome{result, err}
		}(service)
	}
	close(start)
	created, existing := 0, 0
	var scheduleID uint64
	for range 2 {
		value := <-outcomes
		if value.err != nil {
			t.Fatal(value.err)
		}
		created += len(value.result.Created)
		existing += len(value.result.Existing)
		if len(value.result.Created) == 1 {
			scheduleID = value.result.Created[0].ID
		}
	}
	var rows int
	if err := db.Get(&rows, `SELECT COUNT(*) FROM schedules WHERE job_key='vault_mutation_report' AND cron_expression='0 5 * * *'
		AND timezone=? AND archived_at IS NULL`, DefaultTimezone); err != nil || rows != 1 || created != 1 || existing != 1 {
		t.Fatalf("created=%d existing=%d rows=%d error=%v", created, existing, rows, err)
	}
	due := time.Date(2026, 8, 10, 5, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`UPDATE schedules SET next_run_at=? WHERE id=?`, due, scheduleID); err != nil {
		t.Fatal(err)
	}
	if changed, err := first.process(ctx, scheduleID); err != nil || !changed {
		t.Fatalf("bulk-created runtime changed=%v error=%v", changed, err)
	}
	assertAttemptCount(t, db, scheduleID, 1)
}

func TestBulkStateClassifiesNoOpsBeforeTransitionValidationAndRejectsArchivedAtomically(t *testing.T) {
	db, service := integrationService(t)
	ctx := context.Background()
	enabled, err := service.Create(ctx, bulkCreateInput("cif_opening_report", "0 1 * * *", "UTC", true, nil))
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := service.Create(ctx, bulkCreateInput("journal_transaction_report", "0 2 * * *", "UTC", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schedule_occurrences
		(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,
		policy_kind,policy_version,policy_json,policy_checksum)
		SELECT id,next_run_at,'validated_cron','historical','unresolved',revision,job_key,cron_expression,timezone,
		policy_kind,policy_version,policy_json,policy_checksum FROM schedules WHERE id=?`, enabled.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schedules SET cron_expression='invalid cron' WHERE id=?`, enabled.ID); err != nil {
		t.Fatal(err)
	}
	result, err := service.BulkState(ctx, []uint64{enabled.ID, disabled.ID, enabled.ID, disabled.ID}, BulkEnable, nil, nil)
	if err != nil || result != (BulkStateResult{SelectedCount: 2, AffectedCount: 1, NoOpCount: 1}) {
		t.Fatalf("bulk enable=%+v error=%v", result, err)
	}
	enabledAfter, err := service.Get(ctx, enabled.ID)
	if err != nil || enabledAfter.Revision != enabled.Revision {
		t.Fatalf("enabled no-op=%+v error=%v", enabledAfter, err)
	}
	disabledAfter, err := service.Get(ctx, disabled.ID)
	if err != nil || !disabledAfter.Enabled || disabledAfter.Revision != disabled.Revision+1 || disabledAfter.NextRunAt == nil {
		t.Fatalf("disabled transition=%+v error=%v", disabledAfter, err)
	}
	var activeStatus string
	if err := db.Get(&activeStatus, `SELECT status FROM schedule_occurrences WHERE schedule_id=?`, enabled.ID); err != nil || activeStatus != "unresolved" {
		t.Fatalf("enabled no-op occurrence=%q error=%v", activeStatus, err)
	}

	disabledNoOp, err := service.Create(ctx, bulkCreateInput("vault_mutation_report", "0 3 * * *", "UTC", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schedule_occurrences
		(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,
		policy_kind,policy_version,policy_json,policy_checksum)
		SELECT id,UTC_TIMESTAMP(6)-INTERVAL 1 DAY,'validated_cron','historical','unresolved',revision,job_key,cron_expression,timezone,
		policy_kind,policy_version,policy_json,policy_checksum FROM schedules WHERE id=?`, disabledNoOp.ID); err != nil {
		t.Fatal(err)
	}
	result, err = service.BulkState(ctx, []uint64{disabledNoOp.ID}, BulkDisable, nil, nil)
	if err != nil || result != (BulkStateResult{SelectedCount: 1, NoOpCount: 1}) {
		t.Fatalf("bulk disable no-op=%+v error=%v", result, err)
	}
	if err := db.Get(&activeStatus, `SELECT status FROM schedule_occurrences WHERE schedule_id=?`, disabledNoOp.ID); err != nil || activeStatus != "unresolved" {
		t.Fatalf("disabled no-op occurrence=%q error=%v", activeStatus, err)
	}

	if _, err := service.BulkState(ctx, []uint64{enabled.ID}, BulkArchive, nil, nil); err != nil {
		t.Fatal(err)
	}
	atomicCandidate, err := service.Create(ctx, bulkCreateInput("saving_detail", "0 4 * * *", "UTC", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BulkState(ctx, []uint64{atomicCandidate.ID, enabled.ID}, BulkEnable, nil, nil); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived selection error=%v", err)
	}
	atomicAfter, err := service.Get(ctx, atomicCandidate.ID)
	if err != nil || atomicAfter.Enabled || atomicAfter.Revision != atomicCandidate.Revision {
		t.Fatalf("atomic candidate changed=%+v error=%v", atomicAfter, err)
	}
}

func TestBulkStopPreservesAttemptsAndUsesCanonicalCursorBehavior(t *testing.T) {
	db, service := integrationService(t)
	ctx := context.Background()
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	disabled := createDueSchedule(t, db, service, "bulk disable", "cif_opening_report", due)
	archived := createDueSchedule(t, db, service, "bulk archive", "journal_transaction_report", due)
	for _, schedule := range []Schedule{disabled, archived} {
		if changed, err := service.process(ctx, schedule.ID); err != nil || !changed {
			t.Fatalf("process schedule=%d changed=%v error=%v", schedule.ID, changed, err)
		}
	}
	if result, err := service.BulkState(ctx, []uint64{disabled.ID}, BulkDisable, nil, nil); err != nil || result.AffectedCount != 1 {
		t.Fatalf("disable result=%+v error=%v", result, err)
	}
	if result, err := service.BulkState(ctx, []uint64{archived.ID}, BulkArchive, nil, nil); err != nil || result.AffectedCount != 1 {
		t.Fatalf("archive result=%+v error=%v", result, err)
	}
	for _, schedule := range []Schedule{disabled, archived} {
		assertAttemptCount(t, db, schedule.ID, 1)
		var status string
		if err := db.Get(&status, `SELECT status FROM schedule_occurrences WHERE schedule_id=?`, schedule.ID); err != nil || status != "discarded" {
			t.Fatalf("schedule=%d occurrence=%q error=%v", schedule.ID, status, err)
		}
	}
	disabledAfter, err := service.Get(ctx, disabled.ID)
	if err != nil || disabledAfter.Enabled || disabledAfter.NextRunAt != nil || disabledAfter.ArchivedAt != nil || disabledAfter.Revision != disabled.Revision+1 {
		t.Fatalf("disabled=%+v error=%v", disabledAfter, err)
	}
	archivedAfter, err := service.Get(ctx, archived.ID)
	if err != nil || archivedAfter.Enabled || archivedAfter.NextRunAt != nil || archivedAfter.ArchivedAt == nil || archivedAfter.Revision != archived.Revision+1 {
		t.Fatalf("archived=%+v error=%v", archivedAfter, err)
	}
	noOp, err := service.BulkState(ctx, []uint64{disabled.ID}, BulkDisable, nil, nil)
	if err != nil || noOp.NoOpCount != 1 {
		t.Fatalf("second disable=%+v error=%v", noOp, err)
	}
	stillDisabled, _ := service.Get(ctx, disabled.ID)
	if stillDisabled.Revision != disabledAfter.Revision {
		t.Fatalf("no-op revision=%d want=%d", stillDisabled.Revision, disabledAfter.Revision)
	}
	reenabled, err := service.BulkState(ctx, []uint64{disabled.ID}, BulkEnable, nil, nil)
	if err != nil || reenabled.AffectedCount != 1 {
		t.Fatalf("reenable=%+v error=%v", reenabled, err)
	}
	reenabledSchedule, _ := service.Get(ctx, disabled.ID)
	if !reenabledSchedule.Enabled || reenabledSchedule.NextRunAt == nil || reenabledSchedule.Revision != disabledAfter.Revision+1 {
		t.Fatalf("reenabled schedule=%+v", reenabledSchedule)
	}
	assertAttemptCount(t, db, disabled.ID, 1)
	if _, err := service.BulkState(ctx, []uint64{archived.ID}, BulkEnable, nil, nil); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived enable error=%v", err)
	}
}

func TestBulkEnableUsesCurrentLockedArchiveState(t *testing.T) {
	db, service := integrationService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	archived, err := service.Create(ctx, bulkCreateInput("cif_opening_report", "0 1 * * *", "UTC", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Create(ctx, bulkCreateInput("journal_transaction_report", "0 2 * * *", "UTC", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var locked uint64
	if err := blocker.GetContext(ctx, &locked, `SELECT id FROM schedules WHERE id=? FOR UPDATE`, archived.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := service.BulkState(ctx, []uint64{archived.ID, other.ID}, BulkEnable, nil, nil)
		result <- err
	}()
	<-started
	if _, err := blocker.ExecContext(ctx, `UPDATE schedules SET archived_at=UTC_TIMESTAMP(6),revision=revision+1 WHERE id=?`, archived.ID); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrArchived) {
		t.Fatalf("bulk error=%v", err)
	}
	otherAfter, err := service.Get(ctx, other.ID)
	if err != nil || otherAfter.Enabled || otherAfter.Revision != other.Revision {
		t.Fatalf("other schedule changed=%+v error=%v", otherAfter, err)
	}
}

func bulkCreateInput(job, cron, timezone string, enabled bool, actor *uint64) CreateInput {
	policy := PreviousCalendarDayPolicy()
	if job == "cif_detail" || job == "saving_detail" || job == "time_deposit_detail" || job == "loan_detail" {
		policy = LiveSnapshotPolicy()
	}
	return CreateInput{Definition: Definition{Name: job, JobKey: job, CronExpression: cron, Timezone: timezone, Policy: policy}, Enabled: enabled, ActorID: actor}
}

func integrationService(t *testing.T) (*sqlx.DB, *Service) {
	t.Helper()
	db := integrationdb.Open(t)
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schedule_attempts`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schedule_occurrences`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schedules`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE trigger_type='scheduler' OR trigger_reference='busy-control'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM schedule_attempts`)
		_, _ = db.Exec(`DELETE FROM schedule_occurrences`)
		_, _ = db.Exec(`DELETE FROM schedules`)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE trigger_type='scheduler' OR trigger_reference='busy-control'`)
		_, _ = db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id IN ('journal_transaction_report')`)
	})
	return db, newIntegrationService(t, db)
}

func newIntegrationService(t *testing.T, db *sqlx.DB) *Service {
	t.Helper()
	catalog, _ := ingestion.NewCatalog()
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(db, runs.SubmitInTx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func createDueSchedule(t *testing.T, db *sqlx.DB, service *Service, name, job string, due time.Time) Schedule {
	t.Helper()
	policy := PreviousCalendarDayPolicy()
	if definition, found := service.catalog.Find(job); found && definition.DateStrategy == ingestion.NoDate {
		policy = LiveSnapshotPolicy()
	}
	created, err := service.Create(context.Background(), CreateInput{Definition: Definition{
		Name: name, JobKey: job, CronExpression: "0 1 * * *", Timezone: "UTC", Policy: policy,
	}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schedules SET next_run_at=?,scheduler_not_before=NULL WHERE id=?`, due, created.ID); err != nil {
		t.Fatal(err)
	}
	created.NextRunAt = &due
	return created
}

type attemptView struct {
	OccurrenceID uint64 `db:"occurrence_id"`
	RunID        uint64 `db:"run_id"`
	AttemptNo    uint32 `db:"attempt_no"`
}

func assertMaintenanceAttemptDate(t *testing.T, db *sqlx.DB, attempt attemptView, want string) {
	t.Helper()
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	run, err := runs.Get(context.Background(), attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := ingestionrun.DecodeMaintenanceSeries(run.Parameters)
	if err != nil || len(parameters.Dates) != 1 || parameters.Dates[0].String() != want {
		t.Fatalf("attempt %d dates=%v error=%v", attempt.AttemptNo, parameters.Dates, err)
	}
}

func latestAttempt(t *testing.T, db *sqlx.DB, scheduleID uint64) attemptView {
	t.Helper()
	var value attemptView
	if err := db.Get(&value, `SELECT a.occurrence_id,a.ingestion_run_id run_id,a.attempt_no FROM schedule_attempts a
		JOIN schedule_occurrences o ON o.id=a.occurrence_id WHERE o.schedule_id=? ORDER BY a.attempt_no DESC LIMIT 1`, scheduleID); err != nil {
		t.Fatal(err)
	}
	return value
}

func finishRun(t *testing.T, db *sqlx.DB, runID uint64, status ingestionrun.Status, finished time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE ingestion_runs SET status=?,finished_at=? WHERE id=?`, status, finished, runID); err != nil {
		t.Fatal(err)
	}
}

func clearThrottle(t *testing.T, db *sqlx.DB, scheduleID uint64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE schedules SET scheduler_not_before=NULL WHERE id=?`, scheduleID); err != nil {
		t.Fatal(err)
	}
}

func expireThrottle(t *testing.T, db *sqlx.DB, scheduleID uint64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE schedules s JOIN schedule_occurrences o ON o.schedule_id=s.id AND o.status='unresolved'
		SET s.scheduler_not_before=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND,o.retry_not_before=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE s.id=?`, scheduleID); err != nil {
		t.Fatal(err)
	}
}

func assertCursor(t *testing.T, db *sqlx.DB, scheduleID uint64, want time.Time) {
	t.Helper()
	var got time.Time
	if err := db.Get(&got, `SELECT next_run_at FROM schedules WHERE id=?`, scheduleID); err != nil || !got.Equal(want) {
		t.Fatalf("cursor=%s want=%s error=%v", got, want, err)
	}
}

func assertRetry(t *testing.T, db *sqlx.DB, scheduleID uint64, want time.Time) {
	t.Helper()
	var got time.Time
	if err := db.Get(&got, `SELECT retry_not_before FROM schedule_occurrences WHERE schedule_id=? AND status='unresolved'`, scheduleID); err != nil || !got.Equal(want) {
		t.Fatalf("retry=%s want=%s error=%v", got, want, err)
	}
}

func assertAttemptCount(t *testing.T, db *sqlx.DB, scheduleID uint64, want int) {
	t.Helper()
	var got int
	if err := db.Get(&got, `SELECT COUNT(*) FROM schedule_attempts a JOIN schedule_occurrences o ON o.id=a.occurrence_id WHERE o.schedule_id=?`, scheduleID); err != nil || got != want {
		t.Fatalf("schedule %d attempts=%d want=%d error=%v", scheduleID, got, want, err)
	}
}

func assertCanonicalLink(t *testing.T, db *sqlx.DB, scheduleID uint64, attempt attemptView) {
	t.Helper()
	var reference string
	var scheduledFor time.Time
	err := db.QueryRowx(`SELECT o.scheduled_for,r.trigger_reference FROM schedules s
		JOIN schedule_occurrences o ON o.schedule_id=s.id
		JOIN schedule_attempts a ON a.occurrence_id=o.id
		JOIN ingestion_runs r ON r.id=a.ingestion_run_id
		WHERE s.id=? AND o.id=? AND a.attempt_no=? AND r.id=?`, scheduleID, attempt.OccurrenceID, attempt.AttemptNo, attempt.RunID).Scan(&scheduledFor, &reference)
	want := fmt.Sprintf("schedule:%d:%s:attempt:%d", scheduleID, scheduledFor.UTC().Format(time.RFC3339Nano), attempt.AttemptNo)
	if err != nil || reference != want {
		t.Fatalf("canonical linkage reference=%q want=%q error=%v", reference, want, err)
	}
}
