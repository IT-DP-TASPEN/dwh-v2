package datasources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestDatasourceFormNetworks(t *testing.T) {
	for _, network := range []string{"", "tcp", "unix"} {
		values := url.Values{"network": {network}, "name": {"Test"}, "database_name": {"dwh"}, "username": {"dwhadmin"}, "socket_path": {"/tmp/mysql.sock"}}
		request := httptest.NewRequest("POST", "/datasources", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		form, input, ok := (&Handler{}).form(httptest.NewRecorder(), request, true)
		if !ok {
			t.Fatal("form failed to parse")
		}
		if network == "unix" {
			if len(form.Errors) != 0 || input.Password != "" || input.Port != 0 || input.TLSPolicy != reporting.TLSDisabled || input.SocketPath != "/tmp/mysql.sock" {
				t.Fatalf("Unix required TCP fields: %+v", form)
			}
		} else if form.Errors["password"] == "" || form.Errors["port"] == "" || input.Network != "tcp" {
			t.Fatal("TCP required fields or default mode changed")
		}
	}
	if got := publicError(errors.New("password=super-secret DSN")); strings.Contains(got, "super-secret") {
		t.Fatal("public error exposed connection credentials")
	}
}

func TestDatasourceSensitiveFields(t *testing.T) {
	existing := reporting.Datasource{Network: "tcp", Host: "db.internal", Port: 3306, DatabaseName: "dwh", Username: "reader", TLSPolicy: reporting.TLSRequired}
	base := reporting.DatasourceInput{Network: existing.Network, Host: existing.Host, Port: existing.Port, DatabaseName: existing.DatabaseName, Username: existing.Username, TLSPolicy: existing.TLSPolicy}
	for _, test := range []struct {
		name   string
		change func(*reporting.DatasourceInput)
		want   bool
	}{
		{"unchanged", func(*reporting.DatasourceInput) {}, false},
		{"name", func(input *reporting.DatasourceInput) { input.Name = "Renamed" }, false},
		{"description", func(input *reporting.DatasourceInput) { input.Description = "New description" }, false},
		{"normalization", func(input *reporting.DatasourceInput) {
			input.Network = ""
			input.Host = " db.internal "
			input.DatabaseName = " dwh "
			input.Username = " reader "
			input.SocketPath = "/ignored.sock"
		}, false},
		{"network", func(input *reporting.DatasourceInput) {
			input.Network = "unix"
			input.SocketPath = "/db.sock"
			input.TLSPolicy = reporting.TLSDisabled
		}, true},
		{"host", func(input *reporting.DatasourceInput) { input.Host = "other.internal" }, true},
		{"port", func(input *reporting.DatasourceInput) { input.Port++ }, true},
		{"database", func(input *reporting.DatasourceInput) { input.DatabaseName = "other" }, true},
		{"username", func(input *reporting.DatasourceInput) { input.Username = "writer" }, true},
		{"password", func(input *reporting.DatasourceInput) { input.Password = "changed" }, true},
		{"TLS", func(input *reporting.DatasourceInput) { input.TLSPolicy = reporting.TLSDisabled }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.change(&input)
			if got := datasourceConnectionChanged(existing, input); got != test.want {
				t.Fatalf("sensitive=%v, want %v", got, test.want)
			}
		})
	}
	unix := reporting.Datasource{Network: "unix", SocketPath: "/db.sock", DatabaseName: "dwh", Username: "reader", TLSPolicy: reporting.TLSDisabled}
	input := reporting.DatasourceInput{Network: "unix", SocketPath: unix.SocketPath, Host: "ignored", Port: 3306, DatabaseName: unix.DatabaseName, Username: unix.Username}
	if datasourceConnectionChanged(unix, input) {
		t.Fatal("unchanged Unix datasource challenged for ignored TCP fields")
	}
	input.SocketPath = "/other.sock"
	if !datasourceConnectionChanged(unix, input) {
		t.Fatal("socket change did not require step-up")
	}
}

func TestDatasourceRoutesRequireMFAAfterRBAC(t *testing.T) {
	for _, operation := range []struct {
		method, path, permission, body, next string
		freshStatus                          int
	}{
		{http.MethodGet, "/datasources/new", PermissionCreate, "", "/datasources/new", http.StatusOK},
		{http.MethodPost, "/datasources", PermissionCreate, "password=never-replay", "/datasources/new", http.StatusUnprocessableEntity},
		{http.MethodPost, "/datasources/7/test", PermissionTest, "", "/datasources/7", 0},
		{http.MethodPost, "/datasources/7/state", PermissionChangeState, "status=active&revision=bad", "/datasources/7", http.StatusUnprocessableEntity},
	} {
		for _, test := range []struct {
			name             string
			permitted, fresh bool
			want             int
		}{
			{"stale", true, false, http.StatusSeeOther},
			{"RBAC denied stale", false, false, http.StatusForbidden},
			{"RBAC denied fresh", false, true, http.StatusForbidden},
			{"fresh", true, true, operation.freshStatus},
		} {
			if test.want == 0 {
				continue
			}
			t.Run(operation.path+"/"+test.name, func(t *testing.T) {
				principal := browserauth.Principal{UserID: 1, RoleSlug: access.UserRoleSlug}
				if test.permitted {
					principal.Permissions = access.NewPermissionSet([]string{operation.permission})
				}
				if test.fresh {
					principal.MFAVerifiedAt = time.Now().UTC()
				}
				router, token := datasourceMFARouter(t, principal)
				request := httptest.NewRequest(operation.method, operation.path, strings.NewReader(operation.body))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.AddCookie(&http.Cookie{Name: "session", Value: token})
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != test.want {
					t.Fatalf("status=%d, want %d", response.Code, test.want)
				}
				if test.want == http.StatusSeeOther {
					location, err := url.Parse(response.Header().Get("Location"))
					if err != nil || location.Path != "/mfa/step-up" || location.Query().Get("next") != operation.next || location.Query().Get("resubmit") != "1" {
						t.Fatalf("unsafe destination: %q", response.Header().Get("Location"))
					}
					if strings.Contains(response.Header().Get("Location"), "never-replay") {
						t.Fatal("POST secret persisted in redirect")
					}
					if len(response.Result().Cookies()) != 0 {
						t.Fatal("POST state persisted in cookie")
					}
				}
			})
		}
	}
}

type datasourceMFAAuthentication struct{ principal browserauth.Principal }

func (*datasourceMFAAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*datasourceMFAAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (user.User, error) {
	return user.User{}, nil
}
func (service *datasourceMFAAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return service.principal, nil
}
func (*datasourceMFAAuthentication) Logout(context.Context, [32]byte) error { return nil }

func datasourceMFARouter(t *testing.T, principal browserauth.Principal) (http.Handler, string) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := navigation.NewRegistry(nil, PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	errors := render.NewErrorResponder(renderer, "Test", nil)
	cookies := browserauth.NewCookieManager("session", false, time.Hour)
	loader := browserauth.NewHTTP(&datasourceMFAAuthentication{principal}, renderer, cookies, "Test", false, nil, nil, errors)
	router := chi.NewRouter()
	router.Use(loader.LoadPrincipal, loader.RequireAuth)
	NewHandler(adminshell.New(renderer, registry, "Test", errors), nil, nil, nil).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
