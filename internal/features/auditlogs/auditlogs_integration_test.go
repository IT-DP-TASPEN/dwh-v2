//go:build integration

package auditlogs

import (
	"context"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/features/dashboard"
	featureRoles "github.com/ibldzn/go-admin/internal/features/roles"
	featureUsers "github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func integrationDefinitions() []access.PermissionDefinition {
	definitions := dashboard.PermissionDefinitions()
	definitions = append(definitions, featureUsers.PermissionDefinitions()...)
	definitions = append(definitions, featureRoles.PermissionDefinitions()...)
	definitions = append(definitions, PermissionDefinitions()...)
	return definitions
}

func TestAuditLogsMySQLIntegration(t *testing.T) {
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, integrationDefinitions())
	now := integrationdb.Now()
	adminRole := integrationdb.Role(t, db, access.AdminRoleSlug)
	userRole := integrationdb.Role(t, db, access.UserRoleSlug)
	admin := integrationdb.User(t, db, "admin", adminRole.ID, true)
	target := integrationdb.User(t, db, "target", userRole.ID, true)
	identity := audit.Identity{UserID: admin.ID, Username: admin.Username}
	for index := 0; index < 52; index++ {
		action := audit.ActionUserCreated
		if index == 51 {
			action = audit.ActionUserActivated
		}
		if err := audit.Append(context.Background(), db, audit.Event{
			Attribution: audit.Attribution{Actor: &identity, Effective: &identity}, Action: action,
			Resource: audit.ResourceUser, ResourceID: uint64(index + 1), Metadata: audit.StatusChangeMetadata{From: "<script>", To: "safe"}, CreatedAt: now.Add(time.Duration(index) * time.Microsecond),
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(NewRepository(db))
	page, err := service.List(context.Background(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 50 || page.Pagination.Total != 52 || page.Rows[0].ResourceID == nil || *page.Rows[0].ResourceID != 52 {
		t.Fatalf("unexpected first page: rows=%d total=%d first=%+v", len(page.Rows), page.Pagination.Total, page.Rows[0])
	}
	second, err := service.List(context.Background(), string(audit.ActionUserCreated), 2)
	if err != nil || len(second.Rows) != 1 || second.Pagination.Total != 51 {
		t.Fatalf("filtered page: rows=%d total=%d err=%v", len(second.Rows), second.Pagination.Total, err)
	}
	empty, err := service.List(context.Background(), "unknown.action", 1)
	if err != nil || len(empty.Rows) != 0 || empty.Pagination.Total != 0 {
		t.Fatalf("unknown filter: %+v err=%v", empty, err)
	}
	found, err := service.Find(context.Background(), page.Rows[0].ID)
	if err != nil || found.Actor().Label != "@admin" || found.Effective().Label != "@admin" || found.IsImpersonated() {
		t.Fatalf("detail=%+v err=%v", found, err)
	}
	if _, err := service.Find(context.Background(), ^uint64(0)); err != ErrNotFound {
		t.Fatalf("missing detail=%v", err)
	}
	for _, event := range []audit.Event{
		{Action: audit.ActionAuthRegistration, Resource: audit.ResourceUser, ResourceID: target.ID, CreatedAt: now.Add(time.Second)},
		{Action: audit.ActionAdminBootstrap, Resource: audit.ResourceUser, ResourceID: admin.ID, CreatedAt: now.Add(2 * time.Second)},
		{Attribution: audit.Attribution{Actor: &identity, Effective: &audit.Identity{UserID: target.ID, Username: target.Username}}, Action: audit.ActionImpersonationStarted, Resource: audit.ResourceUser, ResourceID: target.ID, Metadata: audit.ImpersonationStartedMetadata{TargetRole: userRole.Slug}, CreatedAt: now.Add(3 * time.Second)},
	} {
		if err := audit.Append(context.Background(), db, event); err != nil {
			t.Fatal(err)
		}
	}
	var publicID, systemID, impersonatedID uint64
	if err := db.Get(&publicID, `SELECT id FROM audit_logs WHERE action=? ORDER BY id DESC LIMIT 1`, audit.ActionAuthRegistration); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&systemID, `SELECT id FROM audit_logs WHERE action=? ORDER BY id DESC LIMIT 1`, audit.ActionAdminBootstrap); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&impersonatedID, `SELECT id FROM audit_logs WHERE action=? ORDER BY id DESC LIMIT 1`, audit.ActionImpersonationStarted); err != nil {
		t.Fatal(err)
	}
	public, _ := service.Find(context.Background(), publicID)
	system, _ := service.Find(context.Background(), systemID)
	impersonated, _ := service.Find(context.Background(), impersonatedID)
	if public.Actor().Label != "Public" || public.Effective().Label != "—" || system.Actor().Label != "System" || !impersonated.IsImpersonated() {
		t.Fatalf("identity rendering: public=%+v system=%+v impersonated=%+v", public, system, impersonated)
	}
}
