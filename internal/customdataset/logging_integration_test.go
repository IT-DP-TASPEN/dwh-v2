//go:build integration

package customdataset

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestImportLifecycleLogsWithoutCellContent(t *testing.T) {
	db := integrationdb.Open(t)
	cleanupDynamicTables(t, db)
	integrationdb.Reset(t, db, nil)
	t.Cleanup(func() { cleanupDynamicTables(t, db) })
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	user := integrationdb.User(t, db, "custom-dataset-logging", role.ID, true)
	requester := integrationdb.Requester(user, role)
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	ddl, _ := NewDDL(db)
	var logs bytes.Buffer
	worker, err := NewWorker(repository, ddl, storage, WorkerConfig{Concurrency: 1, HeartbeatInterval: 50 * time.Millisecond, StaleAfter: time.Second, CleanupInterval: time.Hour}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	columns := []Column{{Ordinal: 1, DisplayName: "Name", QueryName: "name", PhysicalName: "c001", LogicalType: TypeText}, {Ordinal: 2, DisplayName: "Amount", QueryName: "amount", PhysicalName: "c002", LogicalType: TypeInteger}}
	run := func(name, csv string) (Import, map[string]any) {
		t.Helper()
		logs.Reset()
		upload := integrationUpload(t, repository, storage, requester, csv)
		_, submitted, err := repository.Submit(context.Background(), requester, Submission{Name: name, UploadID: upload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now())
		if err != nil {
			t.Fatal(err)
		}
		executeNext(t, repository, worker)
		if strings.Contains(logs.String(), "CELL-SECRET") {
			t.Fatalf("cell content leaked: %s", logs.String())
		}
		var started int
		var completed map[string]any
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("bad log line %q", line)
			}
			if record["event"] == nil {
				continue
			}
			if record["import_id"] != float64(submitted.ID) || record["dataset_id"] != float64(submitted.DatasetID) || record["mode"] != "replace" || record["attempt"] != float64(1) {
				t.Fatalf("correlation=%v", record)
			}
			switch record["event"] {
			case "custom_dataset.import.started":
				started++
			case "custom_dataset.import.completed":
				if completed != nil {
					t.Fatalf("duplicate completion: %s", logs.String())
				}
				completed = record
			}
		}
		if started != 1 || completed == nil || completed["duration_ms"] == nil {
			t.Fatalf("lifecycle: %s", logs.String())
		}
		return submitted, completed
	}

	if _, completed := run("Logged", "Name,Amount\nCELL-SECRET-A,1\nCELL-SECRET-B,2\n"); completed["status"] != "succeeded" || completed["level"] != "INFO" ||
		completed["rows"] != float64(2) || completed["source_records"] == nil {
		t.Fatalf("success=%v", completed)
	}
	if _, completed := run("Rejected", "Name,Amount\nCELL-SECRET-BAD,CELL-SECRET-NOT-INT\n"); completed["status"] != "failed" || completed["level"] != "ERROR" ||
		completed["error_class"] != "validation" {
		t.Fatalf("failure=%v", completed)
	}
}
