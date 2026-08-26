package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

const address = "127.0.0.1:4173"

type fixture struct {
	renderer *render.Renderer
	mu       sync.Mutex
	polls    map[uint64]int
}

func main() {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		log.Fatal(err)
	}
	fixture := &fixture{renderer: renderer, polls: map[uint64]int{}}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(webfiles.Files)))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/case/", fixture.page)
	mux.HandleFunc("/runs/", fixture.status)
	log.Printf("Run Details browser fixture listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
}

func (fixture *fixture) page(writer http.ResponseWriter, request *http.Request) {
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
