package reports

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reporting"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestReportFormRendersTypedControlsWithoutDumpingAllResultRows(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	multiple := reporting.Parameter{Key: "shops", Label: "Shops", Type: reporting.ParameterMultipleOption, Options: []reporting.ParameterOption{{Value: "A", Label: "Alpha"}}}
	data := ShowData{Report: reporting.Template{ID: 9, Name: "Balances"}, Errors: map[string]string{}, Parameters: []ParameterView{{Value: multiple, Input: reporting.InputValue{Values: []string{"A"}}}}, CanExecute: true}
	recorder := httptest.NewRecorder()
	if err := renderer.RenderPage(recorder, 200, "features/reports/show", adminshell.PageData{Title: "Report", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `name="param_shops" value="A" checked`) || strings.Contains(body, "placeholder removed") {
		t.Fatalf("typed controls not rendered correctly: %s", body)
	}
}
