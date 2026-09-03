package sources

import (
	"testing"

	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

func TestManualParameterContractsCoverCatalog(t *testing.T) {
	catalog, err := core.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[core.DateStrategy]int{}
	for _, job := range catalog.Jobs() {
		parameters, validation := Parameters(job, "2026-06-01", "2026-06-03")
		if len(validation) != 0 {
			t.Fatalf("%s validation=%v", job.Key, validation)
		}
		counts[job.DateStrategy]++
		switch job.DateStrategy {
		case core.RangeCapable:
			if parameters.Kind != ingestionrun.FixedRangeV1 {
				t.Fatalf("%s kind=%s", job.Key, parameters.Kind)
			}
		case core.NoDate:
			if parameters.Kind != ingestionrun.LiveSnapshotV1 || string(parameters.JSON) != "{}" {
				t.Fatalf("%s live parameters=%s %s", job.Key, parameters.Kind, parameters.JSON)
			}
		case core.SingleDate:
			want := ingestionrun.MaintenanceDateSeriesV2
			if job.Category == core.CategoryFixed {
				want = ingestionrun.FixedDateSeriesV1
			}
			if parameters.Kind != want {
				t.Fatalf("%s kind=%s want=%s", job.Key, parameters.Kind, want)
			}
		}
	}
	if counts[core.RangeCapable] != 7 || counts[core.SingleDate] != 25 || counts[core.NoDate] != 9 {
		t.Fatalf("date strategy counts=%v", counts)
	}
}

func TestManualParametersRejectInvalidRange(t *testing.T) {
	catalog, _ := core.NewCatalog()
	job, _ := catalog.Find("balance_sheet_report")
	_, validation := Parameters(job, "2026-06-03", "2026-06-01")
	if validation["form"] == "" {
		t.Fatal("reverse range accepted")
	}
}
