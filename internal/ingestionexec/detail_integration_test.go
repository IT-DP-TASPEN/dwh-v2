//go:build integration

package ingestionexec

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestLoanDetailExplicitNullEnumerationSucceedsWithoutTerminalDiagnostic(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}

	var statuses []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/pinjaman/inquiry/rekening/cari":
			statuses = append(statuses, request.URL.Query().Get("status"))
			_, _ = io.WriteString(response, `{"data":{"result":null},"status":"ok"}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	sessions, authProfiles := integrationAuth(t, db, server.URL, "user", "pass", "001", "role")
	executor, err := New(sessions, authProfiles, ingestionstore.NewFixedRepository(db), ingestionstore.NewDetailRepository(db), ingestionstore.NewMasterRepository(db),
		ingestionstore.NewMaintenanceRepository(db), runs, catalog, 1, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	parameters, err := ingestionrun.NewLiveSnapshotExecution("loan_detail")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := runs.Submit(context.Background(), "loan_detail", parameters, ingestionrun.TriggerDirect, "null-listing-regression", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
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

	result := executor.Execute(context.Background(), *run, owner)
	if result.Status != ingestionrun.StatusSucceeded || !result.BusinessComplete || result.Cause != nil {
		t.Fatalf("result=%+v", result)
	}
	stored, err := runs.Get(context.Background(), runID)
	if err != nil || stored.Status != ingestionrun.StatusSucceeded || stored.Progress.Total != 0 || stored.Progress.Started != 0 {
		t.Fatalf("stored=%+v error=%v", stored, err)
	}
	if !reflect.DeepEqual(statuses, []string{"Aktif", "Closed", "WO", "HT"}) {
		t.Fatalf("statuses=%v", statuses)
	}
	events, err := runs.TechnicalEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Terminal || event.Step == "enumerate_identifiers" {
			t.Fatalf("unexpected technical event=%+v", event)
		}
	}
}

func TestSavingDetailStatementWireShapesDriveExactSetPublication(t *testing.T) {
	tests := []struct {
		name, statement string
		wantStatus      ingestionrun.Status
		wantRows        int
	}{
		{name: "object empty", statement: `{"status":"ok","data":{"result":{"mutasi":[]}}}`, wantStatus: ingestionrun.StatusSucceeded},
		{name: "array empty", statement: "{\"status\":\"ok\",\"data\":{\"result\":[ \n ]}}", wantStatus: ingestionrun.StatusSucceeded},
		{name: "non-empty array", statement: `{"status":"ok","data":{"result":[{}]}}`, wantStatus: ingestionrun.StatusFailed, wantRows: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := integrationdb.Open(t)
			clearSavingDetail(t, db)
			t.Cleanup(func() { clearSavingDetail(t, db) })
			if _, err := db.Exec(`INSERT INTO fincloud_saving_details
				(account_no,cif_no,beginning_balance,balance,raw_payload,raw_checksum,last_fetched_at)
				VALUES ('S-1','C-1',1,2,JSON_OBJECT(),REPEAT('0',64),UTC_TIMESTAMP(6))`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO fincloud_saving_account_statements
				(account_no,item_index,raw_item_payload,raw_item_checksum,last_fetched_at)
				VALUES ('S-1',0,JSON_OBJECT('seeded',TRUE),REPEAT('0',64),UTC_TIMESTAMP(6))`); err != nil {
				t.Fatal(err)
			}
			server := savingDetailServer(t, func(response http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(response, test.statement)
			})
			defer server.Close()
			executor, runs, _, _ := integrationExecutor(t, db, server.URL, "statement-wire-shape")
			run := claimedExecution(t, db, runs, "saving_detail")

			result := executor.Execute(context.Background(), run, run.OwnerID)
			wantSucceeded := test.wantStatus == ingestionrun.StatusSucceeded
			if result.Status != test.wantStatus || result.BusinessComplete != wantSucceeded || (result.Cause == nil) != wantSucceeded {
				t.Fatalf("result=%+v want status=%s", result, test.wantStatus)
			}
			var rows, seeded int
			err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(raw_item_checksum=REPEAT('0',64)),0)
				FROM fincloud_saving_account_statements WHERE account_no='S-1'`).Scan(&rows, &seeded)
			if err != nil || rows != test.wantRows || seeded != test.wantRows {
				t.Fatalf("published statement rows=%d seeded=%d want=%d error=%v", rows, seeded, test.wantRows, err)
			}
		})
	}
}
