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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

const savingBlockAccounts = 12

func clearSavingBlock(t *testing.T, db *sqlx.DB) {
	t.Helper()
	for _, table := range []string{"stg_fincloud_saving_block_transactions", "fincloud_saving_block_transactions"} {
		if _, err := db.Exec("DELETE FROM `" + table + "`"); err != nil {
			t.Fatal(err)
		}
	}
}

// savingBlockServer lists BLOCK-SECRET-00..11; account i carries i%3 items.
// broken names one account whose child belongs to another account.
func savingBlockServer(t *testing.T, listing string, broken string, inFlight, peak *atomic.Int32, listingQueries *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/cs/inquiry/block_savings/cari":
			mu.Lock()
			*listingQueries = append(*listingQueries, request.URL.RawQuery)
			mu.Unlock()
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":`+listing+`,"rowCount":"999"}}`)
		case "/cs/inquiry/block_savings/tabungan":
			current := inFlight.Add(1)
			defer inFlight.Add(-1)
			for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			id := request.URL.Query().Get("id")
			var index int
			_, _ = fmt.Sscanf(id, "BLOCK-SECRET-%02d", &index)
			items := make([]string, index%3)
			for item := range items {
				owner := id
				if id == broken {
					owner = "BLOCK-SECRET-OTHER"
				}
				items[item] = fmt.Sprintf(`{"id_transaction":"T-%d","tglblokir":"2025-10-15","norekening":%q,"namanasabah":"CUSTOMER-SECRET","nilaiblokir":"1,000,000.00","not_batch":false,"is_open":false}`, item, owner)
			}
			_, _ = fmt.Fprintf(response, `{"status":"ok","data":{"result":{"total_bo_open":99,"norekening":%q,"namarek":"CUSTOMER-SECRET","datablockopen":[%s]}}}`, id, strings.Join(items, ","))
		default:
			http.NotFound(response, request)
		}
	}))
}

func savingBlockListing() string {
	rows := make([]string, savingBlockAccounts)
	for index := range rows {
		rows[index] = fmt.Sprintf(`{"id":" BLOCK-SECRET-%02d ","nocif":"CIF-SECRET","namanasabah":"CUSTOMER-SECRET"}`, index)
	}
	return "[" + strings.Join(rows, ",") + `,{"id":"BLOCK-SECRET-00"},{"id":""}]`
}

func savingBlockExecutor(t *testing.T, db *sqlx.DB, server *httptest.Server) (*Executor, *ingestionrun.Repository, *bytes.Buffer) {
	t.Helper()
	executor, runs, _, _ := integrationExecutor(t, db, server.URL, "saving-block-"+t.Name())
	executor.runtimeSettings = func(context.Context) (ingestionrun.RuntimeSettings, error) {
		return ingestionrun.RuntimeSettings{MaxRunningJobs: 1, FixedMemberConcurrency: 1, DetailConcurrency: 3}, nil
	}
	logs := &bytes.Buffer{}
	executor.logger = slog.New(slog.NewJSONHandler(logs, nil))
	return executor, runs, logs
}

func seedSavingBlock(t *testing.T, db *sqlx.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO fincloud_saving_block_transactions (account_no,item_index,raw_item_payload,raw_item_checksum,last_fetched_at)
		VALUES ('OLD',0,JSON_OBJECT(),REPEAT('0',64),UTC_TIMESTAMP(6)),('OLD',1,JSON_OBJECT(),REPEAT('0',64),UTC_TIMESTAMP(6))`); err != nil {
		t.Fatal(err)
	}
}

func savingBlockFinalCount(t *testing.T, db *sqlx.DB) (rows, old int) {
	t.Helper()
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(account_no='OLD'),0) FROM fincloud_saving_block_transactions`).Scan(&rows, &old); err != nil {
		t.Fatal(err)
	}
	return rows, old
}

