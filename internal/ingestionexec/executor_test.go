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
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestiondiag"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
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
		{fixedMemberResult{layer: fixedLayerPersistence, step: "stage_fixed_member", err: errors.New("database unavailable")}, "persistence", "stage_fixed_member"},
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
