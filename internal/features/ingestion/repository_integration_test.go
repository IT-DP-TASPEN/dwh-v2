//go:build integration

package ingestion

import (
	"context"
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
