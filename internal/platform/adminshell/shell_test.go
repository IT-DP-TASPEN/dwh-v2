package adminshell

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

const (
	permissionDashboard = "sample.view"
	permissionUsers     = "users.view"
	permissionRoles     = "roles.view"
)

func testPermissions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: permissionDashboard, Name: "Dashboard", Group: "General"},
		{Key: permissionUsers, Name: "Users", Group: "Management"},
		{Key: permissionRoles, Name: "Roles", Group: "Management"},
	}
}

func TestRequirePermissionWithoutPrincipalIsInternalError(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := navigation.NewRegistry(nil, testPermissions())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(renderer, registry, "Go Admin", render.NewErrorResponder(renderer, "Go Admin", logger))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.RequirePermission(permissionDashboard)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler must not run")
	})).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", response.Code)
	}
}

func TestAdminShellRendersFilteredNestedNavigation(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := navigation.NewRegistry([]navigation.Group{{Key: "management", Label: "Management", Items: []navigation.Item{
		{Key: "access", Label: "Access Control", Children: []navigation.Item{
			{Key: "roles", Label: "Roles", Path: "/roles", Permission: permissionRoles, Match: navigation.MatchPrefix},
			{Key: "users", Label: "Users", Path: "/users", Permission: permissionUsers, Match: navigation.MatchPrefix},
		}},
	}}}, testPermissions())
	if err != nil {
		t.Fatal(err)
	}
	principal := browserauth.Principal{Username: "viewer", Name: "Viewer", RoleSlug: access.UserRoleSlug, Permissions: access.NewPermissionSet([]string{permissionRoles})}
	data := PageData{
		Title:       "Roles",
		AppName:     "Go Admin",
		Principal:   principal,
		Navigation:  registry.Prepare("/roles/7", principal.Can),
		CurrentPath: "/roles/7",
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPage(response, http.StatusOK, "features/dashboard/index", data); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{"Management", "Access Control", "Roles", `aria-current="page"`, `aria-expanded="true"`, `id="nav-children-access"`, `data-navigation-key="access"`, `data-navigation-active="true"`, `data-navigation-manual-open="false"`, `method="post" action="/logout"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("response does not contain %q", expected)
		}
	}
	if strings.Contains(body, ">Users<") {
		t.Fatal("unauthorized navigation item rendered")
	}
	head, err := fs.ReadFile(webfiles.Files, "static/js/head-state-init.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sidebar-collapsed", "sidebar-disclosures"} {
		if !strings.Contains(string(head), `localStorage.getItem("`+key+`")`) {
			t.Fatalf("missing persisted %s state", key)
		}
	}
	if script, stylesheet := strings.Index(body, `/static/js/head-state-init.js`), strings.Index(body, `rel="stylesheet"`); script < 0 || stylesheet < 0 || script > stylesheet {
		t.Fatal("state script must run before stylesheet")
	}
	sidebar, err := fs.ReadFile(webfiles.Files, "static/js/sidebar-init.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := fs.ReadFile(webfiles.Files, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(head), `root.dataset.sidebarDisclosurePending = "true"`) || !strings.Contains(string(sidebar), `root.removeAttribute("data-sidebar-disclosure-pending")`) || !strings.Contains(string(css), "data-sidebar-disclosure-pending") {
		t.Fatal("missing pre-paint disclosure hiding/reveal")
	}

	data.Navigation = registry.Prepare("/", principal.Can)
	response = httptest.NewRecorder()
	if err := renderer.RenderPage(response, http.StatusOK, "features/dashboard/index", data); err != nil {
		t.Fatal(err)
	}
	inactiveBody := response.Body.String()
	for _, expected := range []string{`data-navigation-active="false"`, `aria-expanded="false"`, `style="display:none"`} {
		if !strings.Contains(inactiveBody, expected) {
			t.Errorf("inactive response does not contain %q", expected)
		}
	}
}

func TestFrontendStatePersistenceKeys(t *testing.T) {
	javascript, err := fs.ReadFile(webfiles.Files, "static/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(javascript)
	if !strings.Contains(source, "sidebar-collapsed") || !strings.Contains(source, "sidebar-disclosures") || !strings.Contains(source, "theme") {
		t.Fatal("expected desktop sidebar, disclosure, and theme persistence")
	}
	if strings.Contains(source, `setItem("mobile`) || strings.Contains(source, `getItem("mobile`) {
		t.Fatal("mobile drawer state must not be persisted")
	}
}
