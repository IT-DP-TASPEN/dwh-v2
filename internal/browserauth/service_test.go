package browserauth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/user"
)

type fakeUsers struct {
	byUsername      user.User
	findUsernameErr error
	findUsernameArg string
	byID            user.User
	byIDs           map[uint64]user.User
	findIDErr       error
	findIDErrs      map[uint64]error
	created         user.User
	createParams    user.CreateParams
	createErr       error
	lastLoginAt     time.Time
	updateLastErr   error
}

func (store *fakeUsers) Create(_ context.Context, params user.CreateParams, now time.Time) (user.User, error) {
	store.createParams = params
	if store.createErr != nil {
		return user.User{}, store.createErr
	}
	created := store.created
	if created.ID == 0 {
		created = user.User{ID: 8, Username: params.Username, Name: params.Name, RoleID: params.RoleID, IsActive: params.IsActive, CreatedAt: now}
	}
	return created, nil
}

func (store *fakeUsers) FindByID(_ context.Context, id uint64) (user.User, error) {
	if err := store.findIDErrs[id]; err != nil {
		return user.User{}, err
	}
	if found, ok := store.byIDs[id]; ok {
		return found, nil
	}
	return store.byID, store.findIDErr
}

func (store *fakeUsers) FindByUsername(_ context.Context, username string) (user.User, error) {
	store.findUsernameArg = username
	return store.byUsername, store.findUsernameErr
}

func (store *fakeUsers) UpdateLastLoginAt(_ context.Context, _ uint64, now time.Time) error {
	store.lastLoginAt = now
	return store.updateLastErr
}

type fakeRoles struct {
	byID            access.Role
	byIDs           map[uint64]access.Role
	findIDErr       error
	findIDErrs      map[uint64]error
	bySlug          access.Role
	findSlugErr     error
	slugArg         string
	permissionKeys  []string
	permissionErr   error
	permissionCalls int
}

func (store *fakeRoles) FindRoleByID(_ context.Context, id uint64) (access.Role, error) {
	if err := store.findIDErrs[id]; err != nil {
		return access.Role{}, err
	}
	if found, ok := store.byIDs[id]; ok {
		return found, nil
	}
	return store.byID, store.findIDErr
}

func (store *fakeRoles) FindRoleBySlug(_ context.Context, slug string) (access.Role, error) {
	store.slugArg = slug
	return store.bySlug, store.findSlugErr
}

func (store *fakeRoles) ListPermissionKeysForRole(context.Context, uint64) ([]string, error) {
	store.permissionCalls++
	return store.permissionKeys, store.permissionErr
}

type fakeSessions struct {
	createParams auth.CreateSessionParams
	createErr    error
	found        auth.Session
	findErr      error
	touchedID    uint64
	touchedAt    time.Time
	touchErr     error
	revokedHash  [32]byte
	revokeErr    error
}

