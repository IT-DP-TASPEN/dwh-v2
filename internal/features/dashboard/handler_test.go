package dashboard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type routeService struct{ calls int }

func (service *routeService) Load(context.Context, browserauth.Principal, time.Time) (Data, error) {
	service.calls++
	return Data{}, nil
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

func TestRootLandingUsesCapabilitiesAndAvoidsUnauthorizedReads(t *testing.T) {
	for _, test := range []struct {
		name, permission, location string
		status, calls              int
	}{
		{name: "operational", permission: ingestionfeature.PermissionView, status: http.StatusOK, calls: 1},
		{name: "reporting only", permission: reports.PermissionView, status: http.StatusSeeOther, location: "/reports"},
		{name: "no relevant capability", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal := browserauth.Principal{UserID: 1, Username: "viewer", Name: "Viewer", RoleSlug: access.UserRoleSlug,
				Permissions: access.NewPermissionSet([]string{test.permission}), Actor: browserauth.Identity{UserID: 1, Username: "viewer", RoleSlug: access.UserRoleSlug}}
			service := &routeService{}
			router, token := dashboardRouter(t, principal, service)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || response.Header().Get("Location") != test.location || service.calls != test.calls {
				t.Fatalf("status=%d location=%q reads=%d body=%q", response.Code, response.Header().Get("Location"), service.calls, response.Body.String())
			}
		})
	}
}

func dashboardRouter(t *testing.T, principal browserauth.Principal, service dashboardService) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	definitions := append(ingestionfeature.PermissionDefinitions(), reports.PermissionDefinitions()...)
	registry, err := navigation.NewRegistry([]navigation.Group{
		{Key: "general", Label: "General", Items: []navigation.Item{Navigation()}},
		{Key: "reporting", Label: "Reporting", Items: []navigation.Item{reports.Navigation()}},
	}, definitions)
	if err != nil {
		t.Fatal(err)
	}
	shell := adminshell.New(renderer, registry, "Test", errors)
	cookies := browserauth.NewCookieManager("session", false, time.Hour)
	authentication := browserauth.NewHTTP(&routeAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(shell, service).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
