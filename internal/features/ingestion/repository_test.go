package ingestion

import (
	"strings"
	"testing"
)

func TestRunWhereChildVisibility(t *testing.T) {
	for _, test := range []struct {
		name      string
		filter    RunFilter
		wantWhere string
		wantArgs  []any
	}{
		{name: "default", wantWhere: ` WHERE r.kind<>'run_all_child'`},
		{name: "explicit child", filter: RunFilter{Kind: "run_all_child"}, wantWhere: ` WHERE r.kind=?`, wantArgs: []any{"run_all_child"}},
		{name: "explicit parent", filter: RunFilter{Kind: "run_all_parent"}, wantWhere: ` WHERE r.kind=?`, wantArgs: []any{"run_all_parent"}},
		{name: "existing filters", filter: RunFilter{Job: "journal_transaction", Status: "failed", Trigger: "scheduler"},
			wantWhere: ` WHERE r.kind<>'run_all_child' AND r.job_key=? AND r.status=? AND r.trigger_type=?`, wantArgs: []any{"journal_transaction", "failed", "scheduler"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			where, arguments := runWhere(test.filter)
			if where != test.wantWhere || len(arguments) != len(test.wantArgs) {
				t.Fatalf("where=%q args=%v want where=%q args=%v", where, arguments, test.wantWhere, test.wantArgs)
			}
			for index := range arguments {
				if arguments[index] != test.wantArgs[index] {
					t.Fatalf("where=%q args=%v want where=%q args=%v", where, arguments, test.wantWhere, test.wantArgs)
				}
			}
		})
	}
}

func TestRunAllSummaryUsesCanonicalTerminalStatuses(t *testing.T) {
	var summary RunAllSummary
	for status, count := range map[string]uint64{
		"succeeded": 30,
		"failed":    2,
		"running":   1,
		"planned":   3,
	} {
		summary.add(status, count)
	}
	if summary != (RunAllSummary{Total: 36, Complete: 32, Failed: 2, Running: 1}) {
		t.Fatalf("mixed summary=%+v", summary)
	}

	for _, status := range []string{"skipped", "cancelled", "abandoned", "completed", "completed_with_skips"} {
		summary.add(status, 1)
	}
	if summary.Total != 41 || summary.Complete != 37 || summary.Failed != 2 || summary.Running != 1 {
		t.Fatalf("terminal summary=%+v", summary)
	}
}

func TestGroupedRunFiltersQualifyWithoutFilteringWaveAggregation(t *testing.T) {
	query, arguments := groupedRunEntitiesSQL(RunFilter{Job: "journal_transaction_report", Status: "failed"})
	for _, expected := range []string{
		`r.job_key=?`, `r.status=?`, `member_run.job_key=?`, `member_run.status=?`,
		`member_occurrence.scheduled_for=o.scheduled_for`, `MAX(attempt_run.created_at)`, `GROUP BY o.scheduled_for`,
	} {
		if !strings.Contains(query, expected) {
			t.Fatalf("grouped query missing %q: %s", expected, query)
		}
	}
	for _, forbidden := range []string{`attempt_run.job_key=?`, `attempt_run.status=?`} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("wave aggregation was filtered by %q: %s", forbidden, query)
		}
	}
	want := []any{"journal_transaction_report", "failed", "journal_transaction_report", "failed"}
	if len(arguments) != len(want) {
		t.Fatalf("arguments=%v want=%v", arguments, want)
	}
	for index := range want {
		if arguments[index] != want[index] {
			t.Fatalf("arguments=%v want=%v", arguments, want)
		}
	}
}

func TestRunPageURLPreservesFilters(t *testing.T) {
	filter := RunFilter{Job: "journal_transaction", Status: "failed", Kind: "run_all_child", Trigger: "run_all"}
	want := "/runs?job=journal_transaction&kind=run_all_child&page=2&status=failed&trigger=run_all"
	if found := runPageURL(filter, 2); found != want {
		t.Fatalf("url=%q want=%q", found, want)
	}
}

func TestPresentRunKindLabels(t *testing.T) {
	for value, label := range map[string]string{"job": "Job", "run_all_parent": "Run All parent", "run_all_child": "Run All child", "future_kind": "future kind"} {
		if found := presentRunKind(value); found != label {
			t.Errorf("kind %q label=%q want=%q", value, found, label)
		}
	}
}
