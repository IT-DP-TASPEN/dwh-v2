//go:build integration

package mfa_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	usersfeature "github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/ibldzn/go-admin/internal/user"
)

func fixture(t *testing.T) (*mfa.Store, uint64, time.Time) {
	t.Helper()
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, usersfeature.PermissionDefinitions())
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	u := integrationdb.User(t, db, "mfa-admin", role.ID, true)
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &mfa.Store{DB: db, Cipher: secretcrypto.New([32]byte{1}), Lifetime: time.Hour, RememberLifetime: 30 * 24 * time.Hour, IdleTimeout: 2 * time.Hour}, u.ID, now
}

func enroll(t *testing.T, s *mfa.Store, id uint64, now time.Time) (mfa.Verified, string) {
	t.Helper()
	issued, err := s.BeginLogin(context.Background(), id, "integration-hash", false, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Challenge.Purpose != mfa.Enrollment {
		t.Fatal("enrollment challenge required")
	}
	secret, err := s.PendingSecret(issued.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	code, err := mfa.Code(secret, time.Now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Verify(context.Background(), issued.Token, code, 0, "", now)
	if err != nil {
		t.Fatal(err)
	}
	return result, secret
}

func userCount(t *testing.T, s *mfa.Store, table string, id uint64) int {
	t.Helper()
	var n int
	if err := s.DB.Get(&n, "SELECT COUNT(*) FROM "+table+" WHERE user_id=?", id); err != nil {
		t.Fatal(err)
	}
	return n
}

func resetAttribution(id uint64, username string) audit.Attribution {
	identity := audit.Identity{UserID: id, Username: username}
	return audit.Attribution{Actor: &identity, Effective: &identity}
}

func TestAdminResetAuthorizationAndMandatoryReenrollment(t *testing.T) {
	s, actorID, now := fixture(t)
	actorSession, _ := enroll(t, s, actorID, now)
	ctx := context.Background()
	role := integrationdb.Role(t, s.DB, access.UserRoleSlug)
	target := integrationdb.User(t, s.DB, "reset-target", role.ID, true)
	targetSession, _ := enroll(t, s, target.ID, now)
	pending, err := s.BeginLogin(ctx, target.ID, "integration-hash", false, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	attribution := resetAttribution(actorID, "mfa-admin")
	if err := s.Reset(ctx, actorID, actorID, actorSession.Session.ID, attribution, now); !errors.Is(err, mfa.ErrInvalid) {
		t.Fatalf("self reset accepted: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE sessions SET mfa_verified_at=? WHERE id=?`, now.Add(-mfa.Freshness-time.Second), actorSession.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(ctx, target.ID, actorID, actorSession.Session.ID, attribution, now); !errors.Is(err, mfa.ErrInvalid) {
		t.Fatalf("stale administrator reset accepted: %v", err)
	}
	if userCount(t, s, "sessions", target.ID) != 1 || userCount(t, s, "user_totp_enrollments", target.ID) != 1 {
		t.Fatal("rejected reset changed target credentials")
	}
	if _, err := s.DB.Exec(`UPDATE sessions SET mfa_verified_at=? WHERE id=?`, now, actorSession.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(ctx, target.ID, actorID, actorSession.Session.ID, attribution, now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "user_totp_enrollments", "user_mfa_recovery_codes", "mfa_challenges"} {
		if userCount(t, s, table, target.ID) != 0 {
			t.Fatalf("reset retained target %s", table)
		}
	}
	if userCount(t, s, "sessions", actorID) != 1 {
		t.Fatal("target reset revoked actor session")
	}
	if _, err := s.Verify(ctx, pending.Token, targetSession.RecoveryCodes[0], 0, "", now); !errors.Is(err, mfa.ErrInvalid) {
		t.Fatalf("reset challenge resurrected: %v", err)
	}
	var actor, effective, resource uint64
	if err := s.DB.QueryRow(`SELECT actor_user_id,effective_user_id,resource_id FROM audit_logs WHERE action=?`, audit.ActionMFAAdminReset).Scan(&actor, &effective, &resource); err != nil {
		t.Fatal(err)
	}
	if actor != actorID || effective != actorID || resource != target.ID {
		t.Fatalf("reset attribution actor=%d effective=%d target=%d", actor, effective, resource)
	}
	next, err := s.BeginLogin(ctx, target.ID, "integration-hash", false, "/", now)
	if err != nil || next.Challenge.Purpose != mfa.Enrollment || userCount(t, s, "sessions", target.ID) != 0 {
		t.Fatalf("reset did not require fresh enrollment: %v", err)
	}
}

func TestAdminResetRequiresDedicatedPermission(t *testing.T) {
	s, _, now := fixture(t)
	ctx := context.Background()
	role := integrationdb.CustomRole(t, s.DB, "MFA support", "mfa-support")
	actor := integrationdb.User(t, s.DB, "mfa-support", role.ID, true)
	target := integrationdb.User(t, s.DB, "target", integrationdb.Role(t, s.DB, access.UserRoleSlug).ID, true)
	actorSession, _ := enroll(t, s, actor.ID, now)
	enroll(t, s, target.ID, now)
	grant := func(permission string) {
		t.Helper()
		if _, err := s.DB.Exec("INSERT INTO role_permissions(role_id,permission_id) SELECT ?,id FROM permissions WHERE `key`=?", role.ID, permission); err != nil {
			t.Fatal(err)
		}
	}
	grant(usersfeature.PermissionUpdate)
	attribution := resetAttribution(actor.ID, actor.Username)
	if err := s.Reset(ctx, target.ID, actor.ID, actorSession.Session.ID, attribution, now); !errors.Is(err, mfa.ErrInvalid) {
		t.Fatalf("users.update permitted MFA reset: %v", err)
	}
	if userCount(t, s, "user_totp_enrollments", target.ID) != 1 {
		t.Fatal("missing permission changed target")
	}
	grant(usersfeature.PermissionMFAReset)
	if err := s.Reset(ctx, target.ID, actor.ID, actorSession.Session.ID, attribution, now); err != nil {
		t.Fatal(err)
	}
	if userCount(t, s, "user_totp_enrollments", target.ID) != 0 {
		t.Fatal("dedicated permission reset failed")
	}
}

func TestPasswordResetAndDeactivationInvalidatePreAuthChallenge(t *testing.T) {
	for _, operation := range []string{"password", "deactivate"} {
		t.Run(operation, func(t *testing.T) {
			s, _, now := fixture(t)
			ctx := context.Background()
			adminRole := integrationdb.Role(t, s.DB, access.AdminRoleSlug)
			actorUser := integrationdb.User(t, s.DB, "reset-admin", adminRole.ID, true)
			requester := integrationdb.Requester(actorUser, adminRole)
			target := integrationdb.User(t, s.DB, "reset-target", integrationdb.Role(t, s.DB, access.UserRoleSlug).ID, true)
			result, _ := enroll(t, s, target.ID, now)
			pending, err := s.BeginLogin(ctx, target.ID, "integration-hash", false, "/", now)
			if err != nil {
				t.Fatal(err)
			}
			repository := usersfeature.NewRepository(s.DB, audit.Append)
			if operation == "password" {
				err = repository.ResetUserPassword(ctx, requester, target.ID, "replacement-hash", now)
			} else {
				err = repository.SetUserActive(ctx, requester, target.ID, false, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if userCount(t, s, "sessions", target.ID) != 0 || userCount(t, s, "mfa_challenges", target.ID) != 0 {
				t.Fatal("credential change retained sessions/challenges")
			}
			if _, err := s.Verify(ctx, pending.Token, result.RecoveryCodes[0], 0, "", now); !errors.Is(err, mfa.ErrInvalid) {
				t.Fatalf("old password challenge created session: %v", err)
			}
			if operation == "deactivate" {
				if _, err := s.BeginLogin(ctx, target.ID, "integration-hash", false, "/", now); !errors.Is(err, mfa.ErrInvalid) {
					t.Fatalf("inactive user issued challenge: %v", err)
				}
				if err := repository.SetUserActive(ctx, requester, target.ID, true, now); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Verify(ctx, pending.Token, result.RecoveryCodes[0], 0, "", now); !errors.Is(err, mfa.ErrInvalid) {
					t.Fatalf("reactivation resurrected challenge: %v", err)
				}
			} else if _, err := s.BeginLogin(ctx, target.ID, "integration-hash", false, "/", now); !errors.Is(err, mfa.ErrInvalid) {
				t.Fatalf("old password accepted: %v", err)
			}
			hash := "integration-hash"
			if operation == "password" {
				hash = "replacement-hash"
			}
			next, err := s.BeginLogin(ctx, target.ID, hash, false, "/", now)
			if err != nil || next.Challenge.Purpose != mfa.Login || userCount(t, s, "sessions", target.ID) != 0 {
				t.Fatalf("existing MFA not retained: %v", err)
			}
		})
	}
}

func TestConcurrentCredentialResetAndLoginChallenge(t *testing.T) {
	for _, operation := range []string{"admin-mfa", "password"} {
		t.Run(operation, func(t *testing.T) {
			s, actorID, now := fixture(t)
			actorSession, _ := enroll(t, s, actorID, now)
			adminRole := integrationdb.Role(t, s.DB, access.AdminRoleSlug)
			actorUser, err := user.NewRepository(s.DB).FindByID(context.Background(), actorID)
			if err != nil {
				t.Fatal(err)
			}
			target := integrationdb.User(t, s.DB, "race-target", integrationdb.Role(t, s.DB, access.UserRoleSlug).ID, true)
			result, _ := enroll(t, s, target.ID, now)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			pending, err := s.BeginLogin(ctx, target.ID, "integration-hash", false, "/", now)
			if err != nil {
				t.Fatal(err)
			}
			attribution := resetAttribution(actorID, "mfa-admin")
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var verifyErr, resetErr error
			go func() {
				defer wg.Done()
				<-start
				_, verifyErr = s.Verify(ctx, pending.Token, result.RecoveryCodes[0], 0, "", now)
			}()
			go func() {
				defer wg.Done()
				<-start
				if operation == "admin-mfa" {
					resetErr = s.Reset(ctx, target.ID, actorID, actorSession.Session.ID, attribution, now)
				} else {
					requester := integrationdb.Requester(actorUser, adminRole)
					resetErr = usersfeature.NewRepository(s.DB, audit.Append).ResetUserPassword(ctx, requester, target.ID, "replacement-hash", now)
				}
			}()
			close(start)
			wg.Wait()
			if resetErr != nil || verifyErr != nil && !errors.Is(verifyErr, mfa.ErrInvalid) {
				t.Fatalf("reset=%v verify=%v", resetErr, verifyErr)
			}
			if userCount(t, s, "sessions", target.ID) != 0 || userCount(t, s, "mfa_challenges", target.ID) != 0 {
				t.Fatal("racing reset retained session/challenge")
			}
			if _, err := s.Verify(ctx, pending.Token, result.RecoveryCodes[0], 0, "", now); !errors.Is(err, mfa.ErrInvalid) {
				t.Fatalf("challenge resurrected after race: %v", err)
			}
		})
	}
}
