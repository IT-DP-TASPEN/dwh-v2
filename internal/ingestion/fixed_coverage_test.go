package ingestion

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func coverageTestDate(t *testing.T, value string) CalendarDate {
	t.Helper()
	date, err := ParseCalendarDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return date
}

func TestFixedCanonicalPublicationAndSourceModes(t *testing.T) {
	catalog, err := NewCatalog()
	if err != nil || len(catalog.Jobs()) != 41 {
		t.Fatalf("catalog jobs=%d error=%v", len(catalog.Jobs()), err)
	}
	definitions := FixedDefinitions()
	if len(definitions) != 8 {
		t.Fatalf("fixed jobs=%d", len(definitions))
	}
	from, to := coverageTestDate(t, "2026-08-01"), coverageTestDate(t, "2026-09-30")
	for index, definition := range definitions {
		t.Run(definition.Key, func(t *testing.T) {
			wantPublication, wantSource, wantDays := DateAddressable, BoundedDateChunks, 30
			if definition.Key == "profit_loss_statement" {
				wantPublication, wantSource, wantDays = IntervalResult, ExactInterval, 0
			}
			if definition.PublicationMode != wantPublication || definition.SourceRequestMode != wantSource || definition.MaxChunkDays != wantDays {
				t.Fatalf("definition=%+v", definition)
			}
			chunks, err := FixedSourceChunks(definition, from, to)
			if definition.SnapshotDate {
				if err == nil {
					t.Fatal("snapshot accepted a range source request")
				}
				chunks, err = FixedSourceChunks(definition, to, to)
				if err != nil || !reflect.DeepEqual(chunks, []DateChunk{{From: to, To: to}}) {
					t.Fatalf("snapshot chunks=%v error=%v", chunks, err)
				}
			} else {
				want := []DateChunk{{From: from, To: coverageTestDate(t, "2026-08-30")},
					{From: coverageTestDate(t, "2026-08-31"), To: coverageTestDate(t, "2026-09-29")}, {From: to, To: to}}
				if definition.SourceRequestMode == ExactInterval {
					want = []DateChunk{{From: from, To: to}}
				}
				if err != nil || !reflect.DeepEqual(chunks, want) {
					t.Fatalf("source chunks=%v want=%v error=%v", chunks, want, err)
				}
			}
			for _, mutate := range []func(*FixedDefinition){
				func(d *FixedDefinition) { d.PublicationMode = "wrong" },
				func(d *FixedDefinition) { d.SourceRequestMode = "wrong" },
				func(d *FixedDefinition) { d.MaxChunkDays++ },
				func(d *FixedDefinition) { d.CoverageDateHeader = "wrong" },
				func(d *FixedDefinition) { d.CoverageDateLayout = "wrong" },
				func(d *FixedDefinition) { d.SnapshotDate = !d.SnapshotDate },
			} {
				changed := append([]FixedDefinition(nil), definitions...)
				mutate(&changed[index])
				if validateFixedDefinitions(changed) == nil {
					t.Fatal("canonical semantic mutation accepted")
				}
			}
		})
	}
}

func TestFixedSourceDateCoverageContracts(t *testing.T) {
	from, to := coverageTestDate(t, "2026-08-01"), coverageTestDate(t, "2026-08-30")
	for _, definition := range FixedDefinitions() {
		if definition.PublicationMode != DateAddressable || definition.SnapshotDate {
			continue
		}
		t.Run(definition.Key, func(t *testing.T) {
			format := func(date string) string {
				switch definition.Key {
				case "vault_mutation_report":
					return date[:4] + " " + date[4:] + "00:15:00"
				case "teller_mutation_report":
					return date + " 00:15:00"
				default:
					return date
				}
			}
			cases := []struct {
				name, raw string
				valid     bool
			}{
				{"valid", format("2026-08-12"), true},
				{"first", format(from.String()), true},
				{"last", format(to.String()), true},
				{"blank", "", false},
				{"invalid", format("2026-02-30"), false},
				{"before_segment", format("2026-07-31"), false},
				{"after_segment", format("2026-08-31"), false},
				{"leading_space", " " + format("2026-08-12"), false},
				{"trailing_space", format("2026-08-12") + " ", false},
			}
			for _, item := range cases {
				t.Run(item.name, func(t *testing.T) {
					values := map[string]string{definition.CoverageDateHeader: item.raw, "unrelated": " preserve raw text "}
					rows := []FixedCSVRow{{SourceRowNumber: 7, SourceRowChecksum: "checksum", Values: values}}
					err := ValidateFixedCoverage(definition, from, to, rows)
					if (err == nil) != item.valid {
						t.Fatalf("valid=%t error=%v", item.valid, err)
					}
					if rows[0].Values[definition.CoverageDateHeader] != item.raw || rows[0].Values["unrelated"] != " preserve raw text " || rows[0].SourceRowChecksum != "checksum" {
						t.Fatal("coverage validation changed source text/checksum")
					}
					if item.valid && rows[0].CoverageDate.IsZero() {
						t.Fatal("valid row has no coverage date")
					}
					if !item.valid && !rows[0].CoverageDate.IsZero() {
						t.Fatal("invalid row assigned coverage")
					}
				})
			}
			rows := []FixedCSVRow{{SourceRowNumber: 2, Values: map[string]string{definition.CoverageDateHeader: format("2026-08-12")}},
				{SourceRowNumber: 3, Values: map[string]string{definition.CoverageDateHeader: ""}}}
			if ValidateFixedCoverage(definition, from, to, rows) == nil || len(rows) != 2 || !rows[0].CoverageDate.IsZero() || !rows[1].CoverageDate.IsZero() {
				t.Fatal("invalid segment was partially normalized or silently discarded")
			}
			if err := ValidateFixedCoverage(definition, from, to, nil); err != nil {
				t.Fatalf("authoritative empty segment rejected: %v", err)
			}
		})
	}
}

