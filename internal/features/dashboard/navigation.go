package dashboard

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "dashboard", Label: "Dashboard", Icon: "layout-dashboard", Path: "/", Permission: PermissionView, Match: navigation.MatchExact}
}
