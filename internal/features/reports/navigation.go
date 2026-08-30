package reports

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "reports", Label: "Reports", Icon: "table-2", Path: "/reports", Permission: PermissionView, Match: navigation.MatchPrefix}
}
func ExportsNavigation() navigation.Item {
	return navigation.Item{Key: "report-exports", Label: "Exports", Icon: "file-down", Path: "/exports", AnyPermissions: []string{PermissionExport, PermissionViewAllExports}, Match: navigation.MatchPrefix}
}
