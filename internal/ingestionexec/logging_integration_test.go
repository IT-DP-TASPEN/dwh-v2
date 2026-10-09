//go:build integration

package ingestionexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

// Detail runs touch every account/CIF; operational logs must stay per-pool,
// never per-item, and must not carry the identifiers.
func TestDetailRunDoesNotLogPerItemSuccess(t *testing.T) {
	const accounts = 200
	db := integrationdb.Open(t)
	clearSavingDetail(t, db)
	t.Cleanup(func() { clearSavingDetail(t, db) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/tabungan/inquiry/rekening/cari":
			items := make([]string, accounts)
			for index := range items {
				items[index] = fmt.Sprintf(`{"id":"ACCT-SECRET-%03d"}`, index)
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[`+strings.Join(items, ",")+`]}}`)
		case "/tabungan/inquiry/rekening/tabungan":
			id := request.URL.Query().Get("id")
			_, _ = fmt.Fprintf(response, `{"status":"ok","data":{"result":{"norekening":%q,"nocif":"CIF-SECRET-%s","saldoawal":"1","saldoakhir":"2"}}}`, id, id)
		case "/tabungan/inquiry/rekening/historyMutasi":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[]}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	executor, runs, _, _ := integrationExecutor(t, db, server.URL, "detail-log-volume")
	var logs bytes.Buffer
	executor.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	run := claimedExecution(t, db, runs, "saving_detail")

	if result := executor.Execute(context.Background(), run, run.OwnerID); result.Status != ingestionrun.StatusSucceeded {
		t.Fatalf("result=%+v", result)
	}
	output := logs.String()
	if strings.Contains(output, "ACCT-SECRET") || strings.Contains(output, "CIF-SECRET") {
		t.Fatalf("identifiers leaked: %s", output)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > 5 {
		t.Fatalf("%d records for %d accounts: %s", len(lines), accounts, output)
	}
	pools := 0
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("bad log line %q", line)
		}
		if record["event"] == "ingestion.pool.started" {
			pools++
			if record["pool_kind"] != "detail_item" || record["work_items"] != float64(accounts) || record["concurrency"] == nil {
				t.Fatalf("pool=%v", record)
			}
		}
	}
	if pools != 1 {
		t.Fatalf("pool records=%d: %s", pools, output)
	}
}
