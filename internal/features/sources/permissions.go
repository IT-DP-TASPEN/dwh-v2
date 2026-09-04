package sources

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView   = "sources.view"
	PermissionManage = "sources.manage"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Sources", Group: "Ingestion", Description: "View ingestion source settings"},
		{Key: PermissionManage, Name: "Manage Sources", Group: "Ingestion", Description: "Enable, disable, and assign authentication to ingestion sources"},
	}
}
