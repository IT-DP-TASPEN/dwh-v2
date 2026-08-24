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
	if response.Code != http.StatusOK || strings.Count(body, `name="job_keys"`) != 36 ||
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
