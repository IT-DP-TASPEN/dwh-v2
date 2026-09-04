package fincloudauthprofiles

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView   = "fincloud_auth_profiles.view"
	PermissionManage = "fincloud_auth_profiles.manage"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Fincloud Auth Profiles", Group: "Ingestion", Description: "View nonsecret Fincloud authentication configuration"},
		{Key: PermissionManage, Name: "Manage Fincloud Auth Profiles", Group: "Ingestion", Description: "Create, test, update, activate, disable, and archive Fincloud authentication profiles"},
	}
}
