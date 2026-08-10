package access

import (
	"context"
	"errors"
	"testing"
)

type fakePermissionStore struct {
	allowed bool
	err     error
	calls   int
}

func (store *fakePermissionStore) RoleHasPermission(context.Context, uint64, string) (bool, error) {
	store.calls++
	return store.allowed, store.err
}

func TestAuthorizerAdminBypassesStore(t *testing.T) {
	store := &fakePermissionStore{err: errors.New("must not be called")}
	allowed, err := NewAuthorizer(store).Can(context.Background(), Role{Slug: AdminRoleSlug}, "anything.at_all")
	if err != nil || !allowed {
		t.Fatalf("expected admin bypass, got allowed=%v err=%v", allowed, err)
	}
	if store.calls != 0 {
		t.Fatalf("expected no store calls, got %d", store.calls)
	}
}

func TestAuthorizerUsesStore(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		store := &fakePermissionStore{allowed: allowed}
		got, err := NewAuthorizer(store).Can(context.Background(), Role{ID: 7, Slug: "manager"}, "users.view")
		if err != nil || got != allowed {
			t.Fatalf("expected %v, got allowed=%v err=%v", allowed, got, err)
		}
	}
}

func TestAuthorizerPropagatesStoreError(t *testing.T) {
	want := errors.New("database unavailable")
	_, err := NewAuthorizer(&fakePermissionStore{err: want}).Can(context.Background(), Role{ID: 7, Slug: "manager"}, "users.view")
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
}
