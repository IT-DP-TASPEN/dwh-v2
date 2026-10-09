package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestAccessLog(t *testing.T) {
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	responder := render.NewErrorResponder(renderer, "Test", logger)
	service := &fakeAuthentication{principal: browserauth.Principal{UserID: 1}}
	router, token := testRouterWithLogger(t, service, func(router chi.Router) {
		router.Get("/items/{id}", func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("hello"))
		})
		router.Get("/implicit", func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("abc")) })
		router.Get("/empty", func(http.ResponseWriter, *http.Request) {})
		router.Get("/redirect", func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "/items/1", http.StatusSeeOther)
		})
		router.Get("/fail", func(writer http.ResponseWriter, request *http.Request) {
			responder.Internal(writer, request, "load thing", errors.New("database unavailable"))
		})
		router.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
		router.Get("/runs/{id}/status", func(http.ResponseWriter, *http.Request) {})
	}, func(context.Context) error { return nil }, logger)

	serve := func(target string) []map[string]any {
		t.Helper()
		logs.Reset()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: "session", Value: token})
		router.ServeHTTP(httptest.NewRecorder(), request)
		if strings.Contains(logs.String(), "VERY_SECRET_VALUE") {
			t.Fatalf("query leaked into logs: %s", logs.String())
		}
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("bad log line %q", line)
			}
			records = append(records, record)
		}
		return records
	}
	completed := func(records []map[string]any) []map[string]any {
		var found []map[string]any
		for _, record := range records {
			if record["event"] == "http.request.completed" {
				found = append(found, record)
			}
		}
		return found
	}
	expect := func(target, route string, status, bytes int) []map[string]any {
		t.Helper()
		records := serve(target)
		access := completed(records)
		if len(access) != 1 {
			t.Fatalf("%s access records=%v", target, records)
		}
		record := access[0]
		if record["route"] != route || record["status"] != float64(status) || record["method"] != "GET" || record["request_id"] == "" ||
			record["duration_ms"].(float64) < 0 || (bytes >= 0 && record["response_bytes"] != float64(bytes)) {
			t.Fatalf("%s record=%v", target, record)
		}
		return records
	}

	expect("/items/42?token=VERY_SECRET_VALUE", "/items/{id}", http.StatusOK, 5)
	expect("/implicit", "/implicit", http.StatusOK, 3)
	expect("/empty", "/empty", http.StatusOK, 0)
	expect("/redirect", "/redirect", http.StatusSeeOther, -1)
	expect("/missing/1234567890", "unmatched", http.StatusNotFound, -1)

	for target, event := range map[string]string{"/fail?token=VERY_SECRET_VALUE": "http.request.error", "/panic": "http.request.panic"} {
		records := expect(target, strings.Split(target, "?")[0], http.StatusInternalServerError, -1)
		errorsLogged := 0
		for _, record := range records {
			if record["event"] == event && record["level"] == "ERROR" && record["route"] == strings.Split(target, "?")[0] && record["request_id"] != "" {
				errorsLogged++
			}
		}
		if errorsLogged != 1 {
			t.Fatalf("%s detailed error records=%v", target, records)
		}
	}

	for _, target := range []string{"/health", "/ready", "/static/js/app.js", "/runs/7/status"} {
		if records := completed(serve(target)); len(records) != 0 {
			t.Fatalf("%s should be quiet: %v", target, records)
		}
	}
}
