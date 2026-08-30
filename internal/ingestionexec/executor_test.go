package ingestionexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestiondiag"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
)

func TestProgressPersistenceFailureAndDiagnosticFailureAreNonFatal(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			calls := 0
			executor := &Executor{logger: logger, updateProgress: func(context.Context, uint64, string, ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error {
				calls++
				return &mysql.MySQLError{Number: number, Message: "transaction concurrency failure"}
			}}
			run := ingestionrun.Run{ID: 9, JobKey: "saving_detail", OwnerID: "owner"}
			recorder := newRunDiagnosticRecorder(failingTechnicalWriter{err: errors.New("diagnostic persistence unavailable")}, logger, run.ID, run.JobKey)
			ctx := ingestiondiag.WithRecorder(context.Background(), recorder.record, run.ID, run.JobKey)
			enabled := true
			if err := executor.persistProgress(ctx, run, ingestionrun.Progress{Total: 1}, nil, &enabled); err != nil || enabled {
				t.Fatalf("first progress error=%v enabled=%v", err, enabled)
			}
			if err := executor.persistProgress(ctx, run, ingestionrun.Progress{Total: 1}, nil, &enabled); err != nil || calls != 1 {
				t.Fatalf("degraded progress hammered persistence: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestProgressOwnershipLossRemainsFatal(t *testing.T) {
	executor := &Executor{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), updateProgress: func(context.Context, uint64, string, ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error {
		return ingestionrun.ErrOwnershipLost
	}}
	enabled := true
	err := executor.persistProgress(context.Background(), ingestionrun.Run{ID: 1, OwnerID: "old"}, ingestionrun.Progress{}, nil, &enabled)
	if !errors.Is(err, ingestionrun.ErrOwnershipLost) || !enabled {
		t.Fatalf("ownership error=%v enabled=%v", err, enabled)
	}
}

func TestMaintenanceUsesOnlyExactRequestedDirectoryAndPreservesFailureClass(t *testing.T) {
	requested, _ := ingestion.ParseCalendarDate("2026-08-24")
	definition := ingestion.MaintenanceDefinitions()[0]
	const exactFolder = "daily/20260824"
	tests := []struct {
		name, exactPath, folderItem, content, class string
		status                                      int
		wantDownloads                               int
	}{
		{name: "missing with prior date", class: "date_local"},
		{name: "missing with several prior dates", class: "date_local"},
		{name: "malformed exact report", exactPath: "/app/report/daily/20260824/CIF Opening Report (Full).csv", content: "Wrong Header\n", class: "source_contract", wantDownloads: 1},
		{name: "returned path identifies prior date", exactPath: "/app/report/daily/20260823/CIF Opening Report (Full).csv", class: "source_contract"},
		{name: "nested directory identifies prior date", folderItem: "../20260823", class: "source_contract"},
		{name: "Fincloud request failure", status: http.StatusServiceUnavailable, class: "source"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listed := []string{}
			downloads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/admin/access/login":
					_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
				case "/system/downloaderlaporan/pembuatan/loadorDownload":
					folder := request.URL.Query().Get("file")
					listed = append(listed, folder)
					if test.status != 0 {
						response.WriteHeader(test.status)
						return
					}
					path, list := "/app/report/"+folder, `[]`
					if test.exactPath != "" {
						path = test.exactPath[:len(test.exactPath)-len("CIF Opening Report (Full).csv")-1]
						list = `[{"file":"CIF Opening Report (Full).csv","jenis":"File"}]`
					} else if test.folderItem != "" {
						list = fmt.Sprintf(`[{"file":%q,"jenis":"Folder"}]`, test.folderItem)
					}
					_, _ = fmt.Fprintf(response, `{"status":"ok","data":{"result":{"pathfolder":%q,"list":%s}}}`, path, list)
				case "/system/downloaderlaporan/download.php":
					downloads++
					_, _ = io.WriteString(response, test.content)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
			if err != nil {
				t.Fatal(err)
			}
			executor := &Executor{client: client}
			_, err = executor.fetchAndSaveMaintenance(context.Background(), ingestionrun.Run{}, definition, requested)
			if err == nil || maintenanceErrorClass(err) != test.class {
				t.Fatalf("error=%v class=%s want=%s", err, maintenanceErrorClass(err), test.class)
			}
			if !reflect.DeepEqual(listed, []string{exactFolder}) || downloads != test.wantDownloads {
				t.Fatalf("listed=%v downloads=%d", listed, downloads)
			}
		})
	}
}

func TestDetailOutstandingSourceSelection(t *testing.T) {
	var definition ingestion.MaintenanceDefinition
	for _, candidate := range ingestion.MaintenanceDefinitions() {
		if candidate.Key == detailOutstandingDefinitionKey {
			definition = candidate
			break
		}
	}
	root := "/app/report/daily/20260824/"
	merged := root + detailOutstandingMergedFile
	complete := make([]string, len(detailOutstandingBranches))
	for index, branch := range detailOutstandingBranches {
		complete[index] = root + "DetailOutstandingRekeningPinjaman_" + branch + ".csv"
	}
	shuffled := []string{complete[8], complete[2], complete[0], complete[7], complete[1], complete[6], complete[3], complete[5], complete[4]}
	tests := []struct {
		name, mode, errorContains string
		files, wantPaths          []string
	}{
		{name: "merged", files: []string{root + "other.csv", merged}, mode: maintenanceSourceMerged, wantPaths: []string{merged}},
		{name: "complete split canonical order", files: shuffled, mode: maintenanceSourceSplit, wantPaths: complete},
		{name: "complete split ignores extras", files: append(append([]string{}, shuffled...), root+"DetailOutstandingRekeningPinjaman_009.csv", root+"DetailOutstandingRekeningPinjaman_ABC.csv"), mode: maintenanceSourceSplit, wantPaths: complete},
		{name: "extras only are not a source", files: []string{root + "DetailOutstandingRekeningPinjaman_009.csv", root + "DetailOutstandingRekeningPinjaman_ABC.csv"}},
		{name: "one canonical is incomplete", files: []string{complete[5]}, errorContains: "missing branches: 000, 001, 002, 003, 004, 006, 007, 008"},
		{name: "multiple canonical missing", files: append([]string{}, complete[:7]...), errorContains: "missing branches: 007, 008"},
		{name: "merged and one split", files: []string{merged, complete[0]}, errorContains: "merged and split files coexist"},
		{name: "merged and complete split", files: append([]string{merged}, complete...), errorContains: "merged and split files coexist"},
		{name: "merged and extra split-looking", files: []string{merged, root + "DetailOutstandingRekeningPinjaman_ABC.csv"}, errorContains: "merged and split files coexist"},
		{name: "duplicate merged", files: []string{merged, root + "nested/" + detailOutstandingMergedFile}, errorContains: "duplicate DetailOutstandingRekeningPinjaman merged source"},
		{name: "duplicate canonical branch", files: append(append([]string{}, complete...), root+"nested/DetailOutstandingRekeningPinjaman_005.csv"), errorContains: "duplicate DetailOutstandingRekeningPinjaman split branch 005"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := selectMaintenanceSources(definition, test.files)
			if test.errorContains != "" {
				if err == nil || !strings.Contains(err.Error(), test.errorContains) {
					t.Fatalf("selection=%+v error=%v", selection, err)
				}
				return
			}
			wantLogicalFileName := ""
			if len(test.wantPaths) > 0 {
				wantLogicalFileName = detailOutstandingMergedFile
			}
			if err != nil || selection.mode != test.mode || selection.logicalFileName != wantLogicalFileName || !reflect.DeepEqual(selection.paths, test.wantPaths) {
				t.Fatalf("selection=%+v error=%v", selection, err)
			}
		})
	}
}

func TestDetailOutstandingAggregationRebasesOnlyTechnicalRowIdentity(t *testing.T) {
	var definition ingestion.MaintenanceDefinition
	for _, candidate := range ingestion.MaintenanceDefinitions() {
		if candidate.Key == detailOutstandingDefinitionKey {
			definition = candidate
			break
		}
	}
	requested, _ := ingestion.ParseCalendarDate("2026-08-24")
	parts := []struct {
		fileName, content string
	}{
		{"DetailOutstandingRekeningPinjaman_000.csv", "No Rekening|Branch|Value\nLN-000-A|000|a\nLN-000-B|000|b\n"},
		{"DetailOutstandingRekeningPinjaman_003.csv", "No Rekening|Branch|Value\r\nLN-003|004|as-is\r\n"},
		{"DetailOutstandingRekeningPinjaman_005.csv", "No Rekening|Branch|Value\n"},
	}
	var combined ingestion.ParsedMaintenanceCSV
	nextRow := 2
	var wantHashes, wantChecksums []string
	for _, part := range parts {
		parsed, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, requested, part.content)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range parsed.Rows {
			wantHashes = append(wantHashes, row.BusinessKeyHash)
			wantChecksums = append(wantChecksums, row.RowChecksum)
		}
		if err := appendMaintenanceSource(&combined, parsed, part.fileName, maintenanceSourceSplit, &nextRow); err != nil {
			t.Fatal(err)
		}
	}
	if len(combined.Rows) != 3 || nextRow != 5 {
		t.Fatalf("rows=%d next=%d", len(combined.Rows), nextRow)
	}
	for index, row := range combined.Rows {
		wantFile := parts[0].fileName
		if index == 2 {
			wantFile = parts[1].fileName
		}
		if row.SourceRowNumber != index+2 || row.SourceFileName != wantFile || row.BusinessKeyHash != wantHashes[index] || row.RowChecksum != wantChecksums[index] {
			t.Fatalf("row[%d]=%+v", index, row)
		}
	}
	if combined.Rows[2].Values[1] != "004" {
		t.Fatalf("filename/content mismatch altered: %+v", combined.Rows[2])
	}
	mismatch, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, requested, "No Rekening|Different\nLN-X|x\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := appendMaintenanceSource(&combined, mismatch, "DetailOutstandingRekeningPinjaman_006.csv", maintenanceSourceSplit, &nextRow); err == nil || !strings.Contains(err.Error(), "DetailOutstandingRekeningPinjaman_006.csv") {
		t.Fatalf("header mismatch error=%v", err)
	}
	merged, err := ingestion.ParseMaintenanceCSV(context.Background(), definition, requested, parts[0].content)
	if err != nil {
		t.Fatal(err)
	}
	var mergedCombined ingestion.ParsedMaintenanceCSV
	mergedNextRow := 2
	if err := appendMaintenanceSource(&mergedCombined, merged, detailOutstandingMergedFile, maintenanceSourceMerged, &mergedNextRow); err != nil || mergedCombined.Rows[1].SourceRowNumber != 3 {
		t.Fatalf("merged aggregation=%+v next=%d error=%v", mergedCombined.Rows, mergedNextRow, err)
	}
}

