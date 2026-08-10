package user

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxUsernameLength = 191
	MaxNameLength     = 191
)

func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func ValidateUsername(username string) error {
	username = NormalizeUsername(username)
	if username == "" {
		return fmt.Errorf("username must not be empty")
	}
	if !utf8.ValidString(username) {
		return fmt.Errorf("username must be valid UTF-8")
	}
	if utf8.RuneCountInString(username) > MaxUsernameLength {
		return fmt.Errorf("username must be at most %d characters", MaxUsernameLength)
	}
	return nil
}

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("name must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return fmt.Errorf("name must be at most %d characters", MaxNameLength)
	}
	return nil
}
