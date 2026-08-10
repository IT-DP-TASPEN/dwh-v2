package access

import (
	"context"
	"fmt"
)

type PermissionStore interface {
	RoleHasPermission(context.Context, uint64, string) (bool, error)
}

type Authorizer struct {
	store PermissionStore
}

func NewAuthorizer(store PermissionStore) *Authorizer {
	return &Authorizer{store: store}
}

func (a *Authorizer) Can(ctx context.Context, role Role, permission string) (bool, error) {
	if IsAdminRole(role.Slug) {
		return true, nil
	}

	allowed, err := a.store.RoleHasPermission(ctx, role.ID, permission)
	if err != nil {
		return false, fmt.Errorf("authorize role %q for %q: %w", role.Slug, permission, err)
	}
	return allowed, nil
}