func TestDetailOutstandingMissingAndInvalidLayoutsDownloadNothing(t *testing.T) {
	var definition ingestion.MaintenanceDefinition
	for _, candidate := range ingestion.MaintenanceDefinitions() {
		if candidate.Key == detailOutstandingDefinitionKey {
			definition = candidate
			break
		}
	}
	requested, _ := ingestion.ParseCalendarDate("2026-08-24")
	tests := []struct {
		name, class, errorContains string
		files                      []string
		priorComplete              bool
	}{
		{name: "extras only", class: "date_local", files: []string{"DetailOutstandingRekeningPinjaman_009.csv", "DetailOutstandingRekeningPinjaman_ABC.csv"}},
		{name: "incomplete exact date ignores complete prior date", class: "source_contract", errorContains: "missing branches: 001, 002, 003, 004, 005, 006, 007, 008", files: []string{"DetailOutstandingRekeningPinjaman_000.csv", "DetailOutstandingRekeningPinjaman_ABC.csv"}, priorComplete: true},
		{name: "coexistence", class: "source_contract", errorContains: "merged and split files coexist", files: []string{detailOutstandingMergedFile, "DetailOutstandingRekeningPinjaman_000.csv"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			downloads := 0
			listed := []string{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/admin/access/login":
					_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
				case "/system/downloaderlaporan/pembuatan/loadorDownload":
					folder := request.URL.Query().Get("file")
					listed = append(listed, folder)
					files := test.files
					if test.priorComplete && folder == "daily/20260823" {
						files = make([]string, len(detailOutstandingBranches))
						for index, branch := range detailOutstandingBranches {
							files[index] = "DetailOutstandingRekeningPinjaman_" + branch + ".csv"
						}
					}
					items := make([]map[string]string, len(files))
					for index, fileName := range files {
						items[index] = map[string]string{"file": fileName, "jenis": "File"}
					}
					encoded, _ := json.Marshal(items)
					_, _ = fmt.Fprintf(response, `{"status":"ok","data":{"result":{"pathfolder":%q,"list":%s}}}`, "/app/report/"+folder, encoded)
				case "/system/downloaderlaporan/download.php":
					downloads++
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
			if err != nil {
				t.Fatal(err)
			}
			executor := &Executor{client: client}
			_, err = executor.fetchAndSaveMaintenance(context.Background(), ingestionrun.Run{}, definition, requested)
			if err == nil || maintenanceErrorClass(err) != test.class || (test.errorContains != "" && !strings.Contains(err.Error(), test.errorContains)) || downloads != 0 || !reflect.DeepEqual(listed, []string{"daily/20260824"}) {
				t.Fatalf("class=%s error=%v listed=%v downloads=%d", maintenanceErrorClass(err), err, listed, downloads)
			}
		})
	}
}

func TestSourceWideFailureClassification(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusNotFound} {
		if !sourceWide(&fincloud.Error{Kind: fincloud.ErrorUpstream, HTTPStatus: status}) {
			t.Fatalf("HTTP %d was not source-wide fatal", status)
		}
	}
	for _, kind := range []fincloud.ErrorKind{fincloud.ErrorAuthentication, fincloud.ErrorUnauthorized, fincloud.ErrorMalformed} {
		if !sourceWide(&fincloud.Error{Kind: kind}) {
			t.Fatalf("%s was not source-wide fatal", kind)
		}
	}
	if sourceWide(context.Canceled) {
		t.Fatal("context cancellation classified as source failure")
	}
}

