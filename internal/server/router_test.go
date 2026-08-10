package server

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

type fakeAuthentication struct {
	principal browserauth.Principal
	resolved  int
}

func (*fakeAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*fakeAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (user.User, error) {
	return user.User{}, nil
}
func (service *fakeAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	service.resolved++
	return service.principal, nil
}
func (*fakeAuthentication) Logout(context.Context, [32]byte) error { return nil }

func TestRouterOwnsInfrastructureOnly(t *testing.T) {
	service := &fakeAuthentication{principal: browserauth.Principal{UserID: 1, Username: "admin", Actor: browserauth.Identity{UserID: 1, Username: "admin"}}}
	router, token := testRouter(t, service, nil)
	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || service.resolved != 0 || health.Header().Get("Cache-Control") != "" || health.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("health status=%d resolved=%d headers=%v", health.Code, service.resolved, health.Header())
	}
	missing := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/users", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	router.ServeHTTP(missing, request)
	if missing.Code != http.StatusNotFound || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("global feature route still exists: status=%d headers=%v", missing.Code, missing.Header())
	}
}

func TestAuthenticatedFeatureCallback(t *testing.T) {
	service := &fakeAuthentication{principal: browserauth.Principal{UserID: 1, Username: "admin", Actor: browserauth.Identity{UserID: 1, Username: "admin"}}}
	registered := 0
	router, token := testRouter(t, service, func(router chi.Router) {
		registered++
		router.Get("/feature", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	})
	request := httptest.NewRequest(http.MethodGet, "/feature", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if registered != 1 || response.Code != http.StatusNoContent || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("registered=%d status=%d headers=%v", registered, response.Code, response.Header())
	}
}

func TestStaticAndCrossOriginBoundaries(t *testing.T) {
	service := &fakeAuthentication{principal: browserauth.Principal{UserID: 1}}
	router, token := testRouter(t, service, nil)
	staticRequest := httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil)
	staticRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	staticResponse := httptest.NewRecorder()
	router.ServeHTTP(staticResponse, staticRequest)
	if staticResponse.Code != http.StatusOK || service.resolved != 0 || staticResponse.Header().Get("Cache-Control") == "no-store" {
		t.Fatalf("static status=%d resolved=%d cache=%q", staticResponse.Code, service.resolved, staticResponse.Header().Get("Cache-Control"))
	}
	crossOrigin := httptest.NewRequest(http.MethodPost, "/login", nil)
	crossOrigin.Header.Set("Sec-Fetch-Site", "cross-site")
	crossOriginResponse := httptest.NewRecorder()
	router.ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden || service.resolved != 0 {
		t.Fatalf("cross-origin status=%d resolved=%d", crossOriginResponse.Code, service.resolved)
	}
}

func testRouter(t *testing.T, service *fakeAuthentication, register func(chi.Router)) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errors := render.NewErrorResponder(renderer, "Test", logger)
	cookies := browserauth.NewCookieManager("session", false, time.Hour)
	authentication := browserauth.NewHTTP(service, renderer, cookies, "Test", false, logger, func(context.Context, audit.Event) error { return nil }, errors)
	staticFiles, err := fs.Sub(webfiles.Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(RouterDependencies{StaticFiles: staticFiles, Authentication: authentication, RegisterAuthenticated: register, Errors: errors}), token
}
