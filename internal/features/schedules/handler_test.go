package schedules

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	coreuser "github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type scheduleFakeAuthentication struct{ principal browserauth.Principal }

func (*scheduleFakeAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*scheduleFakeAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (coreuser.User, error) {
	return coreuser.User{}, nil
}
func (service *scheduleFakeAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return service.principal, nil
}
func (*scheduleFakeAuthentication) Logout(context.Context, [32]byte) error { return nil }

func TestBulkScheduleFormAndPermission(t *testing.T) {
	authorized := browserauth.Principal{UserID: 7, Actor: browserauth.Identity{UserID: 7},
		Permissions: access.NewPermissionSet([]string{PermissionView, PermissionCreate})}
	router, token := scheduleHandlerRouter(t, authorized)
	request := httptest.NewRequest(http.MethodGet, "/schedules/bulk/new", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Count(body, `name="job_keys"`) != core.CanonicalJobCount ||
		!strings.Contains(body, "Select all") || !strings.Contains(body, "Asia/Jakarta") || !strings.Contains(body, `action="/schedules/bulk"`) {
		t.Fatalf("status=%d job_inputs=%d body=%q", response.Code, strings.Count(body, `name="job_keys"`), body)
	}

	router, token = scheduleHandlerRouter(t, browserauth.Principal{UserID: 8, Actor: browserauth.Identity{UserID: 8},
		Permissions: access.NewPermissionSet([]string{PermissionView})})
	request = httptest.NewRequest(http.MethodGet, "/schedules/bulk/new", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("permissionless status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestBulkScheduleValidationAndResultRendering(t *testing.T) {
	principal := browserauth.Principal{UserID: 7, Actor: browserauth.Identity{UserID: 7},
		Permissions: access.NewPermissionSet([]string{PermissionView, PermissionCreate})}
	router, token := scheduleHandlerRouter(t, principal)
	form := url.Values{"cron_expression": {"0 1 * * *"}, "timezone": {"Asia/Jakarta"}}
	request := httptest.NewRequest(http.MethodPost, "/schedules/bulk", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Select at least one job") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}

	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	data := BulkFormData{Result: &BulkResultData{Selected: 3, Created: 1, Skipped: 2, CronExpression: "0 1 * * *", Timezone: "Asia/Jakarta",
		CreatedSchedules: []BulkSchedule{{ID: 11, JobName: "CIF Detail", Enabled: true}},
		SkippedJobs:      []BulkSkippedJob{{JobName: "Saving Detail", Existing: []BulkSchedule{{ID: 12, JobName: "Saving Detail"}}}}}}
	response = httptest.NewRecorder()
	if err := renderer.RenderPage(response, http.StatusOK, "features/schedules/bulk", adminshell.PageData{Title: "Bulk result", AppName: "Test", Principal: principal, Data: data}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, expected := range []string{"Bulk schedule result", "Selected", "Created", "Already existed", "/schedules/11", "/schedules/12", "disabled"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("result missing %q: %s", expected, body)
		}
	}
}

func TestScheduleIndexBulkActionFollowsCreatePermission(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		canCreate bool
		wantLink  bool
	}{{true, true}, {false, false}} {
		response := httptest.NewRecorder()
		data := ListData{CanCreate: test.canCreate}
		if err := renderer.RenderPage(response, http.StatusOK, "features/schedules/index", adminshell.PageData{Title: "Schedules", AppName: "Test", Data: data}); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(response.Body.String(), `/schedules/bulk/new`); got != test.wantLink {
			t.Fatalf("can_create=%v bulk_link=%v", test.canCreate, got)
		}
	}
}

func TestScheduleIndexBulkSelectionPermissionMatrix(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Schedule{{ID: 11, Name: "Current"}, {ID: 12, Name: "Archived", Archived: true}}
	for _, test := range []struct {
		name                           string
		canEnableDisable, canArchive   bool
		wantCheckboxes, wantSelectAll  int
		wantEnableDisable, wantArchive bool
	}{
		{name: "view only"},
		{name: "enable disable", canEnableDisable: true, wantCheckboxes: 1, wantSelectAll: 1, wantEnableDisable: true},
		{name: "archive", canArchive: true, wantCheckboxes: 1, wantSelectAll: 1, wantArchive: true},
		{name: "all", canEnableDisable: true, canArchive: true, wantCheckboxes: 1, wantSelectAll: 1, wantEnableDisable: true, wantArchive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			canSelect := test.canEnableDisable || test.canArchive
			selectable := 0
			if canSelect {
				selectable = 1
			}
			data := ListData{Rows: rows, CanEnableDisable: test.canEnableDisable, CanArchive: test.canArchive, CanSelect: canSelect, SelectableCount: selectable}
			if err := renderer.RenderPage(response, http.StatusOK, "features/schedules/index", adminshell.PageData{Title: "Schedules", AppName: "Test", Data: data}); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if got := strings.Count(body, "data-schedule-checkbox"); got != test.wantCheckboxes {
				t.Fatalf("checkboxes=%d want=%d", got, test.wantCheckboxes)
			}
			if got := strings.Count(body, `aria-label="Select all schedules on this page"`); got != test.wantSelectAll {
				t.Fatalf("select_all=%d want=%d", got, test.wantSelectAll)
			}
			if strings.Contains(body, `confirmAction('enable')`) != test.wantEnableDisable || strings.Contains(body, `confirmAction('disable')`) != test.wantEnableDisable {
				t.Fatalf("enable_disable actions mismatch: %s", body)
			}
			if strings.Contains(body, `confirmAction('archive')`) != test.wantArchive {
				t.Fatalf("archive action mismatch: %s", body)
			}
			if strings.Contains(body, "Select schedule: Archived") {
				t.Fatal("archived schedule was selectable")
			}
		})
	}
}

func TestBulkScheduleStatePermissionAndRequestValidation(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
		form        url.Values
		want        int
	}{
		{"view only", []string{PermissionView}, url.Values{"action": {"enable"}, "confirmed": {"yes"}, "schedule_ids": {"1"}}, http.StatusForbidden},
		{"enable cannot archive", []string{PermissionView, PermissionEnableDisable}, url.Values{"action": {"archive"}, "confirmed": {"yes"}, "schedule_ids": {"1"}}, http.StatusForbidden},
		{"archive cannot enable", []string{PermissionView, PermissionArchive}, url.Values{"action": {"enable"}, "confirmed": {"yes"}, "schedule_ids": {"1"}}, http.StatusForbidden},
		{"unknown action", []string{PermissionView, PermissionEnableDisable}, url.Values{"action": {"restore"}, "confirmed": {"yes"}, "schedule_ids": {"1"}}, http.StatusUnprocessableEntity},
		{"missing confirmation", []string{PermissionView, PermissionEnableDisable}, url.Values{"action": {"enable"}, "schedule_ids": {"1"}}, http.StatusUnprocessableEntity},
		{"empty selection", []string{PermissionView, PermissionEnableDisable}, url.Values{"action": {"enable"}, "confirmed": {"yes"}}, http.StatusUnprocessableEntity},
		{"invalid selection", []string{PermissionView, PermissionEnableDisable}, url.Values{"action": {"enable"}, "confirmed": {"yes"}, "schedule_ids": {"0"}}, http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			principal := browserauth.Principal{UserID: 7, Actor: browserauth.Identity{UserID: 7}, Permissions: access.NewPermissionSet(test.permissions)}
			router, token := scheduleHandlerRouter(t, principal)
			request := httptest.NewRequest(http.MethodPost, "/schedules/bulk-action", strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func scheduleHandlerRouter(t *testing.T, principal browserauth.Principal) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	registry, err := navigation.NewRegistry(nil, PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	cookies := browserauth.NewCookieManager("session", false, 24*time.Hour)
	authentication := browserauth.NewHTTP(&scheduleFakeAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	catalog, err := core.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(adminshell.New(renderer, registry, "Test", errors), &Service{catalog: catalog}).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
