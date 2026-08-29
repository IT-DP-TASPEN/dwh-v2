package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ibldzn/go-admin/internal/browserauth"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/reportexport"
)

const (
	activeLimit    = 5
	recentLimit    = 8
	attentionLimit = 8
)

type ingestionReader interface {
	OperationalActivity(context.Context, int, int) (ingestionfeature.OperationalActivity, error)
	DashboardSummary(context.Context, time.Time) (ingestionfeature.DashboardSummary, error)
	NeedsAttention(context.Context, bool, bool, int) ([]ingestionfeature.AttentionItem, error)
}

type exportReader interface {
	HealthForUser(context.Context, uint64) (reportexport.Health, error)
	RecentFailuresForUser(context.Context, uint64, int) ([]reportexport.Job, error)
}

type Service struct {
	ingestion ingestionReader
	exports   exportReader
}

func NewService(ingestion ingestionReader, exports exportReader) *Service {
	return &Service{ingestion: ingestion, exports: exports}
}

func (service *Service) Load(ctx context.Context, principal browserauth.Principal, now time.Time) (Data, error) {
	activity, err := service.ingestion.OperationalActivity(ctx, activeLimit, recentLimit)
	if err != nil {
		return Data{}, err
	}
	summary, err := service.ingestion.DashboardSummary(ctx, now.UTC().Add(-24*time.Hour))
	if err != nil {
		return Data{}, err
	}
	canViewSchedules := principal.Can("schedules.view")
	attention, err := service.ingestion.NeedsAttention(ctx, true, canViewSchedules, attentionLimit)
	if err != nil {
		return Data{}, err
	}
	health, err := service.exports.HealthForUser(ctx, principal.UserID)
	if err != nil {
		return Data{}, err
	}
	canViewExports := principal.Can(reports.PermissionExport)
	if canViewExports {
		failures, queryErr := service.exports.RecentFailuresForUser(ctx, principal.UserID, attentionLimit)
		if queryErr != nil {
			return Data{}, queryErr
		}
		for _, job := range failures {
			activityAt := job.FinishedAtValue().UTC()
			attention = append(attention, ingestionfeature.AttentionItem{
				ID: job.ID, Kind: "export", Name: job.ReportName, Detail: fmt.Sprintf("Export #%d", job.ID),
				URL: fmt.Sprintf("/exports#export-%d", job.ID), Time: formatTime(activityAt), ActivityAt: activityAt,
				Status: ingestionfeature.PresentStatus(string(reportexport.StatusFailed)),
			})
		}
	}
	sort.SliceStable(attention, func(left, right int) bool {
		if !attention[left].ActivityAt.Equal(attention[right].ActivityAt) {
			return attention[left].ActivityAt.After(attention[right].ActivityAt)
		}
		if attention[left].Kind != attention[right].Kind {
			return attention[left].Kind < attention[right].Kind
		}
		return attention[left].ID > attention[right].ID
	})
	if len(attention) > attentionLimit {
		attention = attention[:attentionLimit]
	}
	return Data{
		Summary: Summary{
			ActiveIngestion: activity.ActiveCount, FailedIngestion24h: summary.FailedIngestion24h,
			SchedulerUnresolved: summary.SchedulerUnresolved, ExportQueued: health.Queued,
			ExportRunning: health.Running, ExportFailed: health.Failed,
		},
		Attention: attention, Active: activity.Active, Recent: activity.Recent,
		CanViewSchedules: canViewSchedules, CanViewExports: canViewExports,
	}, nil
}

func formatTime(value time.Time) string { return value.UTC().Format("02 Jan 2006 15:04:05 UTC") }
