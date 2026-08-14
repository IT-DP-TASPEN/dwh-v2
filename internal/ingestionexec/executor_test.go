package ingestionexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

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
	result := detailFatalFailure(outcome.fatalLayer, outcome.fatal)
	if result.Error.Class != "source" || result.Error.Step != "fetch_detail" {
		t.Fatalf("source result=%+v", result)
	}

	result = detailFatalFailure(detailLayerPersist, errors.New("database rejected snapshot"))
	if result.Error.Class != "persistence" || result.Error.Step != "persist_detail" {
		t.Fatalf("business persistence result=%+v", result)
	}
	outcome = runDetailPool(context.Background(), []string{"one"}, 1, func(_ context.Context, _ string) detailItemResult {
		return detailItemResult{rows: 1}
	}, func(ingestionrun.Progress, *ingestionrun.MapperDiagnostics) error {
		return errors.New("run progress unavailable")
	})
	result = detailFatalFailure(outcome.fatalLayer, outcome.fatal)
	if result.Error.Class != "persistence" || result.Error.Step != "persist_run_progress" {
		t.Fatalf("run-state persistence result=%+v", result)
	}
}

func TestFetchAndSaveDetailMarksBusinessPersistenceFailure(t *testing.T) {
	item := detailPersistenceFailure(errors.New("database rejected snapshot"))
	if item.err == nil || item.layer != detailLayerPersist {
		t.Fatalf("business persistence failure=%+v", item)
	}
	result := detailFatalFailure(item.layer, item.err)
	if result.Error.Class != "persistence" || result.Error.Step != "persist_detail" {
		t.Fatalf("business persistence result=%+v", result)
	}
}

func testMapperFailure(t *testing.T) error {
	t.Helper()
	date, _ := ingestion.ParseCalendarDate("2026-08-14")
	_, err := ingestion.MapDetailPayload(context.Background(), ingestion.DetailSaving,
		json.RawMessage(`{"norekening":"SAFE","nocif":"SAFE","saldoawal":"not-a-decimal","saldoakhir":"1"}`), date, time.Now().UTC())
	var mapper *ingestion.MapperError
	if !errors.As(err, &mapper) {
		t.Fatalf("expected structured mapper error: %v", err)
	}
	return err
}