func (store *fakeSessions) Create(_ context.Context, params auth.CreateSessionParams, now time.Time) (auth.Session, error) {
	store.createParams = params
	if store.createErr != nil {
		return auth.Session{}, store.createErr
	}
	return auth.Session{
		ID:         11,
		UserID:     params.UserID,
		TokenHash:  params.TokenHash,
		RememberMe: params.RememberMe,
		ExpiresAt:  params.ExpiresAt,
		LastSeenAt: params.LastSeenAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (store *fakeSessions) FindValidByTokenHash(context.Context, [32]byte, time.Time) (auth.Session, error) {
	return store.found, store.findErr
}

func (store *fakeSessions) UpdateLastSeenAt(_ context.Context, id uint64, now time.Time) error {
	store.touchedID = id
	store.touchedAt = now
	return store.touchErr
}

func (store *fakeSessions) Revoke(_ context.Context, hash [32]byte) error {
	store.revokedHash = hash
	return store.revokeErr
}

func TestLoginCreatesFreshHashedSession(t *testing.T) {
	password := "correct horse battery staple"
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 9, 10, 11, 12, 0, time.FixedZone("test", 7*60*60))

	for _, test := range []struct {
		name     string
		remember bool
		lifetime time.Duration
	}{
		{name: "normal", lifetime: 24 * time.Hour},
		{name: "remember", remember: true, lifetime: 30 * 24 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			users := &fakeUsers{byUsername: user.User{ID: 4, PasswordHash: passwordHash, IsActive: true}, updateLastErr: errors.New("metadata unavailable")}
			sessions := &fakeSessions{}
			service := newTestService(t, users, &fakeRoles{}, sessions)

			result, err := service.Login(context.Background(), LoginInput{Username: "  ADMIN ", Password: password, RememberMe: test.remember}, now)
			if err != nil {
				t.Fatal(err)
			}
			if users.findUsernameArg != "admin" || sessions.createParams.UserID != 4 || sessions.createParams.RememberMe != test.remember {
				t.Fatalf("unexpected login persistence: username=%q params=%+v", users.findUsernameArg, sessions.createParams)
			}
			if sessions.createParams.TokenHash != auth.HashToken(result.RawToken) || result.RawToken == "" {
				t.Fatal("session repository did not receive only the raw token hash")
			}
			wantNow := now.UTC()
			if !sessions.createParams.ExpiresAt.Equal(wantNow.Add(test.lifetime)) || !sessions.createParams.LastSeenAt.Equal(wantNow) || !users.lastLoginAt.Equal(wantNow) {
				t.Fatalf("unexpected timestamps: params=%+v last_login=%v", sessions.createParams, users.lastLoginAt)
			}
		})
	}
}

func TestLoginCredentialFailures(t *testing.T) {
	t.Run("unknown user executes dummy hash", func(t *testing.T) {
		service := newTestService(t, &fakeUsers{findUsernameErr: user.ErrNotFound}, &fakeRoles{}, &fakeSessions{})
		called := false
		service.verifyPassword = func(password, encoded string) (bool, error) {
			called = password == "not the password" && encoded == service.dummyHash
			return false, nil
		}
		_, err := service.Login(context.Background(), LoginInput{Username: "missing", Password: "not the password"}, time.Now())
		if !errors.Is(err, ErrInvalidCredentials) || !called {
			t.Fatalf("expected generic failure and dummy verify, got err=%v called=%v", err, called)
		}
	})

	t.Run("inactive user still verifies password", func(t *testing.T) {
		service := newTestService(t, &fakeUsers{byUsername: user.User{ID: 3, PasswordHash: "encoded", IsActive: false}}, &fakeRoles{}, &fakeSessions{})
		called := false
		service.verifyPassword = func(password, encoded string) (bool, error) {
			called = password == "correct password" && encoded == "encoded"
			return true, nil
		}
		_, err := service.Login(context.Background(), LoginInput{Username: "inactive", Password: "correct password"}, time.Now())
		if !errors.Is(err, ErrInvalidCredentials) || !called {
			t.Fatalf("expected verified inactive failure, got err=%v called=%v", err, called)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		service := newTestService(t, &fakeUsers{byUsername: user.User{PasswordHash: "encoded", IsActive: true}}, &fakeRoles{}, &fakeSessions{})
		service.verifyPassword = func(string, string) (bool, error) { return false, nil }
		_, err := service.Login(context.Background(), LoginInput{Username: "known", Password: "wrong password"}, time.Now())
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected generic failure, got %v", err)
		}
	})

	t.Run("database error propagates", func(t *testing.T) {
		databaseError := errors.New("database unavailable")
		service := newTestService(t, &fakeUsers{findUsernameErr: databaseError}, &fakeRoles{}, &fakeSessions{})
		_, err := service.Login(context.Background(), LoginInput{Username: "known", Password: "valid length password"}, time.Now())
		if !errors.Is(err, databaseError) || errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected database error, got %v", err)
		}
	})
}

