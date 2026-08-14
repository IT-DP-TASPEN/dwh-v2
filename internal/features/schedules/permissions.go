package schedules

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView          = "schedules.view"
	PermissionCreate        = "schedules.create"
	PermissionUpdate        = "schedules.update"
	PermissionEnableDisable = "schedules.enable_disable"
	PermissionArchive       = "schedules.archive"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Schedules", Group: "Ingestion", Description: "View ingestion schedules and occurrence history"},
		{Key: PermissionCreate, Name: "Create Schedules", Group: "Ingestion", Description: "Create ingestion schedules"},
		{Key: PermissionUpdate, Name: "Update Schedules", Group: "Ingestion", Description: "Update ingestion schedules"},
		{Key: PermissionEnableDisable, Name: "Enable and Disable Schedules", Group: "Ingestion", Description: "Enable or intentionally disable schedules"},
		{Key: PermissionArchive, Name: "Archive Schedules", Group: "Ingestion", Description: "Permanently archive ingestion schedules"},
	}
}
