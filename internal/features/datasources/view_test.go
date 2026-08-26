package datasources

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reporting"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestDatasourceViewsKeepFormAndStateControls(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	form := httptest.NewRecorder()
	formData := FormData{ID: 7, Name: "Read only", Port: "3306", TLSPolicy: "required", Errors: map[string]string{}}
	if err := renderer.RenderPage(form, 200, "features/datasources/form", adminshell.PageData{Title: "Datasource", AppName: "Test", Data: formData}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="password" type="password"`, `name="tls_policy"`, "dark:bg-slate-900", "dark:bg-slate-950"} {
		if !strings.Contains(form.Body.String(), want) {
			t.Fatalf("datasource form missing %q", want)
		}
	}

	detail := httptest.NewRecorder()
	detailData := DetailData{Value: reporting.Datasource{ID: 7, Name: "Read only", Status: reporting.StatusActive, Revision: 2}, CanState: true, CanTest: true}
	if err := renderer.RenderPage(detail, 200, "features/datasources/show", adminshell.PageData{Title: "Datasource", AppName: "Test", Data: detailData}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Test connection", "Disable", "Archive", "border-orange-300", "border-red-300"} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Fatalf("datasource detail missing %q", want)
		}
	}
}
