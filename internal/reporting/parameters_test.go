package reporting

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

func TestNormalizedValuePresenceIsNotTruthiness(t *testing.T) {
	for name, value := range map[string]NormalizedValue{
		"false":        {Scalar: false},
		"integer zero": {Scalar: int64(0)},
		"decimal zero": {Scalar: "0"},
		"string zero":  {Scalar: "0"},
		"list":         {Multi: []any{"KPR"}},
	} {
		if !value.Provided() {
			t.Fatalf("%s treated as omitted", name)
		}
	}
	if (NormalizedValue{}).Provided() || (NormalizedValue{Multi: []any{}}).Provided() {
		t.Fatal("empty value treated as provided")
	}
}

func TestOptionalBlocksCompileBeforeNormalBinding(t *testing.T) {
	parameters := []Parameter{
		{Key: "date", Label: "Date", Type: ParameterDate, Required: true, DisplayOrder: 0},
		{Key: "products", Label: "Products", Type: ParameterMultipleOption, Options: []ParameterOption{{Value: "KPR", Label: "KPR"}, {Value: "KMG", Label: "KMG", DisplayOrder: 1}}, DisplayOrder: 1},
		{Key: "enabled", Label: "Enabled", Type: ParameterBoolean, DisplayOrder: 2},
		{Key: "min", Label: "Minimum", Type: ParameterInteger, DisplayOrder: 3},
		{Key: "max", Label: "Maximum", Type: ParameterDecimal, DisplayOrder: 4},
	}
	statement := `SELECT * FROM loans WHERE report_date=:date[[ AND product_id IN (:products) ]][[ AND enabled=:enabled ]][[ AND amount BETWEEN :min AND :max ]]ORDER BY id`
	values := map[string]NormalizedValue{
		"date": {Scalar: "2026-09-07"}, "products": {}, "enabled": {}, "min": {}, "max": {},
	}
	query, arguments, err := Bind(statement, parameters, values, SQLMode{})
	if err != nil || query != `SELECT * FROM loans WHERE report_date=?   ORDER BY id` || !reflect.DeepEqual(arguments, []any{"2026-09-07"}) {
		t.Fatalf("omitted query=%q arguments=%#v error=%v", query, arguments, err)
	}
	values["products"] = NormalizedValue{Multi: []any{"KPR", "KMG"}}
	values["enabled"] = NormalizedValue{Scalar: false}
	values["min"] = NormalizedValue{Scalar: int64(0)}
	query, arguments, err = Bind(statement, parameters, values, SQLMode{})
	if err != nil || query != `SELECT * FROM loans WHERE report_date=? AND product_id IN (?,?)  AND enabled=?  ORDER BY id` || !reflect.DeepEqual(arguments, []any{"2026-09-07", "KPR", "KMG", false}) {
		t.Fatalf("partial query=%q arguments=%#v error=%v", query, arguments, err)
	}
	values["max"] = NormalizedValue{Scalar: "0"}
	query, arguments, err = Bind(statement, parameters, values, SQLMode{})
	wantArguments := []any{"2026-09-07", "KPR", "KMG", false, int64(0), "0"}
	if err != nil || query != `SELECT * FROM loans WHERE report_date=? AND product_id IN (?,?)  AND enabled=?  AND amount BETWEEN ? AND ? ORDER BY id` || !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("included query=%q arguments=%#v error=%v", query, arguments, err)
	}
}

