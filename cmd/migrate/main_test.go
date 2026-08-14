package main

import (
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/config"
)

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

func TestMigrationMutationPolicy(t *testing.T) {
	production := config.Config{App: config.AppConfig{Environment: "production"}, Database: config.DatabaseConfig{Name: "dwh"}}
	if err := validateCommandPolicy(production, "up", "dwh"); err != nil {
		t.Fatalf("confirmed production up: %v", err)
	}
	for _, test := range []struct {
		name, command, confirmed string
		config                   config.Config
		want                     string
	}{
		{"missing confirmation", "up", "", production, "--confirm-database dwh"},
		{"wrong confirmation", "up", "other", production, "--confirm-database dwh"},
		{"production down", "down", "", production, "down is disabled"},
		{"legacy mutation", "up", "dwh2", config.Config{App: config.AppConfig{Environment: "development"}, Database: config.DatabaseConfig{Name: "dwh2"}}, "legacy/reference-only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCommandPolicy(test.config, test.command, test.confirmed)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %q", err, test.want)
			}
		})
	}
	if err := validateCommandPolicy(config.Config{App: config.AppConfig{Environment: "development"}, Database: config.DatabaseConfig{Name: "dwh"}}, "up", ""); err != nil {
		t.Fatalf("development up unexpectedly blocked: %v", err)
	}
}
