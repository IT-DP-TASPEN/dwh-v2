//go:build integration

package ingestionrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestTechnicalDiagnosticsDurabilityBoundsOrderingAndCascade(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	repository, _ := NewRepository(db, catalog)
	parameters, _ := NewLiveSnapshotExecution("saving_detail")
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,error_class,error_message,error_step,finished_at)
		VALUES ('job','saving_detail','failed',?,?,?,?, 'direct','item_data','detail failed','map_detail',UTC_TIMESTAMP(6))`,
		parameters.Kind, parameters.Version, parameters.JSON, parameters.Checksum[:])
	if err != nil {
		t.Fatal(err)
	}
	runIDValue, _ := result.LastInsertId()
	runID := uint64(runIDValue)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID) })

	base := time.Now().UTC().Add(-time.Minute)
	for index := range 5000 {
		event := technicalTestEvent(runID, base.Add(time.Duration(index)*time.Microsecond), "mapper_item", "same-group", fmt.Sprintf("item-%d", index))
		if err := repository.AggregateTechnicalEvent(context.Background(), event); err != nil {
			t.Fatalf("aggregate %d: %v", index, err)
		}
	}
	for index := 1; index < MaxTechnicalDiagnosticGroups+2; index++ {
		event := technicalTestEvent(runID, base.Add(time.Duration(6000+index)*time.Microsecond), "mapper_item", fmt.Sprintf("group-%d", index), fmt.Sprintf("other-%d", index))
		if err := repository.AggregateTechnicalEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	terminal := TechnicalEvent{RunID: runID, OccurredAt: base.Add(10 * time.Second), Severity: "error", EventKind: "failure", Terminal: true,
		Class: "item_data", Step: "map_detail", Operation: "map_detail", JobKey: "saving_detail", ErrorType: "*ingestion.MapperError",
		ErrorMessage: "one or more detail identifiers failed", Details: json.RawMessage(`{"terminal":true}`)}
	if err := repository.AppendTechnicalEvent(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}

	events, err := repository.TechnicalEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var same, overflow *TechnicalEvent
	terminalFound := false
	for index := range events {
		if index > 0 && events[index].OccurredAt.Before(events[index-1].OccurredAt) {
			t.Fatal("technical events are not chronological")
		}
		if events[index].EventKind == "overflow" {
			overflow = &events[index]
		}
		if events[index].ErrorMessage == "representative mapper failure" && events[index].OccurrenceCount == 5000 {
			same = &events[index]
		}
		terminalFound = terminalFound || events[index].Terminal
	}
	if len(events) != MaxTechnicalDiagnosticGroups+2 || same == nil || len(same.Samples) != MaxTechnicalSamples || overflow == nil || overflow.OccurrenceCount != 2 || !terminalFound {
		t.Fatalf("rows=%d same=%+v overflow=%+v terminal=%v", len(events), same, overflow, terminalFound)
	}

	const concurrent = 40
	var wait sync.WaitGroup
	errorsFound := make(chan error, concurrent)
	for index := range concurrent {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			event := technicalTestEvent(runID, base.Add(20*time.Second+time.Duration(index)*time.Microsecond), "source_item", "concurrent", fmt.Sprintf("source-%d", index))
			errorsFound <- repository.AggregateTechnicalEvent(context.Background(), event)
		}(index)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, _ = repository.TechnicalEvents(context.Background(), runID)
	var concurrentCount uint64
	for _, event := range events {
		if event.AggregationScope == "source_item" {
			concurrentCount += event.OccurrenceCount
		}
	}
	if concurrentCount != concurrent {
		t.Fatalf("concurrent count=%d", concurrentCount)
	}

	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE ingestion_runs SET error_message='rolled back business change' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	outside := terminal
	outside.OccurredAt, outside.Operation, outside.ErrorMessage = base.Add(30*time.Second), "outside_business_transaction", "diagnostic survived business rollback"
	if err := repository.AppendTechnicalEvent(context.Background(), outside); err != nil {
		t.Fatal(err)
	}

	var deleteRule string
	if err := db.Get(&deleteRule, `SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME='fk_ingestion_run_errors_run'`); err != nil || deleteRule != "CASCADE" {
		t.Fatalf("delete rule=%q error=%v", deleteRule, err)
	}
	var indexColumns string
	if err := db.Get(&indexColumns, `SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ingestion_run_errors' AND INDEX_NAME='idx_ingestion_run_errors_run_time'`); err != nil || indexColumns != "run_id,occurred_at,id" {
		t.Fatalf("index columns=%q error=%v", indexColumns, err)
	}
	if _, err := db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.Get(&remaining, `SELECT COUNT(*) FROM ingestion_run_errors WHERE run_id=?`, runID); err != nil || remaining != 0 {
		t.Fatalf("cascade remaining=%d error=%v", remaining, err)
	}
}

func technicalTestEvent(runID uint64, occurredAt time.Time, scope, group, item string) TechnicalEvent {
	return TechnicalEvent{RunID: runID, OccurredAt: occurredAt, Severity: "error", EventKind: "failure", Class: "item_data", Step: "map_detail",
		Operation: "map_detail", JobKey: "saving_detail", ItemIdentifier: item, ErrorType: "*ingestion.MapperError",
		ErrorMessage: "representative mapper failure", AggregationScope: scope, AggregationKey: TechnicalFingerprint(scope, group), Details: json.RawMessage(`{"mapper":{"field":"nominal","category":"decimal","reason":"invalid_value"}}`)}
}
