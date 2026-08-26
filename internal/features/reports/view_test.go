package reports

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestReportFormRendersTypedControlsWithoutDumpingAllResultRows(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	multiple := reporting.Parameter{Key: "shops", Label: "Shops", Type: reporting.ParameterMultipleOption, Options: []reporting.ParameterOption{{Value: "A", Label: "Alpha"}}}
	requiredBoolean := reporting.Parameter{Key: "confirmed", Label: "Confirmed", Type: reporting.ParameterBoolean, Required: true}
	optionalBoolean := reporting.Parameter{Key: "reviewed", Label: "Reviewed", Type: reporting.ParameterBoolean}
	decimal := reporting.Parameter{Key: "amount", Label: "Amount", Type: reporting.ParameterDecimal}
	data := ShowData{Report: reporting.Template{ID: 9, Name: "Balances"}, Errors: map[string]string{}, Parameters: []ParameterView{{Value: multiple, Input: reporting.InputValue{Values: []string{"A"}}}, {Value: requiredBoolean}, {Value: optionalBoolean}, {Value: decimal, Input: reporting.InputValue{Values: []string{"12345678901234567890.1200"}}}}, CanExecute: true}
	recorder := httptest.NewRecorder()
	if err := renderer.RenderPage(recorder, 200, "features/reports/show", adminshell.PageData{Title: "Report", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `name="param_shops" value="A" checked`) || !strings.Contains(body, `name="param_confirmed"`) || !strings.Contains(body, "Choose…") || !strings.Contains(body, `name="param_reviewed"`) || !strings.Contains(body, "Any") || !strings.Contains(body, `name="param_amount" value="12345678901234567890.1200" type="text" inputmode="decimal"`) || strings.Contains(body, "placeholder removed") {
		t.Fatalf("typed controls not rendered correctly: %s", body)
	}
}

func TestReportResultAndExportsUseAdminTableAndStatusPatterns(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	resultData := ShowData{Report: reporting.Template{ID: 9, Name: "Balances"}, Result: &reporting.InteractiveResult{}, ResultJSON: template.JS(`{"columns":[],"rows":[]}`)}
	result := httptest.NewRecorder()
	if err := renderer.RenderPage(result, 200, "features/reports/show", adminshell.PageData{Title: "Report", AppName: "Test", Data: resultData}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"No rows returned.", "dark:bg-slate-900", "dark:hover:bg-slate-800/40"} {
		if !strings.Contains(result.Body.String(), want) {
			t.Fatalf("result table missing %q", want)
		}
	}

	expires := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	exports := httptest.NewRecorder()
	data := ExportsData{Rows: []reportexport.Job{{ID: 3, ReportName: "Balances", Status: reportexport.StatusSucceeded, ArtifactExpiresAt: &expires}}}
	if err := renderer.RenderPage(exports, 200, "features/reports/exports", adminshell.PageData{Title: "Exports", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bg-emerald-100", `href="/exports/3/download"`, "Available until"} {
		if !strings.Contains(exports.Body.String(), want) {
			t.Fatalf("export table missing %q", want)
		}
	}
}

func TestReportFormRendersLazyDynamicOptionState(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	dynamic := reporting.Parameter{Key: "city", Label: "City", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT code AS value,name AS label FROM cities", Required: true}
	data := ShowData{Report: reporting.Template{ID: 9, Name: "Cities"}, Parameters: []ParameterView{{Value: dynamic}}, ParametersJSON: template.JS(`[{"key":"city","type":"single_option","option_source":"dynamic","required":true,"default":null,"current":[]}]`), CanExecute: true, CanLoadOptions: true}
	recorder := httptest.NewRecorder()
	if err := renderer.RenderPage(recorder, 200, "features/reports/show", adminshell.PageData{Title: "Report", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`reportParameters('report-parameter-data', 9, true)`, "Loading options…", "Unable to load options.", "Retry", `:disabled="!submittable"`, `:value="stateFor('city').present ? '1' : '0'"`} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("dynamic runtime missing %q", want)
		}
	}
}

func TestFormInputCarriesUnknownParametersToServerValidation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/reports/9/run", strings.NewReader("present_city=1&param_city=001&present_tampered=1&param_tampered=x"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	input := formInput(request, []reporting.Parameter{{Key: "city"}})
	if input["city"].Values[0] != "001" || !input["tampered"].Present || input["tampered"].Values[0] != "x" {
		t.Fatalf("input=%+v", input)
	}
}