func TestSavingBlockDetailPublishesCurrentStateThroughDetailPool(t *testing.T) {
	db := integrationdb.Open(t)
	clearSavingBlock(t, db)
	t.Cleanup(func() { clearSavingBlock(t, db) })
	seedSavingBlock(t, db)
	var inFlight, peak atomic.Int32
	var queries []string
	server := savingBlockServer(t, savingBlockListing(), "", &inFlight, &peak, &queries)
	defer server.Close()
	executor, runs, logs := savingBlockExecutor(t, db, server)
	run := claimedExecution(t, db, runs, "saving_block_detail")

	result := executor.Execute(context.Background(), run, run.OwnerID)
	if result.Status != ingestionrun.StatusSucceeded || !result.BusinessComplete {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(queries, []string{"cabang=ALL&datamutasi=false&pagenumber=0&pagesize=999999999999"}) {
		t.Fatalf("listing queries=%v", queries)
	}
	if peak.Load() < 2 || peak.Load() > 3 {
		t.Fatalf("detail concurrency peak=%d want 2..3", peak.Load())
	}
	// 12 accounts, items per account i%3: 4 accounts x0, 4 x1, 4 x2.
	stored, err := runs.Get(context.Background(), run.ID)
	if err != nil || stored.Progress.Total != savingBlockAccounts || stored.Progress.Started != savingBlockAccounts ||
		stored.Progress.Succeeded != savingBlockAccounts || stored.Progress.Failed != 0 || stored.Progress.Rows != 12 {
		t.Fatalf("progress=%+v error=%v", stored.Progress, err)
	}
	if rows, old := savingBlockFinalCount(t, db); rows != 12 || old != 0 {
		t.Fatalf("final rows=%d old=%d", rows, old)
	}
	var staged int
	if err := db.Get(&staged, `SELECT COUNT(*) FROM stg_fincloud_saving_block_transactions WHERE ingestion_run_id=?`, run.ID); err != nil || staged != 0 {
		t.Fatalf("staging left=%d error=%v", staged, err)
	}
	output := logs.String()
	if strings.Contains(output, "BLOCK-SECRET") || strings.Contains(output, "CUSTOMER-SECRET") || strings.Contains(output, "CIF-SECRET") {
		t.Fatalf("identifiers leaked: %s", output)
	}
	pools := 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("bad log line %q", line)
		}
		if record["event"] == "ingestion.pool.started" {
			pools++
			if record["pool_kind"] != "detail_item" || record["work_items"] != float64(savingBlockAccounts) || record["concurrency"] != float64(3) {
				t.Fatalf("pool=%v", record)
			}
		}
	}
	if pools != 1 || strings.Count(output, "\n") > 5 {
		t.Fatalf("pool records=%d logs=%s", pools, output)
	}
}

func TestSavingBlockDetailAccountFailurePreventsPublication(t *testing.T) {
	db := integrationdb.Open(t)
	clearSavingBlock(t, db)
	t.Cleanup(func() { clearSavingBlock(t, db) })
	seedSavingBlock(t, db)
	var inFlight, peak atomic.Int32
	var queries []string
	server := savingBlockServer(t, savingBlockListing(), "BLOCK-SECRET-05", &inFlight, &peak, &queries)
	defer server.Close()
	executor, runs, logs := savingBlockExecutor(t, db, server)
	run := claimedExecution(t, db, runs, "saving_block_detail")

	result := executor.Execute(context.Background(), run, run.OwnerID)
	if result.Status != ingestionrun.StatusFailed || result.BusinessComplete || result.Error.Class != "item_data" {
		t.Fatalf("result=%+v", result)
	}
	if rows, old := savingBlockFinalCount(t, db); rows != 2 || old != 2 {
		t.Fatalf("partial publication rows=%d old=%d", rows, old)
	}
	stored, err := runs.Get(context.Background(), run.ID)
	if err != nil || stored.Progress.Failed != 1 || stored.Progress.Succeeded != savingBlockAccounts-1 {
		t.Fatalf("progress=%+v error=%v", stored.Progress, err)
	}
	if strings.Contains(logs.String(), "BLOCK-SECRET") || strings.Contains(logs.String(), "CUSTOMER-SECRET") {
		t.Fatalf("identifiers leaked: %s", logs.String())
	}
}

func TestSavingBlockDetailEmptyEnumerationClearsCurrentState(t *testing.T) {
	db := integrationdb.Open(t)
	clearSavingBlock(t, db)
	t.Cleanup(func() { clearSavingBlock(t, db) })
	seedSavingBlock(t, db)
	var inFlight, peak atomic.Int32
	var queries []string
	server := savingBlockServer(t, `[]`, "", &inFlight, &peak, &queries)
	defer server.Close()
	executor, runs, _ := savingBlockExecutor(t, db, server)
	run := claimedExecution(t, db, runs, "saving_block_detail")

	if result := executor.Execute(context.Background(), run, run.OwnerID); result.Status != ingestionrun.StatusSucceeded {
		t.Fatalf("result=%+v", result)
	}
	if rows, _ := savingBlockFinalCount(t, db); rows != 0 {
		t.Fatalf("final rows=%d after empty enumeration", rows)
	}
}
