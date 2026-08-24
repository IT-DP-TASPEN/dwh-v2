//go:build integration

package ingestionexec

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestExactMaintenanceReportPublishesValidEmptySnapshot(t *testing.T) {
	db := integrationdb.Open(t)
	var definition ingestion.MaintenanceDefinition
	for _, candidate := range ingestion.MaintenanceDefinitions() {
		if candidate.Key == "cbr_customer" {
			definition = candidate
			break
		}
	}
	_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
	_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
	_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	t.Cleanup(func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
		_, _ = db.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
		_, _ = db.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	})

	listed := []string{}
	downloads := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/system/downloaderlaporan/pembuatan/loadorDownload":
			listed = append(listed, request.URL.Query().Get("file"))
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"pathfolder":"/app/report/cbr/20260824","list":[{"file":"cbrcustomer.csv","jenis":"File"}]}}}`)
		case "/system/downloaderlaporan/download.php":
			downloads++
			_, _ = io.WriteString(response, "Customer\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{client: client, maintenance: ingestionstore.NewMaintenanceRepository(db)}
	requested, _ := ingestion.ParseCalendarDate("2026-08-24")
	rows, err := executor.fetchAndSaveMaintenance(context.Background(), definition, requested)
	if err != nil || rows != 0 {
		t.Fatalf("rows=%d error=%v", rows, err)
	}
	if !reflect.DeepEqual(listed, []string{"cbr/20260824"}) || downloads != 1 {
		t.Fatalf("listed=%v downloads=%d", listed, downloads)
	}
	var published int
	if err := db.Get(&published, "SELECT COUNT(*) FROM `"+definition.TableName+"` WHERE requested_date=? AND as_of_date=?", requested.String(), requested.String()); err != nil || published != 0 {
		t.Fatalf("published rows=%d error=%v", published, err)
	}
}
