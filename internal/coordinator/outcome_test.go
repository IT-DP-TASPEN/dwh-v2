package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/ibldzn/go-admin/internal/ingestionexec"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

func TestRunOutcomeTrustsOnlyPersistedTerminalState(t *testing.T) {
	secret := fmt.Errorf("update run: %w", errors.New("ACCOUNT-SECRET-0042"))
	mysqlSecret := fmt.Errorf("finish: %w", &mysql.MySQLError{Number: 1205, Message: "Lock wait on 'ACCOUNT-SECRET-0042'"})
	succeeded := ingestionexec.Result{Status: ingestionrun.StatusSucceeded}
	failed := ingestionexec.Result{Status: ingestionrun.StatusFailed, Error: ingestionrun.SafeError{Class: "source", Step: "fetch_detail"}}
	row := func(status ingestionrun.Status) ingestionrun.Run { return ingestionrun.Run{Status: status} }
	for _, test := range []struct {
		name                string
		result              ingestionexec.Result
		persisted           ingestionrun.Run
		finishErr, readErr  error
		event, status, want string
	}{
		{"A success", succeeded, row(ingestionrun.StatusSucceeded), nil, nil, "ingestion.run.completed", "succeeded", "INFO"},
		{"B failure", failed, row(ingestionrun.StatusFailed), nil, nil, "ingestion.run.completed", "failed", "ERROR"},
		{"C cancellation fallback", succeeded, row(ingestionrun.StatusCancelled), nil, nil, "ingestion.run.completed", "cancelled", "INFO"},
		{"D finish failed, still running", succeeded, row(ingestionrun.StatusRunning), mysqlSecret, nil, "ingestion.run.finalization_failed", "", "ERROR"},
		{"E finish error, readback terminal", succeeded, row(ingestionrun.StatusAbandoned), ingestionrun.ErrTransition, nil, "ingestion.run.completed", "abandoned", "WARN"},
		{"E finish infra error, readback terminal", succeeded, row(ingestionrun.StatusFailed), secret, nil, "ingestion.run.completed", "failed", "ERROR"},
		{"F readback failed", succeeded, ingestionrun.Run{}, nil, secret, "ingestion.run.finalization_failed", "", "ERROR"},
		{"G both failed with secrets", succeeded, ingestionrun.Run{}, mysqlSecret, secret, "ingestion.run.finalization_failed", "", "ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			logRunOutcome(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), "detail", test.result, time.Second, test.persisted, test.finishErr, test.readErr)
			if strings.Contains(logs.String(), "ACCOUNT-SECRET-0042") {
				t.Fatalf("logs leaked secret: %s", logs.String())
			}
			var outcome map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("bad log line %q", line)
				}
				if event, _ := record["event"].(string); event != "" {
					if outcome != nil {
						t.Fatalf("multiple lifecycle events: %s", logs.String())
					}
					outcome = record
				}
			}
			if outcome["event"] != test.event || outcome["level"] != test.want {
				t.Fatalf("outcome=%v", outcome)
			}
			if test.event == "ingestion.run.completed" && outcome["status"] != test.status {
				t.Fatalf("status=%v want %s", outcome["status"], test.status)
			}
			if test.event == "ingestion.run.finalization_failed" {
				if _, ok := outcome["status"]; ok || outcome["executor_status"] != "succeeded" {
					t.Fatalf("finalization_failed=%v", outcome)
				}
				if _, ok := outcome["persisted_status"]; ok != (test.readErr == nil) {
					t.Fatalf("persisted_status presence wrong: %v", outcome)
				}
			}
		})
	}
}
