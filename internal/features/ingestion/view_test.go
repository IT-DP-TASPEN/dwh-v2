package ingestion

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestMaintenanceParameterFieldsContainOnlyRequestedDates(t *testing.T) {
	from, _ := core.ParseCalendarDate("2026-08-23")
	to, _ := core.ParseCalendarDate("2026-08-24")
	parameters, err := ingestionrun.NewMaintenanceSeriesExecution("cbr_customer", from, to)
	if err != nil {
		t.Fatal(err)
	}
	fields := parameterFields(parameters)
	if len(fields) != 3 || fields[0] != (ParameterField{"From", "2026-08-23"}) || fields[1] != (ParameterField{"To", "2026-08-24"}) || fields[2] != (ParameterField{"Dates", "2"}) {
		t.Fatalf("fields=%+v", fields)
	}
}

func TestRunViewUsesCanonicalTerminalLifecycle(t *testing.T) {
	catalog, err := core.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	service := Service{catalog: catalog}
	for _, status := range []ingestionrun.Status{
		ingestionrun.StatusPlanned, ingestionrun.StatusQueued, ingestionrun.StatusRunning, ingestionrun.StatusSucceeded,
		ingestionrun.StatusFailed, ingestionrun.StatusSkipped, ingestionrun.StatusCancelled, ingestionrun.StatusAbandoned,
		ingestionrun.StatusCompleted, ingestionrun.StatusCompletedWithSkips,
	} {
		if got, want := service.view(runRow{Status: string(status)}).Terminal, ingestionrun.IsTerminal(status); got != want {
			t.Errorf("status %q terminal=%t want %t", status, got, want)
		}
	}
}

func TestRunAllChildStatusesUseRunsListBadges(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	renderPartial := func(page, name string, data any) string {
		t.Helper()
		response := httptest.NewRecorder()
		if err := renderer.RenderPartial(response, http.StatusOK, page, name, render.PageData{Data: data}); err != nil {
			t.Fatal(err)
		}
		return response.Body.String()
	}

	statuses := []string{"planned", "queued", "running", "succeeded", "failed", "skipped", "cancelled", "abandoned", "completed", "completed_with_skips", "future_status"}
	for _, key := range statuses {
		t.Run(key, func(t *testing.T) {
			status := PresentStatus(key)
			list := renderPartial("features/ingestion/runs", "runs-table", RunPage{Rows: listItems(RunView{ID: 1, Status: status})})
			child := RunView{ID: 2, ChildPosition: 1, JobName: "Child", Status: status}
			if key == "skipped" {
				child.SkipReason = "not selected"
			}
			children := renderPartial("features/ingestion/show", "run-children-content", RunDetail{Children: []RunView{child}})
			expected := fmt.Sprintf(`<span aria-label="Status: %s" class="rounded-full px-2 py-1 text-xs %s">%s</span>`, status.Label, status.Class, status.Label)
			for surface, body := range map[string]string{"runs list": list, "Run All children": children} {
				if !strings.Contains(body, expected) {
					t.Errorf("%s missing shared %s badge: %s", surface, key, body)
				}
			}
			if strings.Contains(children, fmt.Sprintf(`<td class="px-4 py-3">%s</td>`, status.Label)) {
				t.Errorf("child %s status rendered as plain text: %s", key, children)
			}
			if key == "future_status" && status.Class != "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300" {
				t.Errorf("unknown status class=%q", status.Class)
			}
		})
	}
}