func TestRegisterUsesUserRole(t *testing.T) {
	users := &fakeUsers{}
	roles := &fakeRoles{bySlug: access.Role{ID: 21, Slug: access.UserRoleSlug}}
	service := newTestService(t, users, roles, &fakeSessions{})
	now := time.Date(2026, 8, 9, 3, 4, 5, 0, time.FixedZone("test", 7*60*60))

	created, err := service.Register(context.Background(), RegisterInput{
		Name:                 "  Example User ",
		Username:             "  EXAMPLE ",
		Password:             "correct horse battery staple",
		PasswordConfirmation: "correct horse battery staple",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if roles.slugArg != access.UserRoleSlug || users.createParams.RoleID != 21 || !users.createParams.IsActive || users.createParams.Username != "example" || users.createParams.Name != "Example User" {
		t.Fatalf("unexpected registration: role=%q params=%+v", roles.slugArg, users.createParams)
	}
	verified, err := auth.VerifyPassword("correct horse battery staple", users.createParams.PasswordHash)
	if err != nil || !verified || created.RoleID != 21 {
		t.Fatalf("unexpected password/user: verified=%v err=%v user=%+v", verified, err, created)
	}
}

func TestRegisterExpectedErrors(t *testing.T) {
	service := newTestService(t, &fakeUsers{}, &fakeRoles{bySlug: access.Role{ID: 2}}, &fakeSessions{})
	_, err := service.Register(context.Background(), RegisterInput{Name: "User", Username: "user", Password: "correct horse battery staple", PasswordConfirmation: "different password value"}, time.Now())
	if !errors.Is(err, ErrPasswordConfirmation) {
		t.Fatalf("expected confirmation error, got %v", err)
	}

	users := &fakeUsers{createErr: user.ErrUsernameTaken}
	service = newTestService(t, users, &fakeRoles{bySlug: access.Role{ID: 2}}, &fakeSessions{})
	_, err = service.Register(context.Background(), RegisterInput{Name: "User", Username: "user", Password: "correct horse battery staple", PasswordConfirmation: "correct horse battery staple"}, time.Now())
	if !errors.Is(err, user.ErrUsernameTaken) {
		t.Fatalf("expected duplicate username error, got %v", err)
	}
}

func TestResolveSession(t *testing.T) {
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	tokenHash := auth.HashToken("token")
	sessions := &fakeSessions{found: auth.Session{ID: 31, UserID: 7, RememberMe: true, LastSeenAt: now.Add(-LastSeenTouchInterval)}}
	users := &fakeUsers{byID: user.User{ID: 7, Username: "admin", Name: "Admin", RoleID: 4, IsActive: true}}
	roles := &fakeRoles{byID: access.Role{ID: 4, Slug: access.AdminRoleSlug}}
	service := newTestService(t, users, roles, sessions)

	principal, err := service.ResolveSession(context.Background(), tokenHash, now)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != 7 || principal.Actor.UserID != 7 || principal.Actor.Username != "admin" || principal.IsImpersonating || principal.RoleSlug != access.AdminRoleSlug || principal.SessionID != 31 || !principal.RememberMe || sessions.touchedID != 31 || !sessions.touchedAt.Equal(now) {
		t.Fatalf("unexpected principal/touch: principal=%+v sessions=%+v", principal, sessions)
	}
}

func TestResolveSessionPermissions(t *testing.T) {
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)

	t.Run("admin bypasses permission query", func(t *testing.T) {
		roles := &fakeRoles{byID: access.Role{ID: 1, Name: "Administrator", Slug: access.AdminRoleSlug}, permissionErr: errors.New("must not query")}
		service := newTestService(t,
			&fakeUsers{byID: user.User{ID: 1, RoleID: 1, IsActive: true}},
			roles,
			&fakeSessions{found: auth.Session{UserID: 1, LastSeenAt: now}},
		)

		principal, err := service.ResolveSession(context.Background(), auth.HashToken("token"), now)
		if err != nil || !principal.Can("unknown.permission") || roles.permissionCalls != 0 {
			t.Fatalf("unexpected admin resolution: principal=%+v calls=%d err=%v", principal, roles.permissionCalls, err)
		}
	})

	t.Run("non-admin loads permission set once", func(t *testing.T) {
		roles := &fakeRoles{byID: access.Role{ID: 2, Name: "User", Slug: access.UserRoleSlug}, permissionKeys: []string{"sample.view"}}
		service := newTestService(t,
			&fakeUsers{byID: user.User{ID: 2, RoleID: 2, IsActive: true}},
			roles,
			&fakeSessions{found: auth.Session{UserID: 2, LastSeenAt: now}},
		)

		principal, err := service.ResolveSession(context.Background(), auth.HashToken("token"), now)
		if err != nil || !principal.Can("sample.view") || principal.Can("users.view") || roles.permissionCalls != 1 {
			t.Fatalf("unexpected permission resolution: principal=%+v calls=%d err=%v", principal, roles.permissionCalls, err)
		}
	})

	t.Run("non-admin supports empty permission set", func(t *testing.T) {
		roles := &fakeRoles{byID: access.Role{ID: 2, Slug: access.UserRoleSlug}}
		service := newTestService(t,
			&fakeUsers{byID: user.User{ID: 2, RoleID: 2, IsActive: true}},
			roles,
			&fakeSessions{found: auth.Session{UserID: 2, LastSeenAt: now}},
		)

		principal, err := service.ResolveSession(context.Background(), auth.HashToken("token"), now)
		if err != nil || principal.Can("sample.view") || roles.permissionCalls != 1 {
			t.Fatalf("unexpected empty permission resolution: principal=%+v calls=%d err=%v", principal, roles.permissionCalls, err)
		}
	})

	t.Run("permission query error propagates", func(t *testing.T) {
		want := errors.New("database unavailable")
		roles := &fakeRoles{byID: access.Role{ID: 2, Slug: access.UserRoleSlug}, permissionErr: want}
		service := newTestService(t,
			&fakeUsers{byID: user.User{ID: 2, RoleID: 2, IsActive: true}},
			roles,
			&fakeSessions{found: auth.Session{UserID: 2, LastSeenAt: now}},
		)

		_, err := service.ResolveSession(context.Background(), auth.HashToken("token"), now)
		if !errors.Is(err, want) || roles.permissionCalls != 1 {
			t.Fatalf("expected wrapped permission error, calls=%d err=%v", roles.permissionCalls, err)
		}
	})
}

func TestResolveImpersonatedSessionUsesTargetIdentityAndPermissions(t *testing.T) {
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	targetID := uint64(2)
	hash := auth.HashToken("token")
	sessions := &fakeSessions{found: auth.Session{ID: 31, UserID: 1, ImpersonatedUserID: &targetID, RememberMe: true, LastSeenAt: now}}
	users := &fakeUsers{byIDs: map[uint64]user.User{
		1: {ID: 1, Username: "admin", Name: "Administrator", RoleID: 1, IsActive: true},
		2: {ID: 2, Username: "branch", Name: "Branch User", RoleID: 3, IsActive: true},
	}}
	roles := &fakeRoles{
		byIDs: map[uint64]access.Role{
			1: {ID: 1, Name: "Administrator", Slug: access.AdminRoleSlug},
			3: {ID: 3, Name: "Branch", Slug: "branch-user"},
		},
		permissionKeys: []string{"sample.view", "users.view"},
	}
	service := newTestService(t, users, roles, sessions)

	principal, err := service.ResolveSession(context.Background(), hash, now)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.IsImpersonating || principal.UserID != 2 || principal.Username != "branch" || principal.RoleSlug != "branch-user" {
		t.Fatalf("unexpected effective identity: %+v", principal)
	}
	if principal.Actor.UserID != 1 || principal.Actor.Username != "admin" || principal.Actor.RoleSlug != access.AdminRoleSlug {
		t.Fatalf("unexpected actor identity: %+v", principal.Actor)
	}
	if !principal.Can("sample.view") || !principal.Can("users.view") || principal.Can("roles.view") || roles.permissionCalls != 1 {
		t.Fatalf("administrator permissions leaked: principal=%+v calls=%d", principal, roles.permissionCalls)
	}
}

func TestResolveImpersonatedSessionRevokesInvalidIdentityState(t *testing.T) {
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	hash := auth.HashToken("token")
	targetID := uint64(2)

	tests := []struct {
		name     string
		users    map[uint64]user.User
		userErrs map[uint64]error
		roles    map[uint64]access.Role
		roleErrs map[uint64]error
	}{
		{name: "inactive actor", users: map[uint64]user.User{1: {ID: 1, RoleID: 1}, 2: {ID: 2, RoleID: 2, IsActive: true}}, roles: map[uint64]access.Role{1: {ID: 1, Slug: access.AdminRoleSlug}, 2: {ID: 2, Slug: access.UserRoleSlug}}},
		{name: "actor lost admin", users: map[uint64]user.User{1: {ID: 1, RoleID: 2, IsActive: true}, 2: {ID: 2, RoleID: 2, IsActive: true}}, roles: map[uint64]access.Role{2: {ID: 2, Slug: access.UserRoleSlug}}},
		{name: "missing actor role", users: map[uint64]user.User{1: {ID: 1, RoleID: 9, IsActive: true}, 2: {ID: 2, RoleID: 2, IsActive: true}}, roles: map[uint64]access.Role{2: {ID: 2, Slug: access.UserRoleSlug}}, roleErrs: map[uint64]error{9: access.ErrNotFound}},
		{name: "missing target", users: map[uint64]user.User{1: {ID: 1, RoleID: 1, IsActive: true}}, userErrs: map[uint64]error{2: user.ErrNotFound}, roles: map[uint64]access.Role{1: {ID: 1, Slug: access.AdminRoleSlug}}},
		{name: "inactive target", users: map[uint64]user.User{1: {ID: 1, RoleID: 1, IsActive: true}, 2: {ID: 2, RoleID: 2}}, roles: map[uint64]access.Role{1: {ID: 1, Slug: access.AdminRoleSlug}, 2: {ID: 2, Slug: access.UserRoleSlug}}},
		{name: "target promoted to admin", users: map[uint64]user.User{1: {ID: 1, RoleID: 1, IsActive: true}, 2: {ID: 2, RoleID: 1, IsActive: true}}, roles: map[uint64]access.Role{1: {ID: 1, Slug: access.AdminRoleSlug}}},
		{name: "missing target role", users: map[uint64]user.User{1: {ID: 1, RoleID: 1, IsActive: true}, 2: {ID: 2, RoleID: 9, IsActive: true}}, roles: map[uint64]access.Role{1: {ID: 1, Slug: access.AdminRoleSlug}}, roleErrs: map[uint64]error{9: access.ErrNotFound}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessions := &fakeSessions{found: auth.Session{ID: 31, UserID: 1, ImpersonatedUserID: &targetID, LastSeenAt: now}}
			service := newTestService(t,
				&fakeUsers{byIDs: test.users, findIDErrs: test.userErrs},
				&fakeRoles{byIDs: test.roles, findIDErrs: test.roleErrs},
				sessions,
			)
			_, err := service.ResolveSession(context.Background(), hash, now)
			if !errors.Is(err, ErrUnauthenticated) || sessions.revokedHash != hash {
				t.Fatalf("expected full revocation: err=%v hash=%x", err, sessions.revokedHash)
			}
		})
	}
}

