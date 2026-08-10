package users

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
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	coreuser "github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type fakeAuthentication struct{ principal browserauth.Principal }

func (*fakeAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*fakeAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (coreuser.User, error) {
	return coreuser.User{}, nil
}
func (service *fakeAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return service.principal, nil
}
func (*fakeAuthentication) Logout(context.Context, [32]byte) error { return nil }

func TestPasswordResetCookieScope(t *testing.T) {
	for _, test := range []struct {
		name         string
		principal    browserauth.Principal
		wantClear    bool
		wantLocation string
	}{
		{
			name: "normal self reset clears owned session",
			principal: browserauth.Principal{UserID: 9, Username: "member", RoleID: 2, RoleSlug: access.UserRoleSlug,
				Actor:       browserauth.Identity{UserID: 9, Username: "member", RoleID: 2, RoleSlug: access.UserRoleSlug},
				Permissions: access.NewPermissionSet([]string{PermissionResetPassword})},
			wantClear: true, wantLocation: "/login",
		},
		{
			name: "impersonated target reset keeps administrator session",
			principal: browserauth.Principal{UserID: 9, Username: "member", RoleID: 2, RoleSlug: access.UserRoleSlug,
				Actor: browserauth.Identity{UserID: 1, Username: "admin", RoleID: 1, RoleSlug: access.AdminRoleSlug}, IsImpersonating: true,
				Permissions: access.NewPermissionSet([]string{PermissionResetPassword})},
			wantLocation: "/users/9?notice=password-reset",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, token := userHandlerRouter(t, test.principal)
			form := url.Values{"password": {"long-enough-password"}, "password_confirmation": {"long-enough-password"}}
			request := httptest.NewRequest(http.MethodPost, "/users/9/reset-password", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.wantLocation {
				t.Fatalf("status=%d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
			}
			cookies := response.Result().Cookies()
			cleared := len(cookies) == 1 && cookies[0].MaxAge == -1
			if cleared != test.wantClear {
				t.Fatalf("clear=%v cookies=%+v", cleared, cookies)
			}
		})
	}
}

func TestUsersFullPageAndHTMXPartial(t *testing.T) {
	principal := browserauth.Principal{UserID: 9, Username: "member", RoleSlug: access.UserRoleSlug, Actor: browserauth.Identity{UserID: 9, Username: "member", RoleSlug: access.UserRoleSlug}, Permissions: access.NewPermissionSet([]string{PermissionView})}
	for _, test := range []struct {
		name, hx string
		wantHTML bool
	}{{name: "full", wantHTML: true}, {name: "htmx", hx: "true"}} {
		t.Run(test.name, func(t *testing.T) {
			router, token := userHandlerRouter(t, principal)
			request := httptest.NewRequest(http.MethodGet, "/users?q=literal%25", nil)
			request.Header.Set("HX-Request", test.hx)
			request.AddCookie(&http.Cookie{Name: "session", Value: token})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `id="users-table"`) || strings.Contains(body, "<!doctype html>") != test.wantHTML {
				t.Fatalf("status=%d body=%q", response.Code, body)
			}
		})
	}
}

func userHandlerRouter(t *testing.T, principal browserauth.Principal) (http.Handler, string) {
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
	authentication := browserauth.NewHTTP(&fakeAuthentication{principal}, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	store := &fakeStore{users: map[uint64]UserRecord{9: {ID: 9, Username: "member", RoleID: 2, RoleSlug: access.UserRoleSlug, IsActive: true}}}
	service := NewService(store, fakeRoles{}, "roles.assign")
	service.hashPassword = func(string) (string, error) { return "hash", nil }
	router := chi.NewRouter()
	router.Use(authentication.LoadPrincipal)
	NewHandler(adminshell.New(renderer, registry, "Test", errors), service, cookies, "roles.assign", nil).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
