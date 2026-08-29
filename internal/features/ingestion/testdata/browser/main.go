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

	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	reportsfeature "github.com/ibldzn/go-admin/internal/features/reports"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/render"
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
}

func main() {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		log.Fatal(err)
	}
	fixture := &fixture{renderer: renderer, polls: map[uint64]int{}, childLoads: map[uint64]int{}, waveLoads: map[string]int{}}
	fixture.resetReports()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(webfiles.Files)))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/case/", fixture.page)
	mux.HandleFunc("/runs", fixture.runsPage)
	mux.HandleFunc("/runs/scheduler-wave", fixture.schedulerWave)
	mux.HandleFunc("/runs/", fixture.status)
	mux.HandleFunc("/reports", fixture.reportsPage)
	mux.HandleFunc("/reports/", fixture.reportMutation)
	log.Printf("Run Details browser fixture listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
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
	parentID, olderParentID, retryParentID := uint64(251), uint64(240), uint64(230)
	return []ingestionfeature.RunView{
		{ID: 260, Kind: "job", KindLabel: "job", JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "direct", CreatedAt: "2026-08-28 10:05:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 259, Kind: "job", KindLabel: "job", JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "scheduler", CreatedAt: "2026-08-28 10:04:00 UTC", Status: ingestionfeature.PresentStatus("failed")},
		{ID: 258, Kind: "job", KindLabel: "job", JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "scheduler", CreatedAt: "2026-08-28 10:03:30 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 253, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &parentID, ChildPosition: 2, JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "run_all", CreatedAt: "2026-08-28 10:03:00 UTC", Status: ingestionfeature.PresentStatus("failed")},
		{ID: 252, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &parentID, ChildPosition: 1, JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "run_all", CreatedAt: "2026-08-28 10:02:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: parentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 10:01:00 UTC", Status: ingestionfeature.PresentStatus("running"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 36, Complete: 32, Failed: 2, Running: 1}},
		{ID: 239, Kind: "job", KindLabel: "job", JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "scheduler", CreatedAt: "2026-08-28 09:04:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 242, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &olderParentID, ChildPosition: 2, JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "run_all", CreatedAt: "2026-08-28 09:03:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: 241, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &olderParentID, ChildPosition: 1, JobKey: "journal_transaction_report", JobName: "Journal Transaction Report", Trigger: "run_all", CreatedAt: "2026-08-28 09:02:00 UTC", Status: ingestionfeature.PresentStatus("failed")},
		{ID: olderParentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 09:01:00 UTC", Status: ingestionfeature.PresentStatus("completed"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 2, Complete: 2, Failed: 1}},
		{ID: 231, Kind: "run_all_child", KindLabel: "run all child", ParentRunID: &retryParentID, ChildPosition: 1, JobKey: "cif_opening_report", JobName: "CIF Opening Report", Trigger: "run_all", CreatedAt: "2026-08-28 08:02:00 UTC", Status: ingestionfeature.PresentStatus("succeeded")},
		{ID: retryParentID, Kind: "run_all_parent", KindLabel: "run all parent", Trigger: "direct", CreatedAt: "2026-08-28 08:01:00 UTC", Status: ingestionfeature.PresentStatus("completed"), RunAllSummary: &ingestionfeature.RunAllSummary{Total: 1, Complete: 1}},
	}
}

func fixtureSchedulerWave(runID uint64) (*ingestionfeature.SchedulerWaveView, bool) {
	switch runID {
	case 259:
		return &ingestionfeature.SchedulerWaveView{ScheduledFor: time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC), ScheduledForLabel: "27 Aug 2026 18:00:00 UTC",
			ScheduledForKey: "2026-08-27T18:00:00Z", URL: "/runs/scheduler-wave?scheduled_for=2026-08-27T18%3A00%3A00Z", DOMID: "1787853600000000",
			ActivityAt: "2026-08-28 10:04:00 UTC", Summary: "2 occurrences · 1 resolved · 1 unresolved · 2 attempts", Total: 2, Resolved: 1, Unresolved: 1, Attempts: 2}, true
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
		detail = ingestionfeature.SchedulerWaveDetail{ScheduledFor: "27 Aug 2026 18:00:00 UTC", Occurrences: []ingestionfeature.SchedulerOccurrenceView{
			{ScheduleID: 10, OccurrenceID: 700, ScheduleName: "Schedule A", JobName: "Journal Transaction Report", Status: ingestionfeature.PresentStatus("resolved"), Attempts: []ingestionfeature.SchedulerAttemptView{
				{RunID: 259, AttemptNo: 1, JobName: "Journal Transaction Report", Status: ingestionfeature.PresentStatus("failed"), CreatedAt: "2026-08-28 10:04:00 UTC"},
				{RunID: 258, AttemptNo: 2, JobName: "Journal Transaction Report", Status: ingestionfeature.PresentStatus("succeeded"), CreatedAt: "2026-08-28 10:03:30 UTC"},
			}},
			{ScheduleID: 11, OccurrenceID: 701, ScheduleName: "Schedule B", JobName: "Loan Detail", Status: ingestionfeature.PresentStatus("unresolved")},
		}}
	case "2026-08-27T06:00:00Z":
		detail = ingestionfeature.SchedulerWaveDetail{ScheduledFor: "27 Aug 2026 06:00:00 UTC", Occurrences: []ingestionfeature.SchedulerOccurrenceView{{
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
	pageData := adminshell.PageData{Title: "Reports", AppName: "Browser fixture", Data: data}
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
	if err != nil || parentID != 251 && parentID != 240 && parentID != 230 {
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
	children := ingestionfeature.RunChildren{ParentID: parentID}
	for _, row := range fixtureRuns() {
		if row.ParentRunID != nil && *row.ParentRunID == parentID {
			children.Rows = append(children.Rows, row)
		}
	}
	sort.Slice(children.Rows, func(left, right int) bool {
		return children.Rows[left].ChildPosition < children.Rows[right].ChildPosition
	})
	pageData := adminshell.PageData{Title: "Run All children", AppName: "Browser fixture", Data: children}
	if err := fixture.renderer.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", "run-all-children", pageData); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
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
