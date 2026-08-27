package reports

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/reporting"
)

func TestOrganizationFilterAndURLs(t *testing.T) {
	folderID := uint64(7)
	for _, test := range []struct {
		url     string
		want    reporting.RuntimeReportFilter
		wantURL string
		invalid bool
	}{
		{url: "/reports?q=NPL", want: reporting.RuntimeReportFilter{Query: "NPL"}, wantURL: "/reports?q=NPL"},
		{url: "/reports?starred=1&q=NPL", want: reporting.RuntimeReportFilter{Query: "NPL", Starred: true}, wantURL: "/reports?q=NPL&starred=1"},
		{url: "/reports?folder=7&q=NPL", want: reporting.RuntimeReportFilter{Query: "NPL", FolderID: &folderID}, wantURL: "/reports?folder=7&q=NPL"},
		{url: "/reports?folder=7&starred=1", invalid: true},
		{url: "/reports?folder=0", invalid: true},
		{url: "/reports?starred=true", invalid: true},
	} {
		request := httptest.NewRequest("GET", test.url, nil)
		got, err := organizationFilter(request)
		if test.invalid {
			if err == nil {
				t.Fatalf("%s accepted: %+v", test.url, got)
			}
			continue
		}
		if err != nil || got.Query != test.want.Query || got.Starred != test.want.Starred || !sameFolder(got.FolderID, test.want.FolderID) {
			t.Fatalf("%s filter=%+v error=%v", test.url, got, err)
		}
		if gotURL := reportsURL(got); gotURL != test.wantURL {
			t.Fatalf("%s canonical URL=%q", test.url, gotURL)
		}
	}
}

func TestFolderDeleteMessageUsesVisibleCountOnly(t *testing.T) {
	message := folderDeleteMessage(reporting.UserReportFolder{Name: "Kredit", VisibleReportCount: 2})
	if message != "Reports will not be deleted. 2 currently visible reports will return to No Folder / All Reports." || strings.Contains(message, "dormant") {
		t.Fatalf("message=%q", message)
	}
}

func sameFolder(left, right *uint64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
