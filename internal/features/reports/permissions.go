package reports

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView    = "reports.view"
	PermissionExecute = "reports.execute"
	PermissionExport  = "reports.export"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Reports", Group: "Reports", Description: "View reports explicitly granted to the user"},
		{Key: PermissionExecute, Name: "Execute Reports", Group: "Reports", Description: "Run bounded interactive report previews"},
		{Key: PermissionExport, Name: "Export Reports", Group: "Reports", Description: "Submit and download background report exports"},
	}
}
