package reports

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView           = "reports.view"
	PermissionExecute        = "reports.execute"
	PermissionExport         = "reports.export"
	PermissionViewAllExports = "report_exports.view_all"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Reports", Group: "Reports", Description: "View reports explicitly granted to the user"},
		{Key: PermissionExecute, Name: "Execute Reports", Group: "Reports", Description: "Run bounded interactive report previews"},
		{Key: PermissionExport, Name: "Export Reports", Group: "Reports", Description: "Submit and download background report exports"},
		{Key: PermissionViewAllExports, Name: "View All Report Exports", Group: "Reports", Description: "Inspect export jobs and download retained export artifacts requested by any user."},
	}
}