func TestFixedDatetimeSpecificFormatsAndCalendarDate(t *testing.T) {
	for _, definition := range FixedDefinitions() {
		var valid string
		var invalid []string
		switch definition.Key {
		case "vault_mutation_report":
			valid = "2026 -08-1200:15:00"
			invalid = []string{"2026-08-12 00:15:00", "2026-08-12T00:15:00Z", "2026 -08-1224:00:00", "2026 -08-1200:15:00+07:00"}
		case "teller_mutation_report":
			valid = "2026-08-12 00:15:00"
			invalid = []string{"2026-08-12", "2026-08-12T00:15:00", "2026-08-12 24:00:00", "2026-08-12 00:15:00+07:00"}
		default:
			continue
		}
		date, err := ParseFixedCoverageDate(definition, valid, CalendarDate{})
		if err != nil || date.String() != "2026-08-12" {
			t.Fatalf("%s source calendar date converted: date=%s error=%v", definition.Key, date, err)
		}
		for _, raw := range invalid {
			if _, err := ParseFixedCoverageDate(definition, raw, CalendarDate{}); err == nil {
				t.Fatalf("%s unsupported datetime accepted: %q", definition.Key, raw)
			}
		}
	}
}

func TestFixedSnapshotAndIntervalCoverage(t *testing.T) {
	date := coverageTestDate(t, "2026-08-12")
	for _, definition := range FixedDefinitions() {
		if definition.SnapshotDate {
			rows, err := ParseFixedCSV(context.Background(), definition, "008", strings.Join(definition.RequiredHeaders, "|")+"\n"+strings.Repeat("|", len(definition.RequiredHeaders)-1)+"\n")
			if err != nil || len(rows) != 1 {
				t.Fatalf("snapshot CSV rows=%v error=%v", rows, err)
			}
			if err := ValidateFixedCoverage(definition, date, date, rows); err != nil || rows[0].CoverageDate != date || rows[0].SourceLocationID != "008" {
				t.Fatalf("snapshot coverage rows=%v error=%v", rows, err)
			}
			if ValidateFixedCoverage(definition, date, date.AddDays(1), rows) == nil {
				t.Fatal("snapshot range accepted")
			}
		}
		if definition.PublicationMode == IntervalResult {
			rows := []FixedCSVRow{{Values: map[string]string{"CoA No": "100"}}}
			if err := ValidateFixedCoverage(definition, date, date.AddDays(60), rows); err != nil || !rows[0].CoverageDate.IsZero() {
				t.Fatalf("interval assigned business date: rows=%v error=%v", rows, err)
			}
			if _, err := ParseFixedCoverageDate(definition, date.String(), date); err == nil {
				t.Fatal("P&L accepted a fabricated source date")
			}
			rows[0].CoverageDate = date
			if ValidateFixedCoverage(definition, date, date.AddDays(60), rows) == nil {
				t.Fatal("P&L accepted a coverage date")
			}
		}
	}
}

func TestFixedSourceChunkCalendarUpperBound(t *testing.T) {
	from, to := coverageTestDate(t, "9999-12-01"), coverageTestDate(t, "9999-12-31")
	chunks, err := ChunkDateRange(from, to, 30)
	want := []DateChunk{{From: from, To: from.AddDays(29)}, {From: to, To: to}}
	if err != nil || !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks=%v want=%v error=%v", chunks, want, err)
	}
}
