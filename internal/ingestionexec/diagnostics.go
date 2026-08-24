package ingestionexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestiondiag"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/ingestionstore"
)

const diagnosticWriteTimeout = 5 * time.Second

type runDiagnosticRecorder struct {
	runs             technicalWriter
	logger           *slog.Logger
	runID            uint64
	jobKey           string
	terminalRecorded atomic.Bool
}

type technicalWriter interface {
	AppendTechnicalEvent(context.Context, ingestionrun.TechnicalEvent) error
	AggregateTechnicalEvent(context.Context, ingestionrun.TechnicalEvent) error
}

func newRunDiagnosticRecorder(runs technicalWriter, logger *slog.Logger, runID uint64, jobKey string) *runDiagnosticRecorder {
	return &runDiagnosticRecorder{runs: runs, logger: logger, runID: runID, jobKey: jobKey}
}

func (recorder *runDiagnosticRecorder) record(ctx context.Context, event ingestionrun.TechnicalEvent, aggregate bool) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diagnosticWriteTimeout)
	defer cancel()
	var err error
	if aggregate {
		err = recorder.runs.AggregateTechnicalEvent(writeCtx, event)
	} else {
		err = recorder.runs.AppendTechnicalEvent(writeCtx, event)
	}
	if err != nil {
		recorder.logger.Error("could not persist ingestion technical diagnostic", "run_id", recorder.runID, "job_key", recorder.jobKey,
			"class", event.Class, "step", event.Step, "operation", event.Operation, "diagnostic_error", err)
		return
	}
	if event.Terminal {
		recorder.terminalRecorded.Store(true)
	}
	attributes := []any{
		"run_id", recorder.runID, "job_key", recorder.jobKey, "class", event.Class, "step", event.Step,
		"operation", event.Operation, "error_type", event.ErrorType, "attempt", event.Attempt,
	}
	var compact struct {
		Source struct {
			Response *struct {
				StatusCode int `json:"status_code"`
			} `json:"response"`
		} `json:"source"`
		Database ingestionstore.DatabaseDiagnostic `json:"database"`
	}
	if json.Unmarshal(event.Details, &compact) == nil {
		if compact.Source.Response != nil {
			attributes = append(attributes, "http_status", compact.Source.Response.StatusCode)
		}
		if compact.Database.Operation != "" || compact.Database.MySQLNumber != 0 {
			attributes = append(attributes, "table", compact.Database.Table, "mysql_error", compact.Database.MySQLNumber,
				"sqlstate", compact.Database.SQLState, "tx_attempt", compact.Database.TxAttempt)
		}
	}
	recorder.logger.Log(ctx, diagnosticLogLevel(event.Severity), "ingestion technical diagnostic", attributes...)
}

