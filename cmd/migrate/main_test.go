package main

import "testing"

func TestMigrationNamePattern(t *testing.T) {
	for _, name := range []string{"create_users", "add-role", "phase0"} {
		if !migrationNamePattern.MatchString(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	for _, name := range []string{"", "../outside", "spaces are bad", "!invalid"} {
		if migrationNamePattern.MatchString(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}
