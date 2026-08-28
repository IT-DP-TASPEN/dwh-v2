//go:build integration

package ingestionexec

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/jmoiron/sqlx"
)

func TestJournalTransactionPartitionsPublishAtomically(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	definition := ingestion.FixedDefinitions()[1]
	from, _ := ingestion.ParseCalendarDate("2099-01-01")
	to, _ := ingestion.ParseCalendarDate("2099-02-01")

	var mutex sync.Mutex
	mode := "success"
	enumerations := 0
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/bukuBesar/laporan/jurnal//listvalues":
			mutex.Lock()
			enumerations++
			currentMode := mode
			mutex.Unlock()
			if currentMode == "success" {
				_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"jenistransaksi":[{"id":" B ","descr":"Beta"},{"id":"A","descr":"Alpha"},{"id":"A","descr":"Duplicate"},{"id":"%","descr":"Wildcard"}]}}}`)
				return
			}
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"jenistransaksi":[{"id":"A"},{"id":"B"},{"id":"C"},{"id":"D"}]}}}`)
		case "/system/laporanUmum/data/lap":
			var parameters []string
			if err := json.Unmarshal([]byte(request.URL.Query().Get("p")), &parameters); err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			mutex.Lock()
			currentMode := mode
			requests = append(requests, strings.Join([]string{parameters[2], parameters[3], parameters[1]}, "/"))
			mutex.Unlock()
			if currentMode == "late failure" && parameters[2] == "2099-01-31" && parameters[1] == "C" {
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(response, journalTestCSV(definition, parameters[2]+"-"+parameters[1], false, false))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	executor, err := New(client, ingestionstore.NewFixedRepository(db), ingestionstore.NewDetailRepository(db), ingestionstore.NewMaintenanceRepository(db),
		runs, catalog, 4, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	var runIDs []uint64
	t.Cleanup(func() { cleanupJournalIntegration(t, db, runIDs) })
	submit := func(ownerByte byte) (ingestionrun.Run, string) {
		parameters, err := ingestionrun.NewRangeExecution(definition.Key, from, to)
		if err != nil {
			t.Fatal(err)
		}
		id, err := runs.Submit(context.Background(), definition.Key, parameters, ingestionrun.TriggerDirect, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, id)
		owner := strings.Repeat(string(ownerByte), 64)
		run, err := runs.Claim(context.Background(), owner)
		if err != nil || run == nil || run.ID != id {
			t.Fatalf("claimed=%+v error=%v want=%d", run, err, id)
		}
		return *run, owner
	}

	first, firstOwner := submit('a')
	firstResult := executor.Execute(context.Background(), first, firstOwner)
	if firstResult.Status != ingestionrun.StatusSucceeded || !firstResult.BusinessComplete {
		t.Fatalf("first result=%+v", firstResult)
	}
	wantFirstRequests := []string{
		"2099-01-01/2099-01-30/A", "2099-01-01/2099-01-30/B",
		"2099-01-31/2099-02-01/A", "2099-01-31/2099-02-01/B",
	}
	mutex.Lock()
	firstEnumerationCount := enumerations
	firstRequests := append([]string(nil), requests...)
	mode, requests = "late failure", nil
	mutex.Unlock()
	if firstEnumerationCount != 1 || !reflect.DeepEqual(firstRequests, wantFirstRequests) {
		t.Fatalf("enumerations=%d requests=%v", firstEnumerationCount, firstRequests)
	}

	var activeLoadID uint64
	if err := db.Get(&activeLoadID, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, from.String(), to.String()); err != nil {
		t.Fatal(err)
	}
	var load struct {
		Status, MemberStatus string
		Expected, Members    int
		Rows, Staged, Final  uint64
		ChecksumBytes        int
	}
	if err := db.QueryRowx(`SELECT load_row.status,load_row.expected_member_count,COUNT(member.member_key),
		MAX(member.status),MAX(member.row_count),MAX(OCTET_LENGTH(member.member_checksum)),
		(SELECT COUNT(*) FROM stg_fincloud_journal_transaction_reports WHERE load_id=load_row.id),
		(SELECT COUNT(*) FROM fincloud_journal_transaction_reports WHERE load_id=load_row.id)
		FROM fixed_report_loads load_row JOIN fixed_report_load_members member ON member.load_id=load_row.id
		WHERE load_row.id=? GROUP BY load_row.id`, activeLoadID).Scan(&load.Status, &load.Expected, &load.Members, &load.MemberStatus,
		&load.Rows, &load.ChecksumBytes, &load.Staged, &load.Final); err != nil {
		t.Fatal(err)
	}
	if load.Status != "published" || load.Expected != 1 || load.Members != 1 || load.MemberStatus != "success" || load.Rows != 4 || load.ChecksumBytes != 32 || load.Staged != 4 || load.Final != 4 {
		t.Fatalf("published load=%+v", load)
	}

	second, secondOwner := submit('b')
	secondResult := executor.Execute(context.Background(), second, secondOwner)
	if secondResult.Status != ingestionrun.StatusFailed || secondResult.BusinessComplete {
		t.Fatalf("second result=%+v", secondResult)
	}
	if err := runs.Finish(context.Background(), second.ID, secondOwner, secondResult.Status, secondResult.Error); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	secondEnumerationCount := enumerations
	secondRequests := append([]string(nil), requests...)
	mutex.Unlock()
	wantSecondRequests := []string{
		"2099-01-01/2099-01-30/A", "2099-01-01/2099-01-30/B", "2099-01-01/2099-01-30/C", "2099-01-01/2099-01-30/D",
		"2099-01-31/2099-02-01/A", "2099-01-31/2099-02-01/B", "2099-01-31/2099-02-01/C",
	}
	if secondEnumerationCount != 2 || !reflect.DeepEqual(secondRequests, wantSecondRequests) || strings.Contains(strings.Join(secondRequests, "/"), "%") {
		t.Fatalf("enumerations=%d requests=%v", secondEnumerationCount, secondRequests)
	}
	var stillActive uint64
	if err := db.Get(&stillActive, `SELECT active_load_id FROM fixed_report_publications WHERE job_key=? AND period_from=? AND period_to=?`, definition.Key, from.String(), to.String()); err != nil || stillActive != activeLoadID {
		t.Fatalf("active load=%d want=%d error=%v", stillActive, activeLoadID, err)
	}
	var failedLoadID, failedStaged uint64
	var failedLoadStatus, failedMemberStatus string
	if err := db.QueryRowx(`SELECT load_row.id,load_row.status,member.status,
		(SELECT COUNT(*) FROM stg_fincloud_journal_transaction_reports WHERE load_id=load_row.id)
		FROM fixed_report_loads load_row JOIN fixed_report_load_members member ON member.load_id=load_row.id
		WHERE load_row.ingestion_run_id=?`, second.ID).Scan(&failedLoadID, &failedLoadStatus, &failedMemberStatus, &failedStaged); err != nil {
		t.Fatal(err)
	}
	if failedLoadID == activeLoadID || failedLoadStatus != "pending" || failedMemberStatus != "pending" || failedStaged != 0 {
		t.Fatalf("failed load id=%d status=%s member=%s staged=%d", failedLoadID, failedLoadStatus, failedMemberStatus, failedStaged)
	}
}

func cleanupJournalIntegration(t *testing.T, db *sqlx.DB, runIDs []uint64) {
	t.Helper()
	for _, runID := range runIDs {
		var loadID uint64
		if err := db.Get(&loadID, `SELECT COALESCE(MAX(id),0) FROM fixed_report_loads WHERE ingestion_run_id=?`, runID); err != nil {
			t.Error(err)
			continue
		}
		if loadID != 0 {
			for _, query := range []string{
				`DELETE FROM stg_fincloud_journal_transaction_reports WHERE load_id=?`,
				`DELETE FROM fincloud_journal_transaction_reports WHERE load_id=?`,
				`DELETE FROM fixed_report_publications WHERE active_load_id=?`,
				`DELETE FROM fixed_report_loads WHERE id=?`,
			} {
				if _, err := db.Exec(query, loadID); err != nil {
					t.Error(err)
				}
			}
		}
		if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID); err != nil {
			t.Error(err)
		}
	}
}
