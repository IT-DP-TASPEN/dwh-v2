package reporttemplates

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reporting"
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
	if body := recorder.Body.String(); !strings.Contains(body, `value="7" selected`) || !strings.Contains(body, "tests use the currently saved datasource") {
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
	for _, want := range []string{`hx-post="/report-templates/8/access/4`, `name="grant" value="false"`, "Revoke", "border-red-300"} {
		if !strings.Contains(body, want) {
			t.Fatalf("ACL control missing %q: %s", want, body)
		}
	}
}
