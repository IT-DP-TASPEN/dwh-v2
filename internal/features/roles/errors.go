package roles

import "errors"

var (
	ErrNotFound            = errors.New("role not found")
	ErrRoleSlugTaken       = errors.New("role slug already exists")
	ErrSelfRolePermissions = errors.New("non-administrator cannot change own role permissions")
	ErrProtectedRole       = errors.New("system role is protected")
	ErrAdminPermissions    = errors.New("administrator permissions cannot be changed")
	ErrRoleAssigned        = errors.New("role is assigned to users")
	ErrUnknownPermission   = errors.New("unknown permission")
)
