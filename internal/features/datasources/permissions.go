package datasources

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView        = "datasources.view"
	PermissionCreate      = "datasources.create"
	PermissionUpdate      = "datasources.update"
	PermissionChangeState = "datasources.change_state"
	PermissionTest        = "datasources.test"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Report Datasources", Group: "Report Datasources", Description: "View reporting datasource configuration without credentials"},
		{Key: PermissionCreate, Name: "Create Report Datasources", Group: "Report Datasources", Description: "Create reporting datasource configuration"},
		{Key: PermissionUpdate, Name: "Update Report Datasources", Group: "Report Datasources", Description: "Update reporting datasource configuration and rotate credentials"},
		{Key: PermissionChangeState, Name: "Change Report Datasource State", Group: "Report Datasources", Description: "Activate, disable, or archive reporting datasources"},
		{Key: PermissionTest, Name: "Test Report Datasources", Group: "Report Datasources", Description: "Test reporting datasource connectivity"},
	}
}
