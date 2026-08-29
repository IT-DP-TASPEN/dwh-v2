package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/reportexport"
)

type fakeIngestionReader struct {
	activity                                    ingestionfeature.OperationalActivity
	summary                                     ingestionfeature.DashboardSummary
	attention                                   []ingestionfeature.AttentionItem
	activeLimit                                 int
	recentLimit                                 int
	attentionLimit                              int
	includeSchedules                            bool
	failedSince                                 time.Time
	activityCalls, summaryCalls, attentionCalls int
}

func (reader *fakeIngestionReader) OperationalActivity(_ context.Context, active, recent int) (ingestionfeature.OperationalActivity, error) {
	reader.activeLimit, reader.recentLimit = active, recent
	reader.activityCalls++
	return reader.activity, nil
}
func (reader *fakeIngestionReader) DashboardSummary(_ context.Context, since time.Time) (ingestionfeature.DashboardSummary, error) {
	reader.failedSince = since
	reader.summaryCalls++
	return reader.summary, nil
}
func (reader *fakeIngestionReader) NeedsAttention(_ context.Context, includeRuns, includeSchedules bool, limit int) ([]ingestionfeature.AttentionItem, error) {
	reader.includeSchedules, reader.attentionLimit = includeSchedules, limit
	reader.attentionCalls++
	if !includeRuns {
		panic("dashboard must always include ingestion attention")
	}
	return append([]ingestionfeature.AttentionItem(nil), reader.attention...), nil
}

type fakeExportReader struct {
	health                    reportexport.Health
	failures                  []reportexport.Job
	healthUser                uint64
	failureUser               uint64
	failureLimit              int
	healthCalls, failureCalls int
}

func (reader *fakeExportReader) HealthForUser(_ context.Context, userID uint64) (reportexport.Health, error) {
	reader.healthUser = userID
	reader.healthCalls++
	return reader.health, nil
}
func (reader *fakeExportReader) RecentFailuresForUser(_ context.Context, userID uint64, limit int) ([]reportexport.Job, error) {
	reader.failureUser, reader.failureLimit = userID, limit
	reader.failureCalls++
	return append([]reportexport.Job(nil), reader.failures...), nil
}

func TestServiceLoadsBoundedCurrentUserOperationalData(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ingestionAttention := make([]ingestionfeature.AttentionItem, 8)
	for index := range ingestionAttention {
		ingestionAttention[index] = ingestionfeature.AttentionItem{ID: uint64(index + 1), Kind: "ingestion", Name: "Failed run", ActivityAt: now.Add(-time.Duration(index+2) * time.Minute)}
	}
	ingestion := &fakeIngestionReader{
		activity:  ingestionfeature.OperationalActivity{ActiveCount: 3},
		summary:   ingestionfeature.DashboardSummary{FailedIngestion24h: 2, SchedulerUnresolved: 4},
		attention: ingestionAttention,
	}
	finished := now.Add(-time.Minute)
	exports := &fakeExportReader{
		health:   reportexport.Health{Queued: 2, Running: 1, Failed: 5},
		failures: []reportexport.Job{{ID: 22, ReportName: "NPL Report", Status: reportexport.StatusFailed, FinishedAt: &finished}},
	}
	principal := browserauth.Principal{UserID: 7, RoleSlug: access.UserRoleSlug,
		Permissions: access.NewPermissionSet([]string{ingestionfeature.PermissionView, "schedules.view", reports.PermissionExport})}
	data, err := NewService(ingestion, exports).Load(context.Background(), principal, now)
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary != (Summary{ActiveIngestion: 3, FailedIngestion24h: 2, SchedulerUnresolved: 4, ExportQueued: 2, ExportRunning: 1, ExportFailed: 5}) {
		t.Fatalf("summary=%+v", data.Summary)
	}
	if ingestion.activeLimit != 5 || ingestion.recentLimit != 8 || ingestion.attentionLimit != 8 || !ingestion.includeSchedules || !ingestion.failedSince.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("ingestion bounds: active=%d recent=%d attention=%d schedules=%t since=%s", ingestion.activeLimit, ingestion.recentLimit, ingestion.attentionLimit, ingestion.includeSchedules, ingestion.failedSince)
	}
	if ingestion.activityCalls != 1 || ingestion.summaryCalls != 1 || ingestion.attentionCalls != 1 || exports.healthCalls != 1 || exports.failureCalls != 1 {
		t.Fatalf("read model calls: ingestion=%d/%d/%d exports=%d/%d", ingestion.activityCalls, ingestion.summaryCalls, ingestion.attentionCalls, exports.healthCalls, exports.failureCalls)
	}
	if exports.healthUser != 7 || exports.failureUser != 7 || exports.failureLimit != 8 {
		t.Fatalf("export scope: health=%d failures=%d limit=%d", exports.healthUser, exports.failureUser, exports.failureLimit)
	}
	if len(data.Attention) != 8 || data.Attention[0].Kind != "export" || data.Attention[0].URL != "/exports#export-22" || data.Attention[0].Detail != "Export #22" {
		t.Fatalf("attention=%+v", data.Attention)
	}
	if !data.CanViewSchedules || !data.CanViewExports || data.Summary.ExportProcessing() != 3 {
		t.Fatalf("capability/read model=%+v", data)
	}
}

func TestServiceKeepsDownstreamHealthAggregateOnlyWithoutDetailPermissions(t *testing.T) {
	ingestion := &fakeIngestionReader{}
	exports := &fakeExportReader{health: reportexport.Health{Running: 1, Failed: 2}, failures: []reportexport.Job{{ID: 99}}}
	principal := browserauth.Principal{UserID: 8, RoleSlug: access.UserRoleSlug, Permissions: access.NewPermissionSet([]string{ingestionfeature.PermissionView})}
	data, err := NewService(ingestion, exports).Load(context.Background(), principal, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ingestion.includeSchedules || exports.failureUser != 0 || data.CanViewSchedules || data.CanViewExports || len(data.Attention) != 0 {
		t.Fatalf("detail leaked without permissions: data=%+v exportFailureUser=%d", data, exports.failureUser)
	}
	if data.Summary.ExportRunning != 1 || data.Summary.ExportFailed != 2 {
		t.Fatalf("aggregate health missing: %+v", data.Summary)
	}
}
