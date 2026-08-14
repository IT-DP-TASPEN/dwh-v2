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
		first, err := parametersForJob(job, from, to, 3)
		if err != nil {
			t.Fatalf("%s: %v", job.Key, err)
		}
		second, err := parametersForJob(job, from, to, 3) // direct/scheduler/Run All all use this constructor path.
		if err != nil || first.Kind != second.Kind || first.Checksum != second.Checksum || !bytes.Equal(first.JSON, second.JSON) {
			t.Fatalf("%s parameters are not deterministic", job.Key)
		}
		if err := first.Validate(job); err != nil {
			t.Fatalf("%s validate: %v", job.Key, err)
		}
		if job.DateStrategy == ingestion.NoDate && string(first.JSON) != "{}" {
			t.Fatalf("%s live detail parameters=%s", job.Key, first.JSON)
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
	if counts[ingestion.RangeCapable] != 7 || counts[ingestion.SingleDate] != 25 || counts[ingestion.NoDate] != 4 {
		t.Fatalf("date strategy counts=%v", counts)
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
