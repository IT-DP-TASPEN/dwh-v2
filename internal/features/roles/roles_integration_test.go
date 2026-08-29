//go:build integration

package roles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/features/auditlogs"
	featureUsers "github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func integrationDefinitions() []access.PermissionDefinition {
	definitions := featureUsers.PermissionDefinitions()
	definitions = append(definitions, PermissionDefinitions()...)
	definitions = append(definitions, auditlogs.PermissionDefinitions()...)
	return definitions
}

func TestRolesMySQLIntegration(t *testing.T) {
	db := integrationdb.Open(t)
	t.Run("canonical replacement preserves unknown assignments", func(t *testing.T) {
		integrationdb.Reset(t, db, integrationDefinitions())
		now := integrationdb.Now()
		adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
		admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
		custom := integrationdb.CustomRole(t, db, "Reviewer", "reviewer")
		result, err := db.Exec(`INSERT INTO permissions (`+"`key`"+`,name,group_name,description,created_at,updated_at) VALUES (?,?,?,?,?,?)`, "legacy.permission", "Legacy", "Legacy", "legacy", now, now)
		if err != nil {
			t.Fatal(err)
		}
		legacyID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO role_permissions (role_id,permission_id) VALUES (?,?)`, custom.ID, legacyID); err != nil {
			t.Fatal(err)
		}
		service := NewService(NewRepository(db, audit.Append), integrationDefinitions())
		if err := service.ReplacePermissions(context.Background(), integrationdb.Requester(admin, adminRole), custom.ID, []string{auditlogs.PermissionView}, now); err != nil {
			t.Fatal(err)
		}
		userRole := integrationdb.Role(t, db, access.UserRoleSlug)
		if err := service.ReplacePermissions(context.Background(), integrationdb.Requester(admin, adminRole), userRole.ID, []string{auditlogs.PermissionView}, now); err != nil {
			t.Fatal(err)
		}
		var legacy, auditView int
		if err := db.Get(&legacy, `SELECT COUNT(*) FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.key=?`, custom.ID, "legacy.permission"); err != nil {
			t.Fatal(err)
		}
		if err := db.Get(&auditView, `SELECT COUNT(*) FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.key=?`, custom.ID, auditlogs.PermissionView); err != nil {
			t.Fatal(err)
		}
		if legacy != 1 || auditView != 1 {
			t.Fatalf("legacy=%d audit.view=%d", legacy, auditView)
		}
		if err := db.Get(&auditView, `SELECT COUNT(*) FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.key=?`, userRole.ID, auditlogs.PermissionView); err != nil || auditView != 1 {
			t.Fatalf("user role audit.view=%d err=%v", auditView, err)
		}
		var permissionCount int
		if err := db.Get(&permissionCount, `SELECT COUNT(*) FROM permissions WHERE `+"`key`"+`=?`, auditlogs.PermissionView); err != nil || permissionCount != 1 {
			t.Fatalf("audit permission count=%d err=%v", permissionCount, err)
		}
	})

	t.Run("audit failure rolls back permission replacement", func(t *testing.T) {
		integrationdb.Reset(t, db, integrationDefinitions())
		now := integrationdb.Now()
		adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
		admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
		custom := integrationdb.CustomRole(t, db, "Reviewer", "reviewer")
		real := NewService(NewRepository(db, audit.Append), integrationDefinitions())
		if err := real.ReplacePermissions(context.Background(), integrationdb.Requester(admin, adminRole), custom.ID, []string{featureUsers.PermissionView}, now); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("injected audit failure")
		failing := NewService(NewRepository(db, func(_ context.Context, executor sqlx.ExtContext, _ audit.Event) error {
			if _, ok := executor.(*sqlx.Tx); !ok {
				t.Fatal("audit append did not receive real transaction")
			}
			return failure
		}), integrationDefinitions())
		err := failing.ReplacePermissions(context.Background(), integrationdb.Requester(admin, adminRole), custom.ID, []string{auditlogs.PermissionView}, now.Add(time.Second))
		if !errors.Is(err, failure) {
			t.Fatalf("replacement failure=%v", err)
		}
		keys, err := NewRepository(db, audit.Append).ListPermissionKeys(context.Background(), custom.ID)
		if err != nil || len(keys) != 1 || keys[0] != featureUsers.PermissionView {
			t.Fatalf("rollback keys=%v err=%v", keys, err)
		}
	})

	t.Run("system and assigned roles remain protected", func(t *testing.T) {
		integrationdb.Reset(t, db, integrationDefinitions())
		adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
		admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
		custom := integrationdb.CustomRole(t, db, "Assigned", "assigned")
		integrationdb.User(t, db, "member", custom.ID, true)
		service := NewService(NewRepository(db, audit.Append), integrationDefinitions())
		requester := integrationdb.Requester(admin, adminRole)
		if _, err := service.Update(context.Background(), requester, adminRole.ID, "Changed", time.Now()); !errors.Is(err, ErrProtectedRole) {
			t.Fatalf("system update=%v", err)
		}
		if err := service.Delete(context.Background(), requester, custom.ID, time.Now()); !errors.Is(err, ErrRoleAssigned) {
			t.Fatalf("assigned delete=%v", err)
		}
	})
}
