package datasources

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "report-datasources", Label: "Datasources", Icon: "server", Path: "/datasources", Permission: PermissionView, Match: navigation.MatchPrefix}
}
