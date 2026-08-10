package access

import "testing"

func TestPermissionSet(t *testing.T) {
	empty := PermissionSet{}
	if empty.Has("dashboard.view") {
		t.Fatal("zero permission set must deny")
	}

	set := NewPermissionSet([]string{"dashboard.view"})
	if !set.Has("dashboard.view") || set.Has("users.view") {
		t.Fatal("permission set returned unexpected result")
	}
}
