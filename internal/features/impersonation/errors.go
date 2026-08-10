package impersonation

import "errors"

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("impersonation requires an administrator")
	ErrTargetNotFound  = errors.New("impersonation target not found")
	ErrTargetInactive  = errors.New("impersonation target is inactive")
	ErrTargetAdmin     = errors.New("administrator cannot be impersonated")
	ErrSelf            = errors.New("cannot impersonate self")
	ErrAlreadyActive   = errors.New("session is already impersonating")
	ErrNotActive       = errors.New("session is not impersonating")
)
