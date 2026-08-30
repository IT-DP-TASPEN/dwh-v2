package reportexport

import "testing"

func TestAuthorizedDownloadAccessIsTrueOr(t *testing.T) {
	for _, test := range []struct {
		owner, viewAll bool
		want           DownloadAccess
	}{
		{want: ""},
		{owner: true, want: DownloadAccessOwner},
		{viewAll: true, want: DownloadAccessViewAll},
		{owner: true, viewAll: true, want: DownloadAccessOwner},
	} {
		if got := authorizedDownloadAccess(test.owner, test.viewAll); got != test.want {
			t.Fatalf("access(%t,%t)=%q want %q", test.owner, test.viewAll, got, test.want)
		}
	}
}

func TestVisibleScope(t *testing.T) {
	where, arguments, err := visibleScope(ScopeMine, 7)
	if err != nil || where != " WHERE j.submitted_by_user_id=?" || len(arguments) != 1 || arguments[0] != uint64(7) {
		t.Fatalf("mine=%q/%v err=%v", where, arguments, err)
	}
	where, arguments, err = visibleScope(ScopeAll, 0)
	if err != nil || where != "" || len(arguments) != 0 {
		t.Fatalf("all=%q/%v err=%v", where, arguments, err)
	}
	if _, _, err := visibleScope(ScopeMine, 0); err == nil {
		t.Fatal("missing effective viewer accepted")
	}
	if _, _, err := visibleScope("invalid", 7); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestVisibleJobRequesterFallback(t *testing.T) {
	if got := (VisibleJob{SubmittedByUserID: 42}).RequesterDisplayName(); got != "User #42" {
		t.Fatalf("fallback=%q", got)
	}
	name := "Operations"
	if got := (VisibleJob{SubmittedByUserID: 42, RequesterName: &name}).RequesterDisplayName(); got != name {
		t.Fatalf("name=%q", got)
	}
}
