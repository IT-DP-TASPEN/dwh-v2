package fincloudauthprofiles

import "github.com/ibldzn/go-admin/internal/platform/navigation"

func Navigation() navigation.Item {
	return navigation.Item{Key: "fincloud-auth-profiles", Label: "Auth Profiles", Icon: "key", Path: "/fincloud-auth-profiles", Permission: PermissionView, Match: navigation.MatchPrefix}
}
