package dashboard

import (
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
)

func Navigation() navigation.Item {
	return navigation.Item{Key: "dashboard", Label: "Dashboard", Icon: "layout-dashboard", Path: "/", Permission: ingestionfeature.PermissionView, Match: navigation.MatchExact}
}