func TestResolveSessionTouchIsThrottledAndBestEffort(t *testing.T) {
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		lastSeenAt time.Time
		touchErr   error
		wantTouch  bool
	}{
		{name: "recent activity", lastSeenAt: now.Add(-LastSeenTouchInterval + time.Second)},
		{name: "touch failure does not lose authentication", lastSeenAt: now.Add(-LastSeenTouchInterval), touchErr: errors.New("write unavailable"), wantTouch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := &fakeSessions{found: auth.Session{ID: 31, UserID: 7, LastSeenAt: test.lastSeenAt}, touchErr: test.touchErr}
			users := &fakeUsers{byID: user.User{ID: 7, Username: "user", Name: "User", RoleID: 4, IsActive: true}}
			roles := &fakeRoles{byID: access.Role{ID: 4, Slug: access.UserRoleSlug}}
			service := newTestService(t, users, roles, sessions)

			principal, err := service.ResolveSession(context.Background(), auth.HashToken("token"), now)
			if err != nil || principal.UserID != 7 {
				t.Fatalf("authentication failed: principal=%+v err=%v", principal, err)
			}
			if (sessions.touchedID != 0) != test.wantTouch {
				t.Fatalf("touch=%v, want %v", sessions.touchedID != 0, test.wantTouch)
			}
		})
	}
}

