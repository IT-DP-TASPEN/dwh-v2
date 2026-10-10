package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func renderJSON(t *testing.T, args ...any) map[string]any {
	t.Helper()
	var output bytes.Buffer
	New(&output, "dwh", "production").Error("failed", args...)
	for _, marker := range []string{"ACCOUNT-SECRET-0042", "CIF-SECRET-991", "SQL-SECRET-VALUE", "CSV-CELL-SECRET", "PASSWORD-SECRET", "PANIC-SECRET-0042"} {
		if strings.Contains(output.String(), marker) {
			t.Fatalf("stdout leaked %s: %s", marker, output.String())
		}
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("not JSON: %q", output.String())
	}
	return record
}

func TestErrNeverEmitsArbitraryMessages(t *testing.T) {
	secret := errors.New("ACCOUNT-SECRET-0042 CIF-SECRET-991 SQL-SECRET-VALUE CSV-CELL-SECRET PASSWORD-SECRET")
	record := renderJSON(t, "run_id", 7, Err(fmt.Errorf("operation failed: %w", secret)))
	if record["error_type"] != "*errors.errorString" || record["run_id"] != float64(7) {
		t.Fatalf("record=%v", record)
	}
	record = renderJSON(t, Err(fmt.Errorf("a: %w", errors.Join(secret, secret))))
	if record["error_type"] != "*errors.joinError" {
		t.Fatalf("record=%v", record)
	}
}

func TestErrKeepsOnlyMySQLCodes(t *testing.T) {
	err := fmt.Errorf("insert: %w", &mysql.MySQLError{Number: 1062, SQLState: [5]byte{'2', '3', '0', '0', '0'}, Message: "Duplicate entry 'ACCOUNT-SECRET-0042' for key"})
	record := renderJSON(t, Err(err))
	if record["error_type"] != "*mysql.MySQLError" || record["mysql_error"] != float64(1062) || record["sqlstate"] != "23000" {
		t.Fatalf("record=%v", record)
	}
}

func TestErrClassifiesContextErrors(t *testing.T) {
	if record := renderJSON(t, Err(fmt.Errorf("query CIF-SECRET-991: %w", context.Canceled))); record["error_kind"] != "cancelled" {
		t.Fatalf("record=%v", record)
	}
	if record := renderJSON(t, Err(fmt.Errorf("query: %w", context.DeadlineExceeded))); record["error_kind"] != "deadline_exceeded" {
		t.Fatalf("record=%v", record)
	}
}

func TestErrNilAndPanicAddNoValues(t *testing.T) {
	record := renderJSON(t, Err(nil), Panic("PANIC-SECRET-0042"))
	if _, ok := record["error_type"]; ok || record["panic_type"] != "string" {
		t.Fatalf("record=%v", record)
	}
}
