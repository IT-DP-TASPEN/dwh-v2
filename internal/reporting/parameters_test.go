package reporting

import (
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

func TestNormalizeBindAndRawDriverConversion(t *testing.T) {
	parameters := []Parameter{
		{Key: "when", Label: "When", Type: ParameterDatetime, Required: true, DisplayOrder: 1},
		{Key: "amount", Label: "Amount", Type: ParameterDecimal, Required: true, DisplayOrder: 2},
		{Key: "ids", Label: "IDs", Type: ParameterMultipleOption, DisplayOrder: 3, Options: []ParameterOption{{Value: "b", Label: "B", DisplayOrder: 2}, {Value: "a", Label: "A", DisplayOrder: 1}}},
	}
	input := map[string]InputValue{
		"when":   {Present: true, Values: []string{"2026-08-24T14:30"}},
		"amount": {Present: true, Values: []string{"12345678901234567890.1200"}},
		"ids":    {Present: true, Values: []string{"b", "a"}},
	}
	normalized, err := NormalizeParameters(parameters, input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized["when"].Scalar != "2026-08-24 14:30:00" {
		t.Fatalf("datetime=%v", normalized["when"].Scalar)
	}
	if normalized["amount"].Scalar != "12345678901234567890.12" {
		t.Fatalf("decimal=%v", normalized["amount"].Scalar)
	}
	query, arguments, err := Bind(`SELECT :when,:amount WHERE id IN (:ids) OR :ids__count=0`, parameters, normalized, SQLMode{})
	if err != nil {
		t.Fatal(err)
	}
	if query != `SELECT ?,? WHERE id IN (?,?) OR ?=0` {
		t.Fatalf("query=%q", query)
	}
	want := []any{"2026-08-24 14:30:00", "12345678901234567890.12", "a", "b", int64(2)}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments=%#v want %#v", arguments, want)
	}
	named, err := DriverNamedValues(arguments)
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range named {
		if value.Ordinal != index+1 || !driver.IsValue(value.Value) {
			t.Fatalf("named[%d]=%#v", index, value)
		}
	}
}

func TestEmptyMultiBindsNULLAndCount(t *testing.T) {
	parameters := []Parameter{{Key: "values", Label: "Values", Type: ParameterMultipleOption, DisplayOrder: 1, Options: []ParameterOption{{Value: "a", Label: "A", DisplayOrder: 1}}}}
	normalized, err := NormalizeParameters(parameters, map[string]InputValue{"values": {Present: true}})
	if err != nil {
		t.Fatal(err)
	}
	query, arguments, err := Bind(`SELECT 1 WHERE x IN (:values) OR :values__count=0`, parameters, normalized, SQLMode{})
	if err != nil {
		t.Fatal(err)
	}
	if query != `SELECT 1 WHERE x IN (?) OR ?=0` || !reflect.DeepEqual(arguments, []any{nil, int64(0)}) {
		t.Fatalf("query=%q arguments=%#v", query, arguments)
	}
}

func TestRawDriverRejectsDecimalObject(t *testing.T) {
	if _, err := DriverNamedValues([]any{json.Number("1.2")}); err == nil {
		t.Fatal("noncanonical raw argument accepted")
	}
	if _, err := DriverNamedValues([]any{decimal.RequireFromString("1.2")}); err == nil {
		t.Fatal("shopspring decimal leaked into raw driver transport")
	}
}

func TestTextWhitespaceAndDatetimeAreNotTimezoneConverted(t *testing.T) {
	parameters := []Parameter{
		{Key: "text", Label: "Text", Type: ParameterText, DisplayOrder: 1},
		{Key: "when", Label: "When", Type: ParameterDatetime, DisplayOrder: 2},
		{Key: "branch", Label: "Branch", Type: ParameterSingleOption, DisplayOrder: 3, Options: []ParameterOption{{Value: "001", Label: "Main", DisplayOrder: 1}}},
	}
	got, err := NormalizeParameters(parameters, map[string]InputValue{
		"text":   {Present: true, Values: []string{"  kept  "}},
		"when":   {Present: true, Values: []string{"2026-08-24 14:30:12.123400"}},
		"branch": {Present: true, Values: []string{"001"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["text"].Scalar != "  kept  " || got["when"].Scalar != "2026-08-24 14:30:12.123400" || got["branch"].Scalar != "001" {
		t.Fatalf("normalized=%#v", got)
	}
}
