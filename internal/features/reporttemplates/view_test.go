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
	if body := recorder.Body.String(); !strings.Contains(body, `value="7" selected`) {
		t.Fatalf("datasource was not selected: %s", body)
	}
}
