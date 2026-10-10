//go:build integration

package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionexec"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

type scriptedExecutor map[uint64]ingestionexec.Result

func (executor scriptedExecutor) Execute(_ context.Context, run ingestionrun.Run, _ string) ingestionexec.Result {
	return executor[run.ID]
}

func TestRunLifecycleLogsOneStartAndOneCanonicalCompletion(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	ids := queueDistinctIntegrationJobs(t, db, runs, 4)
	if _, err := db.Exec(`UPDATE ingestion_runtime_settings SET max_running_jobs=? WHERE id=1`, ingestionrun.MaxRuntimeLimit); err != nil {
		t.Fatal(err)
	}
	secretCause := errors.New("upstream rejected ACCOUNT-SECRET-0042")
	want := map[uint64]ingestionrun.Status{ids[0]: ingestionrun.StatusSucceeded, ids[1]: ingestionrun.StatusFailed, ids[2]: ingestionrun.StatusCancelled,
		ids[3]: ingestionrun.StatusCancelled}
	fake := scriptedExecutor{
		ids[0]: {Status: ingestionrun.StatusSucceeded, BusinessComplete: true},
		ids[1]: {Status: ingestionrun.StatusFailed, Cause: secretCause, Error: ingestionrun.SafeError{Class: "source", Message: "failed", Step: "fetch_detail"}},
		ids[2]: {Status: ingestionrun.StatusCancelled, Cause: ingestionrun.ErrCancellationRequested, Error: ingestionrun.SafeError{Class: "cancelled", Message: "cancelled", Step: "fetch_detail"}},
		// Executor succeeds after cancellation was requested: Finish falls back to cancelled.
		ids[3]: {Status: ingestionrun.StatusSucceeded, BusinessComplete: true},
	}
	var logs bytes.Buffer
	coordinator := &Coordinator{runs: runs, executor: fake, catalog: catalog, logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		local: map[uint64]context.CancelCauseFunc{}, parents: map[uint64]string{}}
	var owners []string
	for range ids {
		owner, _ := ingestionrun.NewOwnerID()
		run, err := runs.Claim(context.Background(), owner)
		if err != nil || run == nil {
			t.Fatalf("claim=%+v err=%v", run, err)
		}
		owners = append(owners, owner)
		if run.ID == ids[3] {
			if _, err := db.Exec(`UPDATE ingestion_runs SET cancel_requested_at=UTC_TIMESTAMP(6) WHERE id=?`, run.ID); err != nil {
				t.Fatal(err)
			}
		}
		coordinator.executeClaimedRun(context.Background(), context.Background(), *run, owner)
	}

	started, completed := map[uint64]int{}, map[uint64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("bad log line %q", line)
		}
		id := uint64(record["run_id"].(float64))
		switch record["event"] {
		case "ingestion.run.started":
			started[id]++
			if record["job_key"] == "" || record["category"] == "" || record["trigger"] != "direct" {
				t.Fatalf("start=%v", record)
			}
		case "ingestion.run.finalization_failed":
			t.Fatalf("unexpected finalization failure: %v", record)
		case "ingestion.run.completed":
			if completed[id] != nil {
				t.Fatalf("duplicate completion for %d", id)
			}
			completed[id] = record
		}
	}
	for id, status := range want {
		record := completed[id]
		if started[id] != 1 || record == nil || record["status"] != string(status) || record["duration_ms"] == nil || record["job_key"] == "" {
			t.Fatalf("run %d started=%d completion=%v", id, started[id], record)
		}
	}
	if failed := completed[ids[1]]; failed["level"] != "ERROR" || failed["error_class"] != "source" || failed["error_step"] != "fetch_detail" {
		t.Fatalf("failed completion=%v", failed)
	}
	if completed[ids[0]]["level"] != "INFO" || completed[ids[2]]["level"] != "INFO" {
		t.Fatalf("non-failure levels: %v %v", completed[ids[0]], completed[ids[2]])
	}
	for _, leaked := range append(owners, "ACCOUNT-SECRET-0042") {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("logs leaked %q: %s", leaked, logs.String())
		}
	}
}