func diagnosticLogLevel(severity string) slog.Level {
	if severity == "error" {
		return slog.LevelError
	}
	if severity == "warning" {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

func diagnosticScope(ctx context.Context, class, step, operation, item, member string) context.Context {
	return ingestiondiag.WithScope(ctx, ingestiondiag.Scope{Class: class, Step: step, Operation: operation,
		ItemIdentifier: item, MemberKey: member})
}

func recordTerminalFallback(ctx context.Context, recorder *runDiagnosticRecorder, result Result) {
	if result.Status != ingestionrun.StatusFailed || result.Cause == nil || recorder.terminalRecorded.Load() {
		return
	}
	ctx = diagnosticScope(ctx, result.Error.Class, result.Error.Step, result.Error.Step, "", "")
	switch result.Error.Class {
	case "source":
		recordSourceDiagnostic(ctx, result.Cause, true, false)
	case "persistence":
		recordPersistenceDiagnostic(ctx, result.Cause, true)
	case "source_contract":
		recordParserDiagnostic(ctx, result.Cause, true)
	case "item_data":
		recordMapperDiagnostic(ctx, result.Cause, "", true)
	default:
		recordGenericDiagnostic(ctx, result.Cause, true)
	}
}

func baseTechnicalEvent(ctx context.Context, err error, terminal bool) ingestionrun.TechnicalEvent {
	event := ingestiondiag.Event(ctx)
	event.Severity, event.EventKind, event.Terminal = "error", "failure", terminal
	event.ErrorType, event.ErrorMessage = fmt.Sprintf("%T", err), boundedError(err)
	event.Details = marshalDetails(map[string]any{"error_chain": safeErrorChain(err, true)})
	return event
}

func recordSourceDiagnostic(ctx context.Context, err error, terminal, aggregate bool) {
	event := baseTechnicalEvent(ctx, err, terminal)
	event.Class = "source"
	details := map[string]any{"error_chain": safeErrorChain(err, true)}
	if diagnostic, ok := fincloud.TechnicalDiagnostic(err); ok {
		details["source"] = diagnostic
	}
	var sourceError *fincloud.Error
	if errors.As(err, &sourceError) {
		event.Operation = canonicalOperation(sourceError.Operation, event.Operation)
	}
	event.Details = marshalDetails(details)
	if aggregate {
		parts := []string{event.Operation, fincloud.SafeCauseClass(err)}
		if diagnostic, ok := fincloud.TechnicalDiagnostic(err); ok {
			parts = append(parts, diagnostic.FailureKind)
			if diagnostic.Response != nil {
				bodyHash := sha256.Sum256([]byte(diagnostic.Response.Body.Body))
				parts = append(parts, fmt.Sprint(diagnostic.Response.StatusCode), diagnostic.Application.Status, hex.EncodeToString(bodyHash[:]))
			}
		}
		event.Terminal = false
		event.AggregationScope = "source_item"
		event.AggregationKey = ingestionrun.TechnicalFingerprint(parts...)
	}
	ingestiondiag.Record(ctx, event, aggregate)
}

func recordPersistenceDiagnostic(ctx context.Context, err error, terminal bool) {
	event := baseTechnicalEvent(ctx, err, terminal)
	event.Class = "persistence"
	diagnostic := ingestionstore.TechnicalDiagnostic(err)
	if diagnostic.Operation != "" {
		event.Operation = diagnostic.Operation
	}
	if diagnostic.TxAttempt > 0 {
		event.Attempt = uint16(diagnostic.TxAttempt)
	}
	if diagnostic.Message != "" {
		event.ErrorMessage = diagnostic.Message
	}
	recovered := false
	event.Recovered = &recovered
	event.Details = marshalDetails(map[string]any{"database": diagnostic, "error_chain": safeErrorChain(err, true)})
	ingestiondiag.Record(ctx, event, false)
}

func recordParserDiagnostic(ctx context.Context, err error, terminal bool) {
	event := baseTechnicalEvent(ctx, err, terminal)
	event.Class = "source_contract"
	var header *ingestion.FixedHeaderError
	if errors.As(err, &header) {
		event.Details = marshalDetails(map[string]any{"parser": map[string]any{
			"report": header.Report, "kind": header.Kind, "column": header.Column, "expected": header.Expected,
			"received_raw": header.ReceivedRaw, "received_normalized": header.ReceivedNormalized,
		}, "error_chain": safeErrorChain(err, true)})
	}
	ingestiondiag.Record(ctx, event, false)
}

func recordFixedMemberDiagnostic(ctx context.Context, result fixedMemberResult, primary bool) {
	switch result.layer {
	case fixedLayerSource:
		sourceCtx := diagnosticScope(ctx, "source", result.step, result.step, "", result.memberKey)
		recordSourceDiagnostic(sourceCtx, result.err, primary, !primary)
	case fixedLayerSourceContract:
		parserCtx := diagnosticScope(ctx, "source_contract", result.step, result.step, "", result.memberKey)
		recordParserDiagnostic(parserCtx, result.err, primary)
	case fixedLayerPersistence:
		persistCtx := diagnosticScope(ctx, "persistence", result.step, result.step, "", result.memberKey)
		recordPersistenceDiagnostic(persistCtx, result.err, primary)
	case fixedLayerContract:
		contractCtx := diagnosticScope(ctx, "contract", result.step, result.step, "", result.memberKey)
		recordGenericDiagnostic(contractCtx, result.err, primary)
	}
}

func recordDetailFatalDiagnostic(ctx context.Context, result detailItemResult, primary bool) {
	switch result.layer {
	case detailLayerFetch:
		sourceCtx := diagnosticScope(ctx, "source", "fetch_detail", "fetch_detail", result.identifier, "")
		recordSourceDiagnostic(sourceCtx, result.err, primary, !primary)
	case detailLayerPersist:
		persistCtx := diagnosticScope(ctx, "persistence", "persist_detail", "persist_detail", result.identifier, "")
		recordPersistenceDiagnostic(persistCtx, result.err, primary)
	case detailLayerRunProgress:
		persistCtx := diagnosticScope(ctx, "persistence", "persist_run_progress", "persist_run_progress", result.identifier, "")
		recordPersistenceDiagnostic(persistCtx, result.err, primary)
	}
}

func recordMapperDiagnostic(ctx context.Context, err error, item string, terminal bool) {
	event := baseTechnicalEvent(ctx, err, terminal)
	event.Class, event.ItemIdentifier = "item_data", item
	var mapper *ingestion.MapperError
	if !errors.As(err, &mapper) {
		ingestiondiag.Record(ctx, event, false)
		return
	}
	metadata := mapper.Metadata()
	event.ErrorMessage = metadata.SafeMessage()
	event.Details = marshalDetails(map[string]any{"mapper": map[string]string{
		"class": metadata.Class(), "field": metadata.Field(), "source_path": metadata.SourcePath(), "category": metadata.Category(),
		"reason": metadata.Reason(), "safe_message": metadata.SafeMessage(),
	}, "error_chain": safeErrorChain(err, false)})
	if !terminal {
		event.AggregationScope = "mapper_item"
		event.AggregationKey = ingestionrun.TechnicalFingerprint(metadata.Field(), metadata.Category(), metadata.Reason(), metadata.SafeMessage())
	}
	ingestiondiag.Record(ctx, event, !terminal)
}

func recordGenericDiagnostic(ctx context.Context, err error, terminal bool) {
	event := baseTechnicalEvent(ctx, err, terminal)
	ingestiondiag.Record(ctx, event, false)
}

func canonicalOperation(value, fallback string) string {
	value = strings.Trim(strings.ToLower(value), " _-")
	if value == "" {
		return fallback
	}
	return strings.NewReplacer(" ", "_", "-", "_").Replace(value)
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > ingestionrun.MaxTechnicalErrorMessage {
		message = message[:ingestionrun.MaxTechnicalErrorMessage]
	}
	return message
}

func safeErrorChain(err error, messages bool) []map[string]string {
	chain := make([]map[string]string, 0, 4)
	queue := []error{err}
	for len(queue) > 0 && len(chain) < 8 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		entry := map[string]string{"type": fmt.Sprintf("%T", current)}
		if messages {
			entry["message"] = boundedError(current)
		}
		chain = append(chain, entry)
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			queue = append(joined.Unwrap(), queue...)
		} else if cause := errors.Unwrap(current); cause != nil {
			queue = append([]error{cause}, queue...)
		}
	}
	return chain
}

func marshalDetails(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
