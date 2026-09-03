package ingestionrun

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

func TestCanonicalParametersCoverAllJobDateContracts(t *testing.T) {
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	from, _ := ingestion.ParseCalendarDate("2026-06-01")
	to, _ := ingestion.ParseCalendarDate("2026-06-03")
	counts := map[ingestion.DateStrategy]int{}
	for _, job := range catalog.Jobs() {
		counts[job.DateStrategy]++
		first, err := parametersForJob(job, from, to)
		if err != nil {
			t.Fatalf("%s: %v", job.Key, err)
		}
		second, err := parametersForJob(job, from, to) // direct/scheduler/Run All all use this constructor path.
		if err != nil || first.Kind != second.Kind || first.Checksum != second.Checksum || !bytes.Equal(first.JSON, second.JSON) {
			t.Fatalf("%s parameters are not deterministic", job.Key)
		}
		if err := first.Validate(job); err != nil {
			t.Fatalf("%s validate: %v", job.Key, err)
		}
		if job.DateStrategy == ingestion.NoDate && string(first.JSON) != "{}" {
			t.Fatalf("%s live-snapshot parameters=%s", job.Key, first.JSON)
		}
		if job.DateStrategy == ingestion.SingleDate {
			var dates []ingestion.CalendarDate
			if job.Category == ingestion.CategoryFixed {
				value, _ := DecodeDateSeries(first)
				dates = value.Dates
			} else {
				value, _ := DecodeMaintenanceSeries(first)
				dates = value.Dates
			}
			want := []string{"2026-06-01", "2026-06-02", "2026-06-03"}
			got := make([]string, len(dates))
			for index := range dates {
				got[index] = dates[index].String()
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s dates=%v", job.Key, got)
			}
		}
	}
	if counts[ingestion.RangeCapable] != 7 || counts[ingestion.SingleDate] != 25 || counts[ingestion.NoDate] != 9 {
		t.Fatalf("date strategy counts=%v", counts)
	}
}

func TestLiveSnapshotWriterIsCanonicalAndLegacyReaderRemainsCompatible(t *testing.T) {
	catalog, _ := ingestion.NewCatalog()
	for _, key := range []string{"cif_detail", "cif_reference_master", "marketing_master"} {
		job, _ := catalog.Find(key)
		parameters, err := NewLiveSnapshotExecution(key)
		if err != nil || parameters.Kind != LiveSnapshotV1 || parameters.Validate(job) != nil {
			t.Fatalf("%s canonical parameters=%+v error=%v", key, parameters, err)
		}
		legacy, _ := encode(DetailLiveSnapshotV1, struct{}{})
		if err := legacy.Validate(job); err != nil {
			t.Fatalf("%s legacy compatibility: %v", key, err)
		}
	}
}

func TestOwnerIdentityIsOpaqueAndUnique(t *testing.T) {
	first, err := NewOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewOwnerID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 64 || len(second) != 64 {
		t.Fatalf("owner identities first=%q second=%q", first, second)
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("owner identity is not lowercase hex: %v", err)
	}
}

func TestValidateReencodesTypedParametersBeforeChecksum(t *testing.T) {
	catalog, _ := ingestion.NewCatalog()
	from, _ := ingestion.ParseCalendarDate("2026-06-01")
	to, _ := ingestion.ParseCalendarDate("2026-06-03")
	tests := []struct {
		job  string
		make func() (Parameters, error)
		json string
	}{
		{"cif_opening_report", func() (Parameters, error) { return NewRangeExecution("cif_opening_report", from, to) }, `{ "to": "2026-06-03", "from": "2026-06-01" }`},
		{"balance_sheet_report", func() (Parameters, error) { return NewDateSeriesExecution("balance_sheet_report", from, to) }, `{ "dates": [ "2026-06-01", "2026-06-02", "2026-06-03" ] }`},
		{"eod_cif_opening_report_full", func() (Parameters, error) {
			return NewMaintenanceSeriesExecution("eod_cif_opening_report_full", from, to)
		}, `{ "dates": ["2026-06-01","2026-06-02","2026-06-03"] }`},
		{"saving_detail", func() (Parameters, error) { return NewLiveSnapshotExecution("saving_detail") }, `{ }`},
	}
	for _, test := range tests {
		parameters, err := test.make()
		if err != nil {
			t.Fatal(err)
		}
		parameters.JSON = []byte(test.json)
		job, _ := catalog.Find(test.job)
		if err := parameters.Validate(job); err != nil {
			t.Fatalf("%s reformatted parameters rejected: %v", test.job, err)
		}
	}
}
