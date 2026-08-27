package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"

	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	reportsfeature "github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/reporting"
	webfiles "github.com/ibldzn/go-admin/web"
)

const address = "127.0.0.1:4173"

type fixture struct {
	renderer  *render.Renderer
	mu        sync.Mutex
	polls     map[uint64]int
	starred   map[uint64]bool
	folders   map[uint64]*uint64
	hasFolder bool
}

func main() {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		log.Fatal(err)
	}
	fixture := &fixture{renderer: renderer, polls: map[uint64]int{}}
	fixture.resetReports()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(webfiles.Files)))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/case/", fixture.page)
	mux.HandleFunc("/runs/", fixture.status)
	mux.HandleFunc("/reports", fixture.reportsPage)
	mux.HandleFunc("/reports/", fixture.reportMutation)
	log.Printf("Run Details browser fixture listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
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
	folderID := uint64(3)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.starred = map[uint64]bool{1: false, 2: true}
	fixture.folders = map[uint64]*uint64{1: &folderID, 2: nil}
	fixture.hasFolder = true
}

func (fixture *fixture) reportsPage(writer http.ResponseWriter, request *http.Request) {
	data := fixture.reportData(request)
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
		if request.PostFormValue("folder_id") == "3" && fixture.hasFolder {
			folderID := uint64(3)
			fixture.folders[id] = &folderID
		} else {
			fixture.folders[id] = nil
		}
	case len(parts) == 4 && parts[0] == "reports" && parts[1] == "folders" && parts[2] == "3" && parts[3] == "delete":
		fixture.hasFolder = false
		for id := range fixture.folders {
			fixture.folders[id] = nil
		}
	default:
		fixture.mu.Unlock()
		http.NotFound(writer, request)
		return
	}
	fixture.mu.Unlock()
	if len(parts) == 4 {
		http.Redirect(writer, request, "/reports", http.StatusSeeOther)
		return
	}
	if request.Header.Get("HX-Request") == "true" {
		fixture.reportsPage(writer, request)
		return
	}
	http.Redirect(writer, request, "/reports"+reportQuery(request), http.StatusSeeOther)
}

func (fixture *fixture) reportData(request *http.Request) reportsfeature.OrganizationData {
	fixture.mu.Lock()
	starred := map[uint64]bool{1: fixture.starred[1], 2: fixture.starred[2]}
	folders := map[uint64]*uint64{1: fixture.folders[1], 2: fixture.folders[2]}
	hasFolder := fixture.hasFolder
	fixture.mu.Unlock()

	query := strings.TrimSpace(request.URL.Query().Get("q"))
	starredScope := request.URL.Query().Get("starred") == "1"
	folderScope := request.URL.Query().Get("folder") == "3" && hasFolder
	values := []reporting.RuntimeReport{
		{ID: 1, Name: "NPL per Cabang", Description: "Loan quality", DatasourceName: "DWH", FolderID: folders[1], Starred: starred[1]},
		{ID: 2, Name: "Nominatif Deposito", Description: "Funding", DatasourceName: "DWH", FolderID: folders[2], Starred: starred[2]},
	}
	visible := make([]reporting.RuntimeReport, 0, len(values))
	for _, value := range values {
		if query != "" && !strings.Contains(strings.ToLower(value.Name+" "+value.Description), strings.ToLower(query)) {
			continue
		}
		if starredScope && !value.Starred || folderScope && (value.FolderID == nil || *value.FolderID != 3) {
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
		filterValues.Set("folder", "3")
	}
	returnQuery := ""
	if encoded := filterValues.Encode(); encoded != "" {
		returnQuery = "?" + encoded
	}
	data := reportsfeature.OrganizationData{
		Query: query, Heading: "All Reports", EmptyMessage: "No reports match this search.", ReturnQuery: returnQuery,
		AllURL: fixtureReportURL(query, ""), StarredURL: fixtureReportURL(query, "starred"), StarredScope: starredScope,
		FolderScope: folderScope, StarredVisibleCount: boolCount(starred[1]) + boolCount(starred[2]),
		Rows: make([]reportsfeature.ReportCard, 0, len(visible)), StarredRows: make([]reportsfeature.ReportCard, 0),
		Folders: make([]reportsfeature.FolderView, 0, 1),
	}
	if starredScope {
		data.Heading, data.EmptyMessage = "Starred", "No starred reports match this search."
	}
	if folderScope {
		data.Heading, data.EmptyMessage, data.CurrentFolderID = "Kredit", "No reports in this folder.", 3
	}
	folderCount := 0
	for _, folderID := range folders {
		if folderID != nil && *folderID == 3 {
			folderCount++
		}
	}
	if hasFolder {
		data.Folders = append(data.Folders, reportsfeature.FolderView{
			Value: reporting.UserReportFolder{ID: 3, Name: "Kredit", VisibleReportCount: folderCount}, Current: folderScope,
			URL: fixtureReportURL(query, "folder"), DeleteMessage: fmt.Sprintf("Reports will not be deleted. %d currently visible reports will return to No Folder / All Reports.", folderCount),
		})
	}
	for _, value := range visible {
		card := reportsfeature.ReportCard{Value: value, Unfiled: value.FolderID == nil, ReturnQuery: returnQuery}
		if hasFolder {
			card.FolderOptions = []reportsfeature.FolderOption{{ID: 3, Name: "Kredit", Selected: value.FolderID != nil && *value.FolderID == 3}}
		}
		data.Rows = append(data.Rows, card)
		if !starredScope && !folderScope && value.Starred {
			data.StarredRows = append(data.StarredRows, card)
		}
	}
	return data
}

func fixtureReportURL(query, scope string) string {
	values := url.Values{}
	if query != "" {
		values.Set("q", query)
	}
	if scope == "starred" {
		values.Set("starred", "1")
	} else if scope == "folder" {
		values.Set("folder", "3")
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
