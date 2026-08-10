package impersonation

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
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type fakeLifecycle struct {
	startResult Result
	startErr    error
	startCalls  int
	stopResult  Result
	stopErr     error
}

func (service *fakeLifecycle) Start(context.Context, browserauth.Principal, [32]byte, uint64, time.Time) (Result, error) {
	service.startCalls++
	return service.startResult, service.startErr
}

func (service *fakeLifecycle) Stop(context.Context, browserauth.Principal, [32]byte, time.Time) (Result, error) {
	return service.stopResult, service.stopErr
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

func TestHandlerActorGuardConflictAndCookieRotation(t *testing.T) {
	oldToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	admin := browserauth.Principal{UserID: 1, Username: "admin", RoleSlug: access.AdminRoleSlug, Actor: browserauth.Identity{UserID: 1, Username: "admin", RoleSlug: access.AdminRoleSlug}}

	t.Run("start rotates cookie", func(t *testing.T) {
		lifecycle := &fakeLifecycle{startResult: Result{RawToken: newToken, Session: auth.Session{RememberMe: true, ExpiresAt: expires}}}
		router := handlerRouter(t, admin, lifecycle)
		response := serveLifecycle(router, http.MethodPost, "/users/2/impersonate", oldToken)
		cookies := response.Result().Cookies()
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?notice=impersonation-started" || len(cookies) != 1 || cookies[0].Value != newToken || !cookies[0].Expires.Equal(expires) {
			t.Fatalf("status=%d location=%q cookies=%+v", response.Code, response.Header().Get("Location"), cookies)
		}
	})

	t.Run("nested reaches service conflict", func(t *testing.T) {
		principal := admin
		principal.UserID, principal.Username, principal.RoleSlug, principal.IsImpersonating = 2, "member", access.UserRoleSlug, true
		lifecycle := &fakeLifecycle{startErr: ErrAlreadyActive}
		response := serveLifecycle(handlerRouter(t, principal, lifecycle), http.MethodPost, "/users/3/impersonate", oldToken)
		if response.Code != http.StatusConflict || lifecycle.startCalls != 1 {
			t.Fatalf("status=%d calls=%d", response.Code, lifecycle.startCalls)
		}
	})

	t.Run("non-admin actor is rejected narrowly", func(t *testing.T) {
		principal := admin
		principal.Actor.RoleSlug = access.UserRoleSlug
		lifecycle := &fakeLifecycle{}
		response := serveLifecycle(handlerRouter(t, principal, lifecycle), http.MethodPost, "/users/2/impersonate", oldToken)
		if response.Code != http.StatusForbidden || lifecycle.startCalls != 0 {
			t.Fatalf("status=%d calls=%d", response.Code, lifecycle.startCalls)
		}
	})

	t.Run("invalid stop clears cookie", func(t *testing.T) {
		principal := admin
		principal.UserID, principal.Username, principal.RoleSlug, principal.IsImpersonating = 2, "member", access.UserRoleSlug, true
		response := serveLifecycle(handlerRouter(t, principal, &fakeLifecycle{stopErr: ErrUnauthenticated}), http.MethodPost, "/impersonation/stop", oldToken)
		cookies := response.Result().Cookies()
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" || len(cookies) != 1 || cookies[0].MaxAge != -1 {
			t.Fatalf("status=%d location=%q cookies=%+v", response.Code, response.Header().Get("Location"), cookies)
		}
	})

	response := serveLifecycle(handlerRouter(t, admin, &fakeLifecycle{}), http.MethodGet, "/users/2/impersonate", oldToken)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET impersonation status=%d", response.Code)
	}
}

func handlerRouter(t *testing.T, principal browserauth.Principal, lifecycle *fakeLifecycle) http.Handler {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	registry, err := navigation.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cookies := browserauth.NewCookieManager("session", false, 24*time.Hour)
	authentication := browserauth.NewHTTP(&fakeAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(adminshell.New(renderer, registry, "Test", errors), lifecycle, cookies).RegisterRoutes(router)
	return router
}

func serveLifecycle(router http.Handler, method, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
