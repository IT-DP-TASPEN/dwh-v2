package sources

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "sources", Label: "Sources", Icon: "database", Path: "/sources", Permission: PermissionView, Match: navigation.MatchPrefix}
}
