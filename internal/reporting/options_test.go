package reporting

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDynamicOptionSinkContractOrderAndLimits(t *testing.T) {
	sink := &optionSink{maxRows: 2, payloadCap: 4096, seen: make(map[string]struct{}), payload: 2}
	if err := sink.Columns([]Column{{Name: "value", DatabaseType: "VARCHAR"}, {Name: "label", DatabaseType: "VARCHAR"}}); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]driver.Value{{[]byte("001"), []byte("First")}, {"000", "Zero"}} {
		if err := sink.Row(row); err != nil {
			t.Fatal(err)
		}
	}
	want := []OptionItem{{Value: "001", Label: "First"}, {Value: "000", Label: "Zero"}}
	if !reflect.DeepEqual(sink.options, want) {
		t.Fatalf("options=%#v want %#v", sink.options, want)
	}
	if err := sink.Row([]driver.Value{"003", "Third"}); err == nil || !strings.Contains(err.Error(), "exceeds 2 rows") {
		t.Fatalf("row limit error=%v", err)
	}
}

func TestDynamicOptionSinkRejectsInvalidResults(t *testing.T) {
	for name, columns := range map[string][]Column{
		"one":   {{Name: "value"}},
		"three": {{Name: "value"}, {Name: "label"}, {Name: "extra"}},
		"alias": {{Name: "Value"}, {Name: "label"}},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &optionSink{maxRows: 10, payloadCap: 4096, seen: make(map[string]struct{})}
			if err := sink.Columns(columns); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	newSink := func() *optionSink {
		sink := &optionSink{maxRows: 10, payloadCap: 4096, seen: make(map[string]struct{}), payload: 2}
		if err := sink.Columns([]Column{{Name: "value"}, {Name: "label"}}); err != nil {
			t.Fatal(err)
		}
		return sink
	}
	for name, row := range map[string][]driver.Value{
		"null_value": {nil, "Label"}, "null_label": {"A", nil}, "empty_value": {"", "Label"},
		"blank_label": {"A", "  "}, "invalid_utf8": {[]byte{0xff}, "Label"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := newSink().Row(row); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	sink := newSink()
	if err := sink.Row([]driver.Value{"A", "First"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Row([]driver.Value{"A", "Duplicate label allowed but value is not"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate error=%v", err)
	}
	limited := &optionSink{maxRows: 10, payloadCap: 20, seen: make(map[string]struct{}), payload: 2, columns: []Column{{Name: "value"}, {Name: "label"}}}
	if err := limited.Row([]driver.Value{"A", strings.Repeat("x", 100)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("payload error=%v", err)
	}
	if len(limited.options) != 0 || len(limited.seen) != 0 || limited.payload != 2 {
		t.Fatalf("oversized option retained: options=%v seen=%v payload=%d", limited.options, limited.seen, limited.payload)
	}
}

func TestDynamicOptionPayloadAccountingIsExactAndIncremental(t *testing.T) {
	for _, option := range []OptionItem{
		{Value: "001", Label: "First"},
		{Value: "<\n", Label: "é&\u2028"},
	} {
		encoded, err := json.Marshal(option)
		if err != nil {
			t.Fatal(err)
		}
		bytes, fits := optionEncodedBytes(option.Value, option.Label, int64(len(encoded)))
		if !fits || bytes != int64(len(encoded)) {
			t.Fatalf("option=%+v bytes=%d want=%d fits=%v", option, bytes, len(encoded), fits)
		}
		if _, fits := optionEncodedBytes(option.Value, option.Label, int64(len(encoded)-1)); fits {
			t.Fatalf("option=%+v fit below encoded size", option)
		}
	}
}

func TestDynamicOptionSinkCanonicalScalarConversionAndRow1001(t *testing.T) {
	date := time.Date(2026, 8, 26, 7, 8, 9, 123000000, time.UTC)
	for _, test := range []struct {
		value        driver.Value
		databaseType string
		want         string
	}{
		{int64(12), "BIGINT", "12"},
		{float64(1.25), "DOUBLE", "1.25"},
		{true, "BOOLEAN", "true"},
		{date, "DATE", "2026-08-26"},
		{date, "DATETIME", "2026-08-26 07:08:09.123"},
	} {
		got, err := dynamicOptionString(test.value, test.databaseType)
		if err != nil || got != test.want {
			t.Fatalf("value=%v type=%s got=%q error=%v", test.value, test.databaseType, got, err)
		}
	}
	sink := &optionSink{maxRows: 1000, payloadCap: 1 << 20, seen: make(map[string]struct{}), payload: 2, columns: []Column{{Name: "value"}, {Name: "label"}}}
	for index := 0; index < 1000; index++ {
		value := strconv.Itoa(index)
		if err := sink.Row([]driver.Value{value, "Label"}); err != nil {
			t.Fatalf("row %d: %v", index+1, err)
		}
	}
	if err := sink.Row([]driver.Value{"overflow", "Overflow"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "1000 rows") {
		t.Fatalf("row 1001 error=%v", err)
	}
}

func TestOptionalDynamicUnsetSkipsOptionQuery(t *testing.T) {
	service := &Service{config: ServiceConfig{DynamicOptionMaxRows: 1000, DynamicOptionPayloadBytes: 1 << 20}}
	for _, parameter := range []Parameter{
		{Key: "city", Label: "City", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: "BROKEN", DisplayOrder: 0},
		{Key: "cities", Label: "Cities", Type: ParameterMultipleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: "BROKEN", DisplayOrder: 0},
	} {
		normalized, err := service.resolveAll(context.Background(), nil, []Parameter{parameter}, map[string]InputValue{parameter.Key: {Present: true}}, SQLMode{})
		if err != nil {
			t.Fatalf("%s unset executed option SQL: %v", parameter.Type, err)
		}
		value := normalized[parameter.Key]
		if value.Scalar != nil || len(value.Multi) != 0 {
			t.Fatalf("%s normalized=%+v", parameter.Type, value)
		}
	}
}

func TestRequiredUnsetBlocksAndDynamicDefaultRequiresCurrentOptions(t *testing.T) {
	service := &Service{config: ServiceConfig{DynamicOptionMaxRows: 1000, DynamicOptionPayloadBytes: 1 << 20}}
	parameter := Parameter{Key: "city", Label: "City", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: "BROKEN", Required: true, DisplayOrder: 0}
	if _, err := service.resolveAll(context.Background(), nil, []Parameter{parameter}, map[string]InputValue{"city": {Present: true}}, SQLMode{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "required") {
		t.Fatalf("required unset error=%v", err)
	}
	parameter.Required = false
	parameter.DefaultValue = json.RawMessage(`"001"`)
	if _, err := service.resolveAll(context.Background(), nil, []Parameter{parameter}, nil, SQLMode{}); err == nil || !strings.Contains(err.Error(), "report database") {
		t.Fatalf("dynamic default did not load current options: %v", err)
	}
}

func TestTemplateBindingUsesAllSQLSurfacesAndOrder(t *testing.T) {
	parameters := []Parameter{
		{Key: "province", Label: "Province", Type: ParameterText, DisplayOrder: 0},
		{Key: "city", Label: "City", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT code AS value,name AS label FROM cities WHERE province=:province`, DisplayOrder: 1},
	}
	if err := ValidateTemplateBinding(`SELECT * FROM report WHERE city=:city`, parameters, SQLMode{}); err != nil {
		t.Fatal(err)
	}
	query, arguments, err := Bind(`SELECT :city`, parameters, map[string]NormalizedValue{"city": {Scalar: "001"}}, SQLMode{})
	if err != nil || query != "SELECT ?" || !reflect.DeepEqual(arguments, []any{"001"}) {
		t.Fatalf("query=%q arguments=%#v error=%v", query, arguments, err)
	}
	parameters[0].DynamicOptionSQL = ""
	parameters[1].DynamicOptionSQL = `SELECT :city AS value,'City' AS label`
	if err := ValidateTemplateBinding(`SELECT :city`, parameters, SQLMode{}); err == nil || !strings.Contains(err.Error(), "non-upstream") {
		t.Fatalf("self reference error=%v", err)
	}
}

func TestDependencyClosureUsesDisplayOrderAndSkipsUnsetOptionalDynamicUpstream(t *testing.T) {
	parameters := []Parameter{
		{Key: "district", Label: "District", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT :city AS value,'District' AS label`, DisplayOrder: 2},
		{Key: "city", Label: "City", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT :province AS value,'City' AS label`, DisplayOrder: 1},
		{Key: "province", Label: "Province", Type: ParameterText, DisplayOrder: 0},
	}
	direct, needed, err := dependencyClosure(parameters, 0, map[string]InputValue{"city": {Present: true, Values: []string{"C"}}}, SQLMode{})
	if err != nil || !reflect.DeepEqual(direct, []string{"city"}) || !needed[1] || !needed[2] {
		t.Fatalf("direct=%v needed=%v error=%v", direct, needed, err)
	}
	_, needed, err = dependencyClosure(parameters, 0, map[string]InputValue{"city": {Present: true}}, SQLMode{})
	if err != nil || !needed[1] || needed[2] {
		t.Fatalf("optional unset closure=%v error=%v", needed, err)
	}
	query, arguments, err := Bind(`SELECT :city`, parameters, map[string]NormalizedValue{"city": {}}, SQLMode{})
	if err != nil || query != "SELECT ?" || !reflect.DeepEqual(arguments, []any{nil}) {
		t.Fatalf("optional unset bind query=%q arguments=%v error=%v", query, arguments, err)
	}
}

func TestDynamicCascadeTypeMatrixAndScalarDependency(t *testing.T) {
	for _, upstreamType := range []ParameterType{ParameterSingleOption, ParameterMultipleOption} {
		for _, downstreamType := range []ParameterType{ParameterSingleOption, ParameterMultipleOption} {
			name := string(upstreamType) + "_to_" + string(downstreamType)
			t.Run(name, func(t *testing.T) {
				condition := ":upstream IS NULL OR 1=1"
				if upstreamType == ParameterMultipleOption {
					condition = ":upstream__count=0 OR 'U' IN (:upstream)"
				}
				mainCondition := ":downstream IS NULL OR 1=1"
				if downstreamType == ParameterMultipleOption {
					mainCondition = ":downstream__count=0 OR 'D' IN (:downstream)"
				}
				parameters := []Parameter{
					{Key: "upstream", Label: "Upstream", Type: upstreamType, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT 'U' AS value,'Upstream' AS label`, DisplayOrder: 0},
					{Key: "downstream", Label: "Downstream", Type: downstreamType, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT 'D' AS value,'Downstream' AS label WHERE ` + condition, DisplayOrder: 1},
				}
				if err := ValidateTemplateBinding(`SELECT 1 WHERE `+mainCondition, parameters, SQLMode{}); err != nil {
					t.Fatal(err)
				}
				_, needed, err := dependencyClosure(parameters, 1, map[string]InputValue{"upstream": {Present: true, Values: []string{"U"}}}, SQLMode{})
				if err != nil || !needed[0] {
					t.Fatalf("needed=%v error=%v", needed, err)
				}
			})
		}
	}
	parameters := []Parameter{
		{Key: "as_of", Label: "As of", Type: ParameterDate, DisplayOrder: 0},
		{Key: "city", Label: "City", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT :as_of AS value,'City' AS label`, DisplayOrder: 1},
	}
	_, needed, err := dependencyClosure(parameters, 1, map[string]InputValue{"as_of": {Present: true, Values: []string{"2026-08-26"}}}, SQLMode{})
	if err != nil || !needed[0] {
		t.Fatalf("date dependency needed=%v error=%v", needed, err)
	}
}

func TestDynamicMembershipRejectsTamperingAndPreservesQueryOrder(t *testing.T) {
	options := []OptionItem{{Value: "002", Label: "Second"}, {Value: "001", Label: "First"}}
	parameter := Parameter{Key: "branches", Label: "Branches", Type: ParameterMultipleOption, OptionSource: OptionSourceDynamic}
	normalized, err := normalizeWithOptions(parameter, []string{"001", "002"}, options)
	if err != nil || !reflect.DeepEqual(normalized.Multi, []any{"002", "001"}) {
		t.Fatalf("normalized=%v error=%v", normalized.Multi, err)
	}
	if _, err := normalizeWithOptions(parameter, []string{"002", "tampered"}, options); err == nil {
		t.Fatal("tampered multi selection accepted")
	}
	parameter.Type = ParameterSingleOption
	if _, err := normalizeWithOptions(parameter, []string{"tampered"}, options); err == nil {
		t.Fatal("tampered single selection accepted")
	}
}

func TestDraftAllowsIncompleteDynamicAndRequiredDependencyWaits(t *testing.T) {
	parameters := []Parameter{
		{Key: "province", Label: "Province", Type: ParameterText, Required: true, DisplayOrder: 0},
		{Key: "city", Label: "City", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: `SELECT :province AS value,'City' AS label`, DisplayOrder: 1},
	}
	if err := ValidateParameters(parameters); err != nil {
		t.Fatal(err)
	}
	service := &Service{config: ServiceConfig{DynamicOptionMaxRows: 1000, DynamicOptionPayloadBytes: 1 << 20}}
	result, err := service.loadTargetOptions(context.Background(), nil, parameters, 1, map[string]InputValue{"province": {Present: true}}, SQLMode{})
	if err != nil || result.State != "waiting" || result.WaitingFor != "province" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	parameters[1].DynamicOptionSQL = ""
	if err := ValidateParameters(parameters); err != nil {
		t.Fatalf("incomplete disabled draft rejected: %v", err)
	}
}

func TestExportSnapshotNormalizesDynamicValuesWithoutOptionQuery(t *testing.T) {
	parameters := []Parameter{{Key: "branches", Label: "Branches", Type: ParameterMultipleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: "SELECT broken", Required: true}}
	input := map[string]InputValue{"branches": {Present: true, Values: []string{"001", "002"}}}
	normalized, err := NormalizeSnapshotParameters(parameters, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized["branches"].Multi, []any{"001", "002"}) {
		t.Fatalf("normalized=%#v", normalized)
	}
}
