//go:build integration

package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestRunReadModelsPaginationAndPlans(t *testing.T) {
	db := integrationdb.Open(t)
	from, _ := core.ParseCalendarDate("2026-06-01")
	parameters, _ := ingestionrun.NewRangeExecution("cif_opening_report", from, from)
	ids := make([]uint64, 0, 61)
	for index := range 60 {
		status := "succeeded"
		if index%3 == 0 {
			status = "failed"
		}
		result, err := db.Exec(`INSERT INTO ingestion_runs
			(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,created_at,finished_at)
			VALUES ('job','cif_opening_report',?,?,?,?,?,?,?, ?,?)`, status, parameters.Kind, parameters.Version, parameters.JSON, parameters.Checksum[:], "direct", fmt.Sprintf("phase6-read-%d-%d", time.Now().UnixNano(), index), time.Now().UTC().Add(time.Duration(index)*time.Microsecond), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		ids = append(ids, uint64(id))
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id)
		}
	})
	service, _ := NewService(NewRepository(db))
	catalog, _ := core.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	technicalBase := time.Now().UTC().Add(-time.Minute)
	for index, body := range []string{"first source response", "second source response"} {
		details := fmt.Sprintf(`{"source":{"response":{"body":{"body_encoding":"utf8","body":%q}}}}`, body)
		event := ingestionrun.TechnicalEvent{RunID: ids[0], OccurredAt: technicalBase.Add(time.Duration(index) * time.Millisecond), Severity: "error",
			EventKind: "failure", Terminal: index == 1, Class: "source", Step: "download_report", Operation: "download_report",
			JobKey: "cif_opening_report", ErrorType: "*fincloud.Error", ErrorMessage: body, Details: []byte(details)}
		if err := runs.AppendTechnicalEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	technicalDetail, err := service.FindRun(context.Background(), ids[0])
	if err != nil || len(technicalDetail.TechnicalErrors) != 2 || technicalDetail.TechnicalErrors[0].Body != "first source response" || technicalDetail.TechnicalErrors[1].BodyEncoding != "utf8" {
		t.Fatalf("technical detail=%+v error=%v", technicalDetail.TechnicalErrors, err)
	}
	oldDetail, err := service.FindRun(context.Background(), ids[1])
	if err != nil || len(oldDetail.TechnicalErrors) != 0 {
		t.Fatalf("old run diagnostics=%+v error=%v", oldDetail.TechnicalErrors, err)
	}
	page, err := service.ListRuns(context.Background(), RunFilter{Job: "cif_opening_report"}, 1)
	if err != nil || len(page.Rows) != 50 || page.Pagination.Total < 60 || page.Rows[0].ID <= page.Rows[49].ID {
		t.Fatalf("page=%+v rows=%d error=%v", page.Pagination, len(page.Rows), err)
	}
	second, err := service.ListRuns(context.Background(), RunFilter{Job: "cif_opening_report"}, 2)
	if err != nil || len(second.Rows) == 0 {
		t.Fatalf("second page rows=%d error=%v", len(second.Rows), err)
	}
	overview, err := service.OverviewRuns(context.Background())
	if err != nil || len(overview.RecentProblems) == 0 || len(overview.RecentSuccesses) == 0 {
		t.Fatalf("overview=%+v error=%v", overview, err)
	}
	sources, err := service.OverviewSources(context.Background())
	if err != nil || sources.Enabled+sources.Disabled != 36 {
		t.Fatalf("canonical source overview=%+v error=%v", sources, err)
	}
	parentID, err := runs.CreateRunAll(context.Background(), from, from, ingestionrun.TriggerDirect, "phase6-read-run-all", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=?`, parentID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, parentID)
	})
	detail, err := service.FindRun(context.Background(), parentID)
	if err != nil || len(detail.Children) != 36 {
		t.Fatalf("Run All detail children=%d error=%v", len(detail.Children), err)
	}

	for name, query := range map[string]string{
		"job":     `EXPLAIN FORMAT=JSON SELECT COUNT(*) FROM ingestion_runs WHERE job_key='cif_opening_report'`,
		"status":  `EXPLAIN FORMAT=JSON SELECT COUNT(*) FROM ingestion_runs WHERE status='failed'`,
		"trigger": `EXPLAIN FORMAT=JSON SELECT COUNT(*) FROM ingestion_runs WHERE trigger_type='direct'`,
	} {
		var plan string
		if err := db.Get(&plan, query); err != nil || !strings.Contains(plan, "idx_ingestion_runs_admin_"+name) {
			t.Fatalf("%s plan did not use Phase 6 index: %v\n%s", name, err, plan)
		}
	}
}

func TestRunsListChildVisibilityAndParentSummaries(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	from, _ := core.ParseCalendarDate("2026-06-02")
	parameters, _ := ingestionrun.NewRangeExecution("cif_opening_report", from, from)
	insertJob := func(jobKey, status, trigger string) uint64 {
		t.Helper()
		result, err := db.Exec(`INSERT INTO ingestion_runs
			(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,created_at,finished_at)
			VALUES ('job',?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))`, jobKey, status, parameters.Kind, parameters.Version, parameters.JSON, parameters.Checksum[:], trigger,
			fmt.Sprintf("runs-list-%s-%d", trigger, time.Now().UnixNano()), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return uint64(id)
	}
	manualID := insertJob("cif_opening_report", "succeeded", "direct")
	scheduledID := insertJob("journal_transaction_report", "failed", "scheduler")

	catalog, _ := core.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	parentID, err := runs.CreateRunAll(ctx, from, from, ingestionrun.TriggerDirect, fmt.Sprintf("runs-list-parent-%d", time.Now().UnixNano()), nil)
	if err != nil {
		t.Fatal(err)
	}
	var childIDs []uint64
	if err := db.Select(&childIDs, `SELECT id FROM ingestion_runs WHERE parent_run_id=? ORDER BY child_position`, parentID); err != nil || len(childIDs) != 36 {
		t.Fatalf("children=%d error=%v", len(childIDs), err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status=CASE
		WHEN child_position<=30 THEN 'succeeded' WHEN child_position<=32 THEN 'failed'
		WHEN child_position=33 THEN 'running' ELSE 'planned' END WHERE parent_run_id=?`, parentID); err != nil {
		t.Fatal(err)
	}

	parentParameters, _ := ingestionrun.NewRunAllRange(from, from)
	emptyResult, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,created_at)
		VALUES ('run_all_parent','running',?,?,?,?,?,?,?)`, parentParameters.Kind, parentParameters.Version, parentParameters.JSON, parentParameters.Checksum[:], "direct",
		fmt.Sprintf("runs-list-empty-%d", time.Now().UnixNano()), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	emptyParentID, _ := emptyResult.LastInsertId()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id=?`, parentID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id IN (?,?,?,?)`, parentID, uint64(emptyParentID), manualID, scheduledID)
	})

	repository := NewRepository(db)
	service, _ := NewService(repository)
	defaultPage, err := service.ListRuns(ctx, RunFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint64{manualID, scheduledID, parentID, uint64(emptyParentID)} {
		if findRunView(defaultPage.Rows, id) == nil {
			t.Errorf("default page missing top-level run %d", id)
		}
	}
	for _, id := range childIDs {
		if findRunView(defaultPage.Rows, id) != nil {
			t.Fatalf("default page exposed child %d", id)
		}
	}
	for index := 1; index < len(defaultPage.Rows); index++ {
		if defaultPage.Rows[index-1].ID <= defaultPage.Rows[index].ID {
			t.Fatalf("top-level order changed at %d: %d <= %d", index, defaultPage.Rows[index-1].ID, defaultPage.Rows[index].ID)
		}
	}
	var topLevelTotal, physicalTotal int64
	if err := db.Get(&topLevelTotal, `SELECT COUNT(*) FROM ingestion_runs WHERE kind<>'run_all_child'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&physicalTotal, `SELECT COUNT(*) FROM ingestion_runs`); err != nil {
		t.Fatal(err)
	}
	if defaultPage.Pagination.Total != topLevelTotal || physicalTotal-topLevelTotal < int64(len(childIDs)) {
		t.Fatalf("pagination total=%d top-level=%d physical=%d children=%d", defaultPage.Pagination.Total, topLevelTotal, physicalTotal, len(childIDs))
	}
	parent := findRunView(defaultPage.Rows, parentID)
	if parent == nil || parent.RunAllSummary == nil || *parent.RunAllSummary != (RunAllSummary{Total: 36, Complete: 32, Failed: 2, Running: 1}) {
		t.Fatalf("parent summary=%+v", parent)
	}
	emptyParent := findRunView(defaultPage.Rows, uint64(emptyParentID))
	if emptyParent == nil || emptyParent.RunAllSummary == nil || *emptyParent.RunAllSummary != (RunAllSummary{}) {
		t.Fatalf("empty parent summary=%+v", emptyParent)
	}

	explicitChildren, err := service.ListRuns(ctx, RunFilter{Kind: "run_all_child"}, 1)
	if err != nil || findRunView(explicitChildren.Rows, childIDs[0]) == nil {
		t.Fatalf("explicit child kind rows=%d error=%v", len(explicitChildren.Rows), err)
	}
	failedDefault, err := service.ListRuns(ctx, RunFilter{Status: "failed"}, 1)
	if err != nil || findRunView(failedDefault.Rows, scheduledID) == nil || findRunView(failedDefault.Rows, childIDs[30]) != nil {
		t.Fatalf("default failed filter rows=%d error=%v", len(failedDefault.Rows), err)
	}
	failedChildren, err := service.ListRuns(ctx, RunFilter{Kind: "run_all_child", Status: "failed"}, 1)
	if err != nil || findRunView(failedChildren.Rows, scheduledID) != nil || findRunView(failedChildren.Rows, childIDs[30]) == nil || findRunView(failedChildren.Rows, childIDs[31]) == nil {
		t.Fatalf("failed child filter rows=%d error=%v", len(failedChildren.Rows), err)
	}
	jobDefault, err := service.ListRuns(ctx, RunFilter{Job: "journal_transaction_report"}, 1)
	if err != nil || findRunView(jobDefault.Rows, scheduledID) == nil || findRunView(jobDefault.Rows, childIDs[1]) != nil {
		t.Fatalf("default job filter rows=%d error=%v", len(jobDefault.Rows), err)
	}
	jobChildren, err := service.ListRuns(ctx, RunFilter{Kind: "run_all_child", Job: "journal_transaction_report"}, 1)
	if err != nil || len(jobChildren.Rows) == 0 {
		t.Fatalf("explicit child job filter rows=%d error=%v", len(jobChildren.Rows), err)
	}
	for _, row := range jobChildren.Rows {
		if row.Kind != "run_all_child" || row.JobKey != "journal_transaction_report" {
			t.Fatalf("explicit child job filter returned %+v", row)
		}
	}

	children, err := service.RunAllChildren(ctx, parentID)
	if err != nil || len(children.Rows) != 36 || children.Parent.ID != parentID || children.Parent.Terminal || children.Parent.RunAllSummary == nil ||
		*children.Parent.RunAllSummary != (RunAllSummary{Total: 36, Complete: 32, Failed: 2, Running: 1}) {
		t.Fatalf("fragment children=%d error=%v", len(children.Rows), err)
	}
	for index, child := range children.Rows {
		if child.ParentRunID == nil || *child.ParentRunID != parentID || child.ChildPosition != uint16(index+1) {
			t.Fatalf("fragment child %d=%+v", index, child)
		}
	}
	emptyChildren, err := service.RunAllChildren(ctx, uint64(emptyParentID))
	if err != nil || len(emptyChildren.Rows) != 0 || emptyChildren.Parent.ID != uint64(emptyParentID) || emptyChildren.Parent.RunAllSummary == nil || *emptyChildren.Parent.RunAllSummary != (RunAllSummary{}) {
		t.Fatalf("empty fragment=%+v error=%v", emptyChildren, err)
	}
	for _, invalidID := range []uint64{manualID, ^uint64(0)} {
		if _, err := service.RunAllChildren(ctx, invalidID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("invalid parent %d error=%v", invalidID, err)
		}
	}

	summaries, err := repository.runAllSummaries(ctx, []uint64{parentID, uint64(emptyParentID)})
	if err != nil || summaries[parentID] != (RunAllSummary{Total: 36, Complete: 32, Failed: 2, Running: 1}) || summaries[uint64(emptyParentID)] != (RunAllSummary{}) {
		t.Fatalf("batched summaries=%+v error=%v", summaries, err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET status='succeeded' WHERE parent_run_id=?`, parentID); err != nil {
		t.Fatal(err)
	}
	allComplete, err := repository.runAllSummaries(ctx, []uint64{parentID})
	if err != nil || allComplete[parentID] != (RunAllSummary{Total: 36, Complete: 36}) {
		t.Fatalf("all-complete summary=%+v error=%v", allComplete[parentID], err)
	}
}

func TestSchedulerWaveGroupingFiltersRetriesAndPagination(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	from, _ := core.ParseCalendarDate("2026-08-27")
	parameters, _ := ingestionrun.NewRangeExecution("cif_opening_report", from, from)
	checksum := make([]byte, 32)
	checksum[0] = 1
	var scheduleIDs, occurrenceIDs, runIDs []uint64
	cleanup := func() {
		for _, occurrenceID := range occurrenceIDs {
			_, _ = db.Exec(`DELETE FROM schedule_attempts WHERE occurrence_id=?`, occurrenceID)
		}
		for _, occurrenceID := range occurrenceIDs {
			_, _ = db.Exec(`DELETE FROM schedule_occurrences WHERE id=?`, occurrenceID)
		}
		for _, scheduleID := range scheduleIDs {
			_, _ = db.Exec(`DELETE FROM schedules WHERE id=?`, scheduleID)
		}
		for _, runID := range runIDs {
			_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID)
		}
	}
	t.Cleanup(cleanup)

	insertSchedule := func(name, jobKey string) uint64 {
		t.Helper()
		result, err := db.Exec(`INSERT INTO schedules
			(name,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,enabled,next_run_at)
			VALUES (?,?,'0 * * * *','UTC','test',1,'{}',?,FALSE,NULL)`, name, jobKey, checksum)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		scheduleIDs = append(scheduleIDs, uint64(id))
		return uint64(id)
	}
	insertOccurrence := func(scheduleID uint64, scheduledFor time.Time, jobKey, status string) uint64 {
		t.Helper()
		result, err := db.Exec(`INSERT INTO schedule_occurrences
			(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,
			 policy_kind,policy_version,policy_json,policy_checksum)
			VALUES (?,?,'validated_cron','historical',?,1,?,'0 * * * *','UTC','test',1,'{}',?)`, scheduleID, scheduledFor, status, jobKey, checksum)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		occurrenceIDs = append(occurrenceIDs, uint64(id))
		return uint64(id)
	}
	insertRun := func(jobKey, status, trigger string, createdAt time.Time) uint64 {
		t.Helper()
		reference := fmt.Sprintf("scheduler-wave-test-%d-%d", time.Now().UnixNano(), len(runIDs))
		result, err := db.Exec(`INSERT INTO ingestion_runs
			(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,created_at,finished_at)
			VALUES ('job',?,?,?,?,?,?,?,?,?,?)`, jobKey, status, parameters.Kind, parameters.Version, parameters.JSON, parameters.Checksum[:], trigger, reference, createdAt, createdAt)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		runIDs = append(runIDs, uint64(id))
		return uint64(id)
	}
	linkAttempt := func(occurrenceID uint64, attemptNo int, runID uint64, submittedAt time.Time) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO schedule_attempts (occurrence_id,attempt_no,ingestion_run_id,trigger_reference,submitted_at)
			VALUES (?,?,?,?,?)`, occurrenceID, attemptNo, runID, fmt.Sprintf("scheduler-wave-attempt-%d-%d", occurrenceID, attemptNo), submittedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE schedule_occurrences SET attempt_count=? WHERE id=?`, attemptNo, occurrenceID); err != nil {
			t.Fatal(err)
		}
	}

	waveTime := time.Date(2098, 8, 27, 18, 0, 0, 123456000, time.UTC)
	otherWaveTime := waveTime.Add(time.Microsecond)
	zeroWaveTime := waveTime.Add(2 * time.Microsecond)
	prefix := fmt.Sprintf("wave-%d", time.Now().UnixNano())
	scheduleA := insertSchedule(prefix+"-A", "cif_opening_report")
	scheduleB := insertSchedule(prefix+"-B", "journal_transaction_report")
	scheduleC := insertSchedule(prefix+"-C", "loan_detail")
	scheduleD := insertSchedule(prefix+"-D", "saving_detail")
	scheduleE := insertSchedule(prefix+"-E", "time_deposit_detail")
	occurrenceA := insertOccurrence(scheduleA, waveTime, "cif_opening_report", "resolved")
	occurrenceB := insertOccurrence(scheduleB, waveTime, "journal_transaction_report", "unresolved")
	_ = insertOccurrence(scheduleC, waveTime, "loan_detail", "resolved")
	occurrenceD := insertOccurrence(scheduleD, otherWaveTime, "saving_detail", "discarded")
	_ = insertOccurrence(scheduleE, zeroWaveTime, "time_deposit_detail", "unresolved")

	activityBase := time.Date(2099, 1, 1, 8, 0, 0, 0, time.UTC)
	a1 := insertRun("cif_opening_report", "failed", "scheduler", activityBase.Add(3*time.Second))
	b1 := insertRun("journal_transaction_report", "succeeded", "scheduler", activityBase.Add(time.Minute+18*time.Second))
	a2 := insertRun("cif_opening_report", "failed", "scheduler", activityBase.Add(2*time.Minute+3*time.Second))
	a3Time := activityBase.Add(4 * time.Hour)
	a3 := insertRun("cif_opening_report", "succeeded", "scheduler", a3Time)
	d1 := insertRun("saving_detail", "succeeded", "scheduler", activityBase.Add(2*time.Minute+4*time.Second))
	linkAttempt(occurrenceA, 1, a1, activityBase.Add(3*time.Second))
	linkAttempt(occurrenceA, 2, a2, activityBase.Add(2*time.Minute+3*time.Second))
	linkAttempt(occurrenceA, 3, a3, a3Time)
	linkAttempt(occurrenceB, 1, b1, activityBase.Add(time.Minute+18*time.Second))
	linkAttempt(occurrenceD, 1, d1, activityBase.Add(2*time.Minute+4*time.Second))

	standalone := insertRun("cif_opening_report", "succeeded", "direct", activityBase.Add(3*time.Hour))
	parentParameters, _ := ingestionrun.NewRunAllRange(from, from)
	parentResult, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,created_at,finished_at)
		VALUES ('run_all_parent','completed',?,?,?,?,?,?,?,?)`, parentParameters.Kind, parentParameters.Version, parentParameters.JSON,
		parentParameters.Checksum[:], "direct", prefix+"-parent", activityBase.Add(2*time.Hour), activityBase.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parentID, _ := parentResult.LastInsertId()
	runIDs = append(runIDs, uint64(parentID))
	legacyScheduler := insertRun("cif_opening_report", "failed", "scheduler", activityBase.Add(time.Hour))

	repository := NewRepository(db)
	service, _ := NewService(repository)
	entities, _, err := repository.ListRunEntities(ctx, RunFilter{}, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.runListItems(ctx, entities)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 5 || items[0].SchedulerWave == nil || !items[0].SchedulerWave.ScheduledFor.Equal(waveTime) ||
		items[1].ID != standalone || items[2].ID != uint64(parentID) || items[3].ID != legacyScheduler ||
		items[4].SchedulerWave == nil || !items[4].SchedulerWave.ScheduledFor.Equal(otherWaveTime) {
		t.Fatalf("hydrated mixed order=%+v", items)
	}
	wave := items[0].SchedulerWave
	if wave.Total != 3 || wave.Resolved != 2 || wave.Unresolved != 1 || wave.Attempts != 4 || wave.ActivityAt != formatTime(a3Time) {
		t.Fatalf("wave summary=%+v", wave)
	}

	failedPage, err := service.ListRuns(ctx, RunFilter{Status: "failed"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	failedWave := findSchedulerWave(failedPage.Rows, waveTime)
	if failedWave == nil || failedWave.Total != 3 || failedWave.Attempts != 4 || failedWave.ActivityAt != formatTime(a3Time) {
		t.Fatalf("failed membership changed full-wave data: %+v", failedWave)
	}
	jobPage, err := service.ListRuns(ctx, RunFilter{Job: "journal_transaction_report"}, 1)
	if err != nil || findSchedulerWave(jobPage.Rows, waveTime) == nil {
		t.Fatalf("job membership wave missing: error=%v", err)
	}
	if findSchedulerWave(jobPage.Rows, waveTime).Attempts != 4 {
		t.Fatalf("job membership filtered wave attempts: %+v", findSchedulerWave(jobPage.Rows, waveTime))
	}
	if findSchedulerWave(failedPage.Rows, zeroWaveTime) != nil {
		t.Fatal("all-zero-attempt wave appeared in run history")
	}

	detail, err := service.SchedulerWave(ctx, waveTime)
	if err != nil || len(detail.Occurrences) != 3 || len(detail.Occurrences[0].Attempts) != 3 || len(detail.Occurrences[1].Attempts) != 1 || len(detail.Occurrences[2].Attempts) != 0 ||
		detail.Wave.Total != 3 || detail.Wave.Resolved != 2 || detail.Wave.Unresolved != 1 || detail.Wave.Attempts != 4 || detail.Wave.ActivityAt != formatTime(a3Time) {
		t.Fatalf("wave detail=%+v error=%v", detail, err)
	}
	if detail.Occurrences[0].Attempts[0].RunID != a1 || detail.Occurrences[0].Attempts[1].RunID != a2 || detail.Occurrences[0].Attempts[2].RunID != a3 {
		t.Fatalf("attempt order=%+v", detail.Occurrences[0].Attempts)
	}
	if _, err := service.SchedulerWave(ctx, zeroWaveTime); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("all-zero wave detail error=%v", err)
	}

	flat, err := service.ListRuns(ctx, RunFilter{Trigger: "scheduler", Status: "failed"}, 1)
	if err != nil || findRunView(flat.Rows, a1) == nil || findRunView(flat.Rows, a2) == nil || findRunView(flat.Rows, legacyScheduler) == nil {
		t.Fatalf("flat scheduler failures rows=%d error=%v", len(flat.Rows), err)
	}
	if findSchedulerWave(flat.Rows, waveTime) != nil {
		t.Fatal("explicit scheduler troubleshooting remained grouped")
	}
}

func findRunView(rows []RunListItem, id uint64) *RunView {
	for index := range rows {
		if rows[index].ID == id {
			return &rows[index].RunView
		}
	}
	return nil
}

func findSchedulerWave(rows []RunListItem, scheduledFor time.Time) *SchedulerWaveView {
	for _, row := range rows {
		if row.SchedulerWave != nil && row.SchedulerWave.ScheduledFor.Equal(scheduledFor) {
			return row.SchedulerWave
		}
	}
	return nil
}
