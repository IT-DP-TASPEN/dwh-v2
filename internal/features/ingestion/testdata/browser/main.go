package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dashboardfeature "github.com/ibldzn/go-admin/internal/features/dashboard"
	authprofilesfeature "github.com/ibldzn/go-admin/internal/features/fincloudauthprofiles"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	reportsfeature "github.com/ibldzn/go-admin/internal/features/reports"
	schedulesfeature "github.com/ibldzn/go-admin/internal/features/schedules"
	sourcesfeature "github.com/ibldzn/go-admin/internal/features/sources"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/fincloudauth"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	webfiles "github.com/ibldzn/go-admin/web"
)

const address = "127.0.0.1:4173"

type fixture struct {
	renderer    *render.Renderer
	mu          sync.Mutex
	polls       map[uint64]int
	childLoads  map[uint64]int
	waveLoads   map[string]int
	starred     map[uint64]bool
	folders     map[uint64]*uint64
	folderNames map[uint64]string
	schedules   map[uint64]schedulesfeature.Schedule
	authProfile fincloudauth.Profile
	sourceBound bool
}

func main() {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		log.Fatal(err)
	}
	fixture := &fixture{renderer: renderer, polls: map[uint64]int{}, childLoads: map[uint64]int{}, waveLoads: map[string]int{}}
	fixture.resetReports()
	fixture.resetSchedules()
	fixture.resetFincloudAuth()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(webfiles.Files)))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/", fixture.dashboardPage)
	mux.HandleFunc("/case/", fixture.page)
	mux.HandleFunc("/ingestion", fixture.ingestionOverview)
	mux.HandleFunc("/ingestion/summary", fixture.ingestionOverview)
	mux.HandleFunc("/runs", fixture.runsPage)
	mux.HandleFunc("/runs/scheduler-wave", fixture.schedulerWave)
	mux.HandleFunc("/runs/", fixture.status)
	mux.HandleFunc("/schedules", fixture.schedulesPage)
	mux.HandleFunc("/schedules/bulk-action", fixture.scheduleMutation)
	mux.HandleFunc("/fincloud-auth-profiles", fixture.authProfilesPage)
	mux.HandleFunc("/fincloud-auth-profiles/", fixture.authProfilePage)
	mux.HandleFunc("/sources", fixture.sourcesPage)
	mux.HandleFunc("/sources/", fixture.sourceMutation)
	mux.HandleFunc("/reports", fixture.reportsPage)
	mux.HandleFunc("/reports/", fixture.reportMutation)
	mux.HandleFunc("/exports", fixture.exportsPage)
	mux.HandleFunc("/exports/", fixture.exportObject)
	log.Printf("Run Details browser fixture listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
}

