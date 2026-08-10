package app

import (
	"testing"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/features/auditlogs"
	"github.com/ibldzn/go-admin/internal/features/dashboard"
	"github.com/ibldzn/go-admin/internal/features/roles"
	"github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
)

func TestPhaseFourNavigation(t *testing.T) {
	registry, err := navigation.NewRegistry(navigationGroups(), PermissionDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		path        string
		permissions []string
		groups      int
		active      string
		open        string
	}{
		{name: "dashboard", path: "/", permissions: []string{dashboard.PermissionView}, groups: 1, active: "dashboard"},
		{name: "users", path: "/users/7", permissions: []string{users.PermissionView}, groups: 1, active: "users"},
		{name: "roles", path: "/roles/7", permissions: []string{roles.PermissionView}, groups: 1, active: "roles", open: "access-control"},
		{name: "audit", path: "/audit-logs/7", permissions: []string{auditlogs.PermissionView}, groups: 1, active: "audit-logs"},
		{name: "management hidden", path: "/", permissions: nil, groups: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := access.NewPermissionSet(test.permissions)
			groups := registry.Prepare(test.path, set.Has)
			if len(groups) != test.groups {
				t.Fatalf("expected %d groups, got %#v", test.groups, groups)
			}
			if test.active != "" && !navigationHas(groups, test.active, true, false) {
				t.Fatalf("active item %q missing: %#v", test.active, groups)
			}
			if test.open != "" && !navigationHas(groups, test.open, false, true) {
				t.Fatalf("open item %q missing: %#v", test.open, groups)
			}
		})
	}
}

func TestPermissionAggregation(t *testing.T) {
	definitions := PermissionDefinitions()
	if len(definitions) != 13 {
		t.Fatalf("got %d permissions, want 13", len(definitions))
	}
	if err := access.ValidateRegistry(definitions); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		dashboard.PermissionView: true,
		users.PermissionView:     true, users.PermissionCreate: true, users.PermissionUpdate: true,
		users.PermissionDisable: true, users.PermissionResetPassword: true,
		roles.PermissionView: true, roles.PermissionCreate: true, roles.PermissionUpdate: true,
		roles.PermissionDelete: true, roles.PermissionAssign: true, roles.PermissionManagePermissions: true,
		auditlogs.PermissionView: true,
	}
	for _, definition := range definitions {
		delete(want, definition.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing canonical permissions: %v", want)
	}
}

func navigationHas(groups []navigation.GroupView, key string, active, open bool) bool {
	var find func([]navigation.ItemView) bool
	find = func(items []navigation.ItemView) bool {
		for _, item := range items {
			if item.Key == key && (!active || item.Active) && (!open || item.Open) {
				return true
			}
			if find(item.Children) {
				return true
			}
		}
		return false
	}
	for _, group := range groups {
		if find(group.Items) {
			return true
		}
	}
	return false
}
