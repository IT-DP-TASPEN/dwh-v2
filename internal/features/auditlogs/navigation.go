package auditlogs

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "audit-logs", Label: "Audit Logs", Icon: "scroll-text", Path: "/audit-logs", Permission: PermissionView, Match: navigation.MatchPrefix}
}
