package auditlogs

import "github.com/ibldzn/go-admin/internal/access"

const PermissionView = "audit.view"

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{{
		Key: PermissionView, Name: "View Audit Logs", Group: "Audit", Description: "View application audit history.",
	}}
}