func TestOptionalBlockValidationAndLegacyBoundary(t *testing.T) {
	optional := Parameter{Key: "product", Label: "Product", Type: ParameterText}
	required := Parameter{Key: "date", Label: "Date", Type: ParameterDate, Required: true, DisplayOrder: 1}
	for _, test := range []struct {
		name       string
		statement  string
		parameters []Parameter
		contains   string
	}{
		{name: "unclosed", statement: `SELECT 1 [[ AND product=:product`, parameters: []Parameter{optional}, contains: "unclosed"},
		{name: "unmatched close", statement: `SELECT 1 ]]`, contains: "unmatched"},
		{name: "nested", statement: `SELECT 1 [[ AND product=:product [[ OR product=:product ]] ]]`, parameters: []Parameter{optional}, contains: "nested"},
		{name: "empty", statement: `SELECT 1 [[ ]]`, contains: "must contain SQL"},
		{name: "comment only", statement: "SELECT 1 [[ /* no SQL */ ]]", contains: "must contain SQL"},
		{name: "unknown", statement: `SELECT 1 [[ AND product=:prodduk ]]`, parameters: []Parameter{optional}, contains: ":prodduk"},
		{name: "required only", statement: `SELECT 1 [[ AND report_date=:date ]]`, parameters: []Parameter{required}, contains: "at least one optional"},
		{name: "optional outside", statement: `SELECT :product [[ AND report_date=:date AND product=:product ]]`, parameters: []Parameter{optional, required}, contains: "outside"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateBinding(test.statement, test.parameters, SQLMode{}); err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	legacy := `SELECT 1 WHERE (:product IS NULL OR product_id=:product)`
	query, arguments, err := Bind(legacy, []Parameter{optional}, map[string]NormalizedValue{"product": {}}, SQLMode{})
	if err != nil || query != `SELECT 1 WHERE (? IS NULL OR product_id=?)` || !reflect.DeepEqual(arguments, []any{nil, nil}) {
		t.Fatalf("legacy query=%q arguments=%#v error=%v", query, arguments, err)
	}
}

func TestOptionalBlockRepeatedParameterAndMissingPresence(t *testing.T) {
	parameters := []Parameter{{Key: "product", Label: "Product", Type: ParameterText}}
	statement := `SELECT 1 [[ WHERE product_id=:product OR parent_product_id=:product ]]`
	if _, _, err := Bind(statement, parameters, nil, SQLMode{}); err == nil || !strings.Contains(err.Error(), "not normalized") {
		t.Fatalf("missing presence error=%v", err)
	}
	query, arguments, err := Bind(statement, parameters, map[string]NormalizedValue{"product": {Scalar: "KPR"}}, SQLMode{})
	if err != nil || query != `SELECT 1  WHERE product_id=? OR parent_product_id=? ` || !reflect.DeepEqual(arguments, []any{"KPR", "KPR"}) {
		t.Fatalf("query=%q arguments=%#v error=%v", query, arguments, err)
	}
}

func TestOptionalBlockMayContainRequiredParameters(t *testing.T) {
	parameters := []Parameter{
		{Key: "date", Label: "Date", Type: ParameterDate, Required: true},
		{Key: "product", Label: "Product", Type: ParameterText, DisplayOrder: 1},
	}
	statement := `SELECT 1 [[ WHERE report_date=:date AND product_id=:product ]]`
	values := map[string]NormalizedValue{"date": {Scalar: "2026-09-07"}, "product": {}}
	query, arguments, err := Bind(statement, parameters, values, SQLMode{})
	if err != nil || query != `SELECT 1  ` || len(arguments) != 0 {
		t.Fatalf("omitted query=%q arguments=%#v error=%v", query, arguments, err)
	}
	values["product"] = NormalizedValue{Scalar: "KPR"}
	query, arguments, err = Bind(statement, parameters, values, SQLMode{})
	if err != nil || query != `SELECT 1  WHERE report_date=? AND product_id=? ` || !reflect.DeepEqual(arguments, []any{"2026-09-07", "KPR"}) {
		t.Fatalf("included query=%q arguments=%#v error=%v", query, arguments, err)
	}
}

func TestCanonicalSnapshotPreservesOptionalBlockPresence(t *testing.T) {
	parameters := []Parameter{
		{Key: "enabled", Label: "Enabled", Type: ParameterBoolean},
		{Key: "minimum", Label: "Minimum", Type: ParameterInteger, DisplayOrder: 1},
		{Key: "products", Label: "Products", Type: ParameterMultipleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT 'KPR','KPR'`, DisplayOrder: 2},
	}
	statement := `SELECT 1 [[ WHERE enabled=:enabled ]][[ AND amount>=:minimum ]][[ AND product IN (:products) ]]`
	direct := map[string]NormalizedValue{
		"enabled": {Scalar: false}, "minimum": {Scalar: int64(0)}, "products": {},
	}
	restored, err := NormalizeSnapshotParameters(parameters, CanonicalInput(direct))
	if err != nil {
		t.Fatal(err)
	}
	directQuery, directArguments, err := Bind(statement, parameters, direct, SQLMode{})
	if err != nil {
		t.Fatal(err)
	}
	restoredQuery, restoredArguments, err := Bind(statement, parameters, restored, SQLMode{})
	if err != nil || restored["products"].Provided() || restoredQuery != directQuery || !reflect.DeepEqual(restoredArguments, directArguments) {
		t.Fatalf("restored=%#v query=%q arguments=%#v error=%v", restored, restoredQuery, restoredArguments, err)
	}
}

func TestTemplateValidationQueriesUseSyntheticShapesAndStayBounded(t *testing.T) {
	parameters := []Parameter{{
		Key: "products", Label: "Products", Type: ParameterMultipleOption, OptionSource: OptionSourceDynamic,
		DynamicOptionSQL: "BROKEN AND DATA DEPENDENT", DefaultValue: json.RawMessage(`["not-current"]`),
	}}
	queries, err := templateValidationQueries(`SELECT 1 [[ WHERE product_id IN (:products) ]]`, parameters, SQLMode{}, maxPreparedTemplateVariants)
	if err != nil || !reflect.DeepEqual(queries, []string{"SELECT 1  ", "SELECT 1  WHERE product_id IN (?) "}) {
		t.Fatalf("queries=%q error=%v", queries, err)
	}

	parameters = nil
	var statement strings.Builder
	statement.WriteString("SELECT 1")
	for index := 0; index < 70; index++ {
		key := fmt.Sprintf("value_%d", index)
		parameters = append(parameters, Parameter{Key: key, Label: key, Type: ParameterText, DisplayOrder: uint16(index)})
		fmt.Fprintf(&statement, " [[ AND %s=:%s ]]", key, key)
	}
	queries, err = templateValidationQueries(statement.String(), parameters, SQLMode{}, maxPreparedTemplateVariants)
	if err != nil || len(queries) != maxPreparedTemplateVariants {
		t.Fatalf("variant count=%d error=%v", len(queries), err)
	}
}
