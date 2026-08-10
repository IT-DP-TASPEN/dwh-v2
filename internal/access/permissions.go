package access

import (
	"fmt"
	"regexp"
	"strings"
)

var permissionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func ValidateRegistry(definitions []PermissionDefinition) error {
	seen := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		if !permissionKeyPattern.MatchString(definition.Key) {
			return fmt.Errorf("permission %d has invalid key %q", index, definition.Key)
		}
		if _, exists := seen[definition.Key]; exists {
			return fmt.Errorf("duplicate permission key %q", definition.Key)
		}
		if strings.TrimSpace(definition.Name) == "" {
			return fmt.Errorf("permission %q has empty name", definition.Key)
		}
		if strings.TrimSpace(definition.Group) == "" {
			return fmt.Errorf("permission %q has empty group", definition.Key)
		}
		seen[definition.Key] = struct{}{}
	}
	return nil
}
