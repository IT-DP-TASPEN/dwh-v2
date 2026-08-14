package schedules

import (
	"testing"
	"time"
)

func TestScheduleOperationalLabels(t *testing.T) {
	occurrence := uint64(7)
	for _, test := range []struct {
		value   Schedule
		overdue bool
		want    string
	}{
		{Schedule{Archived: true}, false, "Archived"},
		{Schedule{}, false, "Disabled"},
		{Schedule{Enabled: true, DeliveryBlockReason: "source_disabled"}, true, "Blocked — source disabled"},
		{Schedule{Enabled: true, RetryNotBefore: "later"}, true, "Retrying"},
		{Schedule{Enabled: true, OccurrenceMode: "live_coalesced"}, true, "Live catch-up"},
		{Schedule{Enabled: true, OccurrenceID: &occurrence}, true, "Backlog"},
		{Schedule{Enabled: true}, true, "Due"},
		{Schedule{Enabled: true}, false, "Enabled"},
	} {
		if got := scheduleState(test.value, test.overdue); got != test.want {
			t.Fatalf("state=%q want=%q", got, test.want)
		}
	}
	if got := compactDuration(50*time.Hour + 12*time.Minute); got != "2d 2h" {
		t.Fatalf("duration=%q", got)
	}
}
