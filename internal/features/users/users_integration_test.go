//go:build integration

package users

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/features/auditlogs"
	featureRoles "github.com/ibldzn/go-admin/internal/features/roles"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/ibldzn/go-admin/internal/user"
)

func integrationDefinitions() []access.PermissionDefinition {
	definitions := PermissionDefinitions()
	definitions = append(definitions, featureRoles.PermissionDefinitions()...)
	definitions = append(definitions, auditlogs.PermissionDefinitions()...)
	return definitions
}

func TestUsersMySQLIntegration(t *testing.T) {
	db := integrationdb.Open(t)
	t.Run("concurrent final administrator", func(t *testing.T) {
		integrationdb.Reset(t, db, integrationDefinitions())
		now := integrationdb.Now()
		adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
		userRole := integrationdb.Role(t, db, access.UserRoleSlug)
		adminA := integrationdb.User(t, db, "admin-a", adminRole.ID, true)
		adminB := integrationdb.User(t, db, "admin-b", adminRole.ID, true)
		repository := NewRepository(db, audit.Append)
		begin := repository.beginTx
		ready := make(chan struct{}, 2)
		release := make(chan struct{})
		repository.beginTx = func(ctx context.Context, options *sql.TxOptions) (*sqlx.Tx, error) {
			transaction, err := begin(ctx, options)
			if err == nil {
				ready <- struct{}{}
				<-release
			}
			return transaction, err
		}
		service := NewService(repository, access.NewRepository(db), featureRoles.PermissionAssign)
		results := make(chan error, 2)
		go func() {
			results <- service.AssignRole(context.Background(), integrationdb.Requester(adminA, adminRole), adminA.ID, userRole.ID, now)
		}()
		go func() {
			results <- service.AssignRole(context.Background(), integrationdb.Requester(adminB, adminRole), adminB.ID, userRole.ID, now)
		}()
		<-ready
		<-ready
		close(release)
		errA, errB := <-results, <-results
		if !((errA == nil && errors.Is(errB, ErrLastActiveAdmin)) || (errB == nil && errors.Is(errA, ErrLastActiveAdmin))) {
			t.Fatalf("expected one success and one conflict, got %v and %v", errA, errB)
		}
		var active, audits int
		if err := db.Get(&active, `SELECT COUNT(*) FROM users u JOIN roles r ON r.id=u.role_id WHERE r.slug=? AND u.is_active=TRUE`, access.AdminRoleSlug); err != nil {
			t.Fatal(err)
		}
		if err := db.Get(&audits, `SELECT COUNT(*) FROM audit_logs WHERE action=?`, audit.ActionUserRoleChanged); err != nil {
			t.Fatal(err)
		}
		if active != 1 || audits != 1 {
			t.Fatalf("active=%d audits=%d", active, audits)
		}
	})

	t.Run("audit attribution rollback and session scope", func(t *testing.T) {
		integrationdb.Reset(t, db, integrationDefinitions())
		now := integrationdb.Now()
		adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
		userRole := integrationdb.Role(t, db, access.UserRoleSlug)
		admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
		delegate := integrationdb.User(t, db, "delegate", userRole.ID, true)
		target := integrationdb.User(t, db, "target", userRole.ID, true)
		requester := securityctx.Requester{
			Actor: securityctx.Identity{UserID: admin.ID, Username: "admin"}, Effective: securityctx.Identity{UserID: delegate.ID, Username: "delegate"},
			EffectiveRoleID: userRole.ID, EffectiveRoleSlug: userRole.Slug,
		}
		repository := NewRepository(db, audit.Append)
		if _, err := repository.UpdateUserProfile(context.Background(), requester, delegate.ID, "delegate-new", "Delegate", now); err != nil {
			t.Fatal(err)
		}
		var actor, effective string
		if err := db.QueryRow(`SELECT actor_username, effective_username FROM audit_logs WHERE action=? AND resource_id=?`, audit.ActionUserProfileUpdated, delegate.ID).Scan(&actor, &effective); err != nil {
			t.Fatal(err)
		}
		if actor != "admin" || effective != "delegate" {
			t.Fatalf("snapshots changed: actor=%q effective=%q", actor, effective)
		}
		sessions := auth.NewSessionRepository(db)
		owned := integrationdb.Session(t, sessions, target.ID, true, "owned", now)
		impersonating := integrationdb.Session(t, sessions, admin.ID, false, "impersonating", now)
		if _, err := db.Exec(`UPDATE sessions SET impersonated_user_id=? WHERE id=?`, target.ID, impersonating.ID); err != nil {
			t.Fatal(err)
		}
		if err := repository.ResetUserPassword(context.Background(), requester, target.ID, "replacement", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		var ownedCount, impersonatingCount int
		if err := db.Get(&ownedCount, `SELECT COUNT(*) FROM sessions WHERE id=?`, owned.ID); err != nil {
			t.Fatal(err)
		}
		if err := db.Get(&impersonatingCount, `SELECT COUNT(*) FROM sessions WHERE id=? AND impersonated_user_id=?`, impersonating.ID, target.ID); err != nil {
			t.Fatal(err)
		}
		if ownedCount != 0 || impersonatingCount != 1 {
			t.Fatalf("password reset scope owned=%d impersonating=%d", ownedCount, impersonatingCount)
		}

		owned = integrationdb.Session(t, sessions, target.ID, false, "owned-2", now)
		failure := errors.New("injected audit failure")
		var realTransaction atomic.Bool
		failing := NewRepository(db, func(_ context.Context, executor sqlx.ExtContext, _ audit.Event) error {
			_, ok := executor.(*sqlx.Tx)
			realTransaction.Store(ok)
			return failure
		})
		if err := failing.SetUserActive(context.Background(), requester, target.ID, false, now.Add(2*time.Second)); !errors.Is(err, failure) || !realTransaction.Load() {
			t.Fatalf("audit failure=%v real_tx=%v", err, realTransaction.Load())
		}
		var active bool
		var remaining int
		if err := db.Get(&active, `SELECT is_active FROM users WHERE id=?`, target.ID); err != nil {
			t.Fatal(err)
		}
		if err := db.Get(&remaining, `SELECT COUNT(*) FROM sessions WHERE id IN (?,?)`, owned.ID, impersonating.ID); err != nil {
			t.Fatal(err)
		}
		if !active || remaining != 2 {
			t.Fatalf("failed audit committed mutation: active=%v sessions=%d", active, remaining)
		}
		if err := repository.SetUserActive(context.Background(), requester, target.ID, false, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := db.Get(&remaining, `SELECT COUNT(*) FROM sessions WHERE user_id=? OR impersonated_user_id=?`, target.ID, target.ID); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("deactivation left %d sessions", remaining)
		}
	})

	t.Run("promotion revocation and duplicate username", func(t *testing.T) {
		integrationdb.Reset(t, db, integrationDefinitions())
		now := integrationdb.Now()
		adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
		userRole := integrationdb.Role(t, db, access.UserRoleSlug)
		admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
		target := integrationdb.User(t, db, "target", userRole.ID, true)
		sessions := auth.NewSessionRepository(db)
		impersonating := integrationdb.Session(t, sessions, admin.ID, false, "promote", now)
		if _, err := db.Exec(`UPDATE sessions SET impersonated_user_id=? WHERE id=?`, target.ID, impersonating.ID); err != nil {
			t.Fatal(err)
		}
		repository := NewRepository(db, audit.Append)
		if err := repository.AssignUserRole(context.Background(), integrationdb.Requester(admin, adminRole), target.ID, adminRole.ID, now); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.Get(&count, `SELECT COUNT(*) FROM sessions WHERE id=?`, impersonating.ID); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("promotion kept impersonating session")
		}
		service := NewService(repository, access.NewRepository(db), featureRoles.PermissionAssign)
		service.hashPassword = func(string) (string, error) { return "hash", nil }
		_, err := service.CreateUser(context.Background(), integrationdb.Requester(admin, adminRole), CreateUserInput{
			Username: " TARGET ", Name: "Duplicate", Password: "integration-password", PasswordConfirmation: "integration-password",
		}, now)
		if !errors.Is(err, user.ErrUsernameTaken) {
			t.Fatalf("duplicate username = %v", err)
		}
	})
}
