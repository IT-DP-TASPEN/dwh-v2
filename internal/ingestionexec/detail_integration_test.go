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
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
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
	client, err := fincloud.NewClient(fincloud.Config{
		BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role",
		HTTPTimeout: time.Second, InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	executor, err := New(client, ingestionstore.NewFixedRepository(db), ingestionstore.NewDetailRepository(db), ingestionstore.NewMasterRepository(db),
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
