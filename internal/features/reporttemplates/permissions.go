package reporttemplates

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView         = "report_templates.view"
	PermissionCreate       = "report_templates.create"
	PermissionUpdate       = "report_templates.update"
	PermissionChangeState  = "report_templates.change_state"
	PermissionManageAccess = "report_templates.manage_access"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Report Templates", Group: "Report Templates", Description: "View report template definitions"},
		{Key: PermissionCreate, Name: "Create Report Templates", Group: "Report Templates", Description: "Create disabled report template drafts"},
		{Key: PermissionUpdate, Name: "Update Report Templates", Group: "Report Templates", Description: "Update report SQL and parameters"},
		{Key: PermissionChangeState, Name: "Change Report Template State", Group: "Report Templates", Description: "Activate, disable, or archive reports"},
		{Key: PermissionManageAccess, Name: "Manage Report Access", Group: "Report Templates", Description: "Grant and revoke per-user report access"},
	}
}
