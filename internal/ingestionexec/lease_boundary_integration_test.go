//go:build integration

package ingestionexec

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

type countedLease struct {
	fincloud.Lease
	releases *atomic.Int32
}

func (lease countedLease) Release() {
	lease.releases.Add(1)
	lease.Lease.Release()
}

func TestSavingDetailReleasesSessionBeforeBlockedPublication(t *testing.T) {
	db := integrationdb.Open(t)
	clearSavingDetail(t, db)
	if _, err := db.Exec(`INSERT INTO fincloud_saving_details
		(account_no,cif_no,beginning_balance,balance,raw_payload,raw_checksum,last_fetched_at)
		VALUES ('S-1','C-1',1,2,JSON_OBJECT(),REPEAT('0',64),UTC_TIMESTAMP(6))`); err != nil {
		t.Fatal(err)
	}
	server := savingDetailServer(t, func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"mutasi":[]}}}`)
	})
	defer server.Close()
	executor, runs, sessions, releases := integrationExecutor(t, db, server.URL, "lease-saving-user")
	run := claimedExecution(t, db, runs, "saving_detail")

	lock, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	var account string
	if err := lock.Get(&account, `SELECT account_no FROM fincloud_saving_details WHERE account_no='S-1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	resultFound := make(chan Result, 1)
	go func() { resultFound <- executor.Execute(context.Background(), run, run.OwnerID) }()
	waitForCount(t, db, `SELECT COUNT(*) FROM stg_fincloud_saving_details WHERE ingestion_run_id=?`, run.ID, 1)

	conflicting := fincloud.AuthContext{ProfileID: 999, Revision: 1, ProfileName: "conflict", Username: "lease-saving-user", Password: "pass", RoleID: "other-role", LocationID: "001"}
	acquired := make(chan fincloud.Lease, 1)
	acquireCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		lease, _ := sessions.Acquire(acquireCtx, conflicting)
		acquired <- lease
	}()
	select {
	case lease := <-acquired:
		if lease == nil {
			t.Fatal("conflicting Fincloud context did not acquire after network pool joined")
		}
		lease.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("saving_detail retained Fincloud lease during blocked publication")
	}
	if releases.Load() != 1 {
		t.Fatalf("physical releases before publication=%d want=1", releases.Load())
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	result := <-resultFound
	if result.Status != ingestionrun.StatusSucceeded || releases.Load() != 1 {
		t.Fatalf("result=%+v physical releases=%d", result, releases.Load())
	}
}

func TestSavingDetailFailurePathsReleaseSessionExactlyOnce(t *testing.T) {
	tests := []struct {
		name      string
		detail    string
		statement func(http.ResponseWriter, *http.Request)
		trigger   string
		cancel    bool
	}{
		{name: "pool fetch", statement: func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusBadGateway) }},
		{name: "map", detail: `{"status":"ok","data":{"result":{"norekening":"S-1","saldoawal":"1","saldoakhir":"2"}}}`, statement: validStatementResponse},
		{name: "stage", trigger: `CREATE TRIGGER fail_statement_stage BEFORE INSERT ON stg_fincloud_saving_account_statements FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced stage failure'`, statement: validStatementResponse},
		{name: "publication", trigger: `CREATE TRIGGER fail_statement_publish BEFORE INSERT ON fincloud_saving_account_statements FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced publish failure'`, statement: validStatementResponse},
		{name: "cancellation", cancel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := integrationdb.Open(t)
			clearSavingDetail(t, db)
			entered := make(chan struct{})
			statement := test.statement
			if test.cancel {
				statement = func(_ http.ResponseWriter, request *http.Request) {
					close(entered)
					<-request.Context().Done()
				}
			}
			server := savingDetailServer(t, statement, test.detail)
			defer server.Close()
			executor, runs, _, releases := integrationExecutor(t, db, server.URL, "lease-failure-"+strings.ReplaceAll(test.name, " ", "-"))
			if test.trigger != "" {
				if _, err := db.Exec(test.trigger); err != nil {
					t.Fatal(err)
				}
				name := strings.Fields(test.trigger)[2]
				t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS `" + name + "`") })
			}
			run := claimedExecution(t, db, runs, "saving_detail")
			ctx, cancel := context.WithCancel(context.Background())
			resultFound := make(chan Result, 1)
			go func() { resultFound <- executor.Execute(ctx, run, run.OwnerID) }()
			if test.cancel {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("statement request did not start")
				}
				cancel()
			} else {
				defer cancel()
			}
			result := <-resultFound
			if result.Status == ingestionrun.StatusSucceeded || releases.Load() != 1 {
				t.Fatalf("result=%+v physical releases=%d", result, releases.Load())
			}
			var current int
			if err := db.Get(&current, `SELECT COUNT(*) FROM fincloud_saving_details`); err != nil || current != 0 {
				t.Fatalf("failed candidate published parent rows=%d error=%v", current, err)
			}
		})
	}
}

