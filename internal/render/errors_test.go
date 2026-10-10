package render_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/render"
	webfiles "github.com/ibldzn/go-admin/web"
)

func TestErrorResponderStatusesAndSafety(t *testing.T) {
	responder, logs := testErrorResponder(t)

	t.Run("not found", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/missing", nil)
		response := httptest.NewRecorder()
		responder.NotFound(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "Page not found") || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected 404 response: status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("internal", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/broken", nil)
		response := httptest.NewRecorder()
		responder.Internal(response, request, "load secret", fmt.Errorf("query: %w", errors.New("SQL-SECRET-VALUE ACCOUNT-SECRET-0042 password=PASSWORD-SECRET")))
		body := response.Body.String()
		if response.Code != http.StatusInternalServerError || strings.Contains(body, "SQL") || strings.Contains(body, "hidden") {
			t.Fatalf("unsafe 500 response: status=%d body=%q", response.Code, body)
		}
		logged := logs.String()
		if !strings.Contains(logged, "load secret") || !strings.Contains(logged, "error_type=*errors.errorString") {
			t.Fatalf("internal error context missing: %s", logged)
		}
		for _, marker := range []string{"SQL-SECRET-VALUE", "ACCOUNT-SECRET-0042", "PASSWORD-SECRET"} {
			if strings.Contains(logged, marker) {
				t.Fatalf("log leaked %s: %s", marker, logged)
			}
		}
	})
}

func TestRecoveryLogsPanicAndAvoidsDuplicateWrites(t *testing.T) {
	responder, logs := testErrorResponder(t)

	t.Run("before response", func(t *testing.T) {
		handler := middleware.RequestID(responder.Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("PANIC-SECRET-0042")
		})))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
		body := response.Body.String()
		if response.Code != http.StatusInternalServerError || strings.Contains(body, "PANIC-SECRET-0042") || !strings.Contains(body, "Request ID:") {
			t.Fatalf("unsafe panic response: status=%d body=%q", response.Code, body)
		}
		logged := logs.String()
		if strings.Contains(logged, "PANIC-SECRET-0042") || !strings.Contains(logged, "panic_type=string") || !strings.Contains(logged, "stack=") || !strings.Contains(logged, "event=http.request.panic") || !strings.Contains(logged, "route=unmatched") {
			t.Fatalf("panic context missing from logs: %s", logged)
		}
	})

	t.Run("after response", func(t *testing.T) {
		handler := responder.Recoverer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
			panic("late panic")
		}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/late", nil))
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("recovery wrote after commit: status=%d body=%q", response.Code, response.Body.String())
		}
	})
}

func testErrorResponder(t *testing.T) (*render.ErrorResponder, *bytes.Buffer) {
	t.Helper()
	renderer, err := render.New(webfiles.Files, false)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	return render.NewErrorResponder(renderer, "Go Admin", logger), &logs
}
