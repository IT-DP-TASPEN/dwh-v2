package ingestion

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func OverviewNavigation() navigation.Item {
	return navigation.Item{Key: "ingestion-overview", Label: "Overview", Icon: "activity", Path: "/ingestion",
		AnyPermissions: []string{PermissionView, "sources.view", "schedules.view"}, Match: navigation.MatchExact}
}

func RunsNavigation() navigation.Item {
	return navigation.Item{Key: "ingestion-runs", Label: "Runs", Icon: "history", Path: "/runs", Permission: PermissionView, Match: navigation.MatchPrefix}
}