func TestNonDetailRetainsSessionThroughBlockedPublication(t *testing.T) {
	db := integrationdb.Open(t)
	if _, err := db.Exec(`DELETE FROM fincloud_reference_items WHERE domain='saving'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fincloud_reference_categories WHERE domain='saving'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fincloud_reference_categories
		(domain,category_key,source_shape,source_item_count,item_count,discarded_blank_count,category_checksum,last_fetched_at)
		VALUES ('saving','probe','empty_array',0,0,0,REPEAT('0',64),UTC_TIMESTAMP(6))`); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/tabungan/inquiry/rekening//listvalues":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"probe":[{"id":"new","descr":"new"}]}}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	executor, runs, sessions, releases := integrationExecutor(t, db, server.URL, "lease-master-user")
	run := claimedExecution(t, db, runs, "saving_reference_master")
	lock, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	var key string
	if err := lock.Get(&key, `SELECT category_key FROM fincloud_reference_categories WHERE domain='saving' AND category_key='probe' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	resultFound := make(chan Result, 1)
	go func() { resultFound <- executor.Execute(context.Background(), run, run.OwnerID) }()
	waitForCount(t, db, `SELECT COUNT(*) FROM stg_fincloud_reference_categories WHERE ingestion_run_id=?`, run.ID, 1)

	conflicting := fincloud.AuthContext{ProfileID: 999, Revision: 1, ProfileName: "conflict", Username: "lease-master-user", Password: "pass", RoleID: "other-role", LocationID: "001"}
	acquired := make(chan fincloud.Lease, 1)
	go func() {
		lease, _ := sessions.Acquire(context.Background(), conflicting)
		acquired <- lease
	}()
	select {
	case lease := <-acquired:
		if lease != nil {
			lease.Release()
		}
		t.Fatal("non-Detail job released Fincloud lease before publication completed")
	case <-time.After(250 * time.Millisecond):
	}
	if releases.Load() != 0 {
		t.Fatalf("non-Detail physical releases while publication blocked=%d", releases.Load())
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	result := <-resultFound
	lease := <-acquired
	if lease == nil {
		t.Fatal("conflicting context did not acquire after non-Detail execution")
	}
	lease.Release()
	if result.Status != ingestionrun.StatusSucceeded || releases.Load() != 1 {
		t.Fatalf("result=%+v physical releases=%d", result, releases.Load())
	}
}

func validStatementResponse(response http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"mutasi":[{"tgltransaksi":"2026-08-31","jam":"01:34:35","saldoawal":"2.00","debit":null,"kredit":"1.00","saldoakhir":"3.00","saldoakhir_equivalent":"3.00","trx_rate":1,"mid_rate_dc":"1.00"}]}}}`)
}

func savingDetailServer(t *testing.T, statement func(http.ResponseWriter, *http.Request), detailOverride ...string) *httptest.Server {
	t.Helper()
	detail := `{"status":"ok","data":{"result":{"norekening":"S-1","nocif":"C-1","saldoawal":"1","saldoakhir":"2"}}}`
	if len(detailOverride) > 0 && detailOverride[0] != "" {
		detail = detailOverride[0]
	}
	return httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/tabungan/inquiry/rekening/cari":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[{"id":"S-1"}]}}`)
		case "/tabungan/inquiry/rekening/tabungan":
			_, _ = io.WriteString(response, detail)
		case "/tabungan/inquiry/rekening/historyMutasi":
			statement(response, request)
		default:
			http.NotFound(response, request)
		}
	}))
}

func integrationExecutor(t *testing.T, db *sqlx.DB, baseURL, username string) (*Executor, *ingestionrun.Repository, *fincloud.SessionCoordinator, *atomic.Int32) {
	t.Helper()
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	sessions, authProfiles := integrationAuth(t, db, baseURL, username, "pass", "001", "role")
	executor, err := New(sessions, authProfiles, ingestionstore.NewFixedRepository(db), ingestionstore.NewDetailRepository(db), ingestionstore.NewMasterRepository(db),
		ingestionstore.NewMaintenanceRepository(db), runs, catalog, 1, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	releases := &atomic.Int32{}
	realAcquire := executor.acquireSession
	executor.acquireSession = func(ctx context.Context, auth fincloud.AuthContext) (fincloud.Lease, error) {
		lease, err := realAcquire(ctx, auth)
		if err != nil {
			return nil, err
		}
		return countedLease{Lease: lease, releases: releases}, nil
	}
	return executor, runs, sessions, releases
}

func claimedExecution(t *testing.T, db *sqlx.DB, runs *ingestionrun.Repository, jobKey string) ingestionrun.Run {
	t.Helper()
	parameters, err := ingestionrun.NewLiveSnapshotExecution(jobKey)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := runs.Submit(context.Background(), jobKey, parameters, ingestionrun.TriggerDirect, t.Name(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"stg_fincloud_saving_details", "stg_fincloud_reference_categories", "stg_fincloud_marketing_master"} {
			_, _ = db.Exec("DELETE FROM `"+table+"` WHERE ingestion_run_id=?", runID)
		}
		_, _ = db.Exec(`DELETE FROM ingestion_run_errors WHERE run_id=?`, runID)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID)
	})
	owner, err := ingestionrun.NewOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Claim(context.Background(), owner)
	if err != nil || run == nil || run.ID != runID {
		t.Fatalf("claimed=%+v error=%v want=%d", run, err, runID)
	}
	return *run
}

func clearSavingDetail(t *testing.T, db *sqlx.DB) {
	t.Helper()
	for _, table := range []string{"stg_fincloud_saving_details", "fincloud_saving_details"} {
		if _, err := db.Exec("DELETE FROM `" + table + "`"); err != nil {
			t.Fatal(err)
		}
	}
}

func waitForCount(t *testing.T, db *sqlx.DB, query string, runID uint64, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.Get(&count, query, runID); err == nil && count == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for staged row count %d", want)
}
