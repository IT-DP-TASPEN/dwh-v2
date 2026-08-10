package render_test

import (
	"bytes"
	"errors"
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
		responder.Internal(response, request, "load secret", errors.New("SQL password=hidden"))
		body := response.Body.String()
		if response.Code != http.StatusInternalServerError || strings.Contains(body, "SQL") || strings.Contains(body, "hidden") {
			t.Fatalf("unsafe 500 response: status=%d body=%q", response.Code, body)
		}
		if !strings.Contains(logs.String(), "load secret") {
			t.Fatal("internal error operation was not logged")
		}
	})
}

func TestRecoveryLogsPanicAndAvoidsDuplicateWrites(t *testing.T) {
	responder, logs := testErrorResponder(t)

	t.Run("before response", func(t *testing.T) {
		handler := middleware.RequestID(responder.Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("private panic detail")
		})))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
		body := response.Body.String()
		if response.Code != http.StatusInternalServerError || strings.Contains(body, "private panic detail") || !strings.Contains(body, "Request ID:") {
			t.Fatalf("unsafe panic response: status=%d body=%q", response.Code, body)
		}
		logged := logs.String()
		if !strings.Contains(logged, "private panic detail") || !strings.Contains(logged, "stack=") || !strings.Contains(logged, "path=/panic") {
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
