//go:build integration

package browserauth_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/features/datasources"
	"github.com/ibldzn/go-admin/internal/features/reporttemplates"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/server"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestSensitiveReportingRoutesUseStoredSessionMFA(t *testing.T) {
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, app.PermissionDefinitions())
	ctx := context.Background()
	now := time.Now().UTC()
	adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, "sensitive-admin", adminRole.ID, true)
	requester := integrationdb.Requester(actor, adminRole)
	config := integrationdb.Config(t)
	if config.Host != "127.0.0.1" {
		t.Fatal("sensitive route fixture requires its explicit localhost destination grant")
	}
	const originalSocket = "/tmp/sensitive-mfa-fixture-original.sock"
	const replacementSocket = "/tmp/sensitive-mfa-fixture-replacement.sock"
	destinations, err := reporting.NewDestinationPolicy([]string{"127.0.0.1/32"}, nil, []string{originalSocket, replacementSocket})
	if err != nil {
		t.Fatal(err)
	}
	cipher := reporting.NewCipher([32]byte{31})
	repository, err := reporting.NewRepository(db, cipher, destinations)
	if err != nil {
		t.Fatal(err)
	}
	pools, err := reporting.NewPoolManager(cipher, reporting.PoolConfig{Destinations: destinations, ConnectTimeout: 5 * time.Second, MySQLMaxPacketBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pools.Close() })
	reportingService, err := reporting.NewService(repository, pools, reporting.ServiceConfig{ConnectTimeout: 5 * time.Second, InteractiveTimeout: 10 * time.Second, InteractiveMaxRows: 100, InteractivePayloadBytes: 1 << 20, DynamicOptionMaxRows: 100, DynamicOptionPayloadBytes: 1 << 20, CellPreviewBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	datasourceInput := reporting.DatasourceInput{Name: "Sensitive source", Network: "tcp", Host: config.Host, Port: uint16(config.Port), DatabaseName: config.Name, Username: config.User, Password: config.Password, TLSPolicy: reporting.TLSDisabled}
	datasource, err := repository.CreateDatasource(ctx, requester, datasourceInput, now)
	if err != nil {
		t.Fatal(err)
	}
	datasourceInput.Name = "Other source"
	otherDatasource, err := repository.CreateDatasource(ctx, requester, datasourceInput, now)
	if err != nil {
		t.Fatal(err)
	}
	unixDatasource, err := repository.CreateDatasource(ctx, requester, reporting.DatasourceInput{Name: "Unix fixture", Network: "unix", SocketPath: originalSocket, DatabaseName: config.Name, Username: config.User, TLSPolicy: reporting.TLSDisabled}, now)
	if err != nil {
		t.Fatal(err)
	}
	parameter := reporting.Parameter{Key: "city", Label: "City", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT '001' AS value, 'One' AS label", DefaultValue: []byte(`"001"`)}
	template, err := repository.CreateTemplate(ctx, requester, reporting.TemplateInput{Name: "Sensitive template", DatasourceID: datasource.ID, SQLText: "SELECT :city AS city", Parameters: []reporting.Parameter{parameter}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, template.ID, actor.ID, actor.ID, now); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := auth.NewSessionRepository(db)
	service, err := browserauth.NewService(user.NewRepository(db), access.NewRepository(db), sessions, time.Hour, 24*time.Hour, logger)
	if err != nil {
		t.Fatal(err)
	}
	store := &mfa.Store{DB: db, Cipher: secretcrypto.New([32]byte{41}), Lifetime: time.Hour, RememberLifetime: 24 * time.Hour, IdleTimeout: 2 * time.Hour}
	service.EnableMFA(store)
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := navigation.NewRegistry(nil, app.PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	errors := render.NewErrorResponder(renderer, "DWH", logger)
	cookies := browserauth.NewCookieManager("sensitive_session", false, time.Hour)
	h := browserauth.NewHTTP(service, renderer, cookies, "DWH", false, logger, nil, errors)
	h.EnableMFA(store)
	static, err := fs.Sub(webfiles.Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	admin := adminshell.New(renderer, registry, "DWH", errors)
	h.SetAuthenticatedPageRenderer(admin)
	router := server.NewRouter(server.RouterDependencies{Authentication: h, Errors: errors, StaticFiles: static, RegisterAuthenticated: func(r chi.Router) {
		datasources.NewHandler(admin, repository, reportingService, pools).RegisterRoutes(r)
		reporttemplates.NewHandler(admin, repository, reportingService).RegisterRoutes(r)
	}})
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	session := integrationdb.Session(t, sessions, actor.ID, false, token, now)
	request := func(method, path string, values url.Values, sessionToken string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "sensitive_session", Value: sessionToken})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	setFresh := func(fresh bool) {
		verifiedAt := time.Now().UTC()
		if !fresh {
			verifiedAt = verifiedAt.Add(-11 * time.Minute)
		}
		if _, err := db.Exec(`UPDATE sessions SET mfa_verified_at=? WHERE id=?`, verifiedAt, session.ID); err != nil {
			t.Fatal(err)
		}
	}
	datasourceForm := func() url.Values {
		value, err := repository.FindDatasource(ctx, datasource.ID)
		if err != nil {
			t.Fatal(err)
		}
		return url.Values{"name": {value.Name}, "description": {value.Description}, "network": {value.Network}, "host": {value.Host}, "port": {strconv.Itoa(int(value.Port))}, "socket_path": {value.SocketPath}, "database_name": {value.DatabaseName}, "username": {value.Username}, "password": {""}, "tls_policy": {string(value.TLSPolicy)}, "revision": {strconv.FormatUint(value.Revision, 10)}}
	}
	const parametersJSON = `[{"key":"city","label":"City","type":"single_option","option_source":"dynamic","dynamic_option_sql":"SELECT '001' AS value, 'One' AS label","required":false,"default":"001"}]`
	templateForm := func() url.Values {
		value, err := repository.FindTemplate(ctx, template.ID)
		if err != nil {
			t.Fatal(err)
		}
		return url.Values{"name": {value.Name}, "description": {value.Description}, "datasource_id": {strconv.FormatUint(value.DatasourceID, 10)}, "sql_text": {value.SQLText}, "parameters_json": {parametersJSON}, "test_values_json": {`{"city":"001"}`}, "revision": {strconv.FormatUint(value.Revision, 10)}, "target_index": {"0"}}
	}
	datasourcePath := fmt.Sprintf("/datasources/%d", datasource.ID)
	templatePath := fmt.Sprintf("/report-templates/%d", template.ID)
	assertChallenge := func(t *testing.T, response *httptest.ResponseRecorder, safeGET string) {
		t.Helper()
		location, err := url.Parse(response.Header().Get("Location"))
		if response.Code != http.StatusSeeOther || err != nil || location.Path != "/mfa/step-up" || location.Query().Get("next") != safeGET || location.Query().Get("resubmit") != "1" {
			t.Fatalf("status=%d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
		}
		if strings.Contains(location.String(), "DO_NOT_STORE") || len(response.Result().Cookies()) != 0 {
			t.Fatal("POST material persisted in redirect or cookie")
		}
	}
	setFresh(false)
	for _, operation := range []struct {
		method, path, next string
		form               func() url.Values
	}{
		{"GET", "/datasources/new", "/datasources/new", func() url.Values { return nil }},
		{"POST", "/datasources", "/datasources/new", func() url.Values { form := datasourceForm(); form.Set("password", "DO_NOT_STORE"); return form }},
		{"POST", datasourcePath + "/test", datasourcePath, func() url.Values { return nil }},
		{"POST", datasourcePath + "/state", datasourcePath, func() url.Values { form := datasourceForm(); form.Set("status", "active"); return form }},
		{"GET", "/report-templates/new", "/report-templates/new", func() url.Values { return nil }},
		{"POST", "/report-templates", "/report-templates/new", templateForm},
		{"POST", templatePath + "/test", templatePath + "/edit", templateForm},
		{"POST", templatePath + "/test-options", templatePath + "/edit", templateForm},
		{"POST", templatePath + "/state", templatePath, func() url.Values { form := templateForm(); form.Set("status", "active"); return form }},
	} {
		t.Run("stale "+operation.method+" "+operation.path, func(t *testing.T) {
			assertChallenge(t, request(operation.method, operation.path, operation.form(), token), operation.next)
		})
	}
	for _, field := range []struct{ name, value string }{{"network", "unix"}, {"host", "192.0.2.1"}, {"port", "1"}, {"database_name", "other"}, {"username", "other"}, {"password", "DO_NOT_STORE"}, {"tls_policy", "required"}} {
		t.Run("stale datasource "+field.name, func(t *testing.T) {
			form := datasourceForm()
			form.Set(field.name, field.value)
			assertChallenge(t, request("POST", datasourcePath, form, token), datasourcePath+"/edit")
		})
	}
	t.Run("stale Unix socket change", func(t *testing.T) {
		form := datasourceForm()
		form.Set("network", "unix")
		form.Set("socket_path", replacementSocket)
		form.Set("tls_policy", "disabled")
		form.Set("revision", strconv.FormatUint(unixDatasource.Revision, 10))
		path := fmt.Sprintf("/datasources/%d", unixDatasource.ID)
		assertChallenge(t, request("POST", path, form, token), path+"/edit")
		value, err := repository.FindDatasource(ctx, unixDatasource.ID)
		if err != nil || value.SocketPath != originalSocket || value.Revision != unixDatasource.Revision {
			t.Fatal("stale socket update persisted", err)
		}
	})
	for _, field := range []struct{ name, value string }{
		{"sql_text", "SELECT 2 AS changed"},
		{"datasource_id", strconv.FormatUint(otherDatasource.ID, 10)},
		{"parameters_json", strings.Replace(parametersJSON, `"required":false`, `"required":true`, 1)},
		{"parameters_json", strings.Replace(parametersJSON, "One", "Changed", 1)},
		{"parameters_json", strings.Replace(parametersJSON, "SELECT '001'", "SELECT '002'", 1)},
	} {
		t.Run("stale template "+field.name+field.value, func(t *testing.T) {
			form := templateForm()
			form.Set(field.name, field.value)
			assertChallenge(t, request("POST", templatePath, form, token), templatePath+"/edit")
		})
	}
	if value, err := repository.FindDatasource(ctx, datasource.ID); err != nil || value.Revision != datasource.Revision {
		t.Fatal("stale datasource request mutated persisted revision", err)
	}
	if value, err := repository.FindTemplate(ctx, template.ID); err != nil || value.Revision != template.Revision {
		t.Fatal("stale template request mutated persisted revision", err)
	}
	for _, resource := range []struct {
		path string
		form func() url.Values
	}{{datasourcePath, datasourceForm}, {templatePath, templateForm}} {
		for _, field := range []string{"name", "description"} {
			t.Run("stale metadata "+resource.path+" "+field, func(t *testing.T) {
				form := resource.form()
				form.Set(field, "Metadata changed "+field)
				previousRevision, _ := strconv.ParseUint(form.Get("revision"), 10, 64)
				response := request("POST", resource.path, form, token)
				if response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
					t.Fatalf("metadata challenged: status=%d body=%q", response.Code, response.Body.String())
				}
				saved := resource.form()
				if saved.Get(field) != form.Get(field) || saved.Get("revision") != strconv.FormatUint(previousRevision+1, 10) {
					t.Fatal("metadata edit or revision update lost")
				}
			})
		}
	}
	setFresh(true)
	t.Run("fresh creation forms", func(t *testing.T) {
		for _, path := range []string{"/datasources/new", "/report-templates/new"} {
			if response := request("GET", path, nil, token); response.Code != 200 {
				t.Fatalf("%s status=%d", path, response.Code)
			}
		}
	})
	t.Run("fresh datasource creation connection update test activation", func(t *testing.T) {
		form := datasourceForm()
		form.Set("name", "Fresh created datasource")
		form.Set("password", config.Password)
		if response := request("POST", "/datasources", form, token); response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
			t.Fatalf("create status=%d body=%q", response.Code, response.Body.String())
		}
		form = datasourceForm()
		form.Set("password", config.Password)
		if response := request("POST", datasourcePath, form, token); response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
			t.Fatalf("credential update status=%d body=%q", response.Code, response.Body.String())
		}
		if response := request("POST", datasourcePath+"/test", nil, token); response.Code != 303 || !strings.Contains(response.Header().Get("Location"), "report-datasource-test-ok") {
			t.Fatalf("connection test status=%d location=%q", response.Code, response.Header().Get("Location"))
		}
		form = datasourceForm()
		form.Set("status", "active")
		if response := request("POST", datasourcePath+"/state", form, token); response.Code != 303 {
			t.Fatalf("activation status=%d body=%q", response.Code, response.Body.String())
		}
		value, err := repository.FindDatasource(ctx, datasource.ID)
		if err != nil || value.Status != reporting.StatusActive {
			t.Fatal("datasource activation not persisted", err)
		}
	})
	t.Run("fresh template creation activation definition update query options", func(t *testing.T) {
		form := templateForm()
		form.Set("name", "Fresh created template")
		if response := request("POST", "/report-templates", form, token); response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
			t.Fatalf("create status=%d body=%q", response.Code, response.Body.String())
		}
		form = templateForm()
		form.Set("status", "active")
		if response := request("POST", templatePath+"/state", form, token); response.Code != 303 {
			t.Fatalf("activation status=%d body=%q", response.Code, response.Body.String())
		}
		form = templateForm()
		form.Set("sql_text", "SELECT :city AS city, 2 AS changed")
		if response := request("POST", templatePath, form, token); response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
			t.Fatalf("definition update status=%d body=%q", response.Code, response.Body.String())
		}
		for _, suffix := range []string{"/test", "/test-options"} {
			if response := request("POST", templatePath+suffix, templateForm(), token); response.Code != 200 {
				t.Fatalf("%s status=%d body=%q", suffix, response.Code, response.Body.String())
			}
		}
	})
	t.Run("stale active metadata remain allowed", func(t *testing.T) {
		setFresh(false)
		for _, resource := range []struct {
			path string
			form func() url.Values
		}{{datasourcePath, datasourceForm}, {templatePath, templateForm}} {
			form := resource.form()
			form.Set("name", "Active metadata name")
			form.Set("description", "Active metadata description")
			if response := request("POST", resource.path, form, token); response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
				t.Fatalf("active metadata status=%d body=%q", response.Code, response.Body.String())
			}
			if saved := resource.form(); saved.Get("name") != form.Get("name") || saved.Get("description") != form.Get("description") {
				t.Fatal("active metadata not persisted")
			}
		}
		setFresh(true)
	})
	t.Run("read only SQL remains mandatory after step up", func(t *testing.T) {
		for _, operation := range []struct{ suffix, field, value string }{
			{"/test", "sql_text", "DELETE FROM users"},
			{"", "sql_text", "DELETE FROM users"},
			{"/test-options", "parameters_json", strings.Replace(parametersJSON, "SELECT '001' AS value, 'One' AS label", "DELETE FROM users", 1)},
			{"", "parameters_json", strings.Replace(parametersJSON, "SELECT '001' AS value, 'One' AS label", "DELETE FROM users", 1)},
		} {
			form := templateForm()
			form.Set(operation.field, operation.value)
			setFresh(false)
			assertChallenge(t, request("POST", templatePath+operation.suffix, form, token), templatePath+"/edit")
			setFresh(true)
			if response := request("POST", templatePath+operation.suffix, form, token); response.Code != 422 {
				t.Fatalf("unsafe SQL accepted on %s: status=%d body=%q", operation.suffix, response.Code, response.Body.String())
			}
		}
		value, err := repository.FindTemplate(ctx, template.ID)
		if err != nil || strings.Contains(value.SQLText, "DELETE") || strings.Contains(value.Parameters[0].DynamicOptionSQL, "DELETE") {
			t.Fatal("unsafe update persisted", err)
		}
		unsafeDraft, err := repository.CreateTemplate(ctx, requester, reporting.TemplateInput{Name: "Unsafe disabled draft", DatasourceID: datasource.ID, SQLText: "DELETE FROM users"}, now)
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/report-templates/%d/state", unsafeDraft.ID)
		form := url.Values{"status": {"active"}, "revision": {strconv.FormatUint(unsafeDraft.Revision, 10)}}
		if response := request("POST", path, form, token); response.Code != 422 {
			t.Fatalf("unsafe activation status=%d body=%q", response.Code, response.Body.String())
		}
		if value, err := repository.FindTemplate(ctx, unsafeDraft.ID); err != nil || value.Status != reporting.StatusDisabled {
			t.Fatal("unsafe draft activated", err)
		}
	})
	t.Run("fresh MFA never grants missing RBAC", func(t *testing.T) {
		role := integrationdb.CustomRole(t, db, "Denied", "sensitive-denied")
		denied := integrationdb.User(t, db, "sensitive-denied", role.ID, true)
		deniedToken, err := auth.GenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		integrationdb.Session(t, sessions, denied.ID, false, deniedToken, time.Now().UTC())
		for _, operation := range []struct {
			method, path string
			form         url.Values
		}{
			{"GET", "/datasources/new", nil}, {"POST", "/datasources", datasourceForm()}, {"POST", datasourcePath, datasourceForm()}, {"POST", datasourcePath + "/test", nil}, {"POST", datasourcePath + "/state", url.Values{"status": {"active"}}},
			{"GET", "/report-templates/new", nil}, {"POST", "/report-templates", templateForm()}, {"POST", templatePath, templateForm()}, {"POST", templatePath + "/test", templateForm()}, {"POST", templatePath + "/test-options", templateForm()}, {"POST", templatePath + "/state", url.Values{"status": {"active"}}},
		} {
			if response := request(operation.method, operation.path, operation.form, deniedToken); response.Code != 403 {
				t.Fatalf("%s %s: status=%d", operation.method, operation.path, response.Code)
			}
		}
	})
	t.Run("stale disable remains allowed", func(t *testing.T) {
		setFresh(false)
		for _, resource := range []struct {
			path string
			form func() url.Values
		}{{templatePath, templateForm}, {datasourcePath, datasourceForm}} {
			form := resource.form()
			form.Set("status", "disabled")
			if response := request("POST", resource.path+"/state", form, token); response.Code != 303 || strings.HasPrefix(response.Header().Get("Location"), "/mfa/") {
				t.Fatalf("disable status=%d body=%q", response.Code, response.Body.String())
			}
		}
		if value, err := repository.FindTemplate(ctx, template.ID); err != nil || value.Status != reporting.StatusDisabled {
			t.Fatal("template disable not persisted", err)
		}
		if value, err := repository.FindDatasource(ctx, datasource.ID); err != nil || value.Status != reporting.StatusDisabled {
			t.Fatal("datasource disable not persisted", err)
		}
	})
}
