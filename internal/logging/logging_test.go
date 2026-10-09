package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-sql-driver/mysql"
)

func TestProductionLoggerEmitsJSONWithBaseFields(t *testing.T) {
	var output bytes.Buffer
	New(&output, "dwh", "production").With("component", "app").Info("application started", "event", "app.started")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("not JSON: %q", output.String())
	}
	for key, want := range map[string]string{"level": "INFO", "msg": "application started", "service": "dwh",
		"environment": "production", "component": "app", "event": "app.started"} {
		if record[key] != want {
			t.Fatalf("%s=%v want %q in %v", key, record[key], want, record)
		}
	}
	if _, ok := record["time"].(string); !ok {
		t.Fatalf("time missing: %v", record)
	}
	output.Reset()
	New(&output, "dwh", "production").Debug("hidden")
	if output.Len() != 0 {
		t.Fatalf("production logged DEBUG: %q", output.String())
	}
}

func TestDevelopmentLoggerIsTextAtDebug(t *testing.T) {
	var output bytes.Buffer
	New(&output, "dwh", "development").Debug("visible")
	if line := output.String(); !strings.Contains(line, "level=DEBUG") || !strings.Contains(line, "service=dwh") || strings.HasPrefix(line, "{") {
		t.Fatalf("development record=%q", line)
	}
}

func TestRouteUsesPatternNeverRawPathOrQuery(t *testing.T) {
	var route string
	router := chi.NewRouter()
	router.Get("/runs/{id}", func(_ http.ResponseWriter, request *http.Request) { route = Route(request) })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/runs/42?token=SECRET", nil))
	if route != "/runs/{id}" {
		t.Fatalf("route=%q", route)
	}
	if got := Route(httptest.NewRequest(http.MethodGet, "/1234567890?token=SECRET", nil)); got != "unmatched" {
		t.Fatalf("unrouted=%q", got)
	}
}

func TestErrorAttrsDropMySQLMessages(t *testing.T) {
	err := fmt.Errorf("insert: %w", &mysql.MySQLError{Number: 1062, SQLState: [5]byte{'2', '3', '0', '0', '0'}, Message: "Duplicate entry 'ACCOUNT-0042' for key"})
	rendered := fmt.Sprint(ErrorAttrs(err)...)
	if strings.Contains(rendered, "ACCOUNT-0042") || !strings.Contains(rendered, "1062") || !strings.Contains(rendered, "23000") {
		t.Fatalf("attrs=%s", rendered)
	}
}
