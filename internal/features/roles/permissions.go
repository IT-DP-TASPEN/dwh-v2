package roles

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView              = "roles.view"
	PermissionCreate            = "roles.create"
	PermissionUpdate            = "roles.update"
	PermissionDelete            = "roles.delete"
	PermissionAssign            = "roles.assign"
	PermissionManagePermissions = "roles.manage_permissions"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Roles", Group: "Roles", Description: "Allow viewing roles"},
		{Key: PermissionCreate, Name: "Create Roles", Group: "Roles", Description: "Allow creating roles"},
		{Key: PermissionUpdate, Name: "Update Roles", Group: "Roles", Description: "Allow updating roles"},
		{Key: PermissionDelete, Name: "Delete Roles", Group: "Roles", Description: "Allow deleting non-system roles"},
		{Key: PermissionAssign, Name: "Assign Roles", Group: "Roles", Description: "Allow assigning roles to users"},
		{Key: PermissionManagePermissions, Name: "Manage Permissions", Group: "Roles", Description: "Allow managing role permission assignments"},
	}
}
