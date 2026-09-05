package customdatasets

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "custom-datasets", Label: "Custom Datasets", Icon: "table", Path: "/custom-datasets", Permission: PermissionView, Match: navigation.MatchPrefix}
}
