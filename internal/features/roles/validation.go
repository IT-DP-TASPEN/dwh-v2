package roles

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/user"
)

var roleSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

func NormalizeSlug(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}

func ValidateSlug(slug string) error {
	slug = NormalizeSlug(slug)
	if slug == "" {
		return fmt.Errorf("slug must not be empty")
	}
	if len(slug) > user.MaxNameLength {
		return fmt.Errorf("slug must be at most %d characters", user.MaxNameLength)
	}
	if !roleSlugPattern.MatchString(slug) {
		return fmt.Errorf("slug must use lowercase letters, numbers, hyphens, or underscores")
	}
	if slug == access.AdminRoleSlug || slug == access.UserRoleSlug {
		return fmt.Errorf("slug %q is reserved", slug)
	}
	return nil
}

func validateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("name must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > user.MaxNameLength {
		return fmt.Errorf("name must be at most %d characters", user.MaxNameLength)
	}
	return nil
}
