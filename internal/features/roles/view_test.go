package roles

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestRoleViewRendersExportOversightPermission(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	data := DetailData{
		Detail: Detail{
			Role: Record{ID: 7, Name: "Operations", Slug: "operations"},
			PermissionGroups: []PermissionGroup{{Name: "Reports", Permissions: []PermissionOption{{
				Key: "report_exports.view_all", Name: "View All Report Exports", Description: "Inspect export jobs and download retained export artifacts requested by any user.", Selected: true,
			}}}},
		},
		CanManagePermissions: true,
	}
	response := httptest.NewRecorder()
	if err := renderer.RenderPage(response, 200, "features/roles/show", adminshell.PageData{Title: "Role", AppName: "Test", Data: data}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{`value="report_exports.view_all" checked`, "View All Report Exports", "Inspect export jobs and download retained export artifacts requested by any user."} {
		if !strings.Contains(body, want) {
			t.Fatalf("role permission missing %q: %s", want, body)
		}
	}
}
