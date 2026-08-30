package roles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type fakeStore struct {
	record    Record
	canonical []string
	selected  []string
}

func (*fakeStore) List(context.Context) ([]Record, error)                 { return nil, nil }
func (store *fakeStore) FindByID(context.Context, uint64) (Record, error) { return store.record, nil }
func (*fakeStore) Create(context.Context, securityctx.Requester, string, string, time.Time) (Record, error) {
	return Record{}, nil
}
func (*fakeStore) UpdateName(context.Context, securityctx.Requester, uint64, string, time.Time) (Record, error) {
	return Record{}, nil
}
func (*fakeStore) Delete(context.Context, securityctx.Requester, uint64, time.Time) error { return nil }
func (*fakeStore) ListPermissionKeys(context.Context, uint64) ([]string, error) {
	return []string{"audit.view", "legacy.permission"}, nil
}
func (store *fakeStore) ReplacePermissions(_ context.Context, _ securityctx.Requester, _ uint64, canonical, selected []string, _ time.Time) error {
	store.canonical, store.selected = canonical, selected
	return nil
}

func TestAggregatedPermissionMatrixAndReplacement(t *testing.T) {
	definitions := []access.PermissionDefinition{
		{Key: "users.view", Name: "View Users", Group: "Users"},
		{Key: "audit.view", Name: "View Audit Logs", Group: "Audit"},
	}
	store := &fakeStore{record: Record{ID: 7, Slug: "reviewer"}}
	service := NewService(store, definitions)
	detail, err := service.Find(context.Background(), 7)
	if err != nil || len(detail.PermissionGroups) != 2 || detail.PermissionGroups[1].Name != "Audit" || !detail.PermissionGroups[1].Permissions[0].Selected {
		t.Fatalf("unexpected matrix: %+v err=%v", detail.PermissionGroups, err)
	}
	requester := securityctx.Requester{Effective: securityctx.Identity{UserID: 1}, EffectiveRoleSlug: access.AdminRoleSlug}
	if err := service.ReplacePermissions(context.Background(), requester, 7, []string{"audit.view", "audit.view"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(store.canonical) != 2 || len(store.selected) != 1 || store.selected[0] != "audit.view" {
		t.Fatalf("unexpected replacement: canonical=%v selected=%v", store.canonical, store.selected)
	}
	if err := service.ReplacePermissions(context.Background(), requester, 7, []string{"legacy.permission"}, time.Now()); !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("unknown permission = %v", err)
	}
}

func TestSystemRoleMatrixIsDisabled(t *testing.T) {
	store := &fakeStore{record: Record{ID: 1, Slug: access.AdminRoleSlug, IsSystem: true}}
	detail, err := NewService(store, []access.PermissionDefinition{{Key: "audit.view", Name: "Audit", Group: "Audit"}}).Find(context.Background(), 1)
	if err != nil || detail.PermissionGroups != nil {
		t.Fatalf("admin matrix = %+v err=%v", detail.PermissionGroups, err)
	}
}

func TestExportOversightPermissionCanBeGrantedAndRevoked(t *testing.T) {
	definition := access.PermissionDefinition{
		Key: "report_exports.view_all", Name: "View All Report Exports", Group: "Reports",
		Description: "Inspect export jobs and download retained export artifacts requested by any user.",
	}
	store := &fakeStore{record: Record{ID: 7, Slug: "operations"}}
	service := NewService(store, []access.PermissionDefinition{definition})
	detail, err := service.Find(context.Background(), 7)
	if err != nil || len(detail.PermissionGroups) != 1 || len(detail.PermissionGroups[0].Permissions) != 1 {
		t.Fatalf("permission matrix=%+v err=%v", detail.PermissionGroups, err)
	}
	option := detail.PermissionGroups[0].Permissions[0]
	if option.Key != definition.Key || option.Name != definition.Name || option.Description != definition.Description {
		t.Fatalf("permission option=%+v", option)
	}
	requester := securityctx.Requester{Effective: securityctx.Identity{UserID: 1}, EffectiveRoleSlug: access.AdminRoleSlug}
	if err := service.ReplacePermissions(context.Background(), requester, 7, []string{definition.Key}, time.Now()); err != nil || len(store.selected) != 1 || store.selected[0] != definition.Key {
		t.Fatalf("grant selected=%v err=%v", store.selected, err)
	}
	if err := service.ReplacePermissions(context.Background(), requester, 7, nil, time.Now()); err != nil || len(store.selected) != 0 {
		t.Fatalf("revoke selected=%v err=%v", store.selected, err)
	}
}
