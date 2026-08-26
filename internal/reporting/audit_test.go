package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/audit"
)

func TestAuditParametersPreserveCanonicalValuesLabelsOrderAndUnset(t *testing.T) {
	parameters := []Parameter{
		{Key: "products", Label: "Products", Type: ParameterMultipleOption, DisplayOrder: 3, Options: []ParameterOption{{Value: "TAB002", Label: "Tabungan B", DisplayOrder: 2}, {Value: "TAB001", Label: "Tabungan A", DisplayOrder: 1}}},
		{Key: "branch", Label: "Branch", Type: ParameterSingleOption, DisplayOrder: 2, Options: []ParameterOption{{Value: "001", Label: "KC Jakarta", DisplayOrder: 1}}},
		{Key: "amount", Label: "Amount", Type: ParameterDecimal, DisplayOrder: 1},
		{Key: "optional", Label: "Optional", Type: ParameterText, DisplayOrder: 4},
	}
	normalized, err := NormalizeParameters(parameters, map[string]InputValue{
		"amount":   {Present: true, Values: []string{"12345678901234567890.1200"}},
		"branch":   {Present: true, Values: []string{"001"}},
		"products": {Present: true, Values: []string{"TAB002", "TAB001"}},
		"optional": {Present: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := AuditParameters(parameters, normalized)
	if !metadata.Complete || metadata.Truncated || len(metadata.Items) != 4 {
		t.Fatalf("metadata=%+v", metadata)
	}
	if got := metadata.Items[0].Values[0].Value; got != "12345678901234567890.12" {
		t.Fatalf("decimal=%q", got)
	}
	branch := metadata.Items[1].Values[0]
	if branch.Value != "001" || branch.Label != "KC Jakarta" {
		t.Fatalf("branch=%+v", branch)
	}
	products := metadata.Items[2].Values
	if len(products) != 2 || products[0].Value != "TAB001" || products[0].Label != "Tabungan A" || products[1].Value != "TAB002" {
		t.Fatalf("products=%+v", products)
	}
	if !metadata.Items[3].Unset || metadata.Items[3].OriginalCount != 0 {
		t.Fatalf("unset=%+v", metadata.Items[3])
	}
}

func TestAuditParametersCaptureResolvedDynamicLabels(t *testing.T) {
	parameter := Parameter{Key: "branch", Label: "Branch", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic}
	normalized, err := normalizeWithOptions(parameter, []string{"001"}, []OptionItem{{Value: "001", Label: "KC Jakarta"}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := AuditParameters([]Parameter{parameter}, map[string]NormalizedValue{"branch": normalized})
	if got := metadata.Items[0].Values[0]; got.Value != "001" || got.Label != "KC Jakarta" {
		t.Fatalf("dynamic option=%+v", got)
	}
}

func TestAuditParameterBoundsAreExplicitAndIncremental(t *testing.T) {
	values := make([]any, 100_000)
	labels := make(map[string]string, len(values))
	for index := range values {
		value := strings.Repeat("0", 30) + string(rune('a'+index%26))
		values[index] = value
		labels[value] = strings.Repeat("label", 20)
	}
	parameter := Parameter{Key: "many", Label: "Many", Type: ParameterMultipleOption}
	metadata := AuditParameters([]Parameter{parameter}, map[string]NormalizedValue{"many": {Multi: values, OptionLabels: labels}})
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	item := metadata.Items[0]
	if len(encoded) > auditParameterBudget || !item.Truncated || item.OriginalCount != len(values) || item.IncludedCount >= item.OriginalCount || item.OmittedCount != item.OriginalCount-item.IncludedCount {
		t.Fatalf("bytes=%d item=%+v", len(encoded), item)
	}
	if cap(item.Values) > item.IncludedCount*2+8 {
		t.Fatalf("audit builder retained oversized capacity: len=%d cap=%d", len(item.Values), cap(item.Values))
	}

	long := strings.Repeat("é", auditParameterTextLimit)
	bounded := AuditParameters([]Parameter{{Key: "text", Label: "Text", Type: ParameterText}}, map[string]NormalizedValue{"text": {Scalar: long}})
	value := bounded.Items[0].Values[0]
	if !value.ValueTruncated || value.ValueOriginalBytes != len(long) || value.ValueIncludedBytes > auditParameterTextLimit || len(value.Value) != value.ValueIncludedBytes {
		t.Fatalf("bounded value=%+v bytes=%d", value, len(value.Value))
	}
}

func TestAuditFailureClassificationNeverPersistsRawErrorOrRows(t *testing.T) {
	stage, class := safeFailure(withFailureStage(failureStageQueryExecution, errors.New("SELECT secret FROM private password=hunter2")))
	if stage != failureStageQueryExecution || class != "query_failed" {
		t.Fatalf("failure=%s/%s", stage, class)
	}
	metadata := audit.ReportExecutionMetadata{Outcome: "failed", FailureStage: stage, FailureClass: class}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hunter2", "SELECT secret", `"rows"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("unsafe execution metadata: %s", encoded)
		}
	}
	if gotStage, gotClass := safeFailure(withFailureStage(failureStageAuthorization, context.DeadlineExceeded)); gotStage != failureStageAuthorization || gotClass != "timed_out" {
		t.Fatalf("timeout=%s/%s", gotStage, gotClass)
	}
}