func TestResolveSessionExpectedInvalidation(t *testing.T) {
	now := time.Now().UTC()
	hash := auth.HashToken("token")

	t.Run("unknown session", func(t *testing.T) {
		service := newTestService(t, &fakeUsers{}, &fakeRoles{}, &fakeSessions{findErr: auth.ErrSessionNotFound})
		_, err := service.ResolveSession(context.Background(), hash, now)
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("expected unauthenticated, got %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		user    user.User
		userErr error
	}{
		{name: "missing user", userErr: user.ErrNotFound},
		{name: "inactive user", user: user.User{ID: 4, IsActive: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := &fakeSessions{found: auth.Session{ID: 2, UserID: 4}}
			service := newTestService(t, &fakeUsers{byID: test.user, findIDErr: test.userErr}, &fakeRoles{}, sessions)
			_, err := service.ResolveSession(context.Background(), hash, now)
			if !errors.Is(err, ErrUnauthenticated) || sessions.revokedHash != hash {
				t.Fatalf("expected revocation, got err=%v hash=%x", err, sessions.revokedHash)
			}
		})
	}
}

func TestResolveSessionErrors(t *testing.T) {
	databaseError := errors.New("database unavailable")
	service := newTestService(t, &fakeUsers{}, &fakeRoles{}, &fakeSessions{findErr: databaseError})
	_, err := service.ResolveSession(context.Background(), auth.HashToken("token"), time.Now())
	if !errors.Is(err, databaseError) || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected database error, got %v", err)
	}

	sessions := &fakeSessions{found: auth.Session{UserID: 4}, revokeErr: databaseError}
	service = newTestService(t, &fakeUsers{byID: user.User{ID: 4, IsActive: false}}, &fakeRoles{}, sessions)
	_, err = service.ResolveSession(context.Background(), auth.HashToken("token"), time.Now())
	if !errors.Is(err, databaseError) || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected revocation error, got %v", err)
	}

	targetID := uint64(8)
	sessions = &fakeSessions{found: auth.Session{UserID: 4, ImpersonatedUserID: &targetID}}
	service = newTestService(t,
		&fakeUsers{
			byIDs:      map[uint64]user.User{4: {ID: 4, RoleID: 1, IsActive: true}},
			findIDErrs: map[uint64]error{8: databaseError},
		},
		&fakeRoles{byIDs: map[uint64]access.Role{1: {ID: 1, Slug: access.AdminRoleSlug}}},
		sessions,
	)
	_, err = service.ResolveSession(context.Background(), auth.HashToken("token"), time.Now())
	if !errors.Is(err, databaseError) || errors.Is(err, ErrUnauthenticated) || sessions.revokedHash != ([32]byte{}) {
		t.Fatalf("unexpected target database failure handling: err=%v revoked=%x", err, sessions.revokedHash)
	}
}

func newTestService(t *testing.T, users userStore, roles roleStore, sessions sessionStore) *Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := NewService(users, roles, sessions, 24*time.Hour, 30*24*time.Hour, logger)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