func TestJakartaSnapshotDateUsesExecutionDate(t *testing.T) {
	if got := jakartaSnapshotDate(time.Date(2026, 8, 13, 16, 59, 59, 0, time.UTC)).String(); got != "2026-08-13" {
		t.Fatalf("snapshot before Jakarta midnight=%s", got)
	}
	if got := jakartaSnapshotDate(time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC)).String(); got != "2026-08-14" {
		t.Fatalf("snapshot after Jakarta midnight=%s", got)
	}
}

func TestDetailMapperFailureContinuesAndCancellationKeepsDiagnostics(t *testing.T) {
	mapperFailure := testMapperFailure(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var durable []byte
	outcome := runDetailPool(ctx, []string{"bad", "good", "later"}, 1, func(_ context.Context, identifier string) detailItemResult {
		if identifier == "bad" {
			return detailItemResult{layer: detailLayerMap, err: mapperFailure}
		}
		return detailItemResult{rows: 1}
	}, func(_ ingestionrun.Progress, diagnostics *ingestionrun.MapperDiagnostics) error {
		if diagnostics != nil {
			durable, _ = diagnostics.Marshal()
		}
		return nil
	})
	if outcome.fatal != nil || outcome.firstLocal == nil || outcome.progress.Started != 3 || outcome.progress.Succeeded != 2 || outcome.progress.Failed != 1 {
		t.Fatalf("local mapper outcome=%+v", outcome)
	}
	if len(durable) == 0 || outcome.diagnostics.TotalCount != 1 {
		t.Fatalf("mapper diagnostics were not flushed: %s", durable)
	}

	ctx, cancel = context.WithCancel(context.Background())
	durable = nil
	outcome = runDetailPool(ctx, []string{"bad", "not-started"}, 1, func(_ context.Context, _ string) detailItemResult {
		return detailItemResult{layer: detailLayerMap, err: mapperFailure}
	}, func(_ ingestionrun.Progress, diagnostics *ingestionrun.MapperDiagnostics) error {
		if diagnostics != nil {
			durable, _ = diagnostics.Marshal()
			cancel()
		}
		return nil
	})
	if len(durable) == 0 || outcome.diagnostics.TotalCount == 0 {
		t.Fatal("cancellation lost already-flushed mapper diagnostics")
	}
}

func TestDetailFatalFailuresStopPoolAndKeepLayer(t *testing.T) {
	calls := 0
	sourceErr := &fincloud.Error{Kind: fincloud.ErrorUpstream, HTTPStatus: http.StatusServiceUnavailable}
	outcome := runDetailPool(context.Background(), make([]string, 100), 1, func(_ context.Context, _ string) detailItemResult {
		calls++
		if calls == 1 {
			return detailItemResult{layer: detailLayerFetch, err: sourceErr}
		}
		return detailItemResult{rows: 1}
	}, func(ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error { return nil })
	if outcome.fatalLayer != detailLayerFetch || calls >= 100 {
		t.Fatalf("fatal source did not stop pool: layer=%d calls=%d", outcome.fatalLayer, calls)
	}
	result := detailFatalFailure(context.Background(), outcome.fatalLayer, outcome.fatal)
	if result.Error.Class != "source" || result.Error.Step != "fetch_detail" {
		t.Fatalf("source result=%+v", result)
	}

	result = detailFatalFailure(context.Background(), detailLayerPersist, errors.New("database rejected candidate"))
	if result.Error.Class != "persistence" || result.Error.Step != "stage_detail" {
		t.Fatalf("business persistence result=%+v", result)
	}
	outcome = runDetailPool(context.Background(), []string{"one"}, 1, func(_ context.Context, _ string) detailItemResult {
		return detailItemResult{rows: 1}
	}, func(ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error {
		return errors.New("run progress unavailable")
	})
	result = detailFatalFailure(context.Background(), outcome.fatalLayer, outcome.fatal)
	if result.Error.Class != "persistence" || result.Error.Step != "persist_run_progress" {
		t.Fatalf("run-state persistence result=%+v", result)
	}
}

func TestFetchAndStageDetailMarksBusinessPersistenceFailure(t *testing.T) {
	item := detailPersistenceFailure(errors.New("database rejected candidate"))
	if item.err == nil || item.layer != detailLayerPersist {
		t.Fatalf("business persistence failure=%+v", item)
	}
	result := detailFatalFailure(context.Background(), item.layer, item.err)
	if result.Error.Class != "persistence" || result.Error.Step != "stage_detail" {
		t.Fatalf("business persistence result=%+v", result)
	}
}

func TestFixedFailureLayersAndCancellationCause(t *testing.T) {
	for _, test := range []struct {
		result fixedMemberResult
		class  string
		step   string
	}{
		{fixedMemberResult{layer: fixedLayerSource, step: "download_report", err: context.DeadlineExceeded}, "source", "download_report"},
		{fixedMemberResult{layer: fixedLayerSourceContract, step: "parse_fixed_csv", err: errors.New("bad header")}, "source_contract", "parse_fixed_csv"},
		{fixedMemberResult{layer: fixedLayerPersistence, step: "stage_fixed_member_segment", err: errors.New("database unavailable")}, "persistence", "stage_fixed_member_segment"},
	} {
		got := fixedFailure(context.Background(), test.result)
		if got.Error.Class != test.class || got.Error.Step != test.step {
			t.Fatalf("result=%+v", got)
		}
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ingestionrun.ErrCancellationRequested)
	got := fixedFailure(ctx, fixedMemberResult{layer: fixedLayerSource, step: "download_report", err: errors.New("generic context canceled")})
	if got.Status != ingestionrun.StatusCancelled || !errors.Is(got.Cause, ingestionrun.ErrCancellationRequested) {
		t.Fatalf("explicit cancellation=%+v", got)
	}

	ctx, cancel = context.WithCancelCause(context.Background())
	cancel(ingestionrun.ErrCoordinatorShutdown)
	got = sourceFailure(ctx, context.Canceled, "download_report")
	if got.Status != ingestionrun.StatusCancelled || got.Error.Message != "application shutdown cancelled the run" {
		t.Fatalf("shutdown=%+v", got)
	}
}

func TestFixedPoolPreservesPrimaryErrorAndJoinsWorkers(t *testing.T) {
	descriptors := []ingestion.RequestDescriptor{{MemberKey: "primary"}, {MemberKey: "sibling"}}
	started := make(chan struct{}, len(descriptors))
	release := make(chan struct{})
	done := make(chan struct{})
	var results []fixedMemberResult
	go func() {
		runFixedPool(context.Background(), descriptors, 2, func(ctx context.Context, descriptor ingestion.RequestDescriptor) fixedMemberResult {
			started <- struct{}{}
			if descriptor.MemberKey == "primary" {
				<-release
				return fixedMemberResult{layer: fixedLayerSource, step: "download_report", err: errors.New("primary source failure")}
			}
			<-ctx.Done()
			return fixedMemberResult{layer: fixedLayerSource, step: "download_report", err: ctx.Err()}
		}, func(result fixedMemberResult) { results = append(results, result) })
		close(done)
	}()
	<-started
	<-started
	select {
	case <-done:
		t.Fatal("pool returned before active workers exited")
	default:
	}
	close(release)
	<-done
	if len(results) != 2 || results[0].err == nil || results[0].err.Error() != "primary source failure" {
		t.Fatalf("results=%+v", results)
	}
}

func TestFreezeJournalTransactionTypesCanonicalizesByID(t *testing.T) {
	got, err := freezeJournalTransactionTypes([]fincloud.JournalTransactionType{
		{ID: " B ", Description: "Beta"},
		{ID: "", Description: "Blank"},
		{ID: " % ", Description: "Wildcard"},
		{ID: "A", Description: "Alpha first"},
		{ID: "A", Description: "Alpha duplicate"},
		{ID: "1", Description: "One"},
		{ID: "01", Description: "Zero one"},
		{ID: "001", Description: "Zero zero one"},
	})
	want := []journalTransactionType{
		{ID: "001", Description: "Zero zero one"},
		{ID: "01", Description: "Zero one"},
		{ID: "1", Description: "One"},
		{ID: "A", Description: "Alpha first"},
		{ID: "B", Description: "Beta"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("frozen=%+v error=%v want=%+v", got, err, want)
	}
	for _, rows := range [][]fincloud.JournalTransactionType{nil, {{ID: " "}, {ID: "%"}, {ID: " % "}}} {
		if _, err := freezeJournalTransactionTypes(rows); err == nil {
			t.Fatalf("zero usable transaction types accepted: %+v", rows)
		}
	}
}

func TestJournalTransactionRequestRequiresExactSelector(t *testing.T) {
	definition := ingestion.FixedDefinitions()[1]
	from, _ := ingestion.ParseCalendarDate("2026-08-11")
	to, _ := ingestion.ParseCalendarDate("2026-08-12")
	request, err := ingestion.BuildFixedRequestDescriptor(definition, ingestion.FixedDateRangeParams{From: from, To: to}, "", "", ingestion.SingleFixedMemberKey)
	if err != nil || request.Parameters[1] != "" {
		t.Fatalf("logical request=%+v error=%v", request, err)
	}
	exact, err := journalTransactionRequest(request, " 001 ")
	if err != nil || exact.Parameters[1] != "001" || request.Parameters[1] != "" {
		t.Fatalf("exact=%+v logical=%+v error=%v", exact, request, err)
	}
	for _, invalid := range []string{"", " ", "%", " % "} {
		if _, err := journalTransactionRequest(request, invalid); err == nil {
			t.Fatalf("selector %q accepted", invalid)
		}
	}
}

func TestJournalTransactionPartitionsAreSequentialAndAggregateOneCandidate(t *testing.T) {
	definition := ingestion.FixedDefinitions()[1]
	executor, requests := journalPartitionTestExecutor(t, func(parameters []string) (int, string) {
		transactionType := parameters[1]
		if transactionType == "B" {
			return http.StatusOK, journalTestCSV(definition, "", true, false)
		}
		return http.StatusOK, journalTestCSV(definition, "same-row", false, false)
	})
	from, _ := ingestion.ParseCalendarDate("2026-01-01")
	to, _ := ingestion.ParseCalendarDate("2026-03-31")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	types := []journalTransactionType{{ID: "A"}, {ID: "B"}, {ID: "C"}}
	var segments []ingestionstore.FixedSegment
	result := executor.fetchFixedMemberSegments(context.Background(), definition, plan.Members[0], types, func(_ context.Context, segment ingestionstore.FixedSegment) error {
		if fetched := len(requests()); fetched != len(segments)+1 {
			t.Fatalf("segment %d staged after %d source requests", segment.Index, fetched)
		}
		segments = append(segments, segment)
		return nil
	})
	wantRequests := []string{
		"2026-01-01/2026-01-30/A", "2026-01-01/2026-01-30/B", "2026-01-01/2026-01-30/C",
		"2026-01-31/2026-03-01/A", "2026-01-31/2026-03-01/B", "2026-01-31/2026-03-01/C",
		"2026-03-02/2026-03-31/A", "2026-03-02/2026-03-31/B", "2026-03-02/2026-03-31/C",
	}
	if result.err != nil || result.rows != 6 || result.segments != 9 || !reflect.DeepEqual(requests(), wantRequests) || len(segments) != 9 {
		t.Fatalf("result=%+v requests=%v segments=%d", result, requests(), len(segments))
	}
	var checksum string
	for index, segment := range segments {
		if segment.Index != index {
			t.Fatalf("segment[%d].Index=%d", index, segment.Index)
		}
		wantDate := []string{"2026-01-30", "2026-03-01", "2026-03-31"}[index/3]
		if segment.AsOfDate.String() != wantDate {
			t.Fatalf("segment[%d].AsOfDate=%s want=%s", index, segment.AsOfDate, wantDate)
		}
		if index%3 == 1 {
			if len(segment.SourceRows) != 0 {
				t.Fatalf("valid empty segment[%d] rows=%d", index, len(segment.SourceRows))
			}
			continue
		}
		if len(segment.SourceRows) != 1 {
			t.Fatalf("segment[%d] rows=%d", index, len(segment.SourceRows))
		}
		if checksum == "" {
			checksum = segment.SourceRows[0].SourceRowChecksum
		} else if segment.SourceRows[0].SourceRowChecksum != checksum {
			t.Fatalf("cross-partition row checksum changed: %s != %s", segment.SourceRows[0].SourceRowChecksum, checksum)
		}
	}
}

func TestJournalTransactionPartitionsAcceptCompatibleCanonicalSchema(t *testing.T) {
	definition := ingestion.FixedDefinitions()[1]
	executor, _ := journalPartitionTestExecutor(t, func(parameters []string) (int, string) {
		return http.StatusOK, journalTestCSV(definition, "same-row", false, parameters[1] == "B")
	})
	date, _ := ingestion.ParseCalendarDate("2026-01-01")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: date, To: date}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	var segments []ingestionstore.FixedSegment
	result := executor.fetchFixedMemberSegments(context.Background(), definition, plan.Members[0], []journalTransactionType{{ID: "A"}, {ID: "B"}}, func(_ context.Context, segment ingestionstore.FixedSegment) error {
		segments = append(segments, segment)
		return nil
	})
	if result.err != nil || result.rows != 2 || len(segments) != 2 || !reflect.DeepEqual(segments[0].SourceRows[0].Values, segments[1].SourceRows[0].Values) {
		t.Fatalf("result=%+v segments=%+v", result, segments)
	}
}

func TestJournalTransactionPartitionFailureStopsRemainingWork(t *testing.T) {
	definition := ingestion.FixedDefinitions()[1]
	tests := []struct {
		name, from, to string
		types          []journalTransactionType
		respond        func([]string) (int, string)
		wantRequests   []string
		wantLayer      fixedLayer
	}{
		{
			name: "download failure skips later type", from: "2026-01-01", to: "2026-01-01",
			types: []journalTransactionType{{ID: "A"}, {ID: "B"}, {ID: "C"}, {ID: "D"}},
			respond: func(parameters []string) (int, string) {
				if parameters[1] == "C" {
					return http.StatusServiceUnavailable, ""
				}
				return http.StatusOK, journalTestCSV(definition, "same-row", false, false)
			},
			wantRequests: []string{"2026-01-01/2026-01-01/A", "2026-01-01/2026-01-01/B", "2026-01-01/2026-01-01/C"}, wantLayer: fixedLayerSource,
		},
		{
			name: "later chunk failure", from: "2026-01-01", to: "2026-02-01",
			types: []journalTransactionType{{ID: "A"}, {ID: "B"}},
			respond: func(parameters []string) (int, string) {
				if parameters[1] == "A" && parameters[2] == "2026-01-31" {
					return http.StatusServiceUnavailable, ""
				}
				return http.StatusOK, journalTestCSV(definition, "same-row", false, false)
			},
			wantRequests: []string{"2026-01-01/2026-01-30/A", "2026-01-01/2026-01-30/B", "2026-01-31/2026-02-01/A"}, wantLayer: fixedLayerSource,
		},
		{
			name: "incompatible schema", from: "2026-01-01", to: "2026-01-01",
			types: []journalTransactionType{{ID: "A"}, {ID: "B"}, {ID: "C"}},
			respond: func(parameters []string) (int, string) {
				if parameters[1] == "B" {
					return http.StatusOK, "Wrong Header\n"
				}
				return http.StatusOK, journalTestCSV(definition, "same-row", false, false)
			},
			wantRequests: []string{"2026-01-01/2026-01-01/A", "2026-01-01/2026-01-01/B"}, wantLayer: fixedLayerSourceContract,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, requests := journalPartitionTestExecutor(t, test.respond)
			from, _ := ingestion.ParseCalendarDate(test.from)
			to, _ := ingestion.ParseCalendarDate(test.to)
			plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
			if err != nil {
				t.Fatal(err)
			}
			var staged []int
			result := executor.fetchFixedMemberSegments(context.Background(), definition, plan.Members[0], test.types, func(_ context.Context, segment ingestionstore.FixedSegment) error {
				staged = append(staged, segment.Index)
				return nil
			})
			if result.err == nil || result.layer != test.wantLayer || !reflect.DeepEqual(requests(), test.wantRequests) || strings.Contains(strings.Join(requests(), "/"), "%") {
				t.Fatalf("result=%+v staged=%v requests=%v", result, staged, requests())
			}
		})
	}
}

func TestFixedSegmentStageFailureStopsBeforeNextFetch(t *testing.T) {
	definition := ingestion.FixedDefinitions()[1]
	executor, requests := journalPartitionTestExecutor(t, func([]string) (int, string) {
		return http.StatusOK, journalTestCSV(definition, "row", false, false)
	})
	date, _ := ingestion.ParseCalendarDate("2026-01-01")
	plan, err := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: date, To: date}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
	if err != nil {
		t.Fatal(err)
	}
	var staged []int
	result := executor.fetchFixedMemberSegments(context.Background(), definition, plan.Members[0], []journalTransactionType{{ID: "A"}, {ID: "B"}, {ID: "C"}, {ID: "D"}}, func(_ context.Context, segment ingestionstore.FixedSegment) error {
		if segment.Index == 2 {
			return errors.New("forced segment staging failure")
		}
		staged = append(staged, segment.Index)
		return nil
	})
	if result.layer != fixedLayerPersistence || result.step != "stage_fixed_member_segment" || !reflect.DeepEqual(staged, []int{0, 1}) {
		t.Fatalf("result=%+v staged=%v", result, staged)
	}
	wantRequests := []string{"2026-01-01/2026-01-01/A", "2026-01-01/2026-01-01/B", "2026-01-01/2026-01-01/C"}
	if got := requests(); !reflect.DeepEqual(got, wantRequests) {
		t.Fatalf("requests=%v want=%v", got, wantRequests)
	}
}

func TestGenericFixedSegmentsStageIncrementallyAndSingleSegmentStaysEquivalent(t *testing.T) {
	definition := ingestion.FixedDefinitions()[5]
	var mutex sync.Mutex
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/system/laporanUmum/data/lap":
			var parameters []string
			if err := json.Unmarshal([]byte(request.URL.Query().Get("p")), &parameters); err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			mutex.Lock()
			requests = append(requests, parameters[4]+"/"+parameters[5])
			mutex.Unlock()
			values := make([]string, len(definition.RequiredHeaders))
			_, _ = io.WriteString(response, strings.Join(definition.RequiredHeaders, "|")+"\n"+strings.Join(values, "|")+"\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{client: client}
	requestCount := func() int {
		mutex.Lock()
		defer mutex.Unlock()
		return len(requests)
	}
	run := func(fromValue, toValue string) (fixedMemberResult, []int) {
		from, _ := ingestion.ParseCalendarDate(fromValue)
		to, _ := ingestion.ParseCalendarDate(toValue)
		plan, planErr := ingestion.BuildFixedPlan(definition, ingestion.FixedDateRangeParams{From: from, To: to}, ingestion.FrozenLocations{}, ingestion.FrozenAccountCodes{})
		if planErr != nil {
			t.Fatal(planErr)
		}
		requestOffset := requestCount()
		var staged []int
		result := executor.fetchFixedMemberSegments(context.Background(), definition, plan.Members[0], nil, func(_ context.Context, segment ingestionstore.FixedSegment) error {
			if requestCount()-requestOffset != len(staged)+1 {
				t.Fatalf("segment %d was not staged before next fetch", segment.Index)
			}
			staged = append(staged, segment.Index)
			return nil
		})
		return result, staged
	}
	multi, staged := run("2026-01-01", "2026-02-01")
	if multi.err != nil || multi.segments != 2 || multi.rows != 2 || !reflect.DeepEqual(staged, []int{0, 1}) {
		t.Fatalf("multi result=%+v staged=%v", multi, staged)
	}
	single, staged := run("2026-02-02", "2026-02-02")
	if single.err != nil || single.segments != 1 || single.rows != 1 || !reflect.DeepEqual(staged, []int{0}) {
		t.Fatalf("single result=%+v staged=%v", single, staged)
	}
}

func journalPartitionTestExecutor(t *testing.T, respond func([]string) (int, string)) (*Executor, func() []string) {
	t.Helper()
	var mutex sync.Mutex
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/admin/access/login":
			_, _ = io.WriteString(response, `{"status":"ok","data":{"result":{"sessionid":"session"}}}`)
		case "/system/laporanUmum/data/lap":
			var parameters []string
			if err := json.Unmarshal([]byte(request.URL.Query().Get("p")), &parameters); err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.URL.Query().Get("nm") != "Journal Transaction csv" || len(parameters) != 6 {
				t.Errorf("journal request name=%q parameters=%v", request.URL.Query().Get("nm"), parameters)
			}
			mutex.Lock()
			requests = append(requests, strings.Join([]string{parameters[2], parameters[3], parameters[1]}, "/"))
			mutex.Unlock()
			status, content := respond(parameters)
			response.WriteHeader(status)
			_, _ = io.WriteString(response, content)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	client, err := fincloud.NewClient(fincloud.Config{BaseURL: server.URL, Username: "user", Password: "pass", LocationID: "001", RoleID: "role", HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{client: client}, func() []string {
		mutex.Lock()
		defer mutex.Unlock()
		return append([]string(nil), requests...)
	}
}

func journalTestCSV(definition ingestion.FixedDefinition, marker string, empty, reordered bool) string {
	headers := append([]string(nil), definition.RequiredHeaders...)
	if empty {
		return strings.Join(headers, "|") + "\n"
	}
	values := make([]string, len(headers))
	for index, header := range headers {
		if header == "Journal ID" || header == "Transaction Type" {
			values[index] = marker
		}
	}
	if reordered {
		headers[0], headers[1] = headers[1], headers[0]
		values[0], values[1] = values[1], values[0]
	}
	return strings.Join(headers, "|") + "\n" + strings.Join(values, "|") + "\n"
}

func testMapperFailure(t *testing.T) error {
	t.Helper()
	_, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailSaving,
		json.RawMessage(`{"norekening":"SAFE","nocif":"SAFE","saldoawal":"not-a-decimal","saldoakhir":"1"}`), time.Now().UTC())
	var mapper *ingestion.MapperError
	if !errors.As(err, &mapper) {
		t.Fatalf("expected structured mapper error: %v", err)
	}
	return err
}
