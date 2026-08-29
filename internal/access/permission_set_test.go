package access

import "testing"

func TestPermissionSet(t *testing.T) {
	empty := PermissionSet{}
	if empty.Has("sample.view") {
		t.Fatal("zero permission set must deny")
	}

	set := NewPermissionSet([]string{"sample.view"})
	if !set.Has("sample.view") || set.Has("users.view") {
		t.Fatal("permission set returned unexpected result")
	}
}
