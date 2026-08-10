package users

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
)

type fakeStore struct {
	users   map[uint64]UserRecord
	created user.CreateParams
}

func (store *fakeStore) CountUsers(context.Context, string) (int64, error) {
	return int64(len(store.users)), nil
}
func (store *fakeStore) ListUsers(context.Context, string, int, int) ([]UserRecord, error) {
	return nil, nil
}
func (store *fakeStore) FindUserByID(_ context.Context, id uint64) (UserRecord, error) {
	found, ok := store.users[id]
	if !ok {
		return UserRecord{}, ErrNotFound
	}
	return found, nil
}
func (store *fakeStore) CreateUser(_ context.Context, _ securityctx.Requester, params user.CreateParams, _ time.Time) (UserRecord, error) {
	store.created = params
	return UserRecord{ID: 9, Username: params.Username, Name: params.Name, RoleID: params.RoleID, IsActive: params.IsActive}, nil
}
func (*fakeStore) UpdateUserProfile(context.Context, securityctx.Requester, uint64, string, string, time.Time) (UserRecord, error) {
	return UserRecord{}, nil
}
func (*fakeStore) AssignUserRole(context.Context, securityctx.Requester, uint64, uint64, time.Time) error {
	return nil
}
func (*fakeStore) SetUserActive(context.Context, securityctx.Requester, uint64, bool, time.Time) error {
	return nil
}
func (*fakeStore) ResetUserPassword(context.Context, securityctx.Requester, uint64, string, time.Time) error {
	return nil
}

type fakeRoles struct{ roles []access.Role }

func (store fakeRoles) ListRoles(context.Context) ([]access.Role, error) {
	return append([]access.Role(nil), store.roles...), nil
}
func (store fakeRoles) FindRoleByID(_ context.Context, id uint64) (access.Role, error) {
	for _, role := range store.roles {
		if role.ID == id {
			return role, nil
		}
	}
	return access.Role{}, access.ErrNotFound
}
func (store fakeRoles) FindRoleBySlug(_ context.Context, slug string) (access.Role, error) {
	for _, role := range store.roles {
		if role.Slug == slug {
			return role, nil
		}
	}
	return access.Role{}, access.ErrNotFound
}

func TestCreateUsesEffectivePermissionsAndDefaultRole(t *testing.T) {
	store := &fakeStore{users: map[uint64]UserRecord{1: {ID: 1, RoleSlug: access.AdminRoleSlug}, 2: {ID: 2, RoleSlug: access.UserRoleSlug}}}
	roles := fakeRoles{[]access.Role{{ID: 1, Slug: access.AdminRoleSlug}, {ID: 2, Slug: access.UserRoleSlug}}}
	service := NewService(store, roles, "roles.assign")
	service.hashPassword = func(string) (string, error) { return "hash", nil }
	requester := securityctx.Requester{Effective: securityctx.Identity{UserID: 2}, EffectiveRoleSlug: access.UserRoleSlug}
	input := CreateUserInput{Name: " Member ", Username: " MEMBER ", Password: "long-enough-password", PasswordConfirmation: "long-enough-password"}
	if _, err := service.CreateUser(context.Background(), requester, input, time.Now()); err != nil {
		t.Fatal(err)
	}
	if store.created.RoleID != 2 || store.created.Username != "member" || store.created.PasswordHash != "hash" || !store.created.IsActive {
		t.Fatalf("unexpected creation: %+v", store.created)
	}
	adminID := uint64(1)
	input.RoleID = &adminID
	if _, err := service.CreateUser(context.Background(), requester, input, time.Now()); !errors.Is(err, ErrRoleSubmissionForbidden) {
		t.Fatalf("unpermitted role submission = %v", err)
	}
	requester.Permissions = access.NewPermissionSet([]string{"roles.assign"})
	if _, err := service.CreateUser(context.Background(), requester, input, time.Now()); !errors.Is(err, ErrAdminMutation) {
		t.Fatalf("non-admin admin creation = %v", err)
	}
}

func TestEffectiveAdminSeesAllRoles(t *testing.T) {
	roles := fakeRoles{[]access.Role{{ID: 1, Slug: access.AdminRoleSlug}, {ID: 2, Slug: access.UserRoleSlug}}}
	service := NewService(&fakeStore{}, roles, "roles.assign")
	nonAdmin, err := service.AvailableRoles(context.Background(), securityctx.Requester{EffectiveRoleSlug: access.UserRoleSlug})
	if err != nil || len(nonAdmin) != 1 || access.IsAdminRole(nonAdmin[0].Slug) {
		t.Fatalf("non-admin roles = %+v err=%v", nonAdmin, err)
	}
	admin, err := service.AvailableRoles(context.Background(), securityctx.Requester{EffectiveRoleSlug: access.AdminRoleSlug})
	if err != nil || len(admin) != 2 {
		t.Fatalf("admin roles = %+v err=%v", admin, err)
	}
}
