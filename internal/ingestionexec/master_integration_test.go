//go:build integration

package ingestionexec

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestMasterExecutorPublishesReferenceAndMarketingWithoutSnapshotDate(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/cif/inquiry/cif//listvalues":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"gender":[{"id":"","descr":"Choose"},{"id":"1","descr":"One"}]}}}`)
		case "/system/marketing/pembuatan/cari?nama=":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":[{"id":"001","nama_marketing":"Name","locationname":"HQ","aktif":"1","status_dokumen":"ok","tgltransaksi":"raw"}]}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	sessions, authProfiles := integrationAuth(t, db, server.URL, "u", "p", "001", "r")
	executor, err := New(sessions, authProfiles, ingestionstore.NewFixedRepository(db), ingestionstore.NewDetailRepository(db), ingestionstore.NewMasterRepository(db), ingestionstore.NewMaintenanceRepository(db), runs, catalog, 1, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`DELETE FROM fincloud_reference_categories WHERE domain='cif'`)
	_, _ = db.Exec(`DELETE FROM fincloud_marketing_master`)
	var runIDs []uint64
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM stg_fincloud_reference_items`)
		_, _ = db.Exec(`DELETE FROM stg_fincloud_reference_categories`)
		_, _ = db.Exec(`DELETE FROM stg_fincloud_marketing_master`)
		_, _ = db.Exec(`DELETE FROM fincloud_reference_categories WHERE domain='cif'`)
		_, _ = db.Exec(`DELETE FROM fincloud_marketing_master`)
		for _, id := range runIDs {
			_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id)
		}
	})
	for _, key := range []string{"cif_reference_master", "marketing_master"} {
		parameters, _ := ingestionrun.NewLiveSnapshotExecution(key)
		runID, err := runs.Submit(context.Background(), key, parameters, ingestionrun.TriggerDirect, "master-executor-"+key, nil)
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, runID)
		owner, _ := ingestionrun.NewOwnerID()
		run, err := runs.Claim(context.Background(), owner)
		if err != nil || run == nil || run.ID != runID {
			t.Fatalf("claim %s=%+v error=%v", key, run, err)
		}
		result := executor.Execute(context.Background(), *run, owner)
		if result.Status != ingestionrun.StatusSucceeded || !result.BusinessComplete {
			t.Fatalf("%s result=%+v", key, result)
		}
		stored, err := runs.Get(context.Background(), runID)
		if err != nil || stored.Status != ingestionrun.StatusSucceeded || !stored.SnapshotDate.IsZero() {
			t.Fatalf("%s stored=%+v error=%v", key, stored, err)
		}
	}
	var categories, items, marketing int
	if err := db.QueryRowx(`SELECT (SELECT COUNT(*) FROM fincloud_reference_categories WHERE domain='cif'),(SELECT COUNT(*) FROM fincloud_reference_items WHERE domain='cif'),(SELECT COUNT(*) FROM fincloud_marketing_master)`).Scan(&categories, &items, &marketing); err != nil || categories != 1 || items != 1 || marketing != 1 {
		t.Fatalf("published categories=%d items=%d marketing=%d error=%v", categories, items, marketing, err)
	}
}
