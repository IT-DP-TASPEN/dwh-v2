package impersonation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/user"
)

type fakeUsers struct {
	found user.User
	err   error
}

func (store fakeUsers) FindByID(context.Context, uint64) (user.User, error) {
	return store.found, store.err
}

type fakeRoles struct {
	found access.Role
	err   error
}

func (store fakeRoles) FindRoleByID(context.Context, uint64) (access.Role, error) {
	return store.found, store.err
}

type fakeTransitions struct {
	attribution audit.Attribution
	started     bool
	stopped     bool
}

func (store *fakeTransitions) Start(_ context.Context, _ [32]byte, targetID uint64, attribution audit.Attribution, _ time.Time, next func() ([32]byte, error)) (auth.Session, error) {
	hash, err := next()
	store.attribution, store.started = attribution, true
	return auth.Session{TokenHash: hash, ImpersonatedUserID: &targetID}, err
}

func (store *fakeTransitions) Stop(_ context.Context, _ [32]byte, attribution audit.Attribution, _ time.Time, next func() ([32]byte, error)) (auth.Session, error) {
	hash, err := next()
	store.attribution, store.stopped = attribution, true
	return auth.Session{TokenHash: hash}, err
}

func TestLifecycleOwnsRulesAndAttribution(t *testing.T) {
	transitions := &fakeTransitions{}
	service := NewService(fakeUsers{found: user.User{ID: 2, Username: "member", RoleID: 2, IsActive: true}}, fakeRoles{found: access.Role{ID: 2, Slug: access.UserRoleSlug}}, transitions)
	service.generateToken = func() (string, error) { return "new-token", nil }
	principal := browserauth.Principal{UserID: 1, Username: "admin", RoleSlug: access.AdminRoleSlug, Actor: browserauth.Identity{UserID: 1, Username: "admin", RoleSlug: access.AdminRoleSlug}}
	result, err := service.Start(context.Background(), principal, auth.HashToken("old"), 2, time.Now())
	if err != nil || result.RawToken != "new-token" || !transitions.started {
		t.Fatalf("start failed: result=%+v err=%v", result, err)
	}
	if transitions.attribution.Actor.UserID != 1 || transitions.attribution.Effective.UserID != 2 {
		t.Fatalf("wrong start attribution: %+v", transitions.attribution)
	}
	principal.UserID, principal.Username, principal.RoleSlug, principal.IsImpersonating = 2, "member", access.UserRoleSlug, true
	if _, err := service.Start(context.Background(), principal, result.Session.TokenHash, 2, time.Now()); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("nested start = %v", err)
	}
	if _, err := service.Stop(context.Background(), principal, result.Session.TokenHash, time.Now()); err != nil || !transitions.stopped {
		t.Fatalf("stop failed: %v", err)
	}
	if transitions.attribution.Actor.UserID != 1 || transitions.attribution.Effective.UserID != 2 {
		t.Fatalf("wrong stop attribution: %+v", transitions.attribution)
	}
}

func TestEligibility(t *testing.T) {
	admin := browserauth.Principal{Actor: browserauth.Identity{UserID: 1, RoleSlug: access.AdminRoleSlug}}
	if !CanStart(admin, 2, access.UserRoleSlug, true) || CanStart(admin, 1, access.UserRoleSlug, true) || CanStart(admin, 2, access.AdminRoleSlug, true) {
		t.Fatal("unexpected eligibility")
	}
	admin.IsImpersonating = true
	if CanStart(admin, 2, access.UserRoleSlug, true) {
		t.Fatal("nested impersonation eligible")
	}
}

func TestStartRejectsInvalidTargets(t *testing.T) {
	admin := browserauth.Principal{UserID: 1, RoleSlug: access.AdminRoleSlug, Actor: browserauth.Identity{UserID: 1, RoleSlug: access.AdminRoleSlug}}
	for _, test := range []struct {
		name      string
		principal browserauth.Principal
		users     fakeUsers
		roles     fakeRoles
		targetID  uint64
		want      error
	}{
		{name: "non-admin actor", principal: browserauth.Principal{Actor: browserauth.Identity{RoleSlug: access.UserRoleSlug}}, targetID: 2, want: ErrForbidden},
		{name: "self", principal: admin, targetID: 1, want: ErrSelf},
		{name: "missing", principal: admin, users: fakeUsers{err: user.ErrNotFound}, targetID: 2, want: ErrTargetNotFound},
		{name: "inactive", principal: admin, users: fakeUsers{found: user.User{ID: 2, RoleID: 2}}, roles: fakeRoles{found: access.Role{ID: 2, Slug: access.UserRoleSlug}}, targetID: 2, want: ErrTargetInactive},
		{name: "administrator", principal: admin, users: fakeUsers{found: user.User{ID: 2, RoleID: 1, IsActive: true}}, roles: fakeRoles{found: access.Role{ID: 1, Slug: access.AdminRoleSlug}}, targetID: 2, want: ErrTargetAdmin},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewService(test.users, test.roles, &fakeTransitions{}).Start(context.Background(), test.principal, [32]byte{1}, test.targetID, time.Now())
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}
