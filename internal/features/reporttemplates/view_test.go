package reporttemplates

import (
	"context"
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

func TestTemplateFormRendersSelectedDatasource(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	data := FormData{ID: 2, DatasourceID: "7", Datasources: []reporting.Datasource{{ID: 7, Name: "Read only", Status: reporting.StatusActive}}, ParametersJSON: "[]", TestValuesJSON: "{}", Errors: map[string]string{}}
	recorder := httptest.NewRecorder()
	if err := renderer.RenderPage(recorder, 200, "features/reporttemplates/form", adminshell.PageData{Title: "Template", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `value="7" selected`) || !strings.Contains(body, "tests use the currently saved datasource") || !strings.Contains(body, "[[ AND product_id = :product ]]") {
		t.Fatalf("datasource boundary was not rendered: %s", body)
	}
}

func TestTemplateFormRendersStructuredBuilderWithoutVisibleJSON(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	parameters := `[{"key":"branch","label":"Branch","type":"single_option","required":true,"default":"001","options":[{"value":"001","label":"Main"}]}]`
	data := FormData{ID: 2, DatasourceID: "7", Datasources: []reporting.Datasource{{ID: 7, Name: "Read only"}}, ParametersJSON: parameters, TestValuesJSON: `{"branch":"001"}`, Errors: map[string]string{"parameters": "Parameter branch needs attention."}}
	recorder := httptest.NewRecorder()
	if err := renderer.RenderPage(recorder, 422, "features/reporttemplates/form", adminshell.PageData{Title: "Template", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, want := range []string{`x-data="reportTemplateEditor"`, `type="hidden" name="parameters_json"`, `type="hidden" name="test_values_json"`, "+ Add parameter", "Static options", "Test Query", "Parameter branch needs attention.", "branch"} {
		if !strings.Contains(body, want) {
			t.Fatalf("form missing %q: %s", want, body)
		}
	}
	for _, forbidden := range []string{"Typed parameters (JSON)", "Test values (JSON)", `<textarea name="parameters_json"`, `<textarea name="test_values_json"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("form retained visible JSON control %q", forbidden)
		}
	}
}

func TestDecodeParametersUsesVisibleArrayOrder(t *testing.T) {
	parameters, err := decodeParameters(`[
		{"key":"branches","label":"Branches","type":"multiple_option","required":true,"default":["002"],"order":44,"options":[{"value":"002","label":"Second","order":8},{"value":"001","label":"First","order":3}]},
		{"key":"amount","label":"Amount","type":"decimal","required":false,"default":"12345678901234567890.1200","order":2}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if parameters[0].Key != "branches" || parameters[0].DisplayOrder != 0 || parameters[1].DisplayOrder != 1 {
		t.Fatalf("parameter order was not normalized: %#v", parameters)
	}
	if parameters[0].Options[0].Value != "002" || parameters[0].Options[0].DisplayOrder != 0 || parameters[0].Options[1].DisplayOrder != 1 {
		t.Fatalf("option order was not normalized: %#v", parameters[0].Options)
	}
	if string(parameters[1].DefaultValue) != `"12345678901234567890.1200"` {
		t.Fatalf("decimal default lost precision: %s", parameters[1].DefaultValue)
	}
	if encoded := encodeParameters(parameters); strings.Contains(encoded, `"order"`) {
		t.Fatalf("internal order leaked into authoring payload: %s", encoded)
	}
}

func TestDecodeStructuredTestValues(t *testing.T) {
	values, err := decodeTestValues(`{"required_boolean":false,"optional_boolean":null,"branches":["001","002"],"amount":"12345678901234567890.1200"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := values["required_boolean"]; !got.Present || len(got.Values) != 1 || got.Values[0] != "false" {
		t.Fatalf("boolean=%#v", got)
	}
	if got := values["optional_boolean"]; !got.Present || len(got.Values) != 0 {
		t.Fatalf("optional boolean=%#v", got)
	}
	if got := values["branches"].Values; len(got) != 2 || got[0] != "001" || got[1] != "002" {
		t.Fatalf("branches=%#v", got)
	}
	if got := values["amount"].Values; len(got) != 1 || got[0] != "12345678901234567890.1200" {
		t.Fatalf("amount=%#v", got)
	}
	if _, err := decodeTestValues(`{broken`); err == nil || strings.Contains(err.Error(), "JSON") {
		t.Fatalf("internal JSON parser error leaked: %v", err)
	}
}

func TestDynamicOptionDraftRoundTripsThroughStructuredEditor(t *testing.T) {
	parameters, err := decodeParameters(`[
		{"key":"province","label":"Province","type":"text","required":true,"default":null},
		{"key":"city","label":"City","type":"single_option","option_source":"dynamic","dynamic_option_sql":"","required":false,"default":"001"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if parameters[1].OptionSource != reporting.OptionSourceDynamic || parameters[1].DynamicOptionSQL != "" || len(parameters[1].Options) != 0 {
		t.Fatalf("dynamic parameter=%+v", parameters[1])
	}
	encoded := encodeParameters(parameters)
	if !strings.Contains(encoded, `"option_source": "dynamic"`) {
		t.Fatalf("encoded=%s", encoded)
	}
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	data := FormData{ID: 2, ParametersJSON: encoded, TestValuesJSON: `{}`, Errors: map[string]string{}}
	if err := renderer.RenderPage(recorder, 200, "features/reporttemplates/form", adminshell.PageData{Title: "Template", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Option Source", "Dynamic Query", "Dynamic option SQL", "Test options", "Available upstream parameters"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("dynamic editor missing %q", want)
		}
	}
}

func TestTemplateDetailKeepsACLControls(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	data := DetailData{Value: reporting.Template{ID: 8, Name: "Balances", Status: reporting.StatusActive}, CanAccess: true, Access: AccessData{ReportID: 8, Rows: []reporting.AccessUser{{ID: 4, Name: "Operator", Username: "operator", Granted: true}}}}
	recorder := httptest.NewRecorder()
	if err := renderer.RenderPage(recorder, 200, "features/reporttemplates/show", adminshell.PageData{Title: "Template", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, want := range []string{`hx-post="/report-templates/8/access/4`, `name="grant" value="false"`, "Revoke", "btn-danger-outline"} {
		if !strings.Contains(body, want) {
			t.Fatalf("ACL control missing %q: %s", want, body)
		}
	}
}

func TestTemplateCanonicalDefinitionComparison(t *testing.T) {
	parameters, err := decodeParameters(`[{"key":"city","label":"City","type":"single_option","option_source":"dynamic","dynamic_option_sql":"SELECT city FROM locations","required":false,"default":null},{"key":"region","label":"Region","type":"single_option","required":true,"default":"east","options":[{"value":"east","label":"East"},{"value":"west","label":"West"}]}]`)
	if err != nil {
		t.Fatal(err)
	}
	canonical := encodeParameters(parameters)
	for _, test := range []struct {
		name   string
		change func([]reporting.Parameter)
	}{
		{"dynamic SQL", func(value []reporting.Parameter) { value[0].DynamicOptionSQL = "SELECT city FROM other" }},
		{"key", func(value []reporting.Parameter) { value[0].Key = "other" }},
		{"type", func(value []reporting.Parameter) { value[0].Type = reporting.ParameterMultipleOption }},
		{"required", func(value []reporting.Parameter) { value[0].Required = true }},
		{"default", func(value []reporting.Parameter) { value[1].DefaultValue = []byte(`"west"`) }},
		{"option source", func(value []reporting.Parameter) { value[0].OptionSource = reporting.OptionSourceStatic }},
		{"option value", func(value []reporting.Parameter) { value[1].Options[0].Value = "other" }},
		{"parameter order", func(value []reporting.Parameter) { value[0], value[1] = value[1], value[0] }},
		{"option order", func(value []reporting.Parameter) {
			value[1].Options[0], value[1].Options[1] = value[1].Options[1], value[1].Options[0]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, err := decodeParameters(canonical)
			if err != nil {
				t.Fatal(err)
			}
			test.change(changed)
			if encodeParameters(changed) == canonical {
				t.Fatal("query definition change classified as metadata edit")
			}
		})
	}
	parameters[0].ID, parameters[0].ReportID = 99, 7
	parameters[1].Options[0].ID, parameters[1].Options[0].ParameterID = 88, 99
	if encodeParameters(parameters) != canonical {
		t.Fatal("database IDs changed canonical query definition")
	}
}

func TestTemplateRoutesRequireMFAAfterRBAC(t *testing.T) {
	for _, operation := range []struct {
		method, path, permission, body, next string
		freshStatus                          int
	}{
		{http.MethodGet, "/report-templates/new", PermissionCreate, "", "/report-templates/new", 0},
		{http.MethodPost, "/report-templates", PermissionCreate, "sql_text=never-replay&broken=%", "/report-templates/new", http.StatusBadRequest},
		{http.MethodPost, "/report-templates/7/test", PermissionUpdate, "sql_text=never-replay&broken=%", "/report-templates/7/edit", http.StatusBadRequest},
		{http.MethodPost, "/report-templates/7/test-options", PermissionUpdate, "sql_text=never-replay&broken=%", "/report-templates/7/edit", http.StatusBadRequest},
		{http.MethodPost, "/report-templates/7/state", PermissionChangeState, "status=active&revision=bad", "/report-templates/7", http.StatusUnprocessableEntity},
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
					principal.Permissions = access.NewPermissionSet([]string{operation.permission, "reports.execute"})
				}
				if test.fresh {
					principal.MFAVerifiedAt = time.Now().UTC()
				}
				router, token := templateMFARouter(t, principal)
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
						t.Fatal("POST SQL persisted in redirect")
					}
					if len(response.Result().Cookies()) != 0 {
						t.Fatal("POST state persisted in cookie")
					}
				}
			})
		}
	}
}

type templateMFAAuthentication struct{ principal browserauth.Principal }

func (*templateMFAAuthentication) Login(context.Context, browserauth.LoginInput, time.Time) (browserauth.LoginResult, error) {
	return browserauth.LoginResult{}, browserauth.ErrInvalidCredentials
}
func (*templateMFAAuthentication) Register(context.Context, browserauth.RegisterInput, time.Time) (user.User, error) {
	return user.User{}, nil
}
func (service *templateMFAAuthentication) ResolveSession(context.Context, [32]byte, time.Time) (browserauth.Principal, error) {
	return service.principal, nil
}
func (*templateMFAAuthentication) Logout(context.Context, [32]byte) error { return nil }

func templateMFARouter(t *testing.T, principal browserauth.Principal) (http.Handler, string) {
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
	loader := browserauth.NewHTTP(&templateMFAAuthentication{principal}, renderer, cookies, "Test", false, nil, nil, errors)
	router := chi.NewRouter()
	router.Use(loader.LoadPrincipal, loader.RequireAuth)
	NewHandler(adminshell.New(renderer, registry, "Test", errors), nil, nil).RegisterRoutes(router)
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return router, token
}
