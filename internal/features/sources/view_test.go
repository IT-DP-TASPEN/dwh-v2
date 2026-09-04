package sources

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestSourceAuthProfileSelectAutoSavesWithoutButton(t *testing.T) {
	catalog, err := core.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	data := ListData{Rows: []Source{{Job: catalog.Jobs()[0], Category: "Fixed", Enabled: true, ConfigurationRequired: true}}, AuthProfiles: []AuthProfileOption{{ID: 7, Name: "Operations", Status: "active"}}, CanManage: true}
	body := renderSources(t, data)
	for _, want := range []string{`hx-post="/sources/`, `hx-target="closest tr"`, `hx-swap="outerHTML"`, `hx-on::response-error="this.reset()`, `hx-on::send-error="this.reset()`, `data-source-auth-error`, `onchange="this.form.requestSubmit()"`, `border-slate-300`, `dark:border-slate-700`, `rounded-xl border border-slate-200`, `dark:border-slate-800`, "Configuration required"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sources page missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `>Save</button>`) {
		t.Fatalf("per-row Save button still rendered: %s", body)
	}
}

func TestSourceMutationFailureFragmentRestoresPersistedSelection(t *testing.T) {
	catalog, _ := core.NewCatalog()
	id := uint64(7)
	data := SourceRowData{Source: Source{Job: catalog.Jobs()[0], Category: "Fixed", Enabled: true, AuthProfileID: &id, AuthProfileName: "Operations", AuthProfileStatus: "active"}, AuthProfiles: []AuthProfileOption{{ID: id, Name: "Operations", Status: "active"}, {ID: 8, Name: "Rejected", Status: "active"}}, CanManage: true, Error: "assignment rejected; persisted selection restored."}
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/sources/index", "source-row", data); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if !strings.Contains(body, `value="7"  selected`) && !strings.Contains(body, `value="7" selected`) {
		t.Fatalf("persisted selection not restored: %s", body)
	}
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "persisted selection restored") {
		t.Fatalf("failure not rendered inline: %s", body)
	}
}

func renderSources(t *testing.T, data ListData) string {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPage(response, http.StatusOK, "features/sources/index", adminshell.PageData{Title: "Sources", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	return response.Body.String()
}
