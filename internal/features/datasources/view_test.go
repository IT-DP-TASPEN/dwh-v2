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
	for _, want := range []string{`name="password" type="password"`, `name="tls_policy"`, `name="network"`, `value="unix"`, `name="socket_path"`, `:disabled="network !== 'tcp'"`, "dark:bg-slate-900", "dark:bg-slate-950"} {
		if !strings.Contains(form.Body.String(), want) {
			t.Fatalf("datasource form missing %q", want)
		}
	}

	detail := httptest.NewRecorder()
	detailData := DetailData{Value: reporting.Datasource{ID: 7, Name: "Read only", Status: reporting.StatusActive, Revision: 2}, CanState: true, CanTest: true}
	if err := renderer.RenderPage(detail, 200, "features/datasources/show", adminshell.PageData{Title: "Datasource", AppName: "Test", Data: detailData}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Test connection", "Disable", "Archive", "border-orange-300", "btn-danger-outline"} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Fatalf("datasource detail missing %q", want)
		}
	}
}

func TestUnixDatasourceViews(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	form := httptest.NewRecorder()
	data := FormData{ID: 7, Network: "unix", OriginalNetwork: "unix", SocketPath: "/tmp/mysql.sock", Username: "dwhadmin", Errors: map[string]string{}}
	if err := renderer.RenderPage(form, 200, "features/datasources/form", adminshell.PageData{Data: data}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-network="unix"`, `data-original-network="unix"`, `value="/tmp/mysql.sock"`, "process identity", `originalNetwork === 'unix'`} {
		if !strings.Contains(form.Body.String(), want) {
			t.Fatalf("Unix form missing %q", want)
		}
	}
	detail := httptest.NewRecorder()
	if err := renderer.RenderPage(detail, 200, "features/datasources/show", adminshell.PageData{Data: DetailData{Value: reporting.Datasource{Network: "unix", SocketPath: "/tmp/mysql.sock", Host: "ignored", Port: 3306}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Body.String(), "Unix socket: /tmp/mysql.sock") || !strings.Contains(detail.Body.String(), "Not applicable") || strings.Contains(detail.Body.String(), "ignored:3306") {
		t.Fatal("Unix detail shows a TCP endpoint or TLS policy")
	}
}
