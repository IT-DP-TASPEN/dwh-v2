package schedules

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "schedules", Label: "Schedules", Icon: "calendar-clock", Path: "/schedules", Permission: PermissionView, Match: navigation.MatchPrefix}
}
