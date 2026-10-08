package main

import "testing"

func TestValidateTargetFailsClosed(t *testing.T) {
	for _, item := range []struct {
		name, configured, actual, confirmation string
		valid                                  bool
	}{
		{"production", "dwh", "dwh", "dwh", true},
		{"legacy_configured", "dwh2", "dwh2", "dwh2", false},
		{"legacy_selected", "dwh", "dwh2", "dwh2", false},
		{"legacy_case_variant", "DWH2", "DWH2", "DWH2", false},
		{"empty_all", "", "", "", false},
		{"empty_configured", "", "dwh", "dwh", false},
		{"empty_selected", "dwh", "", "dwh", false},
		{"empty_confirmation", "dwh", "dwh", "", false},
		{"wrong_selected", "dwh", "other", "other", false},
		{"wrong_confirmation", "dwh", "dwh", "other", false},
		{"confirmation_whitespace", "dwh", "dwh", "dwh ", false},
	} {
		t.Run(item.name, func(t *testing.T) {
			if err := validateTarget(item.configured, item.actual, item.confirmation); (err == nil) != item.valid {
				t.Fatalf("valid=%t error=%v", item.valid, err)
			}
		})
	}
}
