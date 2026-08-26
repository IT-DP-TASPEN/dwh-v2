package ingestion

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
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
