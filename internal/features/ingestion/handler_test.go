package ingestion

import (
	"context"
	"database/sql"
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
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type childRouteService struct {
	runService
	calls     int
	id        uint64
	err       error
	waveCalls int
	waveTime  time.Time
}

func (service *childRouteService) RunAllChildren(_ context.Context, id uint64) (RunChildren, error) {
	service.calls++
	service.id = id
	if service.err != nil {
		return RunChildren{}, service.err
	}
	return RunChildren{ParentID: id, Rows: []RunView{{ID: 252, ChildPosition: 1, JobName: "CIF Opening Report", Status: PresentStatus("succeeded")}}}, nil
}

func (service *childRouteService) SchedulerWave(_ context.Context, scheduledFor time.Time) (SchedulerWaveDetail, error) {
	service.waveCalls++
	service.waveTime = scheduledFor
	if service.err != nil {
		return SchedulerWaveDetail{}, service.err
	}
	return SchedulerWaveDetail{ScheduledFor: formatTime(scheduledFor), Occurrences: []SchedulerOccurrenceView{{
		ScheduleID: 1, OccurrenceID: 2, ScheduleName: "Daily CIF", JobName: "CIF Opening Report", Status: presentOccurrenceStatus("resolved"),
		Attempts: []SchedulerAttemptView{{RunID: 3, AttemptNo: 1, JobName: "CIF Opening Report", Status: PresentStatus("succeeded"), CreatedAt: formatTime(scheduledFor)}},
	}}}, nil
}

type routeAuthentication struct{ principal browserauth.Principal }

func (*routeAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*routeAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (user.User, error) {
	return user.User{}, nil
}
func (authentication *routeAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return authentication.principal, nil
}
func (*routeAuthentication) Logout(context.Context, [32]byte) error { return nil }

func TestRunAllChildrenRouteRequiresViewPermission(t *testing.T) {
	for _, test := range []struct {
		name        string
		permissions []string
		wantStatus  int
		wantCalls   int
		serviceErr  error
	}{
		{name: "permissionless", wantStatus: http.StatusForbidden},
		{name: "viewer", permissions: []string{PermissionView}, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "unknown parent", permissions: []string{PermissionView}, wantStatus: http.StatusNotFound, wantCalls: 1, serviceErr: sql.ErrNoRows},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal := browserauth.Principal{UserID: 1, Username: "viewer", RoleSlug: access.UserRoleSlug, Permissions: access.NewPermissionSet(test.permissions),
				Actor: browserauth.Identity{UserID: 1, Username: "viewer", RoleSlug: access.UserRoleSlug}}
			service := &childRouteService{err: test.serviceErr}
			router, token := runChildrenRouter(t, principal, service)
			request := httptest.NewRequest(http.MethodGet, "/runs/251/children", nil)
			request.Header.Set("HX-Request", "true")
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || service.calls != test.wantCalls {
				t.Fatalf("status=%d calls=%d body=%q", response.Code, service.calls, response.Body.String())
			}
			if test.wantStatus == http.StatusOK && (service.id != 251 || !strings.Contains(response.Body.String(), `href="/runs/252">#252</a>`)) {
				t.Fatalf("id=%d body=%q", service.id, response.Body.String())
			}
		})
	}
}

func TestSchedulerWaveRouteAuthorizationAndExactTimestamp(t *testing.T) {
	viewer := browserauth.Principal{UserID: 1, Username: "viewer", RoleSlug: access.UserRoleSlug, Permissions: access.NewPermissionSet([]string{PermissionView}),
		Actor: browserauth.Identity{UserID: 1, Username: "viewer", RoleSlug: access.UserRoleSlug}}
	for _, test := range []struct {
		name       string
		principal  browserauth.Principal
		query      string
		serviceErr error
		wantStatus int
		wantCalls  int
	}{
		{name: "permissionless", principal: browserauth.Principal{UserID: 1, Username: "viewer", RoleSlug: access.UserRoleSlug, Actor: viewer.Actor}, query: "2026-08-27T18:00:00.123456Z", wantStatus: http.StatusForbidden},
		{name: "viewer", principal: viewer, query: "2026-08-28T01:00:00.123456+07:00", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "invalid", principal: viewer, query: "not-a-time", wantStatus: http.StatusBadRequest},
		{name: "sub-microsecond", principal: viewer, query: "2026-08-27T18:00:00.123456789Z", wantStatus: http.StatusBadRequest},
		{name: "missing wave", principal: viewer, query: "2026-08-27T18:00:00Z", serviceErr: sql.ErrNoRows, wantStatus: http.StatusNotFound, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &childRouteService{err: test.serviceErr}
			router, token := runChildrenRouter(t, test.principal, service)
			request := httptest.NewRequest(http.MethodGet, "/runs/scheduler-wave?scheduled_for="+url.QueryEscape(test.query), nil)
			request.Header.Set("HX-Request", "true")
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || service.waveCalls != test.wantCalls {
				t.Fatalf("status=%d calls=%d body=%q", response.Code, service.waveCalls, response.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				want := time.Date(2026, 8, 27, 18, 0, 0, 123456000, time.UTC)
				if !service.waveTime.Equal(want) || !strings.Contains(response.Body.String(), `href="/runs/3"`) {
					t.Fatalf("scheduled_for=%s body=%q", service.waveTime, response.Body.String())
				}
			}
		})
	}
}

func runChildrenRouter(t *testing.T, principal browserauth.Principal, service runService) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	registry, err := navigation.NewRegistry([]navigation.Group{{Key: "ingestion", Label: "Ingestion", Items: []navigation.Item{RunsNavigation()}}}, PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	shell := adminshell.New(renderer, registry, "Test", errors)
	cookies := browserauth.NewCookieManager("session", false, time.Hour)
	authentication := browserauth.NewHTTP(&routeAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(shell, service, nil).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
