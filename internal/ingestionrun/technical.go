package ingestionrun

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

const (
	MaxTechnicalDiagnosticGroups = 16
	MaxTechnicalSamples          = 5
	MaxTechnicalErrorMessage     = 2048
)

type TechnicalSample struct {
	OccurredAt     time.Time `json:"occurred_at"`
	ItemIdentifier string    `json:"item_identifier,omitempty"`
	MemberKey      string    `json:"member_key,omitempty"`
	Attempt        uint16    `json:"attempt,omitempty"`
}

type TechnicalEvent struct {
	ID, RunID                      uint64
	OccurredAt, LastOccurredAt     time.Time
	Severity, EventKind            string
	Terminal                       bool
	Recovered                      *bool
	Class, Step, Operation, JobKey string
	ItemIdentifier, MemberKey      string
	Attempt                        uint16
	ErrorType, ErrorMessage        string
	AggregationScope               string
	AggregationKey                 [32]byte
	OccurrenceCount                uint64
	Samples                        []TechnicalSample
	Details                        json.RawMessage
}

func TechnicalFingerprint(parts ...string) [32]byte {
	return sha256.Sum256([]byte(strings.Join(parts, "\x00")))
}

func (event *TechnicalEvent) normalize() error {
	if event == nil || event.RunID == 0 || strings.TrimSpace(event.JobKey) == "" || strings.TrimSpace(event.Class) == "" ||
		strings.TrimSpace(event.Step) == "" || strings.TrimSpace(event.Operation) == "" {
		return fmt.Errorf("complete technical diagnostic identity is required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	if event.LastOccurredAt.IsZero() {
		event.LastOccurredAt = event.OccurredAt
	} else {
		event.LastOccurredAt = event.LastOccurredAt.UTC()
	}
	if event.LastOccurredAt.Before(event.OccurredAt) {
		return fmt.Errorf("technical diagnostic time range is invalid")
	}
	if event.Severity != "info" && event.Severity != "warning" && event.Severity != "error" {
		return fmt.Errorf("invalid technical diagnostic severity")
	}
	if event.EventKind != "failure" && event.EventKind != "retry" && event.EventKind != "recovery" && event.EventKind != "overflow" {
		return fmt.Errorf("invalid technical diagnostic event kind")
	}
	if event.AggregationScope != "" {
		switch event.AggregationScope {
		case "mapper_item", "source_item", "persistence_retry":
		default:
			return fmt.Errorf("invalid technical diagnostic aggregation scope")
		}
		if event.AggregationKey == ([32]byte{}) {
			return fmt.Errorf("aggregated technical diagnostic key is required")
		}
	} else if event.AggregationKey != ([32]byte{}) {
		return fmt.Errorf("technical diagnostic aggregation scope is required")
	}
	event.ErrorMessage = safeTechnicalText(event.ErrorMessage, MaxTechnicalErrorMessage)
	if len(event.Details) == 0 {
		event.Details = json.RawMessage(`{}`)
	}
	if !json.Valid(event.Details) {
		return fmt.Errorf("technical diagnostic details must be valid JSON")
	}
	if event.OccurrenceCount == 0 {
		event.OccurrenceCount = 1
	}
	if len(event.Samples) == 0 && event.AggregationScope != "" {
		event.Samples = []TechnicalSample{{OccurredAt: event.OccurredAt, ItemIdentifier: event.ItemIdentifier, MemberKey: event.MemberKey, Attempt: event.Attempt}}
	}
	if len(event.Samples) > MaxTechnicalSamples {
		event.Samples = event.Samples[:MaxTechnicalSamples]
	}
	return nil
}

func safeTechnicalText(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (repository *Repository) AppendTechnicalEvent(ctx context.Context, event TechnicalEvent) error {
	if err := event.normalize(); err != nil {
		return err
	}
	return repository.insertTechnicalEvent(ctx, repository.db, event)
}

func (repository *Repository) AggregateTechnicalEvent(ctx context.Context, event TechnicalEvent) error {
	if err := event.normalize(); err != nil {
		return err
	}
	if event.AggregationScope == "" {
		return fmt.Errorf("aggregated technical diagnostic scope is required")
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID uint64
	if err := tx.GetContext(ctx, &runID, `SELECT id FROM ingestion_runs WHERE id=? FOR UPDATE`, event.RunID); err != nil {
		return err
	}
	updated, err := repository.updateTechnicalAggregate(ctx, tx, event)
	if err != nil {
		return err
	}
	if !updated {
		var groups int
		if err := tx.GetContext(ctx, &groups, `SELECT COUNT(*) FROM ingestion_run_errors
			WHERE run_id=? AND aggregation_scope=? AND event_kind<>'overflow'`, event.RunID, event.AggregationScope); err != nil {
			return err
		}
		if groups < MaxTechnicalDiagnosticGroups {
			if err := repository.insertTechnicalEvent(ctx, tx, event); err != nil {
				return err
			}
		} else {
			overflow := event
			overflow.EventKind = "overflow"
			overflow.Terminal = false
			overflow.Recovered = nil
			overflow.ErrorType = ""
			overflow.ErrorMessage = "Additional technical diagnostic groups omitted"
			overflow.AggregationKey = TechnicalFingerprint(event.AggregationScope, "overflow")
			overflow.Details, _ = json.Marshal(map[string]any{"aggregation_scope": event.AggregationScope, "group_limit": MaxTechnicalDiagnosticGroups})
			updated, err = repository.updateTechnicalAggregate(ctx, tx, overflow)
			if err != nil {
				return err
			}
			if !updated {
				if err := repository.insertTechnicalEvent(ctx, tx, overflow); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

type technicalExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (repository *Repository) insertTechnicalEvent(ctx context.Context, executor technicalExecutor, event TechnicalEvent) error {
	samples, err := json.Marshal(event.Samples)
	if err != nil {
		return err
	}
	var scope, key, sampleValue any
	if event.AggregationScope != "" {
		scope, key, sampleValue = event.AggregationScope, event.AggregationKey[:], string(samples)
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO ingestion_run_errors
		(run_id,occurred_at,last_occurred_at,severity,event_kind,terminal,recovered,class,step,operation,job_key,
		 item_identifier,member_key,attempt,error_type,error_message,aggregation_scope,aggregation_key,occurrence_count,sample_items,technical_details)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.RunID, event.OccurredAt, event.LastOccurredAt, event.Severity,
		event.EventKind, event.Terminal, event.Recovered, event.Class, event.Step, event.Operation, event.JobKey,
		nullable(event.ItemIdentifier), nullable(event.MemberKey), nullableUint16(event.Attempt), nullable(event.ErrorType), nullable(event.ErrorMessage),
		scope, key, event.OccurrenceCount, sampleValue, string(event.Details))
	return err
}

func (repository *Repository) updateTechnicalAggregate(ctx context.Context, tx *sqlx.Tx, event TechnicalEvent) (bool, error) {
	var current struct {
		ID              uint64 `db:"id"`
		OccurrenceCount uint64 `db:"occurrence_count"`
		Samples         []byte `db:"sample_items"`
	}
	err := tx.GetContext(ctx, &current, `SELECT id,occurrence_count,sample_items FROM ingestion_run_errors
		WHERE run_id=? AND aggregation_scope=? AND aggregation_key=? FOR UPDATE`, event.RunID, event.AggregationScope, event.AggregationKey[:])
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if math.MaxUint64-current.OccurrenceCount < event.OccurrenceCount {
		return false, fmt.Errorf("technical diagnostic occurrence count overflow")
	}
	var samples []TechnicalSample
	if len(current.Samples) > 0 {
		if err := json.Unmarshal(current.Samples, &samples); err != nil {
			return false, err
		}
	}
	for _, sample := range event.Samples {
		if len(samples) >= MaxTechnicalSamples {
			break
		}
		samples = append(samples, sample)
	}
	encoded, err := json.Marshal(samples)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_run_errors SET occurred_at=LEAST(occurred_at,?),last_occurred_at=GREATEST(last_occurred_at,?),
		occurrence_count=occurrence_count+?,sample_items=? WHERE id=?`, event.OccurredAt, event.LastOccurredAt, event.OccurrenceCount, string(encoded), current.ID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

func (repository *Repository) TechnicalEvents(ctx context.Context, runID uint64) ([]TechnicalEvent, error) {
	var rows []struct {
		ID               uint64         `db:"id"`
		RunID            uint64         `db:"run_id"`
		OccurredAt       time.Time      `db:"occurred_at"`
		LastOccurredAt   time.Time      `db:"last_occurred_at"`
		Severity         string         `db:"severity"`
		EventKind        string         `db:"event_kind"`
		Terminal         bool           `db:"terminal"`
		Recovered        sql.NullBool   `db:"recovered"`
		Class            string         `db:"class"`
		Step             string         `db:"step"`
		Operation        string         `db:"operation"`
		JobKey           string         `db:"job_key"`
		ItemIdentifier   sql.NullString `db:"item_identifier"`
		MemberKey        sql.NullString `db:"member_key"`
		Attempt          sql.NullInt64  `db:"attempt"`
		ErrorType        sql.NullString `db:"error_type"`
		ErrorMessage     sql.NullString `db:"error_message"`
		AggregationScope sql.NullString `db:"aggregation_scope"`
		AggregationKey   []byte         `db:"aggregation_key"`
		OccurrenceCount  uint64         `db:"occurrence_count"`
		Samples          []byte         `db:"sample_items"`
		Details          []byte         `db:"technical_details"`
	}
	if err := repository.db.SelectContext(ctx, &rows, `SELECT id,run_id,occurred_at,last_occurred_at,severity,event_kind,terminal,recovered,
		class,step,operation,job_key,item_identifier,member_key,attempt,error_type,error_message,aggregation_scope,aggregation_key,
		occurrence_count,sample_items,technical_details FROM ingestion_run_errors WHERE run_id=? ORDER BY occurred_at,id`, runID); err != nil {
		return nil, err
	}
	events := make([]TechnicalEvent, len(rows))
	for index, found := range rows {
		event := TechnicalEvent{ID: found.ID, RunID: found.RunID, OccurredAt: found.OccurredAt, LastOccurredAt: found.LastOccurredAt,
			Severity: found.Severity, EventKind: found.EventKind, Terminal: found.Terminal, Class: found.Class, Step: found.Step,
			Operation: found.Operation, JobKey: found.JobKey, ItemIdentifier: found.ItemIdentifier.String, MemberKey: found.MemberKey.String,
			Attempt: uint16(found.Attempt.Int64), ErrorType: found.ErrorType.String, ErrorMessage: found.ErrorMessage.String,
			AggregationScope: found.AggregationScope.String, OccurrenceCount: found.OccurrenceCount, Details: append(json.RawMessage(nil), found.Details...)}
		if found.Recovered.Valid {
			value := found.Recovered.Bool
			event.Recovered = &value
		}
		copy(event.AggregationKey[:], found.AggregationKey)
		if len(found.Samples) > 0 {
			if err := json.Unmarshal(found.Samples, &event.Samples); err != nil {
				return nil, fmt.Errorf("decode technical diagnostic samples: %w", err)
			}
		}
		events[index] = event
	}
	return events, nil
}

func nullableUint16(value uint16) any {
	if value == 0 {
		return nil
	}
	return value
}
