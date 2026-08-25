package reports

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "reports", Label: "Reports", Icon: "table-2", Path: "/reports", Permission: PermissionView, Match: navigation.MatchPrefix}
}
func ExportsNavigation() navigation.Item {
	return navigation.Item{Key: "report-exports", Label: "My exports", Icon: "file-down", Path: "/exports", Permission: PermissionExport, Match: navigation.MatchPrefix}
}