func (fixture *fixture) dashboardPage(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	if request.URL.Query().Get("persona") == "reporting" {
		http.Redirect(writer, request, "/reports?persona=reporting", http.StatusSeeOther)
		return
	}
	wave, _ := fixtureSchedulerWave(259)
	data := dashboardfeature.Data{
		Summary: dashboardfeature.Summary{ActiveIngestion: 3, FailedIngestion24h: 2, SchedulerUnresolved: 1, ExportQueued: 1, ExportRunning: 1, ExportFailed: 1},
		Attention: []ingestionfeature.AttentionItem{
			{ID: 240, Kind: "ingestion", Name: "Run All #240", Detail: "Run #240", Time: "28 Aug 2026 09:01:00 UTC", URL: "/runs/240", Status: ingestionfeature.PresentStatus("failed"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 2, Complete: 2, Failed: 1}},
			{ID: 700, Kind: "scheduler", Name: "Schedule A", Detail: "Journal Transaction Report · Occurrence #700", Time: "28 Aug 2026 10:04:00 UTC", URL: "/schedules/10/occurrences/700", Status: ingestionfeature.StatusView{Key: "retry_waiting", Label: "Retry waiting", Class: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300"}},
			{ID: 12, Kind: "export", Name: "NPL Report", Detail: "Export #12", Time: "28 Aug 2026 08:00:00 UTC", URL: "/exports#export-12", Status: ingestionfeature.PresentStatus("failed")},
		},
		Active: []ingestionfeature.OperationalItem{
			{RunListItem: ingestionfeature.RunListItem{RunView: fixtureRuns()[5]}, InterestingChildren: []ingestionfeature.RunView{fixtureRuns()[3]}},
			{RunListItem: ingestionfeature.RunListItem{RunView: ingestionfeature.RunView{ID: 261, Kind: "job", JobName: "Loan Detail", Trigger: "direct", CreatedAt: "2026-08-28 10:06:00 UTC", Status: ingestionfeature.PresentStatus("running")}}},
			{RunListItem: ingestionfeature.RunListItem{SchedulerWave: wave}},
		},
		Recent: []ingestionfeature.RunListItem{
			{RunView: fixtureRuns()[0]}, {RunView: fixtureRuns()[9]}, {SchedulerWave: wave},
		},
		CanViewSchedules: true, CanViewExports: true,
	}
	pageData := adminshell.PageData{Title: "Dashboard", AppName: "Browser fixture", CurrentPath: "/", Navigation: fixtureNavigation(true, "/"), Data: data}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/dashboard/index", "admin", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) ingestionOverview(writer http.ResponseWriter, request *http.Request) {
	data := ingestionfeature.OverviewData{
		Runs: &ingestionfeature.RunOverview{Running: 2, Queued: 1}, Sources: &ingestionfeature.SourceOverview{Enabled: 35, Disabled: 1},
		Schedules: &ingestionfeature.ScheduleOverview{Overdue: 1, Retrying: 1, BlockedBusy: 1}, CanRunAll: true, AttentionVisible: true,
	}
	if request.URL.Query().Get("healthy") != "1" {
		data.Attention = []ingestionfeature.AttentionItem{{ID: 240, Kind: "ingestion", Name: "Run All #240", Detail: "Run #240", Time: "28 Aug 2026 09:01:00 UTC", URL: "/runs/240", Status: ingestionfeature.PresentStatus("failed"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 2, Complete: 2, Failed: 1}}}
	}
	pageData := adminshell.PageData{Title: "Ingestion Overview", AppName: "Browser fixture", CurrentPath: "/ingestion", Navigation: fixtureNavigation(true, "/ingestion"), Data: data}
	name := "admin"
	if request.Header.Get("HX-Request") == "true" {
		name = "ingestion-summary"
	}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/ingestion/index", name, pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func fixtureNavigation(operational bool, path string) []navigation.GroupView {
	groups := make([]navigation.GroupView, 0, 3)
	if operational {
		groups = append(groups,
			navigation.GroupView{Key: "general", Label: "General", Items: []navigation.ItemView{{Key: "dashboard", Label: "Dashboard", Icon: "layout-dashboard", Path: "/", Depth: 1, Active: path == "/"}}},
			navigation.GroupView{Key: "data-ingestion", Label: "Data Ingestion", Items: []navigation.ItemView{{Key: "ingestion-overview", Label: "Overview", Icon: "activity", Path: "/ingestion", Depth: 1, Active: path == "/ingestion"}, {Key: "ingestion-runs", Label: "Runs", Icon: "history", Path: "/runs", Depth: 1}, {Key: "schedules", Label: "Schedules", Icon: "calendar-clock", Path: "/schedules", Depth: 1, Active: path == "/schedules"}}},
		)
	}
	groups = append(groups, navigation.GroupView{Key: "reporting", Label: "Reporting", Items: []navigation.ItemView{{Key: "reports", Label: "Reports", Icon: "file-chart-column", Path: "/reports", Depth: 1, Active: path == "/reports"}, {Key: "report-exports", Label: "Exports", Icon: "file-down", Path: "/exports", Depth: 1, Active: path == "/exports"}}})
	return groups
}

func fixtureExports() []reportexport.VisibleJob {
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	expires := now.Add(24 * time.Hour)
	deleted := now.Add(time.Hour)
	requesterA, usernameA := "User A", "user-a"
	requesterB, usernameB := "User B", "user-b"
	artifactA, artifactB, artifactMissing := "owned.xlsx", "cross-user.xlsx", "missing.xlsx"
	kind := "xlsx"
	size := uint64(8)
	failure := "Export stopped because report execution failed."
	return []reportexport.VisibleJob{
		{ID: 203, ReportID: 13, ReportName: "Missing Artifact", SubmittedByUserID: 2, RequesterName: &requesterB, RequesterUsername: &usernameB, Status: reportexport.StatusSucceeded, ArtifactName: &artifactMissing, ArtifactType: &kind, ArtifactSize: &size, ArtifactExpiresAt: &expires, CreatedAt: now.Add(3 * time.Minute), StartedAt: timePointer(now.Add(4 * time.Minute)), FinishedAt: timePointer(now.Add(5 * time.Minute)), UpdatedAt: now.Add(5 * time.Minute), Attempt: 1, ProgressRows: 1},
		{ID: 202, ReportID: 12, ReportName: "Expired Report", SubmittedByUserID: 2, RequesterName: &requesterB, RequesterUsername: &usernameB, Status: reportexport.StatusSucceeded, ArtifactDeletedAt: &deleted, CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(6 * time.Minute), Attempt: 1},
		{ID: 201, ReportID: 11, ReportName: "Failed Report", SubmittedByUserID: 2, RequesterName: &requesterB, RequesterUsername: &usernameB, Status: reportexport.StatusFailed, FailureMessage: &failure, CreatedAt: now.Add(time.Minute), FinishedAt: timePointer(now.Add(2 * time.Minute)), UpdatedAt: now.Add(2 * time.Minute), Attempt: 1},
		{ID: 200, ReportID: 10, ReportName: "Cross-user Report", SubmittedByUserID: 2, RequesterName: &requesterB, RequesterUsername: &usernameB, Status: reportexport.StatusSucceeded, ArtifactName: &artifactB, ArtifactType: &kind, ArtifactSize: &size, ArtifactExpiresAt: &expires, CreatedAt: now, StartedAt: timePointer(now.Add(time.Minute)), FinishedAt: timePointer(now.Add(2 * time.Minute)), UpdatedAt: now.Add(2 * time.Minute), Attempt: 1, ProgressRows: 1},
		{ID: 100, ReportID: 9, ReportName: "Owned Report", SubmittedByUserID: 1, RequesterName: &requesterA, RequesterUsername: &usernameA, Status: reportexport.StatusSucceeded, ArtifactName: &artifactA, ArtifactType: &kind, ArtifactSize: &size, ArtifactExpiresAt: &expires, CreatedAt: now.Add(-time.Minute), StartedAt: timePointer(now), FinishedAt: timePointer(now.Add(time.Minute)), UpdatedAt: now.Add(time.Minute), Attempt: 1, ProgressRows: 1},
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func (fixture *fixture) exportsPage(writer http.ResponseWriter, request *http.Request) {
	normal := request.URL.Query().Get("persona") == "normal"
	scope := reportexport.ScopeAll
	if normal {
		scope = reportexport.ScopeMine
	}
	switch requested := request.URL.Query().Get("scope"); requested {
	case "":
	case string(reportexport.ScopeMine):
		scope = reportexport.ScopeMine
	case string(reportexport.ScopeAll):
		if normal {
			http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		scope = reportexport.ScopeAll
	default:
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	rows := make([]reportexport.VisibleJob, 0)
	for _, job := range fixtureExports() {
		if scope == reportexport.ScopeAll || normal && job.SubmittedByUserID == 1 || !normal && job.SubmittedByUserID == 3 {
			rows = append(rows, job)
		}
	}
	page := pagination.New(1, reportsfeature.ExportPageSize, int64(len(rows)))
	data := reportsfeature.ExportsData{
		Rows: rows, Scope: scope, CanViewAll: !normal, Pagination: page,
		AllURL: "/exports?scope=all", MineURL: "/exports?scope=mine",
	}
	pageData := adminshell.PageData{Title: "Exports", AppName: "Browser fixture", CurrentPath: "/exports", Navigation: fixtureNavigation(false, "/exports"), Data: data}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/reports/exports", "admin", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) exportObject(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/exports/")
	download := strings.HasSuffix(path, "/download")
	if download {
		path = strings.TrimSuffix(path, "/download")
	}
	id, err := strconv.ParseUint(path, 10, 64)
	if err != nil || id == 0 {
		http.NotFound(writer, request)
		return
	}
	var job reportexport.VisibleJob
	found := false
	for _, candidate := range fixtureExports() {
		if candidate.ID == id {
			job, found = candidate, true
			break
		}
	}
	normal := request.URL.Query().Get("persona") == "normal"
	if !found || normal && job.SubmittedByUserID != 1 {
		http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if download {
		if job.Status != reportexport.StatusSucceeded || job.ArtifactDeletedAt != nil || job.ArtifactName == nil {
			http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if job.ID == 203 {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, *job.ArtifactName))
		writer.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		_, _ = writer.Write([]byte("artifact"))
		return
	}
	backScope := reportexport.ScopeAll
	if normal {
		backScope = reportexport.ScopeMine
	}
	data := reportsfeature.ExportDetailData{Job: job, CanDownload: job.Status == reportexport.StatusSucceeded && job.ArtifactDeletedAt == nil && job.ArtifactName != nil, BackURL: "/exports?scope=" + string(backScope)}
	pageData := adminshell.PageData{Title: fmt.Sprintf("Export #%d", job.ID), AppName: "Browser fixture", CurrentPath: "/exports", Navigation: fixtureNavigation(false, "/exports"), Data: data}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/reports/export", "admin", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) runsPage(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("HX-Request") != "true" {
		fixture.mu.Lock()
		fixture.childLoads = map[uint64]int{}
		fixture.waveLoads = map[string]int{}
		fixture.mu.Unlock()
	}
	filter := ingestionfeature.RunFilter{
		Job: request.URL.Query().Get("job"), Status: request.URL.Query().Get("status"), Kind: request.URL.Query().Get("kind"), Trigger: request.URL.Query().Get("trigger"),
	}
	grouped := filter.Kind == "" && filter.Trigger == ""
	rows := make([]ingestionfeature.RunListItem, 0)
	for _, row := range fixtureRuns() {
		if grouped {
			if wave, leader := fixtureSchedulerWave(row.ID); wave != nil {
				if leader && fixtureWaveMatches(wave.ScheduledForKey, filter) {
					rows = append(rows, ingestionfeature.RunListItem{SchedulerWave: wave})
				}
				continue
			}
		}
		if filter.Kind == "" && row.Kind == "run_all_child" {
			continue
		}
		if filter.Job != "" && row.JobKey != filter.Job || filter.Status != "" && row.Status.Key != filter.Status || filter.Kind != "" && row.Kind != filter.Kind || filter.Trigger != "" && row.Trigger != filter.Trigger {
			continue
		}
		rows = append(rows, ingestionfeature.RunListItem{RunView: row})
	}
	catalog, err := core.NewCatalog()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	data := ingestionfeature.RunPage{
		Rows: rows, Filter: filter, Pagination: pagination.New(1, ingestionfeature.RunPageSize, int64(len(rows))), Jobs: catalog.Jobs(),
		Statuses: []string{"planned", "queued", "running", "succeeded", "failed", "skipped", "cancelled", "abandoned", "completed", "completed_with_skips"},
		Kinds:    []ingestionfeature.RunKindOption{{Value: "job", Label: "Job"}, {Value: "run_all_parent", Label: "Run All parent"}, {Value: "run_all_child", Label: "Run All child"}},
		Triggers: []string{"direct", "scheduler", "run_all"},
	}
	pageData := adminshell.PageData{Title: "Runs", AppName: "Browser fixture", Data: data}
	name := "admin"
	if request.Header.Get("HX-Request") == "true" {
		name = "runs-table"
	}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", name, pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func fixtureRuns() []ingestionfeature.RunView {
	parentID, olderParentID, retryParentID, refreshParentID := uint64(251), uint64(240), uint64(230), uint64(220)
	return []ingestionfeature.RunView{
		{ID: 260, Kind: "job", KindLabel: "job", JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "direct", CreatedAt: "2026-08-28 10:05:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 259, Kind: "job", KindLabel: "job", JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "scheduler", CreatedAt: "2026-08-28 10:04:00 UTC", Status: ingestionfeature.PresentStatus("failed")},
		{ID: 258, Kind: "job", KindLabel: "job", JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "scheduler", CreatedAt: "2026-08-28 10:03:30 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 253, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &parentID, ChildPosition: 2, JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "run_all", CreatedAt: "2026-08-28 10:03:00 UTC", Status: ingestionfeature.PresentStatus("running")},
		{ID: 252, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &parentID, ChildPosition: 1, JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "run_all", CreatedAt: "2026-08-28 10:02:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: parentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 10:01:00 UTC", Status: ingestionfeature.PresentStatus("running"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 2, Complete: 1, Running: 1}},
		{ID: 239, Kind: "job", KindLabel: "job", JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "scheduler", CreatedAt: "2026-08-28 09:04:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 242, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &olderParentID, ChildPosition: 2, JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "run_all", CreatedAt: "2026-08-28 09:03:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 241, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &olderParentID, ChildPosition: 1, JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "run_all", CreatedAt: "2026-08-28 09:02:00 UTC", Status: ingestionfeature.PresentStatus("failed")},
		{ID: olderParentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 09:01:00 UTC", Status: ingestionfeature.PresentStatus("completed"), Terminal: true, RunAllSummary: &ingestionfeature.RunAllSummary{Total: 2, Complete: 2, Failed: 1}},
		{ID: 231, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &retryParentID, ChildPosition: 1, JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "run_all", CreatedAt: "2026-08-28 08:02:00 UTC", Status: ingestionfeature.PresentStatus("queued")},
		{ID: retryParentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 08:01:00 UTC", Status: ingestionfeature.PresentStatus("queued"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 1}},
		{ID: 221, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &refreshParentID, ChildPosition: 1, JobKey: "loan_detail", JobName: "Loan Detail", Trigger: "run_all", CreatedAt: "2026-08-28 07:02:00 UTC", Status: ingestionfeature.PresentStatus("running")},
		{ID: refreshParentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 07:01:00 UTC", Status: ingestionfeature.PresentStatus("running"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 1, Running: 1}},
	}
}

func fixtureSchedulerWave(runID uint64) (*ingestionfeature.SchedulerWaveView, bool) {
	switch runID {
	case 259:
		return &ingestionfeature.SchedulerWaveView{ScheduledFor: time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC), ScheduledForLabel: "27 Aug 2026 18:00:00 UTC",
			ScheduledForKey: "2026-08-27T18:00:00Z", URL: "/runs/scheduler-wave?scheduled_for=2026-08-27T18%3A00%3A00Z", DOMID: "1787853600000000",
			ActivityAt: "2026-08-28 10:04:00 UTC", Summary: "1 occurrence · 1 unresolved · 1 attempt", Total: 1, Unresolved: 1, Attempts: 1}, true
	case 258:
		wave, _ := fixtureSchedulerWave(259)
		return wave, false
	case 239:
		return &ingestionfeature.SchedulerWaveView{ScheduledFor: time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC), ScheduledForLabel: "27 Aug 2026 06:00:00 UTC",
			ScheduledForKey: "2026-08-27T06:00:00Z", URL: "/runs/scheduler-wave?scheduled_for=2026-08-27T06%3A00%3A00Z", DOMID: "1787810400000000",
			ActivityAt: "2026-08-28 09:04:00 UTC", Summary: "1 occurrence · 1 resolved · 1 attempt", Total: 1, Resolved: 1, Attempts: 1}, true
	}
	return nil, false
}

func fixtureWaveMatches(key string, filter ingestionfeature.RunFilter) bool {
	for _, row := range fixtureRuns() {
		wave, _ := fixtureSchedulerWave(row.ID)
		if wave == nil || wave.ScheduledForKey != key {
			continue
		}
		if filter.Job != "" && row.JobKey != filter.Job || filter.Status != "" && row.Status.Key != filter.Status {
			continue
		}
		return true
	}
	return false
}

func (fixture *fixture) schedulerWave(writer http.ResponseWriter, request *http.Request) {
	key := request.URL.Query().Get("scheduled_for")
	fixture.mu.Lock()
	fixture.waveLoads[key]++
	loads := fixture.waveLoads[key]
	fixture.mu.Unlock()
	if key == "2026-08-27T06:00:00Z" && loads == 1 {
		http.Error(writer, "temporary scheduler fragment failure", http.StatusInternalServerError)
		return
	}
	var detail ingestionfeature.SchedulerWaveDetail
	switch key {
	case "2026-08-27T18:00:00Z":
		wave, _ := fixtureSchedulerWave(259)
		attempts := []ingestionfeature.SchedulerAttemptView{{RunID: 259, AttemptNo: 1, JobName: "Journal Transaction Report", Status: ingestionfeature.PresentStatus("failed"), CreatedAt: "2026-08-28 10:04:00 UTC"}}
		occurrenceStatus := "unresolved"
		if loads >= 2 {
			attemptStatus := "running"
			if loads >= 3 {
				attemptStatus, occurrenceStatus = "succeeded", "resolved"
			}
			attempts = append(attempts, ingestionfeature.SchedulerAttemptView{RunID: 258, AttemptNo: 2, JobName: "Journal Transaction Report", Status: ingestionfeature.PresentStatus(attemptStatus), CreatedAt: "2026-08-28 10:10:00 UTC"})
			wave.Attempts, wave.Summary, wave.ActivityAt = 2, "1 occurrence · 1 unresolved · 2 attempts", "2026-08-28 10:10:00 UTC"
		}
		if loads >= 3 {
			wave.Resolved, wave.Unresolved, wave.Summary = 1, 0, "1 occurrence · 1 resolved · 2 attempts"
		}
		detail = ingestionfeature.SchedulerWaveDetail{Wave: *wave, Occurrences: []ingestionfeature.SchedulerOccurrenceView{{
			ScheduleID: 10, OccurrenceID: 700, ScheduleName: "Schedule A", JobName: "Journal Transaction Report", Status: ingestionfeature.PresentStatus(occurrenceStatus), Attempts: attempts,
		}}}
	case "2026-08-27T06:00:00Z":
		wave, _ := fixtureSchedulerWave(239)
		detail = ingestionfeature.SchedulerWaveDetail{Wave: *wave, Occurrences: []ingestionfeature.SchedulerOccurrenceView{{
			ScheduleID: 12, OccurrenceID: 702, ScheduleName: "Schedule C", JobName: "CIF Opening Report", Status: ingestionfeature.PresentStatus("resolved"), Attempts: []ingestionfeature.SchedulerAttemptView{{
				RunID: 239, AttemptNo: 1, JobName: "CIF Opening Report", Status: ingestionfeature.PresentStatus("succeeded"), CreatedAt: "2026-08-28 09:04:00 UTC",
			}},
		}}}
	default:
		http.NotFound(writer, request)
		return
	}
	pageData := adminshell.PageData{Title: "Scheduler wave", AppName: "Browser fixture", Data: detail}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", "scheduler-wave-attempts", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) page(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/case/fincloud-auth" {
		fixture.resetFincloudAuth()
		http.Redirect(writer, request, "/fincloud-auth-profiles", http.StatusSeeOther)
		return
	}
	if request.URL.Path == "/case/fincloud-auth-stale" {
		fixture.resetFincloudAuth()
		fixture.mu.Lock()
		fixture.authProfile.RoleID, fixture.authProfile.LocationID = "Missing-Role", "Missing-Location"
		fixture.mu.Unlock()
		http.Redirect(writer, request, "/fincloud-auth-profiles/1/edit", http.StatusSeeOther)
		return
	}
	if request.URL.Path == "/case/schedules" {
		fixture.resetSchedules()
		http.Redirect(writer, request, "/schedules", http.StatusSeeOther)
		return
	}
	if request.URL.Path == "/case/reports" {
		fixture.resetReports()
		fixture.reportsPage(writer, request)
		return
	}
	id := map[string]uint64{"/case/cancel": 1, "/case/recover": 2, "/case/terminal": 3}[request.URL.Path]
	if id == 0 {
		http.NotFound(writer, request)
		return
	}
	fixture.mu.Lock()
	fixture.polls[id] = 0
	fixture.mu.Unlock()
	body, err := fixture.render("run-status", detail(id, 0))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(writer, `<!doctype html><html lang="en"><head><meta charset="utf-8"><script defer src="/static/js/app.js"></script></head><body><main>%s</main></body></html>`, body)
}

func (fixture *fixture) resetFincloudAuth() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.authProfile = fincloudauth.Profile{ID: 1, Name: "Operations", Username: "CaseSensitive", RoleID: "Role-A", LocationID: "Location-01", Status: fincloudauth.StatusDisabled, Revision: 1}
	fixture.sourceBound = false
}

func (fixture *fixture) authProfilesPage(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost {
		_ = request.ParseForm()
		fixture.mu.Lock()
		fixture.authProfile = fincloudauth.Profile{ID: 1, Name: strings.TrimSpace(request.PostFormValue("name")), Username: request.PostFormValue("username"), RoleID: request.PostFormValue("role_id"), LocationID: request.PostFormValue("location_id"), Status: fincloudauth.StatusDisabled, Revision: 1}
		fixture.mu.Unlock()
		http.Redirect(writer, request, "/fincloud-auth-profiles/1", http.StatusSeeOther)
		return
	}
	fixture.mu.Lock()
	profile := fixture.authProfile
	fixture.mu.Unlock()
	canManage := request.URL.Query().Get("persona") != "view"
	data := authprofilesfeature.ListData{Rows: []fincloudauth.Profile{profile}, CanManage: canManage}
	fixture.renderAdmin(writer, request, "features/fincloudauthprofiles/index", "Fincloud Auth Profiles", "/fincloud-auth-profiles", data)
}

func (fixture *fixture) authProfilePage(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/fincloud-auth-profiles/")
	if path == "new" && request.Method == http.MethodGet {
		values := fixtureAuthListValues()
		fixture.renderAdmin(writer, request, "features/fincloudauthprofiles/form", "New Fincloud Auth Profile", "/fincloud-auth-profiles", authprofilesfeature.FormData{Roles: values.Roles, Locations: values.Locations, Errors: map[string]string{}})
		return
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	profile := fixture.authProfile
	switch {
	case path == "1" && request.Method == http.MethodGet:
		data := authprofilesfeature.DetailData{Value: profile, CanManage: request.URL.Query().Get("persona") != "view"}
		fixture.renderAdmin(writer, request, "features/fincloudauthprofiles/show", "Fincloud Auth Profile", "/fincloud-auth-profiles", data)
	case path == "1/edit" && request.Method == http.MethodGet:
		values := fixtureAuthListValues()
		data := authprofilesfeature.FormData{ID: profile.ID, Revision: profile.Revision, Name: profile.Name, Username: profile.Username, RoleID: profile.RoleID, LocationID: profile.LocationID, Roles: values.Roles, Locations: values.Locations, RoleUnavailable: !hasListValue(values.Roles, profile.RoleID), LocationUnavailable: !hasListValue(values.Locations, profile.LocationID), Errors: map[string]string{}}
		fixture.renderAdmin(writer, request, "features/fincloudauthprofiles/form", "Edit Fincloud Auth Profile", "/fincloud-auth-profiles", data)
	case path == "1" && request.Method == http.MethodPost:
		_ = request.ParseForm()
		fixture.authProfile.Name = strings.TrimSpace(request.PostFormValue("name"))
		fixture.authProfile.Username = request.PostFormValue("username")
		fixture.authProfile.RoleID = request.PostFormValue("role_id")
		fixture.authProfile.LocationID = request.PostFormValue("location_id")
		fixture.authProfile.Revision++
		http.Redirect(writer, request, "/fincloud-auth-profiles/1", http.StatusSeeOther)
	case path == "1/test" && request.Method == http.MethodPost:
		http.Redirect(writer, request, "/fincloud-auth-profiles/1?notice=fincloud-auth-profile-test-ok", http.StatusSeeOther)
	case path == "1/state" && request.Method == http.MethodPost:
		_ = request.ParseForm()
		fixture.authProfile.Status = fincloudauth.Status(request.PostFormValue("status"))
		fixture.authProfile.Revision++
		http.Redirect(writer, request, "/fincloud-auth-profiles/1", http.StatusSeeOther)
	default:
		http.NotFound(writer, request)
	}
}

func fixtureAuthListValues() fincloud.AuthListValues {
	return fincloud.AuthListValues{Roles: []fincloud.ListValue{{ID: "Role-A", Description: "Operations"}, {ID: "Role-B", Description: "Reporting"}}, Locations: []fincloud.ListValue{{ID: "Location-01", Description: "Head Office"}, {ID: "Location-02", Description: "Branch"}}}
}

func hasListValue(values []fincloud.ListValue, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func (fixture *fixture) sourcesPage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	fixture.mu.Lock()
	profile, bound := fixture.authProfile, fixture.sourceBound
	fixture.mu.Unlock()
	catalog, _ := core.NewCatalog()
	jobs := catalog.Jobs()
	row := sourcesfeature.Source{Job: jobs[0], Category: "Fixed", Enabled: true, ConfigurationRequired: !bound || profile.Status != fincloudauth.StatusActive}
	if bound {
		row.AuthProfileID, row.AuthProfileName, row.AuthProfileStatus = &profile.ID, profile.Name, string(profile.Status)
	}
	data := sourcesfeature.ListData{Rows: []sourcesfeature.Source{row}, AuthProfiles: fixtureAuthProfiles(profile), CanManage: true}
	fixture.renderAdmin(writer, request, "features/sources/index", "Sources", "/sources", data)
}

func (fixture *fixture) sourceMutation(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/auth-profile") {
		http.NotFound(writer, request)
		return
	}
	_ = request.ParseForm()
	fixture.mu.Lock()
	failed := request.PostFormValue("profile_id") == "2"
	if !failed {
		fixture.sourceBound = request.PostFormValue("profile_id") == "1"
	}
	profile, bound := fixture.authProfile, fixture.sourceBound
	fixture.mu.Unlock()
	if request.Header.Get("HX-Request") == "true" {
		catalog, _ := core.NewCatalog()
		row := sourcesfeature.Source{Job: catalog.Jobs()[0], Category: "Fixed", Enabled: true, ConfigurationRequired: !bound || profile.Status != fincloudauth.StatusActive}
		if bound {
			row.AuthProfileID, row.AuthProfileName, row.AuthProfileStatus = &profile.ID, profile.Name, string(profile.Status)
		}
		message := ""
		if failed {
			message = "Fincloud Auth Profile cannot be assigned; persisted selection restored."
		}
		data := sourcesfeature.SourceRowData{Source: row, AuthProfiles: fixtureAuthProfiles(profile), CanManage: true, Error: message}
		if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/sources/index", "source-row", data); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(writer, request, "/sources", http.StatusSeeOther)
}

func fixtureAuthProfiles(profile fincloudauth.Profile) []sourcesfeature.AuthProfileOption {
	return []sourcesfeature.AuthProfileOption{{ID: profile.ID, Name: profile.Name, Status: string(profile.Status)}, {ID: 2, Name: "Rejected profile", Status: "active"}}
}

func (fixture *fixture) renderAdmin(writer http.ResponseWriter, request *http.Request, template, title, path string, data any) {
	page := adminshell.PageData{Title: title, AppName: "Browser fixture", CurrentPath: path, Navigation: fixtureNavigation(true, path), Data: data}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, template, "admin", page); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) resetSchedules() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.schedules = map[uint64]schedulesfeature.Schedule{
		101: {ID: 101, Name: "Disabled schedule", JobName: "CIF Detail", CronExpression: "0 1 * * *", Timezone: "Asia/Jakarta", Revision: 1, StateLabel: "Disabled"},
		102: {ID: 102, Name: "Enabled schedule", JobName: "Saving Detail", CronExpression: "0 2 * * *", Timezone: "Asia/Jakarta", Enabled: true, Revision: 1, StateLabel: "Enabled", NextRunAt: "31 Aug 2026 18:00:00 UTC"},
		103: {ID: 103, Name: "Archived schedule", JobName: "Loan Detail", CronExpression: "0 3 * * *", Timezone: "Asia/Jakarta", Archived: true, Revision: 2, StateLabel: "Archived", ArchivedAt: "30 Aug 2026 10:00:00 UTC"},
	}
}

func (fixture *fixture) schedulesPage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	filter := schedulesfeature.Filter{Enabled: request.URL.Query().Get("enabled"), Archived: request.URL.Query().Get("archived")}
	fixture.mu.Lock()
	rows := make([]schedulesfeature.Schedule, 0, len(fixture.schedules))
	for _, schedule := range fixture.schedules {
		if filter.Enabled != "" && schedule.Enabled != (filter.Enabled == "true") {
			continue
		}
		if filter.Archived != "" && schedule.Archived != (filter.Archived == "true") {
			continue
		}
		rows = append(rows, schedule)
	}
	fixture.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	selectable := 0
	for _, schedule := range rows {
		if !schedule.Archived {
			selectable++
		}
	}
	query := request.URL.Query()
	query.Del("notice")
	returnQuery := ""
	if encoded := query.Encode(); encoded != "" {
		returnQuery = "?" + encoded
	}
	data := schedulesfeature.ListData{Rows: rows, Filter: filter, Pagination: pagination.New(1, schedulesfeature.PageSize, int64(len(rows))),
		CanEnableDisable: true, CanArchive: true, CanSelect: true, SelectableCount: selectable, ReturnQuery: returnQuery}
	pageData := adminshell.PageData{Title: "Schedules", AppName: "Browser fixture", CurrentPath: "/schedules", Navigation: fixtureNavigation(true, "/schedules"), Data: data}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/schedules/index", "admin", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) scheduleMutation(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.ParseForm() != nil || request.PostFormValue("confirmed") != "yes" {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	action := request.PostFormValue("action")
	if action != "enable" && action != "disable" && action != "archive" {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	ids := make([]uint64, 0, len(request.PostForm["schedule_ids"]))
	for _, value := range request.PostForm["schedule_ids"] {
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	fixture.mu.Lock()
	for _, id := range ids {
		schedule, found := fixture.schedules[id]
		if !found || schedule.Archived {
			fixture.mu.Unlock()
			http.Error(writer, "conflict", http.StatusConflict)
			return
		}
	}
	for _, id := range ids {
		schedule := fixture.schedules[id]
		switch action {
		case "enable":
			if !schedule.Enabled {
				schedule.Enabled, schedule.StateLabel, schedule.NextRunAt = true, "Enabled", "31 Aug 2026 18:00:00 UTC"
				schedule.Revision++
			}
		case "disable":
			if schedule.Enabled {
				schedule.Enabled, schedule.StateLabel, schedule.NextRunAt = false, "Disabled", ""
				schedule.Revision++
			}
		case "archive":
			schedule.Enabled, schedule.Archived, schedule.StateLabel, schedule.NextRunAt = false, true, "Archived", ""
			schedule.ArchivedAt = "31 Aug 2026 12:00:00 UTC"
			schedule.Revision++
		}
		fixture.schedules[id] = schedule
	}
	fixture.mu.Unlock()
	target := "/schedules"
	if request.URL.RawQuery != "" {
		target += "?" + request.URL.RawQuery
	}
	http.Redirect(writer, request, target, http.StatusSeeOther)
}

func (fixture *fixture) resetReports() {
	kreditID := uint64(3)
	depositoID := uint64(4)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.starred = map[uint64]bool{1: false, 2: true}
	fixture.folders = map[uint64]*uint64{1: &kreditID, 2: &depositoID}
	fixture.folderNames = map[uint64]string{3: "Kredit", 4: "Deposito"}
}

func (fixture *fixture) reportsPage(writer http.ResponseWriter, request *http.Request) {
	data := fixture.reportData(request, 0, "", "")
	pageData := adminshell.PageData{Title: "Reports", AppName: "Browser fixture", CurrentPath: "/reports", Data: data}
	if request.URL.Query().Get("persona") == "reporting" {
		pageData.Navigation = fixtureNavigation(false, "/reports")
	}
	name := "admin"
	if request.Header.Get("HX-Request") == "true" {
		name = "report-browser"
	}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/reports/index", name, pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) reportMutation(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.ParseForm() != nil {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	fixture.mu.Lock()
	switch {
	case len(parts) == 3 && parts[0] == "reports" && parts[2] == "star":
		id, _ := strconv.ParseUint(parts[1], 10, 64)
		fixture.starred[id] = request.PostFormValue("starred") == "true"
	case len(parts) == 3 && parts[0] == "reports" && parts[2] == "folder":
		id, _ := strconv.ParseUint(parts[1], 10, 64)
		folderID, _ := strconv.ParseUint(request.PostFormValue("folder_id"), 10, 64)
		if _, exists := fixture.folderNames[folderID]; exists {
			fixture.folders[id] = &folderID
		} else {
			fixture.folders[id] = nil
		}
	case len(parts) == 4 && parts[0] == "reports" && parts[1] == "folders" && parts[3] == "delete":
		folderID, _ := strconv.ParseUint(parts[2], 10, 64)
		delete(fixture.folderNames, folderID)
		for reportID, assignedFolderID := range fixture.folders {
			if assignedFolderID != nil && *assignedFolderID == folderID {
				fixture.folders[reportID] = nil
			}
		}
	case len(parts) == 4 && parts[0] == "reports" && parts[1] == "folders" && parts[3] == "rename":
		folderID, _ := strconv.ParseUint(parts[2], 10, 64)
		submitted := request.PostFormValue("name")
		name := strings.TrimSpace(submitted)
		errorMessage := ""
		if name == "" {
			errorMessage = "folder name must not be empty"
		}
		for otherID, otherName := range fixture.folderNames {
			if otherID != folderID && strings.EqualFold(otherName, name) {
				errorMessage = "Folder name already exists."
			}
		}
		if errorMessage != "" {
			fixture.mu.Unlock()
			fixture.renderReports(writer, request, http.StatusUnprocessableEntity, fixture.reportData(request, folderID, submitted, errorMessage))
			return
		}
		if _, exists := fixture.folderNames[folderID]; !exists {
			fixture.mu.Unlock()
			http.NotFound(writer, request)
			return
		}
		fixture.folderNames[folderID] = name
	default:
		fixture.mu.Unlock()
		http.NotFound(writer, request)
		return
	}
	fixture.mu.Unlock()
	if len(parts) == 4 {
		http.Redirect(writer, request, "/reports"+reportQuery(request), http.StatusSeeOther)
		return
	}
	if request.Header.Get("HX-Request") == "true" {
		fixture.reportsPage(writer, request)
		return
	}
	http.Redirect(writer, request, "/reports"+reportQuery(request), http.StatusSeeOther)
}

func (fixture *fixture) renderReports(writer http.ResponseWriter, request *http.Request, status int, data reportsfeature.OrganizationData) {
	pageData := adminshell.PageData{Title: "Reports", AppName: "Browser fixture", Data: data}
	if err := fixture.renderer.RenderPartial(writer, status, "features/reports/index", "admin", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (fixture *fixture) reportData(request *http.Request, renameFolderID uint64, renameValue, renameError string) reportsfeature.OrganizationData {
	fixture.mu.Lock()
	starred := map[uint64]bool{1: fixture.starred[1], 2: fixture.starred[2]}
	folders := map[uint64]*uint64{1: fixture.folders[1], 2: fixture.folders[2]}
	folderNames := make(map[uint64]string, len(fixture.folderNames))
	for id, name := range fixture.folderNames {
		folderNames[id] = name
	}
	fixture.mu.Unlock()

	query := strings.TrimSpace(request.URL.Query().Get("q"))
	starredScope := request.URL.Query().Get("starred") == "1"
	requestedFolderID, _ := strconv.ParseUint(request.URL.Query().Get("folder"), 10, 64)
	_, folderScope := folderNames[requestedFolderID]
	values := []reporting.RuntimeReport{
		{ID: 1, Name: "NPL per Cabang", Description: "Loan quality", DatasourceName: "DWH", FolderID: folders[1], Starred: starred[1]},
		{ID: 2, Name: "Nominatif Deposito", Description: "Funding", DatasourceName: "DWH", FolderID: folders[2], Starred: starred[2]},
	}
	visible := make([]reporting.RuntimeReport, 0, len(values))
	for _, value := range values {
		if query != "" && !strings.Contains(strings.ToLower(value.Name+" "+value.Description), strings.ToLower(query)) {
			continue
		}
		if starredScope && !value.Starred || folderScope && (value.FolderID == nil || *value.FolderID != requestedFolderID) {
			continue
		}
		visible = append(visible, value)
	}
	filterValues := url.Values{}
	if query != "" {
		filterValues.Set("q", query)
	}
	if starredScope {
		filterValues.Set("starred", "1")
	} else if folderScope {
		filterValues.Set("folder", strconv.FormatUint(requestedFolderID, 10))
	}
	returnQuery := ""
	if encoded := filterValues.Encode(); encoded != "" {
		returnQuery = "?" + encoded
	}
	data := reportsfeature.OrganizationData{
		Query: query, Heading: "All Reports", EmptyMessage: "No reports match this search.", ReturnQuery: returnQuery,
		AllURL: fixtureReportURL(query, true, 0), StarredURL: fixtureReportURL(query, false, 0), StarredScope: starredScope,
		FolderScope: folderScope, StarredVisibleCount: boolCount(starred[1]) + boolCount(starred[2]),
		Rows: make([]reportsfeature.ReportCard, 0, len(visible)), StarredRows: make([]reportsfeature.ReportCard, 0),
		Folders: make([]reportsfeature.FolderView, 0, len(folderNames)),
	}
	if starredScope {
		data.Heading, data.EmptyMessage = "Starred", "No starred reports match this search."
	}
	if folderScope {
		data.Heading, data.EmptyMessage, data.CurrentFolderID = folderNames[requestedFolderID], "No reports in this folder.", requestedFolderID
	}
	folderIDs := make([]uint64, 0, len(folderNames))
	for folderID := range folderNames {
		folderIDs = append(folderIDs, folderID)
	}
	sort.Slice(folderIDs, func(left, right int) bool { return folderNames[folderIDs[left]] < folderNames[folderIDs[right]] })
	for _, folderID := range folderIDs {
		folderCount := 0
		for _, assignedFolderID := range folders {
			if assignedFolderID != nil && *assignedFolderID == folderID {
				folderCount++
			}
		}
		view := reportsfeature.FolderView{
			Value: reporting.UserReportFolder{ID: folderID, Name: folderNames[folderID], VisibleReportCount: folderCount}, Current: folderScope && requestedFolderID == folderID,
			URL: fixtureReportURL(query, false, folderID), DeleteMessage: fmt.Sprintf("Reports will not be deleted. %d currently visible reports will return to No Folder / All Reports.", folderCount),
		}
		if folderID == renameFolderID {
			view.Editing, view.RenameValue, view.NameError = true, renameValue, renameError
		}
		data.Folders = append(data.Folders, view)
	}
	for _, value := range visible {
		card := reportsfeature.ReportCard{Value: value, Unfiled: value.FolderID == nil, ReturnQuery: returnQuery}
		for _, folderID := range folderIDs {
			selected := value.FolderID != nil && *value.FolderID == folderID
			card.FolderOptions = append(card.FolderOptions, reportsfeature.FolderOption{ID: folderID, Name: folderNames[folderID], Selected: selected})
			if selected {
				card.CurrentFolderName = folderNames[folderID]
			}
		}
		data.Rows = append(data.Rows, card)
		if !starredScope && !folderScope && value.Starred {
			data.StarredRows = append(data.StarredRows, card)
		}
	}
	return data
}

func fixtureReportURL(query string, all bool, folderID uint64) string {
	values := url.Values{}
	if query != "" {
		values.Set("q", query)
	}
	if !all && folderID == 0 {
		values.Set("starred", "1")
	} else if folderID != 0 {
		values.Set("folder", strconv.FormatUint(folderID, 10))
	}
	if encoded := values.Encode(); encoded != "" {
		return "/reports?" + encoded
	}
	return "/reports"
}

func reportQuery(request *http.Request) string {
	if request.URL.RawQuery == "" {
		return ""
	}
	return "?" + request.URL.RawQuery
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (fixture *fixture) status(writer http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) == 3 && parts[0] == "runs" && parts[2] == "children" {
		fixture.runAllChildren(writer, request, parts[1])
		return
	}
	if len(parts) != 3 || parts[0] != "runs" || parts[2] != "status" {
		http.NotFound(writer, request)
		return
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id < 1 || id > 3 {
		http.NotFound(writer, request)
		return
	}
	fixture.mu.Lock()
	fixture.polls[id]++
	polls := fixture.polls[id]
	fixture.mu.Unlock()
	value := detail(id, polls)
	value.SwapCancelAction = request.URL.Query().Get("can_cancel") != strconv.FormatBool(value.CanCancel)
	value.SwapRecoverAction = request.URL.Query().Get("can_recover") != strconv.FormatBool(value.CanRecover)
	if value.SwapCancelAction {
		writer.Header().Set("X-Cancel-Action-Swap", "true")
	}
	if value.SwapRecoverAction {
		writer.Header().Set("X-Recover-Action-Swap", "true")
	}
	body, err := fixture.render("run-status-poll", value)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(body))
}

func (fixture *fixture) runAllChildren(writer http.ResponseWriter, request *http.Request, rawID string) {
	parentID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || parentID != 251 && parentID != 240 && parentID != 230 && parentID != 220 {
		http.NotFound(writer, request)
		return
	}
	fixture.mu.Lock()
	fixture.childLoads[parentID]++
	loads := fixture.childLoads[parentID]
	fixture.mu.Unlock()
	if parentID == 230 && loads == 1 {
		http.Error(writer, "fixture child load failure", http.StatusInternalServerError)
		return
	}
	if parentID == 220 && loads == 2 {
		http.Error(writer, "fixture child refresh failure", http.StatusInternalServerError)
		return
	}
	children := fixtureRunAllDetail(parentID, loads)
	pageData := adminshell.PageData{Title: "Run All children", AppName: "Browser fixture", Data: children}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", "run-all-children", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func fixtureRunAllDetail(parentID uint64, loads int) ingestionfeature.RunChildren {
	parent := ingestionfeature.RunView{ID: parentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", Status: ingestionfeature.PresentStatus("running")}
	child := func(id uint64, position uint16, job, status string) ingestionfeature.RunView {
		return ingestionfeature.RunView{ID: id, Kind: "run_all_child", ChildPosition: position, JobName: job, Trigger: "run_all", Status: ingestionfeature.PresentStatus(status)}
	}
	switch parentID {
	case 251:
		secondStatus := "running"
		summary := ingestionfeature.RunAllSummary{Total: 2, Complete: 1, Running: 1}
		if loads >= 2 {
			secondStatus, summary = "succeeded", ingestionfeature.RunAllSummary{Total: 2, Complete: 2}
		}
		if loads >= 3 {
			parent.Status, parent.Terminal = ingestionfeature.PresentStatus("completed"), true
		}
		parent.RunAllSummary = &summary
		return ingestionfeature.RunChildren{Parent: parent, Rows: []ingestionfeature.RunView{child(252, 1, "CIF Opening Report", "succeeded"), child(253, 2, "Journal Transaction Report", secondStatus)}}
	case 240:
		summary := ingestionfeature.RunAllSummary{Total: 2, Complete: 2, Failed: 1}
		parent.Status, parent.Terminal, parent.RunAllSummary = ingestionfeature.PresentStatus("completed"), true, &summary
		return ingestionfeature.RunChildren{Parent: parent, Rows: []ingestionfeature.RunView{child(241, 1, "Journal Transaction Report", "failed"), child(242, 2, "CIF Opening Report", "succeeded")}}
	case 230:
		status := "queued"
		summary := ingestionfeature.RunAllSummary{Total: 1}
		if loads >= 3 {
			status, summary.Running = "running", 1
			parent.Status = ingestionfeature.PresentStatus("running")
		} else {
			parent.Status = ingestionfeature.PresentStatus("queued")
		}
		parent.RunAllSummary = &summary
		return ingestionfeature.RunChildren{Parent: parent, Rows: []ingestionfeature.RunView{child(231, 1, "CIF Opening Report", status)}}
	default:
		status := "running"
		summary := ingestionfeature.RunAllSummary{Total: 1, Running: 1}
		if loads >= 3 {
			status, summary = "succeeded", ingestionfeature.RunAllSummary{Total: 1, Complete: 1}
		}
		parent.RunAllSummary = &summary
		return ingestionfeature.RunChildren{Parent: parent, Rows: []ingestionfeature.RunView{child(221, 1, "Loan Detail", status)}}
	}
}

func (fixture *fixture) render(name string, detail ingestionfeature.RunDetail) (string, error) {
	response := httptest.NewRecorder()
	if err := fixture.renderer.RenderPartial(response, http.StatusOK, "features/ingestion/show", name, render.PageData{Data: detail}); err != nil {
		return "", err
	}
	return response.Body.String(), nil
}

func detail(id uint64, polls int) ingestionfeature.RunDetail {
	run := ingestionfeature.RunView{
		ID: id, Kind: "job", JobName: "Browser fixture", Status: ingestionfeature.PresentStatus("running"),
		ProgressTotal: 10, ProgressSucceeded: uint64(polls), ProgressUnit: "items", CurrentStep: fmt.Sprintf("poll_%d", polls),
		StartedAt: "2026-08-26 10:00:00", OwnerID: strings.Repeat("a", 64), HeartbeatAt: "2026-08-26 10:00:00",
		HeartbeatEvidence: "2026-08-26T03:00:00Z",
	}
	if id == 1 {
		run.FincloudAuthProfileID, run.FincloudAuthProfileRevision = 1, 7
		run.FincloudAuthProfileName, run.FincloudAuthUsername = "Operations", "CaseSensitive"
		run.FincloudAuthRoleID, run.FincloudAuthLocationID = "Role-A", "Location-01"
	}
	result := ingestionfeature.RunDetail{Run: run, Polling: true}
	switch id {
	case 1:
		result.CanCancel = true
		result.TechnicalErrors = technicalEvents(polls)
	case 2:
		result.CanRecover = polls >= 1
	case 3:
		result.CanCancel, result.CanRecover = true, true
		if polls >= 2 {
			result.Run.Status = ingestionfeature.PresentStatus("succeeded")
			result.Run.Terminal, result.Run.FinishedAt = true, "2026-08-26 10:01:00"
			result.CanCancel, result.CanRecover, result.Polling = false, false, false
		}
	}
	return result
}

func technicalEvents(count int) []ingestionfeature.TechnicalEventView {
	result := make([]ingestionfeature.TechnicalEventView, count)
	for index := range result {
		result[index] = ingestionfeature.TechnicalEventView{
			ID: uint64(index + 1), OccurredAt: fmt.Sprintf("2026-08-26 10:00:%02d", index+1),
			Severity: "info", EventKind: "retry", Class: "fixture", Step: "poll", Operation: "poll",
			ErrorMessage: fmt.Sprintf("diagnostic %d", index+1),
		}
	}
	return result
}
