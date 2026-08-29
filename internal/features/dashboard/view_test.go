package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestDashboardRendersOperationalSectionsWithoutStarterContent(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	data := Data{
		Summary:          Summary{ActiveIngestion: 2, FailedIngestion24h: 3, SchedulerUnresolved: 1, ExportQueued: 1, ExportRunning: 1, ExportFailed: 1},
		Attention:        []ingestionfeature.AttentionItem{{Name: "Run All #41", Detail: "Run #41", Time: "29 Aug 2026 10:00:00 UTC", URL: "/runs/41", Status: ingestionfeature.PresentStatus("failed"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 36, Complete: 36, Failed: 2}}},
		Active:           []ingestionfeature.OperationalItem{{RunListItem: ingestionfeature.RunListItem{RunView: ingestionfeature.RunView{ID: 42, Kind: "run_all_parent", CreatedAt: "29 Aug 2026 11:00:00 UTC", Status: ingestionfeature.PresentStatus("running"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 36, Complete: 30, Running: 2}}}, InterestingChildren: []ingestionfeature.RunView{{ID: 43, JobName: "Long running child", Status: ingestionfeature.PresentStatus("running")}}}},
		Recent:           []ingestionfeature.RunListItem{{RunView: ingestionfeature.RunView{ID: 40, Kind: "job", JobName: "Journal Transaction Report", Trigger: "direct", CreatedAt: "29 Aug 2026 09:00:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")}}},
		CanViewSchedules: true, CanViewExports: true,
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/dashboard/index", "content", render.PageData{Data: data}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{"Operational Dashboard", "Active ingestion", "Failed ingestion · 24h", "Needs attention", "Reporting export health", "Recent activity", "Run All #41", "2 failed", `href="/runs/43"`, `href="/exports"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("dashboard missing %q: %s", expected, body)
		}
	}
	for _, removed := range []string{"Current role", "Authentication", "Starter status", "Foundation ready", "freshness", "chart"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(removed)) {
			t.Errorf("dashboard retained %q", removed)
		}
	}
}

func TestDashboardHealthyAndAggregateOnlyState(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/dashboard/index", "content", render.PageData{Data: Data{}}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{"No items currently require attention.", "No ingestion work is currently active.", "No recent operational activity."} {
		if !strings.Contains(body, expected) {
			t.Errorf("healthy dashboard missing %q", expected)
		}
	}
	for _, forbidden := range []string{`href="/schedules"`, `href="/exports"`, "All data is fresh"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("aggregate-only dashboard leaked %q", forbidden)
		}
	}
}
