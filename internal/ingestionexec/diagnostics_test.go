package ingestionexec

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestiondiag"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

func TestDiagnosticStorageFailureNeverReplacesPrimaryFailure(t *testing.T) {
	storageFailure := errors.New("diagnostic database unavailable")
	writer := failingTechnicalWriter{err: storageFailure}
	var logs bytes.Buffer
	recorder := newRunDiagnosticRecorder(writer, slog.New(slog.NewTextHandler(&logs, nil)), 44, "saving_detail")
	ctx := ingestiondiag.WithRecorder(context.Background(), recorder.record, 44, "saving_detail")
	primary := errors.New("Fincloud HTTP 500")
	result := failed("source", "Fincloud source operation failed", "download_report", primary)
	recordTerminalFallback(ctx, recorder, result)
	if !errors.Is(result.Cause, primary) || recorder.terminalRecorded.Load() || !strings.Contains(logs.String(), storageFailure.Error()) {
		t.Fatalf("result=%+v terminal=%v logs=%q", result, recorder.terminalRecorded.Load(), logs.String())
	}
}

func TestSourceTimeoutIsNotOperatorCancellation(t *testing.T) {
	result := sourceFailure(context.Background(), context.DeadlineExceeded, "download_report")
	if result.Status != ingestionrun.StatusFailed || result.Error.Class != "source" || result.Error.Step != "download_report" {
		t.Fatalf("result=%+v", result)
	}
}

type failingTechnicalWriter struct{ err error }

func (writer failingTechnicalWriter) AppendTechnicalEvent(context.Context, ingestionrun.TechnicalEvent) error {
	return writer.err
}

func (writer failingTechnicalWriter) AggregateTechnicalEvent(context.Context, ingestionrun.TechnicalEvent) error {
	return writer.err
}

func TestDiagnosticStdoutIsSafeSummaryOnly(t *testing.T) {
	var logs bytes.Buffer
	recorder := newRunDiagnosticRecorder(recordingTechnicalWriter{}, slog.New(slog.NewJSONHandler(&logs, nil)), 44, "saving_detail")
	recovered := false
	recorder.record(context.Background(), ingestionrun.TechnicalEvent{
		RunID: 44, JobKey: "saving_detail", Severity: "error", Terminal: true, Recovered: &recovered,
		Class: "source", Step: "fetch_detail", Operation: "fetch_saving_detail", ErrorType: "*fincloud.Error", Attempt: 2,
		ItemIdentifier: "ACCOUNT-SECRET-0042", MemberKey: "MEMBER-SECRET", ErrorMessage: "rejected ACCOUNT-SECRET-0042",
		Details: []byte(`{"source":{"request":{"query":{"id":["ACCOUNT-SECRET-0042"]}},"response":{"status_code":502,"body":{"body":"BODY-SECRET CIF-SECRET"}}}}`),
	}, false)
	line := logs.String()
	for _, secret := range []string{"ACCOUNT-SECRET", "MEMBER-SECRET", "BODY-SECRET", "CIF-SECRET"} {
		if strings.Contains(line, secret) {
			t.Fatalf("stdout leaked %s: %s", secret, line)
		}
	}
	for _, field := range []string{`"event":"ingestion.technical_diagnostic"`, `"class":"source"`, `"http_status":502`, `"terminal":true`, `"recovered":false`, `"level":"ERROR"`} {
		if !strings.Contains(line, field) {
			t.Fatalf("missing %s: %s", field, line)
		}
	}
}

type recordingTechnicalWriter struct{}

func (recordingTechnicalWriter) AppendTechnicalEvent(context.Context, ingestionrun.TechnicalEvent) error {
	return nil
}

func (recordingTechnicalWriter) AggregateTechnicalEvent(context.Context, ingestionrun.TechnicalEvent) error {
	return nil
}