func TestRunsListHierarchyFiltersAndParentSummaries(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	parentID := uint64(251)
	page := RunPage{
		Kinds: []RunKindOption{{"job", "Job"}, {"run_all_parent", "Run All parent"}, {"run_all_child", "Run All child"}},
		Rows: listItems(
			RunView{ID: 249, Kind: "run_all_parent", KindLabel: "run all parent", Status: PresentStatus("completed"), Terminal: true, RunAllSummary: &RunAllSummary{}},
			RunView{ID: parentID, Kind: "run_all_parent", KindLabel: "run all parent", Status: PresentStatus("running"), RunAllSummary: &RunAllSummary{Total: 36, Complete: 32, Failed: 2, Running: 1}},
			RunView{ID: 252, Kind: "run_all_parent", KindLabel: "run all parent", Status: PresentStatus("completed"), Terminal: true, RunAllSummary: &RunAllSummary{Total: 36, Complete: 36}},
			RunView{ID: 253, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &parentID, JobName: "Journal Transaction Report", Status: PresentStatus("failed")},
		),
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/runs", "content", render.PageData{Data: page}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`value="run_all_parent"`,
		`Run All parent`,
		`aria-label="Expand Run All #251 children"`,
		`aria-controls="run-all-children-251"`,
		`hx-get="/runs/251/children"`,
		`@htmx:before-request=`,
		`@htmx:after-swap=`,
		`@htmx:after-request=`,
		`data-runs-accordion`,
		`:data-open="open.toString()"`,
		`every 5s [this.dataset.loaded === 'true' && this.closest('[data-runs-accordion]').dataset.open === 'true']`,
		`No children`,
		`32 / 36 complete`,
		`2 failed`,
		`1 running`,
		`36 / 36 complete`,
		`href="/runs/253">#253</a>`,
		`<span aria-hidden="true">↳</span> Journal Transaction Report`,
		`href="/runs/251" class="block text-xs text-slate-500 hover:underline">Run All #251</a>`,
		`aria-label="Status: Failed"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("runs render missing %q: %s", expected, body)
		}
	}
	for _, removed := range []string{`include_run_all_children`, `Include Run All children in All`, `hx-trigger="click once"`} {
		if strings.Contains(body, removed) {
			t.Errorf("runs render retained %q: %s", removed, body)
		}
	}

	response = httptest.NewRecorder()
	children := RunChildren{Parent: RunView{ID: parentID, Status: PresentStatus("running"), RunAllSummary: &RunAllSummary{Total: 2, Complete: 2}}, Rows: []RunView{
		{ID: 252, ChildPosition: 1, JobName: "CIF Opening Report", Status: PresentStatus("succeeded"), ProgressSucceeded: 3, ProgressTotal: 3},
		{ID: 253, ChildPosition: 2, JobName: "Journal Transaction Report", Status: PresentStatus("failed")},
	}}
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/runs", "run-all-children", render.PageData{Data: children}); err != nil {
		t.Fatal(err)
	}
	fragment := response.Body.String()
	for _, expected := range []string{`aria-label="Run All #251 children"`, `data-loaded="true"`, `every 5s`, `data-child-position="1"`, `href="/runs/252">#252</a>`, `CIF Opening Report`, `aria-label="Status: Failed"`, `id="run-all-summary-251" hx-swap-oob="outerHTML"`, `2 / 2 complete`, `id="run-all-status-251" hx-swap-oob="outerHTML"`} {
		if !strings.Contains(fragment, expected) {
			t.Errorf("child fragment missing %q: %s", expected, fragment)
		}
	}
	children.Parent.Terminal = true
	children.Parent.Status = PresentStatus("completed")
	response = httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/runs", "run-all-children", render.PageData{Data: children}); err != nil {
		t.Fatal(err)
	}
	if terminal := response.Body.String(); strings.Contains(terminal, "every 5s") || !strings.Contains(terminal, `aria-label="Status: Completed"`) {
		t.Fatalf("terminal Run All fragment polling/status=%s", terminal)
	}
}

