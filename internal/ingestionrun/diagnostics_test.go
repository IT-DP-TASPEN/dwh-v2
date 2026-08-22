package ingestionrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

func TestMapperDiagnosticsBoundsCountsAndSorts(t *testing.T) {
	first := savingMapperError(t, "secret-invalid-decimal", "1").Metadata()
	groups := []MapperDiagnosticGroup{{
		Class: first.Class(), Field: first.Field(), Category: first.Category(), Reason: first.Reason(), SafeMessage: first.SafeMessage(), Count: 1,
	}}
	for _, field := range []string{
		"account_name", "location_id", "blocked_balance", "debit_mutation", "credit_mutation",
		"open_date", "closed_date", "customer_type", "identity_type", "ktp_no", "birth_date",
		"cif_open_date", "record_created_at", "mutasideposito.nominal", "jadwalangsuran.installment_no",
	} {
		groups = append(groups, MapperDiagnosticGroup{Class: "detail_mapping", Field: field, Category: "string", Reason: "invalid_value", SafeMessage: "detail field value is invalid", Count: 1})
	}
	diagnostics := MapperDiagnostics{TotalCount: 16, Groups: groups}
	if err := diagnostics.Add(first); err != nil {
		t.Fatal(err)
	}
	retainedCount := uint64(0)
	for _, group := range diagnostics.Groups {
		if group.Field == first.Field() && group.Category == first.Category() {
			retainedCount = group.Count
		}
	}
	if diagnostics.TotalCount != 17 || diagnostics.OverflowCount != 0 || retainedCount != 2 {
		t.Fatalf("existing group after capacity: %+v", diagnostics)
	}
	newGroup := savingMapperError(t, "1", "another-secret-invalid-decimal").Metadata()
	if err := diagnostics.Add(newGroup); err != nil {
		t.Fatal(err)
	}
	if diagnostics.TotalCount != 18 || diagnostics.OverflowCount != 1 || len(diagnostics.Groups) != MaxMapperDiagnosticGroups {
		t.Fatalf("overflow accounting: %+v", diagnostics)
	}
	data, err := diagnostics.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded MapperDiagnostics
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.TotalCount != 18 {
		t.Fatalf("serialized diagnostics=%s error=%v", data, err)
	}
	for index := 1; index < len(decoded.Groups); index++ {
		left, right := decoded.Groups[index-1], decoded.Groups[index]
		if left.Field > right.Field {
			t.Fatalf("groups are not canonically sorted: %s", data)
		}
	}
}

func TestMapperDiagnosticsNeverSerializeWrappedCauseOrSourceValues(t *testing.T) {
	const sentinel = "account-customer-raw-sentinel-991122"
	mapper := savingMapperError(t, sentinel, "1")
	if cause := errors.Unwrap(mapper); cause == nil || !strings.Contains(cause.Error(), sentinel) {
		t.Fatalf("test cause does not contain sentinel: %v", cause)
	}
	var diagnostics MapperDiagnostics
	if err := diagnostics.Add(mapper.Metadata()); err != nil {
		t.Fatal(err)
	}
	data, err := diagnostics.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{sentinel, "SAFE-ACCOUNT", "SAFE-CUSTOMER"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("mapper diagnostics leaked %q: %s", forbidden, data)
		}
	}
}

func savingMapperError(t *testing.T, beginning, balance string) *ingestion.MapperError {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"norekening": "SAFE-ACCOUNT", "nocif": "SAFE-CUSTOMER", "saldoawal": beginning, "saldoakhir": balance,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingestion.MapDetailPayload(context.Background(), ingestion.DetailSaving, payload, time.Now().UTC())
	var mapper *ingestion.MapperError
	if !errors.As(err, &mapper) {
		t.Fatalf("error is not a structured mapper error: %v", err)
	}
	return mapper
}
