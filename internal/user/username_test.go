package user

import (
	"strings"
	"testing"
)

func TestNormalizeUsername(t *testing.T) {
	if got := NormalizeUsername("  HAYTSAM  "); got != "haytsam" {
		t.Fatalf("expected haytsam, got %q", got)
	}
}

func TestValidateUsername(t *testing.T) {
	if err := ValidateUsername("   "); err == nil {
		t.Fatal("expected empty username error")
	}
	if err := ValidateUsername(strings.Repeat("a", MaxUsernameLength)); err != nil {
		t.Fatalf("expected maximum length to pass: %v", err)
	}
	if err := ValidateUsername(strings.Repeat("a", MaxUsernameLength+1)); err == nil {
		t.Fatal("expected overlong username error")
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("   "); err == nil {
		t.Fatal("expected empty name error")
	}
	if err := ValidateName(strings.Repeat("a", MaxNameLength+1)); err == nil {
		t.Fatal("expected overlong name error")
	}
}
