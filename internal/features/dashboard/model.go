package dashboard

import ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"

type Summary struct {
	ActiveIngestion, FailedIngestion24h, SchedulerUnresolved uint64
	ExportQueued, ExportRunning, ExportFailed                uint64
}

func (summary Summary) ExportProcessing() uint64 { return summary.ExportQueued + summary.ExportRunning }

type Data struct {
	Summary          Summary
	Attention        []ingestionfeature.AttentionItem
	Active           []ingestionfeature.OperationalItem
	Recent           []ingestionfeature.RunListItem
	CanViewSchedules bool
	CanViewExports   bool
}