func TestSchedulerWaveRendersLazySummaryAndFullAttempts(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	scheduledFor := time.Date(2026, 8, 27, 18, 0, 0, 123000000, time.UTC)
	wave := schedulerWaveView(scheduledFor, time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC), schedulerWaveSummaryRow{
		ScheduledFor: scheduledFor, Total: 2, Resolved: 1, Unresolved: 1, Attempts: 3,
	})
	page := RunPage{Rows: []RunListItem{
		{RunView: RunView{ID: 300, Kind: "job", JobName: "Standalone", Status: PresentStatus("succeeded")}},
		{SchedulerWave: wave},
		{RunView: RunView{ID: 299, Kind: "run_all_parent", Status: PresentStatus("completed"), Terminal: true, RunAllSummary: &RunAllSummary{}}},
	}, Pagination: pagination.New(1, RunPageSize, 3)}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/runs", "runs-table", render.PageData{Data: page}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"Activity", "Scheduled · 27 Aug 2026 18:00:00 UTC", "2 occurrences · 1 resolved · 1 unresolved · 3 attempts",
		`aria-label="Expand Scheduled 27 Aug 2026 18:00:00 UTC attempts"`, `hx-trigger="load-scheduler-wave, every 5s`,
		`scheduled_for=2026-08-27T18%3A00%3A00.123Z`, "Could not load scheduler attempts", "3 activities",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("scheduler wave render missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `hx-trigger="click once"`) {
		t.Fatal("scheduler wave load cannot retry")
	}
	if standalone, scheduled, runAll := strings.Index(body, ">#300</a>"), strings.Index(body, "Scheduled ·"), strings.Index(body, ">#299</a>"); !(standalone < scheduled && scheduled < runAll) {
		t.Fatalf("mixed entity order changed: standalone=%d scheduled=%d runAll=%d", standalone, scheduled, runAll)
	}

	detail := SchedulerWaveDetail{Wave: *wave, Occurrences: []SchedulerOccurrenceView{
		{ScheduleID: 10, OccurrenceID: 20, ScheduleName: "Schedule A", JobName: "CIF Detail", Status: presentOccurrenceStatus("resolved"), Attempts: []SchedulerAttemptView{
			{RunID: 238, AttemptNo: 1, JobName: "CIF Detail", Status: PresentStatus("failed"), CreatedAt: "27 Aug 2026 18:00:03 UTC"},
			{RunID: 269, AttemptNo: 2, JobName: "CIF Detail", Status: PresentStatus("succeeded"), CreatedAt: "28 Aug 2026 10:00:00 UTC"},
		}},
		{ScheduleID: 11, OccurrenceID: 21, ScheduleName: "Schedule B", JobName: "Loan Detail", Status: presentOccurrenceStatus("unresolved")},
	}}
	response = httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/runs", "scheduler-wave-attempts", render.PageData{Data: detail}); err != nil {
		t.Fatal(err)
	}
	fragment := response.Body.String()
	for _, expected := range []string{`data-loaded="true"`, `every 5s`, `href="/schedules/10"`, `data-scheduler-occurrence="20"`, `data-scheduler-attempt="1"`, `href="/runs/238"`, `href="/runs/269"`, "No attempts submitted", `id="scheduler-wave-summary-1787853600123000" hx-swap-oob="outerHTML"`} {
		if !strings.Contains(fragment, expected) {
			t.Errorf("scheduler wave fragment missing %q: %s", expected, fragment)
		}
	}
	if strings.Contains(fragment, `hx-swap-oob="outerHTML">28 Aug 2026 10:00:00 UTC`) || strings.Contains(fragment, `scheduler-wave-activity`) {
		t.Fatalf("scheduler fragment updated frozen Activity: %s", fragment)
	}
	detail.Wave.Unresolved, detail.Wave.Resolved, detail.Wave.Summary = 0, 2, "2 occurrences · 2 resolved · 3 attempts"
	response = httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/runs", "scheduler-wave-attempts", render.PageData{Data: detail}); err != nil {
		t.Fatal(err)
	}
	if terminal := response.Body.String(); strings.Contains(terminal, "every 5s") || !strings.Contains(terminal, "2 occurrences · 2 resolved · 3 attempts") {
		t.Fatalf("resolved Scheduler fragment polling/summary=%s", terminal)
	}
}

func listItems(views ...RunView) []RunListItem {
	items := make([]RunListItem, len(views))
	for index, view := range views {
		items[index].RunView = view
	}
	return items
}

func TestTechnicalDetailsRenderEscapedCopyableAndWithLegacyFallback(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{Run: RunView{ID: 7}, TechnicalErrors: []TechnicalEventView{{OccurredAt: "2026-08-21 20:38:29.820",
		Severity: "error", EventKind: "failure", Terminal: true, Class: "source", Step: "download_report", Operation: "download_report",
		ErrorMessage: "actual HTTP 500", Details: `{"response":"<script>alert(1)</script>"}`, BodyEncoding: "base64", Body: `<img src=x onerror=alert(1)>`}}}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/show", "run-status", render.PageData{Data: detail}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{"Technical Details", "actual HTTP 500", "Binary/base64", "Copy diagnostic", "&lt;script&gt;", "&lt;img"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("render missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "<img src=x") {
		t.Fatal("technical payload rendered as executable HTML")
	}

	response = httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/show", "run-status", render.PageData{Data: RunDetail{Run: RunView{ID: 8}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), "No detailed diagnostic was captured for this run.") {
		t.Fatal("legacy run fallback missing")
	}
}

