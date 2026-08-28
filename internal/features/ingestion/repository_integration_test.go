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
	if err != nil || len(children.Rows) != 36 {
		t.Fatalf("fragment children=%d error=%v", len(children.Rows), err)
	}
	for index, child := range children.Rows {
		if child.ParentRunID == nil || *child.ParentRunID != parentID || child.ChildPosition != uint16(index+1) {
			t.Fatalf("fragment child %d=%+v", index, child)
		}
	}
	emptyChildren, err := service.RunAllChildren(ctx, uint64(emptyParentID))
	if err != nil || len(emptyChildren.Rows) != 0 {
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

func findRunView(rows []RunView, id uint64) *RunView {
	for index := range rows {
		if rows[index].ID == id {
			return &rows[index]
		}
	}
	return nil
}
