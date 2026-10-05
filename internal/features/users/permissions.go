package users

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionMFAReset      = "users.mfa.reset"
	PermissionView          = "users.view"
	PermissionCreate        = "users.create"
	PermissionUpdate        = "users.update"
	PermissionDisable       = "users.disable"
	PermissionResetPassword = "users.reset_password"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionMFAReset, Name: "Reset MFA", Group: "Users", Description: "Revoke MFA credentials and require enrollment"},
		{Key: PermissionView, Name: "View Users", Group: "Users", Description: "Allow viewing users"},
		{Key: PermissionCreate, Name: "Create Users", Group: "Users", Description: "Allow creating users"},
		{Key: PermissionUpdate, Name: "Update Users", Group: "Users", Description: "Allow updating users"},
		{Key: PermissionDisable, Name: "Disable Users", Group: "Users", Description: "Allow activating and deactivating users"},
		{Key: PermissionResetPassword, Name: "Reset Password", Group: "Users", Description: "Allow resetting another user's password"},
	}
}
