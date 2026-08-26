package auditlogs

import (
	"context"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type fakeStore struct {
	action     string
	limit      int
	offset     int
	countCalls int
	listCalls  int
	records    []Record
	found      Record
	findErr    error
}

func TestReportingMetadataRendersHumanReadableParameters(t *testing.T) {
	metadata := audit.ReportExecutionMetadata{
		ReportIdentityMetadata: audit.ReportIdentityMetadata{ReportTemplateID: 7, ReportName: "Daily balances", ReportRevision: 3, DatasourceID: 4, DatasourceName: "Core banking"},
		ExecutionMode:          "interactive", Outcome: "succeeded", ExecutionDuration: 42,
		Parameters: audit.ReportParametersMetadata{Complete: true, OriginalCount: 3, IncludedCount: 3, Items: []audit.ReportParameterMetadata{
			{Key: "branch", Label: "Branch", Type: "single_option", Values: []audit.ReportParameterValueMetadata{{Value: "001", Label: "KC Jakarta"}}, OriginalCount: 1, IncludedCount: 1},
			{Key: "products", Label: "Products", Type: "multiple_option", Values: []audit.ReportParameterValueMetadata{{Value: "TAB001", Label: "Tabungan A"}, {Value: "TAB002", Label: "Tabungan B"}}, OriginalCount: 2, IncludedCount: 2},
			{Key: "optional", Label: "Optional", Type: "text", Unset: true, Values: []audit.ReportParameterValueMetadata{}},
		}},
	}
	rows, truncated := 2, false
	metadata.ReturnedRowCount, metadata.ResultTruncated = &rows, &truncated
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{ID: 9, Action: string(audit.ActionReportExecuted), Metadata: encoded, CreatedAt: time.Now().UTC()}
	view := record.Reporting()
	if view == nil || view.ReportTemplateID != 7 || view.ResultState != "Complete" || len(view.Parameters.Items) != 3 {
		t.Fatalf("view=%+v", view)
	}
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPartial(response, http.StatusOK, "features/auditlogs/show", "content", render.PageData{Data: record}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{"Daily balances", "Core banking", "KC Jakarta (001)", "Tabungan A (TAB001)", "Tabungan B (TAB002)", "Any / Not set", "Technical details", "dark:bg-slate-900"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("render missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, ">Metadata<") {
		t.Fatal("reporting event fell back to raw metadata view")
	}
	if legacy := (Record{Action: string(audit.ActionReportExecuted), Metadata: []byte(`{"outcome":"succeeded"}`)}).Reporting(); legacy != nil {
		t.Fatalf("legacy event did not retain raw fallback: %+v", legacy)
	}
}

func (store *fakeStore) Count(_ context.Context, action string) (int64, error) {
	store.action, store.countCalls = action, store.countCalls+1
	return 101, nil
}

func (store *fakeStore) List(_ context.Context, action string, limit, offset int) ([]Record, error) {
	store.action, store.limit, store.offset, store.listCalls = action, limit, offset, store.listCalls+1
	return store.records, nil
}

func (store *fakeStore) Find(context.Context, uint64) (Record, error) {
	return store.found, store.findErr
}

func TestListUsesTwoQueriesAndFixedPageSize(t *testing.T) {
	store := &fakeStore{}
	page, err := NewService(store).List(context.Background(), "  user.created ", 2)
	if err != nil {
		t.Fatal(err)
	}
	if store.countCalls != 1 || store.listCalls != 1 || store.action != "user.created" || store.limit != 50 || store.offset != 50 {
		t.Fatalf("unexpected calls: %+v", store)
	}
	if page.Pagination.Total != 101 || page.Pagination.TotalPages != 3 {
		t.Fatalf("unexpected pagination: %+v", page.Pagination)
	}
}

func TestIdentityMetadataAndLinks(t *testing.T) {
	actorID, effectiveID := uint64(1), uint64(2)
	record := Record{ActorUserID: &actorID, ActorUsername: "admin", EffectiveUserID: &effectiveID, EffectiveUsername: "member", Metadata: []byte(`{"note":"<script>alert(1)</script>"}`)}
	if record.Actor().Label != "@admin" || record.Effective().Label != "@member" || !record.IsImpersonated() {
		t.Fatalf("unexpected identities: actor=%+v effective=%+v", record.Actor(), record.Effective())
	}
	parsed := template.Must(template.New("metadata").Parse(`{{.MetadataText}}`))
	var output strings.Builder
	if err := parsed.Execute(&output, record); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "<script>") || !strings.Contains(output.String(), "&lt;script&gt;") {
		t.Fatalf("metadata was not escaped: %s", output.String())
	}
	output.Reset()
	if err := parsed.Execute(&output, Record{Metadata: []byte(`<img src=x onerror=alert(1)>`)}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "<img") || !strings.Contains(output.String(), "&lt;img") || (Record{}).MetadataText() != "—" {
		t.Fatalf("invalid/null metadata was unsafe: %s", output.String())
	}
	if got := pageURL("user.created", 2); got != "/audit-logs?action=user.created&page=2" {
		t.Fatalf("filter not preserved: %s", got)
	}
	if label := (Record{Action: string(audit.ActionAuthRegistration)}).Actor().Label; label != "Public" {
		t.Fatalf("registration actor = %q", label)
	}
	if label := (Record{Action: string(audit.ActionAdminBootstrap)}).Actor().Label; label != "System" {
		t.Fatalf("bootstrap actor = %q", label)
	}
}

type fakeAuthentication struct{ principal browserauth.Principal }

func (*fakeAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*fakeAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (user.User, error) {
	return user.User{}, nil
}
func (service *fakeAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return service.principal, nil
}
func (*fakeAuthentication) Logout(context.Context, [32]byte) error { return nil }

func TestRoutesRequireAuditPermissionAndRemainReadOnly(t *testing.T) {
	for _, test := range []struct {
		name       string
		principal  browserauth.Principal
		path       string
		method     string
		wantStatus int
		wantBanner bool
	}{
		{name: "permissionless", principal: browserauth.Principal{RoleSlug: access.UserRoleSlug}, path: "/audit-logs", method: http.MethodGet, wantStatus: http.StatusForbidden},
		{name: "impersonated permissionless", principal: browserauth.Principal{RoleSlug: access.UserRoleSlug, IsImpersonating: true, Actor: browserauth.Identity{UserID: 7, Username: "admin", RoleSlug: access.AdminRoleSlug}}, path: "/audit-logs", method: http.MethodGet, wantStatus: http.StatusForbidden, wantBanner: true},
		{name: "explicit permission", principal: browserauth.Principal{RoleSlug: access.UserRoleSlug, Permissions: access.NewPermissionSet([]string{PermissionView})}, path: "/audit-logs", method: http.MethodGet, wantStatus: http.StatusOK},
		{name: "administrator bypass", principal: browserauth.Principal{RoleSlug: access.AdminRoleSlug}, path: "/audit-logs", method: http.MethodGet, wantStatus: http.StatusOK},
		{name: "malformed detail", principal: browserauth.Principal{RoleSlug: access.AdminRoleSlug}, path: "/audit-logs/nope", method: http.MethodGet, wantStatus: http.StatusNotFound},
		{name: "no mutation", principal: browserauth.Principal{RoleSlug: access.AdminRoleSlug}, path: "/audit-logs", method: http.MethodPost, wantStatus: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.principal.UserID, test.principal.Username = 1, "viewer"
			if test.principal.Actor.UserID == 0 {
				test.principal.Actor = browserauth.Identity{UserID: 1, Username: "viewer", RoleSlug: test.principal.RoleSlug}
			}
			router, token := auditRouter(t, test.principal)
			request := httptest.NewRequest(test.method, test.path, nil)
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if test.wantBanner && (!strings.Contains(response.Body.String(), "is impersonating") || !strings.Contains(response.Body.String(), "Return to Admin")) {
				t.Fatalf("impersonation banner missing: %q", response.Body.String())
			}
		})
	}
}

func auditRouter(t *testing.T, principal browserauth.Principal) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	registry, err := navigation.NewRegistry([]navigation.Group{{Key: "system", Label: "System", Items: []navigation.Item{Navigation()}}}, PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	shell := adminshell.New(renderer, registry, "Test", errors)
	cookies := browserauth.NewCookieManager("session", false, time.Hour)
	authentication := browserauth.NewHTTP(&fakeAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(shell, NewService(&fakeStore{})).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
