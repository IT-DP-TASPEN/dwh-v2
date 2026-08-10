package users

import "errors"

var (
	ErrNotFound                = errors.New("management record not found")
	ErrAdminMutation           = errors.New("administrator account mutation requires an administrator")
	ErrRoleSubmissionForbidden = errors.New("role assignment is not allowed")
	ErrSelfRoleChange          = errors.New("non-administrator cannot change own role")
	ErrLastActiveAdmin         = errors.New("at least one active administrator must remain")
	ErrSelfDeactivation        = errors.New("cannot deactivate current account")
)
