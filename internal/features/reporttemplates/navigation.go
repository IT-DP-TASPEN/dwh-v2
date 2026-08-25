package reporttemplates

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "report-templates", Label: "Templates", Icon: "file-code", Path: "/report-templates", Permission: PermissionView, Match: navigation.MatchPrefix}
}
