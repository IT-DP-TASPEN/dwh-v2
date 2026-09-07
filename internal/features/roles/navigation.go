package roles

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "roles", Label: "Roles", Icon: "user-shield", Path: "/roles", Permission: PermissionView, Match: navigation.MatchPrefix}
}
