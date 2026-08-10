package securityctx

import (
	"testing"

	"github.com/ibldzn/go-admin/internal/access"
)

func TestRequesterCan(t *testing.T) {
	viewer := Requester{Permissions: access.NewPermissionSet([]string{"users.view"})}
	if !viewer.Can("users.view") || viewer.Can("roles.view") {
		t.Fatal("effective permission set returned unexpected result")
	}
	admin := Requester{EffectiveRoleSlug: access.AdminRoleSlug}
	if !admin.Can("anything") {
		t.Fatal("administrator bypass missing")
	}
}
