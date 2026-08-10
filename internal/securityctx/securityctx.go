package securityctx

import "github.com/ibldzn/go-admin/internal/access"

type Identity struct {
	UserID   uint64
	Username string
}

type Requester struct {
	Actor             Identity
	Effective         Identity
	EffectiveRoleID   uint64
	EffectiveRoleSlug string
	Permissions       access.PermissionSet
}

func (requester Requester) Can(permission string) bool {
	return requester.IsEffectiveAdmin() || requester.Permissions.Has(permission)
}

func (requester Requester) IsEffectiveAdmin() bool {
	return access.IsAdminRole(requester.EffectiveRoleSlug)
}
