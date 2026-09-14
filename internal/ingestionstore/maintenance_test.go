package ingestionstore

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

func TestCreateMaintenanceTableIncludesConfiguredSecondaryIndex(t *testing.T) {
	definition := maintenanceDefinitionForTest(t, "eod_detail_outstanding_rekening_pinjaman")
	query, err := createMaintenanceTableSQL(ingestion.ParsedMaintenanceCSV{
		Definition: definition,
		Columns:    []ingestion.MaintenanceColumn{{OriginalHeader: "No Rekening", PhysicalName: "no_rekening", Ordinal: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "KEY `idx_eod_outstanding_no_rekening_as_of_date` (`no_rekening`(64), `as_of_date`)"
	if !strings.Contains(query, want) {
		t.Fatalf("CREATE TABLE missing %s: %s", want, query)
	}
}

func TestSecondaryIndexDefinitionMatching(t *testing.T) {
	index := ingestion.SecondaryIndex{Name: "idx_test", Columns: []ingestion.SecondaryIndexColumn{{Name: "no_rekening", PrefixLength: 64}, {Name: "as_of_date"}}}
	exact := []physicalSecondaryIndexColumn{
		{Column: sql.NullString{String: "no_rekening", Valid: true}, PrefixLength: sql.NullInt64{Int64: 64, Valid: true}, NonUnique: 1, Sequence: 1},
		{Column: sql.NullString{String: "as_of_date", Valid: true}, NonUnique: 1, Sequence: 2},
	}
	if !secondaryIndexMatches(index, exact) {
		t.Fatal("exact secondary index rejected")
	}
	wrongPrefix := append([]physicalSecondaryIndexColumn(nil), exact...)
	wrongPrefix[0].PrefixLength.Int64 = 32
	wrongOrder := append([]physicalSecondaryIndexColumn(nil), exact...)
	wrongOrder[0].Column.String, wrongOrder[1].Column.String = wrongOrder[1].Column.String, wrongOrder[0].Column.String
	wrongUnique := append([]physicalSecondaryIndexColumn(nil), exact...)
	wrongUnique[0].NonUnique, wrongUnique[1].NonUnique = 0, 0
	for name, physical := range map[string][]physicalSecondaryIndexColumn{
		"prefix": wrongPrefix,
		"order":  wrongOrder,
		"unique": wrongUnique,
	} {
		t.Run(name, func(t *testing.T) {
			if secondaryIndexMatches(index, physical) {
				t.Fatal("mismatched secondary index accepted")
			}
		})
	}
}

func maintenanceDefinitionForTest(t *testing.T, key string) ingestion.MaintenanceDefinition {
	t.Helper()
	for _, definition := range ingestion.MaintenanceDefinitions() {
		if definition.Key == key {
			return definition
		}
	}
	t.Fatalf("missing maintenance definition %s", key)
	return ingestion.MaintenanceDefinition{}
}
