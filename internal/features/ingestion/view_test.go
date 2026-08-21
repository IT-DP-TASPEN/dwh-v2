package ingestion

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

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
