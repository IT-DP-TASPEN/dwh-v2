package navigation

import (
	"net/url"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/access"
)

const (
	permissionDashboard = "sample.view"
	permissionUsers     = "users.view"
	permissionRoles     = "roles.view"
)

func testPermissions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: permissionDashboard, Name: "Dashboard", Group: "General"},
		{Key: permissionUsers, Name: "Users", Group: "Management"},
		{Key: permissionRoles, Name: "Roles", Group: "Management"},
	}
}

func TestRegistryValidation(t *testing.T) {
	leaf := Item{Key: "dashboard", Label: "Dashboard", Path: "/", Permission: permissionDashboard, Match: MatchExact}
	valid := []Group{{Key: "general", Label: "General", Items: []Item{leaf}}}
	if _, err := NewRegistry(valid, testPermissions()); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}

	tests := []struct {
		name   string
		groups []Group
		want   string
	}{
		{name: "empty group key", groups: []Group{{Label: "General"}}, want: "empty key"},
		{name: "empty group label", groups: []Group{{Key: "general"}}, want: "empty label"},
		{name: "duplicate group", groups: []Group{{Key: "general", Label: "One"}, {Key: "general", Label: "Two"}}, want: "duplicate navigation group"},
		{name: "empty item key", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Label: "Dashboard"}}}}, want: "empty key"},
		{name: "duplicate item", groups: []Group{{Key: "general", Label: "General", Items: []Item{leaf, leaf}}}, want: "duplicate navigation item"},
		{name: "empty item label", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Key: "dashboard"}}}}, want: "empty label"},
		{name: "leaf path", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Key: "dashboard", Label: "Dashboard", Permission: permissionDashboard, Match: MatchExact}}}}, want: "empty path"},
		{name: "leaf permission", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Key: "dashboard", Label: "Dashboard", Path: "/", Match: MatchExact}}}}, want: "empty permission"},
		{name: "match mode", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Key: "dashboard", Label: "Dashboard", Path: "/", Permission: permissionDashboard, Match: "guess"}}}}, want: "invalid match mode"},
		{name: "unknown permission", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Key: "dashboard", Label: "Dashboard", Path: "/", Permission: "unknown.view", Match: MatchExact}}}}, want: "unknown permission"},
		{name: "container route", groups: []Group{{Key: "general", Label: "General", Items: []Item{{Key: "parent", Label: "Parent", Path: "/parent", Children: []Item{leaf}}}}}, want: "cannot have path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRegistry(test.groups, testPermissions())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestRegistryDepth(t *testing.T) {
	leaf := func(key string) Item {
		return Item{Key: key, Label: key, Path: "/" + key, Permission: permissionDashboard, Match: MatchExact}
	}
	for depth := 1; depth <= 3; depth++ {
		item := leaf("level-leaf")
		for level := depth - 1; level >= 1; level-- {
			item = Item{Key: "level-" + string(rune('0'+level)), Label: "Container", Children: []Item{item}}
		}
		if _, err := NewRegistry([]Group{{Key: "group", Label: "Group", Items: []Item{item}}}, testPermissions()); err != nil {
			t.Fatalf("depth %d rejected: %v", depth, err)
		}
	}

	tooDeep := Item{Key: "one", Label: "One", Children: []Item{{Key: "two", Label: "Two", Children: []Item{{Key: "three", Label: "Three", Children: []Item{{Key: "four", Label: "Four", Path: "/four", Permission: permissionDashboard, Match: MatchExact}}}}}}}
	if _, err := NewRegistry([]Group{{Key: "group", Label: "Group", Items: []Item{tooDeep}}}, testPermissions()); err == nil || !strings.Contains(err.Error(), "maximum depth 3") {
		t.Fatalf("expected depth error, got %v", err)
	}
}

func TestNavigationFilteringAndActiveState(t *testing.T) {
	registry, err := NewRegistry([]Group{
		{Key: "general", Label: "General", Items: []Item{
			{Key: "dashboard", Label: "Dashboard", Path: "/", Permission: permissionDashboard, Match: MatchExact},
		}},
		{Key: "management", Label: "Management", Items: []Item{
			{Key: "access", Label: "Access Control", Children: []Item{
				{Key: "roles", Label: "Roles", Children: []Item{
					{Key: "role-list", Label: "Role list", Path: "/roles", Permission: permissionRoles, Match: MatchPrefix},
				}},
				{Key: "users", Label: "Users", Path: "/users", Permission: permissionUsers, Match: MatchPrefix},
			}},
		}},
	}, testPermissions())
	if err != nil {
		t.Fatal(err)
	}

	onlyRoles := registry.Prepare("/roles/7/edit", func(key string) bool { return key == permissionRoles })
	if len(onlyRoles) != 1 || onlyRoles[0].Key != "management" || len(onlyRoles[0].Items) != 1 {
		t.Fatalf("unexpected filtered groups: %+v", onlyRoles)
	}
	accessItem := onlyRoles[0].Items[0]
	roleContainer := accessItem.Children[0]
	roleLeaf := roleContainer.Children[0]
	if !accessItem.Active || !accessItem.Open || !roleContainer.Active || !roleContainer.Open || !roleLeaf.Active {
		t.Fatalf("active ancestors not prepared: %+v", accessItem)
	}
	inactive := registry.Prepare("/", func(key string) bool { return key == permissionRoles })
	if len(inactive) != 1 || inactive[0].Items[0].Active || inactive[0].Items[0].Open {
		t.Fatalf("inactive ancestors must render closed: %+v", inactive)
	}

	none := registry.Prepare("/", func(string) bool { return false })
	if len(none) != 0 {
		t.Fatalf("expected empty navigation, got %+v", none)
	}

	all := registry.Prepare("/users", func(string) bool { return true })
	if len(all) != 2 || len(all[1].Items[0].Children) != 2 {
		t.Fatalf("expected all navigation, got %+v", all)
	}
}

func TestNavigationMatchModes(t *testing.T) {
	registry, err := NewRegistry([]Group{{Key: "general", Label: "General", Items: []Item{
		{Key: "dashboard", Label: "Dashboard", Path: "/", Permission: permissionDashboard, Match: MatchExact},
		{Key: "users", Label: "Users", Path: "/users", Permission: permissionUsers, Match: MatchPrefix},
	}}}, testPermissions())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path      string
		activeKey string
	}{
		{path: "/", activeKey: "dashboard"},
		{path: "/users", activeKey: "users"},
		{path: "/users/123/edit", activeKey: "users"},
		{path: "/users2"},
		{path: "/other"},
	}
	for _, test := range tests {
		groups := registry.Prepare(test.path, func(string) bool { return true })
		active := ""
		for _, item := range groups[0].Items {
			if item.Active {
				active = item.Key
			}
		}
		if active != test.activeKey {
			t.Errorf("path %q activated %q, want %q", test.path, active, test.activeKey)
		}
	}

	requestURL, err := url.Parse("/users?page=2")
	if err != nil {
		t.Fatal(err)
	}
	groups := registry.Prepare(requestURL.Path, func(string) bool { return true })
	if !groups[0].Items[1].Active {
		t.Fatal("query string changed active route matching")
	}
}
