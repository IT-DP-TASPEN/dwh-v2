package render

import "testing"

func TestNoticeRegistryAllowsKnownIDsOnly(t *testing.T) {
	notice := NoticeFromID("user-created")
	if notice == nil || notice.Severity != "success" || notice.Title == "" || notice.Message == "" {
		t.Fatalf("unexpected notice: %+v", notice)
	}
	notice.Title = "changed"
	if NoticeFromID("user-created").Title == "changed" {
		t.Fatal("notice registry returned mutable shared value")
	}
	if NoticeFromID("<script>alert(1)</script>") != nil || NoticeFromID("") != nil {
		t.Fatal("unknown notice was reflected")
	}
}
