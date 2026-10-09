//go:build integration

package reportexport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestExportLifecycleLogsWithoutSQLParametersOrCells(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Exporter", "export-logging")
	if _, err := database.Exec(`INSERT INTO role_permissions (role_id,permission_id) SELECT ?,id FROM permissions WHERE `+"`key`"+`=?`, role.ID, reports.PermissionExport); err != nil {
		t.Fatal(err)
	}
	user := integrationdb.User(t, database, "export-logging", role.ID, true)
	requester := integrationdb.Requester(user, role)
	connection := integrationdb.Config(t)
	cipher := reporting.NewCipher([32]byte{})
	reportsRepository, err := reporting.NewRepository(database, cipher)
	if err != nil {
		t.Fatal(err)
	}
	datasource, err := reportsRepository.CreateDatasource(context.Background(), requester, reporting.DatasourceInput{
		Name: "Logging", Host: connection.Host, Port: uint16(connection.Port), DatabaseName: connection.Name,
		Username: connection.User, Password: connection.Password, TLSPolicy: reporting.TLSDisabled,
	}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_datasources SET status='active' WHERE id=?`, datasource.ID); err != nil {
		t.Fatal(err)
	}
	parameters := []reporting.Parameter{{Key: "branch", Label: "Branch", Type: reporting.ParameterSingleOption,
		Options: []reporting.ParameterOption{{Value: "PARAM-SECRET-77", Label: "Branch"}}}}
	exports, err := reportexport.NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	submit := func(name, sqlText string) reportexport.Job {
		t.Helper()
		template, err := reportsRepository.CreateTemplate(context.Background(), requester, reporting.TemplateInput{Name: name, DatasourceID: datasource.ID, SQLText: sqlText, Parameters: parameters}, integrationdb.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE report_templates SET status='active' WHERE id=?`, template.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, template.ID, user.ID, user.ID, integrationdb.Now()); err != nil {
			t.Fatal(err)
		}
		normalized, err := reporting.NormalizeParameters(template.Parameters, map[string]reporting.InputValue{"branch": {Present: true, Values: []string{"PARAM-SECRET-77"}}})
		if err != nil {
			t.Fatal(err)
		}
		job, err := exports.Submit(context.Background(), requester, template, normalized, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	succeeded := submit("Logged", "SELECT :branch AS branch, 'CELL-SECRET' AS customer UNION ALL SELECT :branch, 'CELL-SECRET-2'")
	failed := submit("Broken", "SELECT * FROM missing_table_secret WHERE code=:branch")

	pools, err := reporting.NewPoolManager(cipher, reporting.PoolConfig{ConnectTimeout: 5 * time.Second, MySQLMaxPacketBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pools.Close() })
	exportDir := t.TempDir()
	storage, err := reportexport.NewStorage(exportDir)
	if err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	worker, err := reportexport.NewWorker(exports, reportsRepository, pools, storage, reportexport.WorkerConfig{Concurrency: 1, ExportTimeout: 30 * time.Second,
		HeartbeatInterval: time.Second, StaleAfter: 10 * time.Second, Retention: time.Hour, CleanupInterval: time.Hour, OrphanGrace: time.Minute},
		slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	t.Cleanup(func() { stop(); <-done })

	completions := map[uint64]map[string]any{}
	started := map[uint64]int{}
	for deadline := time.Now().Add(20 * time.Second); len(completions) < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("exports did not complete: %s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
		completions, started = map[uint64]map[string]any{}, map[uint64]int{}
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var record map[string]any
			if json.Unmarshal([]byte(line), &record) != nil {
				continue
			}
			id, _ := record["export_job_id"].(float64)
			switch record["event"] {
			case "report_export.started":
				started[uint64(id)]++
			case "report_export.completed":
				if completions[uint64(id)] != nil {
					t.Fatalf("duplicate completion: %s", logs.String())
				}
				completions[uint64(id)] = record
			}
		}
	}
	success, failure := completions[succeeded.ID], completions[failed.ID]
	if started[succeeded.ID] != 1 || success["status"] != "succeeded" || success["level"] != "INFO" || success["rows"] != float64(2) ||
		success["parts"] != float64(1) || success["artifact_size_bytes"].(float64) <= 0 || success["duration_ms"] == nil ||
		success["report_id"] == nil || success["datasource_id"] != float64(datasource.ID) || success["attempt"] != float64(1) {
		t.Fatalf("success=%v", success)
	}
	if started[failed.ID] != 1 || failure["status"] != "failed" || failure["level"] != "ERROR" || failure["stage"] != "query" ||
		failure["error_class"] != "query_failed" || failure["mysql_error"] != float64(1146) {
		t.Fatalf("failure=%v", failure)
	}
	for _, secret := range []string{"PARAM-SECRET", "CELL-SECRET", "missing_table_secret", "SELECT", exportDir} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs leaked %q: %s", secret, logs.String())
		}
	}
}
