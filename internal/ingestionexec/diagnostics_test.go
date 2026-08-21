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
