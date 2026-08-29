//go:build integration

package impersonation

import (
	"bytes"
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
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/features/auditlogs"
	featureRoles "github.com/ibldzn/go-admin/internal/features/roles"
	featureUsers "github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/ibldzn/go-admin/internal/user"
)

func integrationDefinitions() []access.PermissionDefinition {
	definitions := featureUsers.PermissionDefinitions()
	definitions = append(definitions, featureRoles.PermissionDefinitions()...)
	definitions = append(definitions, auditlogs.PermissionDefinitions()...)
	return definitions
}

func TestImpersonationMySQLIntegration(t *testing.T) {
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, integrationDefinitions())
	now := integrationdb.Now()
	adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
	userRole := integrationdb.Role(t, db, access.UserRoleSlug)
	admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
	target := integrationdb.User(t, db, "target", userRole.ID, true)
	principal := browserauth.Principal{
		UserID: admin.ID, Username: admin.Username, RoleID: adminRole.ID, RoleSlug: adminRole.Slug,
		Actor: browserauth.Identity{UserID: admin.ID, Username: admin.Username, RoleID: adminRole.ID, RoleSlug: adminRole.Slug},
	}
	sessions := auth.NewSessionRepository(db)
	real := NewService(user.NewRepository(db), access.NewRepository(db), NewRepository(db, audit.Append))
	real.generateToken = tokenSequence("started", "stopped", "start-for-stop-failure")

	base := integrationdb.Session(t, sessions, admin.ID, true, "base", now)
	started, err := real.Start(context.Background(), principal, base.TokenHash, target.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.FindValidByTokenHash(context.Background(), base.TokenHash, now.Add(time.Second)); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("old start token valid: %v", err)
	}
	startedHash := auth.HashToken(started.RawToken)
	stored, err := sessions.FindValidByTokenHash(context.Background(), startedHash, now.Add(time.Second))
	if err != nil || stored.ImpersonatedUserID == nil || *stored.ImpersonatedUserID != target.ID || !stored.RememberMe || !stored.ExpiresAt.Equal(base.ExpiresAt) {
		t.Fatalf("started state=%+v err=%v", stored, err)
	}
	impersonatedPrincipal := principal
	impersonatedPrincipal.UserID, impersonatedPrincipal.Username = target.ID, target.Username
	impersonatedPrincipal.RoleID, impersonatedPrincipal.RoleSlug, impersonatedPrincipal.IsImpersonating = userRole.ID, userRole.Slug, true
	if _, err := real.Start(context.Background(), impersonatedPrincipal, startedHash, target.ID, now); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("nested start=%v", err)
	}
	stopped, err := real.Stop(context.Background(), impersonatedPrincipal, startedHash, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stoppedHash := auth.HashToken(stopped.RawToken)
	stored, err = sessions.FindValidByTokenHash(context.Background(), stoppedHash, now.Add(2*time.Second))
	if err != nil || stored.ImpersonatedUserID != nil || !stored.ExpiresAt.Equal(base.ExpiresAt) {
		t.Fatalf("stopped state=%+v err=%v", stored, err)
	}
	var startAudits, stopAudits int
	if err := db.Get(&startAudits, `SELECT COUNT(*) FROM audit_logs WHERE action=? AND actor_user_id=? AND effective_user_id=?`, audit.ActionImpersonationStarted, admin.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&stopAudits, `SELECT COUNT(*) FROM audit_logs WHERE action=? AND actor_user_id=? AND effective_user_id=?`, audit.ActionImpersonationStopped, admin.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	if startAudits != 1 || stopAudits != 1 {
		t.Fatalf("start audits=%d stop audits=%d", startAudits, stopAudits)
	}

	failure := errors.New("injected audit failure")
	var realTransaction atomic.Bool
	failing := NewService(user.NewRepository(db), access.NewRepository(db), NewRepository(db, func(_ context.Context, executor sqlx.ExtContext, _ audit.Event) error {
		_, ok := executor.(*sqlx.Tx)
		realTransaction.Store(ok)
		return failure
	}))
	failing.generateToken = tokenSequence("failed-start", "failed-stop")
	startFailureBase := integrationdb.Session(t, sessions, admin.ID, false, "start-failure-base", now.Add(3*time.Second))
	if _, err := failing.Start(context.Background(), principal, startFailureBase.TokenHash, target.ID, now.Add(4*time.Second)); !errors.Is(err, failure) || !realTransaction.Load() {
		t.Fatalf("start failure=%v real_tx=%v", err, realTransaction.Load())
	}
	assertSessionState(t, db, startFailureBase.ID, startFailureBase.TokenHash, nil)
	if _, err := sessions.FindValidByTokenHash(context.Background(), startFailureBase.TokenHash, now.Add(4*time.Second)); err != nil {
		t.Fatalf("old start token invalid: %v", err)
	}

	stopFailureBase := integrationdb.Session(t, sessions, admin.ID, false, "stop-failure-base", now.Add(5*time.Second))
	stopFailureStarted, err := real.Start(context.Background(), principal, stopFailureBase.TokenHash, target.ID, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stopFailureHash := auth.HashToken(stopFailureStarted.RawToken)
	if _, err := failing.Stop(context.Background(), impersonatedPrincipal, stopFailureHash, now.Add(7*time.Second)); !errors.Is(err, failure) {
		t.Fatalf("stop failure=%v", err)
	}
	assertSessionState(t, db, stopFailureBase.ID, stopFailureHash, &target.ID)
	if _, err := sessions.FindValidByTokenHash(context.Background(), stopFailureHash, now.Add(7*time.Second)); err != nil {
		t.Fatalf("old stop token invalid: %v", err)
	}

	if _, err := db.Exec(`UPDATE users SET is_active=FALSE WHERE id=?`, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := real.Stop(context.Background(), impersonatedPrincipal, stopFailureHash, now.Add(8*time.Second)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("invalid target stop=%v", err)
	}
	if _, err := sessions.FindValidByTokenHash(context.Background(), stopFailureHash, now.Add(8*time.Second)); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("invalid session remained: %v", err)
	}
}

func tokenSequence(tokens ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		if index >= len(tokens) {
			return "fallback-token", nil
		}
		token := tokens[index]
		index++
		return token, nil
	}
}

func assertSessionState(t *testing.T, db *sqlx.DB, id uint64, hash [32]byte, target *uint64) {
	t.Helper()
	var row struct {
		TokenHash []byte        `db:"token_hash"`
		Target    sql.NullInt64 `db:"impersonated_user_id"`
	}
	if err := db.Get(&row, `SELECT token_hash,impersonated_user_id FROM sessions WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(row.TokenHash, hash[:]) {
		t.Fatal("token hash changed after failed audit")
	}
	if target == nil && row.Target.Valid || target != nil && (!row.Target.Valid || uint64(row.Target.Int64) != *target) {
		t.Fatalf("target changed after failed audit: %+v", row.Target)
	}
}
