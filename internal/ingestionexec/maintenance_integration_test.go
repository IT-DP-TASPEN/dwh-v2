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
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
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
	owner := strings.Repeat("a", 64)
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,owner_id,claimed_at,heartbeat_at,started_at)
		VALUES ('job',?,'running','maintenance_series_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, definition.Key, owner)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	rows, err := executor.fetchAndSaveMaintenance(context.Background(), ingestionrun.Run{ID: uint64(runID), OwnerID: owner}, definition, requested)
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

func TestDetailOutstandingMergedAndSplitPublicationIsAtomic(t *testing.T) {
	db := integrationdb.Open(t)
	var definition ingestion.MaintenanceDefinition
	for _, candidate := range ingestion.MaintenanceDefinitions() {
		if candidate.Key == detailOutstandingDefinitionKey {
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

	header := "No Rekening|Branch|Value\n"
	contents := map[string]string{
		"DetailOutstandingRekeningPinjaman_000.csv": header + "LN-000-A|000|a\nLN-000-B|000|b\n",
		"DetailOutstandingRekeningPinjaman_001.csv": "No Rekening|Branch|Value\r\nLN-001|001|one\r\n",
		"DetailOutstandingRekeningPinjaman_002.csv": header + "LN-002|002|two\n",
		"DetailOutstandingRekeningPinjaman_003.csv": header + "LN-003|004|filename-content-mismatch\n",
		"DetailOutstandingRekeningPinjaman_004.csv": header + "LN-004|004|four\n",
		"DetailOutstandingRekeningPinjaman_005.csv": header,
		"DetailOutstandingRekeningPinjaman_006.csv": header + "LN-006|006|six\n",
		"DetailOutstandingRekeningPinjaman_007.csv": header + "LN-007|007|seven\n",
		"DetailOutstandingRekeningPinjaman_008.csv": header + "LN-008|008|eight\n",
		"DetailOutstandingRekeningPinjaman_009.csv": header + "EXTRA|009|must-not-load\n",
		"DetailOutstandingRekeningPinjaman_ABC.csv": header + "EXTRA-ABC|ABC|must-not-load\n",
	}
	complete := make([]string, len(detailOutstandingBranches))
	for index, branch := range detailOutstandingBranches {
		complete[index] = "DetailOutstandingRekeningPinjaman_" + branch + ".csv"
	}
	mode := "merged"
	listed, downloads := []string{}, []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/system/downloaderlaporan/pembuatan/loadorDownload":
			listed = append(listed, request.URL.Query().Get("file"))
			files := []string{detailOutstandingMergedFile}
			switch mode {
			case "split", "header mismatch", "download failure":
				files = []string{complete[8], complete[2], complete[0], complete[7], complete[1], complete[6], complete[3], complete[5], complete[4], "DetailOutstandingRekeningPinjaman_009.csv", "DetailOutstandingRekeningPinjaman_ABC.csv"}
			case "incomplete":
				files = append(append([]string{}, complete[:5]...), complete[6:]...)
			}
			items := make([]map[string]string, len(files))
			for index, fileName := range files {
				items[index] = map[string]string{"file": fileName, "jenis": "File"}
			}
			encoded, _ := json.Marshal(items)
			_, _ = fmt.Fprintf(response, `{"status":"ok","data":{"result":{"pathfolder":"/app/report/daily/20260824","list":%s}}}`, encoded)
		case "/system/downloaderlaporan/download.php":
			fileName := request.URL.Query().Get("file")
			downloads = append(downloads, fileName)
			if mode == "download failure" && fileName == complete[8] {
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if fileName == detailOutstandingMergedFile {
				_, _ = io.WriteString(response, header+"OLD|000|old\n")
				return
			}
			if mode == "header mismatch" && fileName == complete[6] {
				_, _ = io.WriteString(response, "No Rekening|Different\nLN-006|different\n")
				return
			}
			_, _ = io.WriteString(response, contents[fileName])
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	executor := &Executor{client: client, maintenance: ingestionstore.NewMaintenanceRepository(db), logger: slog.New(slog.NewTextHandler(&logs, nil))}
	requested, _ := ingestion.ParseCalendarDate("2026-08-24")
	owner := strings.Repeat("b", 64)
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,owner_id,claimed_at,heartbeat_at,started_at)
		VALUES ('job',?,'running','maintenance_date_series_v2',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, definition.Key, owner)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	run := ingestionrun.Run{ID: uint64(runID), OwnerID: owner}

	rows, err := executor.fetchAndSaveMaintenance(context.Background(), run, definition, requested)
	if err != nil || rows != 1 || !reflect.DeepEqual(downloads, []string{detailOutstandingMergedFile}) {
		t.Fatalf("merged rows=%d downloads=%v error=%v", rows, downloads, err)
	}
	var oldValue string
	if err := db.Get(&oldValue, "SELECT `value` FROM `"+definition.TableName+"` WHERE as_of_date=?", requested.String()); err != nil || oldValue != "old" {
		t.Fatalf("merged value=%q error=%v", oldValue, err)
	}

	mode, downloads = "split", nil
	rows, err = executor.fetchAndSaveMaintenance(context.Background(), run, definition, requested)
	if err != nil || rows != 9 || !reflect.DeepEqual(downloads, complete) {
		t.Fatalf("split rows=%d downloads=%v error=%v", rows, downloads, err)
	}
	type publishedRow struct {
		Account     string `db:"account"`
		Branch      string `db:"branch"`
		Value       string `db:"value"`
		FileName    string `db:"file_name"`
		Checksum    string `db:"checksum"`
		BusinessKey string `db:"business_key"`
		RowNumber   int    `db:"row_number"`
	}
	var published []publishedRow
	if err := db.Select(&published, "SELECT no_rekening AS account, branch, `value`, source_file_name AS file_name, source_row_number AS row_number, source_row_checksum AS checksum, business_key_hash AS business_key FROM `"+definition.TableName+"` WHERE as_of_date=? ORDER BY source_row_number", requested.String()); err != nil {
		t.Fatal(err)
	}
	if len(published) != 9 {
		t.Fatalf("published rows=%d: %+v", len(published), published)
	}
	expectedMetadata := map[string]ingestion.MaintenanceRow{}
	expectedFiles := map[string]string{}
	for _, fileName := range complete {
		parsed, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, requested, contents[fileName])
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range parsed.Rows {
			expectedMetadata[row.Values[0]] = row
			expectedFiles[row.Values[0]] = fileName
		}
	}
	for index, row := range published {
		expected := expectedMetadata[row.Account]
		if row.RowNumber != index+2 || row.FileName != expectedFiles[row.Account] || row.Checksum != expected.RowChecksum || row.BusinessKey != expected.BusinessKeyHash {
			t.Fatalf("published[%d]=%+v expected metadata=%+v", index, row, expected)
		}
	}
	if published[4].Account != "LN-003" || published[4].Branch != "004" || published[4].FileName != complete[3] {
		t.Fatalf("filename/content mismatch was altered: %+v", published[4])
	}
	var registryFilename string
	if err := db.Get(&registryFilename, `SELECT last_seen_filename FROM dynamic_csv_sources WHERE source_id=?`, definition.Key); err != nil || registryFilename != detailOutstandingMergedFile {
		t.Fatalf("registry filename=%q error=%v", registryFilename, err)
	}
	var seenCounts []int
	if err := db.Select(&seenCounts, `SELECT seen_count FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key); err != nil {
		t.Fatal(err)
	}
	for _, count := range seenCounts {
		if count != 2 {
			t.Fatalf("column registry updated per branch: counts=%v", seenCounts)
		}
	}
	if !strings.Contains(logs.String(), "source_mode=split") || !strings.Contains(logs.String(), complete[8]) {
		t.Fatalf("split source log missing: %s", logs.String())
	}
	stable := append([]publishedRow(nil), published...)

	for _, failure := range []struct {
		mode, class, message string
	}{
		{mode: "incomplete", class: "source_contract", message: "missing branches: 005"},
		{mode: "header mismatch", class: "source_contract", message: "header mismatch in DetailOutstandingRekeningPinjaman_006.csv"},
		{mode: "download failure", class: "source", message: "DetailOutstandingRekeningPinjaman_008.csv"},
	} {
		mode, downloads = failure.mode, nil
		_, err = executor.fetchAndSaveMaintenance(context.Background(), run, definition, requested)
		if err == nil || maintenanceErrorClass(err) != failure.class || !strings.Contains(err.Error(), failure.message) {
			t.Fatalf("mode=%s class=%s error=%v", failure.mode, maintenanceErrorClass(err), err)
		}
		published = nil
		if err := db.Select(&published, "SELECT no_rekening AS account, branch, `value`, source_file_name AS file_name, source_row_number AS row_number, source_row_checksum AS checksum, business_key_hash AS business_key FROM `"+definition.TableName+"` WHERE as_of_date=? ORDER BY source_row_number", requested.String()); err != nil || !reflect.DeepEqual(published, stable) {
			t.Fatalf("mode=%s changed authoritative snapshot: rows=%+v error=%v", failure.mode, published, err)
		}
	}
	for _, folder := range listed {
		if folder != "daily/20260824" {
			t.Fatalf("non-exact folder listed: %v", listed)
		}
	}
}
