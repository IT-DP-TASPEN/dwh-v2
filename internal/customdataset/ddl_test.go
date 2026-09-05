package customdataset

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPhysicalDDLAndViewContract(t *testing.T) {
	date := "YYYY-MM-DD"
	dataset := Dataset{ID: 42}
	columns := []Column{{Ordinal: 1, PhysicalName: "c001", QueryName: "name", LogicalType: TypeText}, {Ordinal: 2, PhysicalName: "c002", QueryName: "opened_on", LogicalType: TypeDate, DateFormat: &date}}
	table := createTableSQL(dataset, columns)
	for _, required := range []string{"`_row_id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY", "UNIQUE KEY `uq_import_attempt_source` (`_import_id`,`_import_attempt`,`_source_record_number`)", "`c001` LONGTEXT NULL", "`c002` DATE NULL"} {
		if !strings.Contains(table, required) {
			t.Fatalf("table DDL missing %q: %s", required, table)
		}
	}
	if strings.Count(table, "_import_id") != 2 {
		t.Fatalf("unexpected duplicate physical index: %s", table)
	}
	view := createViewSQL(dataset, columns)
	for _, required := range []string{"CREATE SQL SECURITY INVOKER VIEW `custom_dataset_view_42` AS", "d.current_generation_id=i.generation_id", "r._import_attempt=i.published_attempt", "i.status='succeeded'"} {
		if !strings.Contains(view, required) {
			t.Fatalf("view DDL missing %q: %s", required, view)
		}
	}
	if strings.Contains(view, "ALGORITHM") || strings.Contains(view, "archived") {
		t.Fatalf("view contains forbidden clause: %s", view)
	}
}

func TestPacketPreflight(t *testing.T) {
	batch := newInserter(nil, Dataset{ID: 1}, []Column{{PhysicalName: "c001"}}, Import{ID: 2, Attempt: 1}, strings.Repeat("o", 64), 100)
	err := batch.Add(context.Background(), 2, []any{strings.Repeat("x", 200)})
	var infrastructure InfrastructureError
	if !errors.As(err, &infrastructure) {
		t.Fatalf("error=%v", err)
	}
}
