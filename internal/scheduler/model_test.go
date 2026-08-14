package scheduler

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

func TestPolicyCronAndRetryContracts(t *testing.T) {
	policy := PreviousCalendarDayPolicy()
	if got := hex.EncodeToString(policy.Checksum[:]); got != "26a0205ad7c897a1a5b184bd52ffe653b473513890f17dc62e4b8ebca0e72f1a" {
		t.Fatalf("policy checksum=%s", got)
	}
	spaced, err := canonicalPolicy(policy.Kind, policy.Version, []byte("{ }"))
	if err != nil || spaced.Checksum != policy.Checksum {
		t.Fatalf("semantic policy checksum changed with JSON formatting: %v", err)
	}
	_, err = canonicalPolicy(policy.Kind, 2, policy.Payload)
	if err == nil {
		t.Fatal("policy version was not part of the validated semantic envelope")
	}
	policy.Checksum[0]++
	if _, err := validatePolicy(policy); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("checksum mismatch error=%v", err)
	}

	reference := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	parsed, err := parseCron("0 1 * * *", "Asia/Jakarta", reference)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 14, 18, 0, 0, 0, time.UTC)
	if got := parsed.Next(reference); !got.Equal(want) || !parsed.IsOccurrence(got) {
		t.Fatalf("next=%s occurrence=%v", got, parsed.IsOccurrence(got))
	}
	if _, err := parseCron("@daily", "Asia/Jakarta", reference); err == nil {
		t.Fatal("descriptor cron accepted")
	}
	if _, err := parseCron("0 0 1 * * *", "Asia/Jakarta", reference); err == nil {
		t.Fatal("six-field cron accepted")
	}
	if _, err := parseCron("0 0 * * *", "+07:00", reference); err == nil {
		t.Fatal("raw fixed-offset timezone accepted")
	}
	if _, err := parseCron("0 0 * * *", "Etc/GMT-7", reference); err != nil {
		t.Fatalf("valid constant-offset IANA zone rejected: %v", err)
	}
	if _, err := parseCron("0 0 30 2 *", "Asia/Jakarta", reference); err == nil {
		t.Fatal("unsatisfiable cron accepted")
	}

	wants := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for index, want := range wants {
		if got := retryDelay(uint32(index + 1)); got != want {
			t.Fatalf("attempt %d delay=%s want=%s", index+1, got, want)
		}
	}
}

func TestOccurrenceParametersUseHistoricalJakartaBusinessDate(t *testing.T) {
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	scheduledFor := time.Date(2026, 8, 14, 18, 0, 0, 0, time.UTC) // Aug 15 01:00 Jakarta.
	tests := []struct {
		key  string
		kind ingestionrun.ParameterKind
	}{
		{"cif_opening_report", ingestionrun.FixedRangeV1},
		{"balance_sheet_report", ingestionrun.FixedDateSeriesV1},
		{"eod_cif_opening_report_full", ingestionrun.MaintenanceDateSeriesV1},
		{"cif_detail", ingestionrun.DetailLiveSnapshotV1},
	}
	for _, test := range tests {
		job, _ := catalog.Find(test.key)
		parameters, err := parametersForOccurrence(job, scheduledFor)
		if err != nil || parameters.Kind != test.kind {
			t.Fatalf("%s kind=%s error=%v", test.key, parameters.Kind, err)
		}
		switch parameters.Kind {
		case ingestionrun.FixedRangeV1:
			value, _ := ingestionrun.DecodeRange(parameters)
			if value.From.String() != "2026-08-14" || value.To != value.From {
				t.Fatalf("range=%+v", value)
			}
		case ingestionrun.FixedDateSeriesV1:
			value, _ := ingestionrun.DecodeDateSeries(parameters)
			if len(value.Dates) != 1 || value.Dates[0].String() != "2026-08-14" {
				t.Fatalf("date series=%+v", value)
			}
		case ingestionrun.MaintenanceDateSeriesV1:
			value, _ := ingestionrun.DecodeMaintenanceSeries(parameters)
			if len(value.Dates) != 1 || value.Dates[0].String() != "2026-08-14" || value.LookbackDays != 3 {
				t.Fatalf("maintenance=%+v", value)
			}
		case ingestionrun.DetailLiveSnapshotV1:
			if string(parameters.JSON) != "{}" {
				t.Fatalf("detail=%s", parameters.JSON)
			}
		}
	}
	counts := map[ingestion.DateStrategy]int{}
	for _, job := range catalog.Jobs() {
		counts[job.DateStrategy]++
		parameters, err := parametersForOccurrence(job, scheduledFor)
		if err != nil {
			t.Fatalf("%s: %v", job.Key, err)
		}
		if err := parameters.Validate(job); err != nil {
			t.Fatalf("%s scheduled parameters: %v", job.Key, err)
		}
	}
	if counts[ingestion.RangeCapable] != 7 || counts[ingestion.SingleDate] != 25 || counts[ingestion.NoDate] != 4 {
		t.Fatalf("scheduled date strategies=%v", counts)
	}
}
