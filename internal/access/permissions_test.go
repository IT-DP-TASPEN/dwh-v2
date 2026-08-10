package access

import (
	"strings"
	"testing"
)

func TestPermissionRegistryValidation(t *testing.T) {
	valid := PermissionDefinition{Key: "users.view", Name: "View Users", Group: "Users"}
	tests := []struct {
		name        string
		definitions []PermissionDefinition
		want        string
	}{
		{"duplicate", []PermissionDefinition{valid, valid}, "duplicate"},
		{"empty key", []PermissionDefinition{{Name: "Name", Group: "Group"}}, "invalid key"},
		{"malformed key", []PermissionDefinition{{Key: "Users.View", Name: "Name", Group: "Group"}}, "invalid key"},
		{"empty name", []PermissionDefinition{{Key: "users.view", Group: "Users"}}, "empty name"},
		{"empty group", []PermissionDefinition{{Key: "users.view", Name: "View Users"}}, "empty group"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRegistry(test.definitions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}