func TestRunStatusPollingBoundaryAndActionEdges(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	renderTemplate := func(name string, detail RunDetail) string {
		t.Helper()
		response := httptest.NewRecorder()
		if err := renderer.RenderPartial(response, http.StatusOK, "features/ingestion/show", name, render.PageData{Data: detail}); err != nil {
			t.Fatal(err)
		}
		return response.Body.String()
	}

	detail := RunDetail{Run: RunView{ID: 9, ProgressTotal: 10, ProgressUnit: "items"}, Polling: true, CanCancel: true}
	page := renderTemplate("run-status", detail)
	liveStart := strings.Index(page, `id="run-live-state"`)
	detailsStart := strings.Index(page, `id="run-technical-details"`)
	if liveStart < 0 || detailsStart < liveStart {
		t.Fatalf("polling boundary missing: %s", page)
	}
	if live := page[liveStart:detailsStart]; strings.Contains(live, "<form") {
		t.Fatalf("editable form rendered inside polling target: %s", live)
	}
	for _, expected := range []string{`id="run-cancel-action"`, `action="/runs/9/cancel"`, `id="run-recover-action"`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("page missing %q: %s", expected, page)
		}
	}

	detail.CanRecover = true
	detail.SwapRecoverAction = actionCapabilityChanged("false", detail.CanRecover)
	transition := renderTemplate("run-status-poll", detail)
	for _, expected := range []string{
		`hx-get="/runs/9/status?can_cancel=true&amp;can_recover=true"`,
		`id="run-recover-action" hx-swap-oob="outerHTML"`,
		`action="/runs/9/recover-abandoned"`,
	} {
		if !strings.Contains(transition, expected) {
			t.Fatalf("transition response missing %q: %s", expected, transition)
		}
	}
	if strings.Contains(transition, `id="run-cancel-action"`) {
		t.Fatal("unchanged cancellation action was replaced")
	}

	detail.SwapRecoverAction = actionCapabilityChanged("true", detail.CanRecover)
	steady := renderTemplate("run-status-poll", detail)
	if strings.Contains(steady, `id="run-recover-action"`) {
		t.Fatal("recovery action OOB repeated after rendered state advanced")
	}

	detail.Polling, detail.CanCancel, detail.CanRecover = false, false, false
	detail.SwapCancelAction, detail.SwapRecoverAction = true, true
	terminal := renderTemplate("run-status-poll", detail)
	if strings.Contains(terminal, "hx-get=") {
		t.Fatalf("terminal response kept polling: %s", terminal)
	}
	for _, expected := range []string{
		`id="run-cancel-action" hx-swap-oob="outerHTML" hidden`,
		`id="run-recover-action" hx-swap-oob="outerHTML" hidden`,
	} {
		if !strings.Contains(terminal, expected) {
			t.Fatalf("terminal response missing %q: %s", expected, terminal)
		}
	}
	if strings.Contains(terminal, `action="/runs/9/cancel"`) || strings.Contains(terminal, `action="/runs/9/recover-abandoned"`) {
		t.Fatal("terminal response retained invalid action form")
	}
}

func TestActionCapabilityHintsOnlyControlRenderingEdges(t *testing.T) {
	for _, test := range []struct {
		rendered string
		current  bool
		changed  bool
	}{
		{"false", false, false},
		{"true", true, false},
		{"false", true, true},
		{"true", false, true},
		{"", false, true},
		{"tampered", true, true},
	} {
		if changed := actionCapabilityChanged(test.rendered, test.current); changed != test.changed {
			t.Errorf("rendered=%q current=%v changed=%v want=%v", test.rendered, test.current, changed, test.changed)
		}
	}
}
