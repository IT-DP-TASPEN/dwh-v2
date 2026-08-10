package dashboard

import "github.com/ibldzn/go-admin/internal/access"

const PermissionView = "dashboard.view"

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{{
		Key: PermissionView, Name: "View Dashboard", Group: "Dashboard", Description: "Allow viewing the admin dashboard",
	}}
}
