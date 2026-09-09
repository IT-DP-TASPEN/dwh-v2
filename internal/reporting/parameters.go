package reporting

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	decimalPattern  = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)
	datetimePattern = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2})[T ]([0-9]{2}:[0-9]{2})(?::([0-9]{2})(\.[0-9]{1,6})?)?$`)
)

func (value NormalizedValue) Provided() bool { return value.Scalar != nil || len(value.Multi) != 0 }

func ValidateParameters(parameters []Parameter) error {
	keys := make(map[string]struct{}, len(parameters))
	orders := make(map[uint16]struct{}, len(parameters))
	for _, parameter := range parameters {
		if !parameterKeyPattern.MatchString(parameter.Key) || strings.HasSuffix(parameter.Key, "__count") {
			return fmt.Errorf("%w: invalid parameter key %q", ErrInvalid, parameter.Key)
		}
		if _, found := keys[parameter.Key]; found {
			return fmt.Errorf("%w: duplicate parameter %q", ErrInvalid, parameter.Key)
		}
		if _, found := orders[parameter.DisplayOrder]; found {
			return fmt.Errorf("%w: duplicate parameter display order", ErrInvalid)
		}
		keys[parameter.Key], orders[parameter.DisplayOrder] = struct{}{}, struct{}{}
		if strings.TrimSpace(parameter.Label) == "" {
			return fmt.Errorf("%w: parameter %q has no label", ErrInvalid, parameter.Key)
		}
		switch parameter.Type {
		case ParameterText, ParameterInteger, ParameterDecimal, ParameterDate, ParameterDatetime, ParameterBoolean:
			if len(parameter.Options) != 0 || parameter.OptionSource != "" || parameter.DynamicOptionSQL != "" {
				return fmt.Errorf("%w: parameter %q cannot have option configuration", ErrInvalid, parameter.Key)
			}
		case ParameterSingleOption, ParameterMultipleOption:
			switch effectiveOptionSource(parameter) {
			case OptionSourceStatic:
				if len(parameter.Options) == 0 {
					return fmt.Errorf("%w: parameter %q requires options", ErrInvalid, parameter.Key)
				}
				if parameter.DynamicOptionSQL != "" {
					return fmt.Errorf("%w: static parameter %q cannot have dynamic SQL", ErrInvalid, parameter.Key)
				}
			case OptionSourceDynamic:
				if len(parameter.Options) != 0 {
					return fmt.Errorf("%w: dynamic parameter %q cannot have static options", ErrInvalid, parameter.Key)
				}
				if len(parameter.DynamicOptionSQL) > 1<<20 {
					return fmt.Errorf("%w: dynamic option SQL for %q exceeds 1 MiB", ErrInvalid, parameter.Key)
				}
			default:
				return fmt.Errorf("%w: parameter %q has invalid option source", ErrInvalid, parameter.Key)
			}
		default:
			return fmt.Errorf("%w: parameter %q has invalid type", ErrInvalid, parameter.Key)
		}
		optionValues := make(map[string]struct{}, len(parameter.Options))
		optionOrders := make(map[uint16]struct{}, len(parameter.Options))
		for _, option := range parameter.Options {
			if option.Value == "" || strings.TrimSpace(option.Label) == "" {
				return fmt.Errorf("%w: parameter %q has an empty option", ErrInvalid, parameter.Key)
			}
			if _, found := optionValues[option.Value]; found {
				return fmt.Errorf("%w: parameter %q has duplicate option value", ErrInvalid, parameter.Key)
			}
			if _, found := optionOrders[option.DisplayOrder]; found {
				return fmt.Errorf("%w: parameter %q has duplicate option order", ErrInvalid, parameter.Key)
			}
			optionValues[option.Value], optionOrders[option.DisplayOrder] = struct{}{}, struct{}{}
		}
	}
	return nil
}

func isOptionType(value ParameterType) bool {
	return value == ParameterSingleOption || value == ParameterMultipleOption
}

func effectiveOptionSource(parameter Parameter) OptionSource {
	if isOptionType(parameter.Type) && parameter.OptionSource == "" {
		return OptionSourceStatic
	}
	return parameter.OptionSource
}

func NormalizeParameters(parameters []Parameter, input map[string]InputValue) (map[string]NormalizedValue, error) {
	if err := ValidateParameters(parameters); err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(parameters))
	result := make(map[string]NormalizedValue, len(parameters))
	for _, parameter := range parameters {
		if effectiveOptionSource(parameter) == OptionSourceDynamic {
			return nil, fmt.Errorf("%w: dynamic parameter %q requires option resolution", ErrInvalid, parameter.Key)
		}
		known[parameter.Key] = struct{}{}
		provided := input[parameter.Key]
		values := append([]string(nil), provided.Values...)
		if !provided.Present && len(parameter.DefaultValue) != 0 && !bytes.Equal(parameter.DefaultValue, []byte("null")) {
			var err error
			values, err = defaultStrings(parameter.DefaultValue)
			if err != nil {
				return nil, fmt.Errorf("%w: parameter %q has invalid default: %v", ErrInvalid, parameter.Key, err)
			}
		}
		normalized, err := normalizeParameter(parameter, values)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, parameter.Label, err)
		}
		if parameter.Required && !normalized.Provided() {
			return nil, fmt.Errorf("%w: %s is required", ErrInvalid, parameter.Label)
		}
		result[parameter.Key] = normalized
	}
	for key := range input {
		if _, found := known[key]; !found {
			return nil, fmt.Errorf("%w: unknown parameter %q", ErrInvalid, key)
		}
	}
	return result, nil
}

func NormalizeSnapshotParameters(parameters []Parameter, input map[string]InputValue) (map[string]NormalizedValue, error) {
	if err := ValidateParameters(parameters); err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(parameters))
	result := make(map[string]NormalizedValue, len(parameters))
	for _, parameter := range parameters {
		known[parameter.Key] = struct{}{}
		provided := input[parameter.Key]
		values := append([]string(nil), provided.Values...)
		if !provided.Present && len(parameter.DefaultValue) != 0 && !bytes.Equal(parameter.DefaultValue, []byte("null")) {
			var err error
			values, err = defaultStrings(parameter.DefaultValue)
			if err != nil {
				return nil, fmt.Errorf("%w: parameter %q has invalid default: %v", ErrInvalid, parameter.Key, err)
			}
		}
		var normalized NormalizedValue
		var err error
		if effectiveOptionSource(parameter) == OptionSourceDynamic {
			normalized, err = normalizeDynamicSnapshot(parameter, values)
		} else {
			normalized, err = normalizeParameter(parameter, values)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, parameter.Label, err)
		}
		if parameter.Required && !normalized.Provided() {
			return nil, fmt.Errorf("%w: %s is required", ErrInvalid, parameter.Label)
		}
		result[parameter.Key] = normalized
	}
	for key := range input {
		if _, found := known[key]; !found {
			return nil, fmt.Errorf("%w: unknown parameter %q", ErrInvalid, key)
		}
	}
	return result, nil
}

func normalizeDynamicSnapshot(parameter Parameter, values []string) (NormalizedValue, error) {
	if parameter.Type == ParameterSingleOption {
		if len(values) > 1 {
			return NormalizedValue{}, fmt.Errorf("accepts one value")
		}
		if len(values) == 0 || values[0] == "" {
			return NormalizedValue{}, nil
		}
		return NormalizedValue{Scalar: values[0]}, nil
	}
	seen := make(map[string]struct{}, len(values))
	result := NormalizedValue{Multi: make([]any, 0, len(values))}
	for _, value := range values {
		if value == "" {
			return NormalizedValue{}, fmt.Errorf("invalid option")
		}
		if _, found := seen[value]; found {
			return NormalizedValue{}, fmt.Errorf("duplicate selection %q", value)
		}
		seen[value] = struct{}{}
		result.Multi = append(result.Multi, value)
	}
	return result, nil
}

func CanonicalInput(values map[string]NormalizedValue) map[string]InputValue {
	result := make(map[string]InputValue, len(values))
	for key, value := range values {
		input := InputValue{Present: true}
		if value.Scalar != nil {
			switch typed := value.Scalar.(type) {
			case string:
				input.Values = []string{typed}
			case int64:
				input.Values = []string{strconv.FormatInt(typed, 10)}
			case bool:
				input.Values = []string{strconv.FormatBool(typed)}
			}
		} else {
			for _, item := range value.Multi {
				input.Values = append(input.Values, item.(string))
			}
		}
		result[key] = input
	}
	return result
}

func normalizeParameter(parameter Parameter, values []string) (NormalizedValue, error) {
	if parameter.Type == ParameterMultipleOption {
		selected := make(map[string]struct{}, len(values))
		for _, value := range values {
			if _, found := selected[value]; found {
				return NormalizedValue{}, fmt.Errorf("duplicate selection %q", value)
			}
			selected[value] = struct{}{}
		}
		options := append([]ParameterOption(nil), parameter.Options...)
		sort.Slice(options, func(i, j int) bool { return options[i].DisplayOrder < options[j].DisplayOrder })
		multi := make([]any, 0, len(selected))
		labels := make(map[string]string, len(selected))
		for _, option := range options {
			if _, found := selected[option.Value]; found {
				multi = append(multi, option.Value)
				labels[option.Value] = option.Label
				delete(selected, option.Value)
			}
		}
		if len(selected) != 0 {
			return NormalizedValue{}, fmt.Errorf("invalid option")
		}
		return NormalizedValue{Multi: multi, OptionLabels: labels}, nil
	}
	if len(values) > 1 {
		return NormalizedValue{}, fmt.Errorf("accepts one value")
	}
	value := ""
	if len(values) == 1 {
		value = values[0]
	}
	if value == "" {
		return NormalizedValue{}, nil
	}
	switch parameter.Type {
	case ParameterText:
		return NormalizedValue{Scalar: value}, nil
	case ParameterInteger:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return NormalizedValue{}, fmt.Errorf("must be a 64-bit integer")
		}
		return NormalizedValue{Scalar: parsed}, nil
	case ParameterDecimal:
		if !decimalPattern.MatchString(value) {
			return NormalizedValue{}, fmt.Errorf("must be a decimal number")
		}
		parsed, err := decimal.NewFromString(value)
		if err != nil {
			return NormalizedValue{}, fmt.Errorf("must be a decimal number")
		}
		return NormalizedValue{Scalar: parsed.String()}, nil
	case ParameterDate:
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil || parsed.Format("2006-01-02") != value {
			return NormalizedValue{}, fmt.Errorf("must use YYYY-MM-DD")
		}
		return NormalizedValue{Scalar: value}, nil
	case ParameterDatetime:
		matches := datetimePattern.FindStringSubmatch(value)
		if matches == nil {
			return NormalizedValue{}, fmt.Errorf("must use YYYY-MM-DD HH:MM[:SS[.ffffff]]")
		}
		seconds := matches[3]
		if seconds == "" {
			seconds = "00"
		}
		normalized := matches[1] + " " + matches[2] + ":" + seconds + matches[4]
		layout := "2006-01-02 15:04:05"
		if matches[4] != "" {
			layout += ".999999"
		}
		if _, err := time.ParseInLocation(layout, normalized, time.UTC); err != nil {
			return NormalizedValue{}, fmt.Errorf("has invalid date or time components")
		}
		return NormalizedValue{Scalar: normalized}, nil
	case ParameterBoolean:
		switch strings.ToLower(value) {
		case "true", "1":
			return NormalizedValue{Scalar: true}, nil
		case "false", "0":
			return NormalizedValue{Scalar: false}, nil
		default:
			return NormalizedValue{}, fmt.Errorf("must be true or false")
		}
	case ParameterSingleOption:
		for _, option := range parameter.Options {
			if value == option.Value {
				return NormalizedValue{Scalar: value, OptionLabels: map[string]string{value: option.Label}}, nil
			}
		}
		return NormalizedValue{}, fmt.Errorf("invalid option")
	default:
		return NormalizedValue{}, fmt.Errorf("invalid type")
	}
}

func defaultStrings(raw json.RawMessage) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	convert := func(item any) (string, error) {
		switch typed := item.(type) {
		case string:
			return typed, nil
		case bool:
			return strconv.FormatBool(typed), nil
		case json.Number:
			return typed.String(), nil
		default:
			return "", fmt.Errorf("default must contain strings, numbers, or booleans")
		}
	}
	if list, ok := value.([]any); ok {
		result := make([]string, len(list))
		for index, item := range list {
			converted, err := convert(item)
			if err != nil {
				return nil, err
			}
			result[index] = converted
		}
		return result, nil
	}
	converted, err := convert(value)
	if err != nil {
		return nil, err
	}
	return []string{converted}, nil
}

func DefaultInput(parameter Parameter) InputValue {
	if len(parameter.DefaultValue) == 0 || bytes.Equal(parameter.DefaultValue, []byte("null")) {
		return InputValue{}
	}
	values, err := defaultStrings(parameter.DefaultValue)
	if err != nil {
		return InputValue{}
	}
	return InputValue{Values: values}
}

type validatedOptionalBlock struct {
	optionalBlock
	OptionalKeys []string
}

func scanAndValidateStatement(statement string, parameters []Parameter, mode SQLMode) (statementScan, []validatedOptionalBlock, error) {
	scan, err := scanStatement(statement, mode)
	if err != nil {
		return statementScan{}, nil, err
	}
	if len(scan.OptionalBlocks) == 0 {
		if err := validateReferences(scan.Placeholders, parameters); err != nil {
			return statementScan{}, nil, err
		}
		return scan, nil, nil
	}

	definitions := make(map[string]Parameter, len(parameters))
	for _, parameter := range parameters {
		definitions[parameter.Key] = parameter
	}
	inside := make(map[int]struct{})
	blocks := make([]validatedOptionalBlock, 0, len(scan.OptionalBlocks))
	for _, block := range scan.OptionalBlocks {
		keys := make([]string, 0)
		seen := make(map[string]struct{})
		for _, placeholder := range block.Placeholders {
			inside[placeholder.Start] = struct{}{}
			key, count := splitPlaceholderKey(placeholder.Key)
			parameter, found := definitions[key]
			if !found {
				return statementScan{}, nil, fmt.Errorf("%w: optional SQL block references unknown parameter :%s", ErrInvalid, placeholder.Key)
			}
			if count && parameter.Type != ParameterMultipleOption {
				return statementScan{}, nil, fmt.Errorf("%w: %q count is only valid for multiple options", ErrInvalid, key)
			}
			if parameter.Required {
				continue
			}
			if _, found := seen[key]; !found {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			return statementScan{}, nil, fmt.Errorf("%w: optional SQL block must reference at least one optional parameter", ErrInvalid)
		}
		blocks = append(blocks, validatedOptionalBlock{optionalBlock: block, OptionalKeys: keys})
	}
	for _, placeholder := range scan.Placeholders {
		if _, found := inside[placeholder.Start]; found {
			continue
		}
		key, count := splitPlaceholderKey(placeholder.Key)
		parameter, found := definitions[key]
		if !found {
			return statementScan{}, nil, fmt.Errorf("%w: SQL references unknown parameter %q", ErrInvalid, placeholder.Key)
		}
		if count && parameter.Type != ParameterMultipleOption {
			return statementScan{}, nil, fmt.Errorf("%w: %q count is only valid for multiple options", ErrInvalid, key)
		}
		if !parameter.Required {
			return statementScan{}, nil, fmt.Errorf("%w: optional parameter :%s is referenced outside an optional SQL block", ErrInvalid, key)
		}
	}
	return scan, blocks, nil
}

func resolveOptionalBlocks(statement string, blocks []validatedOptionalBlock, values map[string]NormalizedValue) (string, error) {
	if len(blocks) == 0 {
		return statement, nil
	}
	var result strings.Builder
	result.Grow(len(statement))
	position := 0
	for _, block := range blocks {
		result.WriteString(statement[position:block.Start])
		include := true
		for _, key := range block.OptionalKeys {
			value, found := values[key]
			if !found {
				return "", fmt.Errorf("%w: parameter %q is not normalized", ErrInvalid, key)
			}
			include = include && value.Provided()
		}
		if include {
			result.WriteString(statement[block.ContentStart:block.ContentEnd])
		} else {
			result.WriteByte(' ')
		}
		position = block.End
	}
	result.WriteString(statement[position:])
	return result.String(), nil
}

func templateValidationQueries(statement string, parameters []Parameter, mode SQLMode, limit int) ([]string, error) {
	_, blocks, err := scanAndValidateStatement(statement, parameters, mode)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	optionalKeys := make([]string, 0)
	for _, parameter := range parameters {
		if !parameter.Required {
			optionalKeys = append(optionalKeys, parameter.Key)
		}
	}
	type presenceState map[string]bool
	states := make([]presenceState, 0, min(limit, 2+len(blocks)+len(optionalKeys)))
	seenStates := make(map[string]struct{})
	addState := func(state presenceState) {
		if len(states) >= limit {
			return
		}
		fingerprint := make([]byte, len(optionalKeys))
		for index, key := range optionalKeys {
			if state[key] {
				fingerprint[index] = 1
			}
		}
		key := string(fingerprint)
		if _, found := seenStates[key]; found {
			return
		}
		seenStates[key] = struct{}{}
		states = append(states, state)
	}
	allPresent := make(presenceState, len(optionalKeys))
	for _, key := range optionalKeys {
		allPresent[key] = true
	}
	if len(blocks) == 0 {
		addState(allPresent)
	} else {
		addState(nil)
		addState(allPresent)
		for _, block := range blocks {
			state := make(presenceState, len(block.OptionalKeys))
			for _, key := range block.OptionalKeys {
				state[key] = true
			}
			addState(state)
		}
		for _, omitted := range optionalKeys {
			state := make(presenceState, len(optionalKeys)-1)
			for _, key := range optionalKeys {
				if key != omitted {
					state[key] = true
				}
			}
			addState(state)
		}
	}

	queries := make([]string, 0, len(states))
	seenQueries := make(map[string]struct{})
	for _, state := range states {
		values := make(map[string]NormalizedValue, len(parameters))
		for _, parameter := range parameters {
			if !parameter.Required && !state[parameter.Key] {
				values[parameter.Key] = NormalizedValue{}
			} else if parameter.Type == ParameterMultipleOption {
				values[parameter.Key] = NormalizedValue{Multi: []any{"optional-block-validation"}}
			} else {
				values[parameter.Key] = NormalizedValue{Scalar: "optional-block-validation"}
			}
		}
		query, _, err := Bind(statement, parameters, values, mode)
		if err != nil {
			return nil, err
		}
		if _, found := seenQueries[query]; found {
			continue
		}
		seenQueries[query] = struct{}{}
		queries = append(queries, query)
	}
	return queries, nil
}

func Bind(statement string, parameters []Parameter, values map[string]NormalizedValue, mode SQLMode) (string, []any, error) {
	_, blocks, err := scanAndValidateStatement(statement, parameters, mode)
	if err != nil {
		return "", nil, err
	}
	statement, err = resolveOptionalBlocks(statement, blocks, values)
	if err != nil {
		return "", nil, err
	}
	placeholders, err := ScanPlaceholders(statement, mode)
	if err != nil {
		return "", nil, err
	}
	definitions := make(map[string]Parameter, len(parameters))
	for _, parameter := range parameters {
		definitions[parameter.Key] = parameter
	}
	var query strings.Builder
	arguments := make([]any, 0, len(placeholders))
	position := 0
	for _, placeholder := range placeholders {
		query.WriteString(statement[position:placeholder.Start])
		key, count := splitPlaceholderKey(placeholder.Key)
		parameter := definitions[key]
		value, found := values[key]
		if !found {
			return "", nil, fmt.Errorf("%w: parameter %q is not normalized", ErrInvalid, key)
		}
		if count {
			if parameter.Type != ParameterMultipleOption {
				return "", nil, fmt.Errorf("%w: %q count is only valid for multiple options", ErrInvalid, key)
			}
			query.WriteByte('?')
			arguments = append(arguments, int64(len(value.Multi)))
		} else if parameter.Type == ParameterMultipleOption {
			if len(value.Multi) == 0 {
				query.WriteByte('?')
				arguments = append(arguments, nil)
			} else {
				for index, item := range value.Multi {
					if index != 0 {
						query.WriteByte(',')
					}
					query.WriteByte('?')
					arguments = append(arguments, item)
				}
			}
		} else {
			query.WriteByte('?')
			arguments = append(arguments, value.Scalar)
		}
		position = placeholder.End
	}
	query.WriteString(statement[position:])
	return query.String(), arguments, nil
}

func ValidateBinding(statement string, parameters []Parameter, mode SQLMode) error {
	if strings.TrimSpace(statement) == "" {
		return fmt.Errorf("%w: SQL must not be empty", ErrInvalid)
	}
	if err := ValidateParameters(parameters); err != nil {
		return err
	}
	if err := validateDefaults(parameters); err != nil {
		return err
	}
	_, _, err := scanAndValidateStatement(statement, parameters, mode)
	return err
}

func splitPlaceholderKey(key string) (string, bool) {
	if strings.HasSuffix(key, "__count") {
		return strings.TrimSuffix(key, "__count"), true
	}
	return key, false
}

func validateReferences(placeholders []Placeholder, parameters []Parameter) error {
	definitions := make(map[string]Parameter, len(parameters))
	for _, parameter := range parameters {
		definitions[parameter.Key] = parameter
	}
	for _, placeholder := range placeholders {
		key, count := splitPlaceholderKey(placeholder.Key)
		parameter, found := definitions[key]
		if !found {
			return fmt.Errorf("%w: SQL references unknown parameter %q", ErrInvalid, placeholder.Key)
		}
		if count && parameter.Type != ParameterMultipleOption {
			return fmt.Errorf("%w: %q count is only valid for multiple options", ErrInvalid, key)
		}
	}
	return nil
}

func ReferencedParameters(statement string, parameters []Parameter, mode SQLMode) ([]string, error) {
	scan, _, err := scanAndValidateStatement(statement, parameters, mode)
	if err != nil {
		return nil, err
	}
	placeholders := scan.Placeholders
	seen := make(map[string]struct{}, len(placeholders))
	result := make([]string, 0, len(placeholders))
	for _, placeholder := range placeholders {
		key, _ := splitPlaceholderKey(placeholder.Key)
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result, nil
}

func ValidateTemplateBinding(statement string, parameters []Parameter, mode SQLMode) error {
	if strings.TrimSpace(statement) == "" {
		return fmt.Errorf("%w: SQL must not be empty", ErrInvalid)
	}
	if err := ValidateParameters(parameters); err != nil {
		return err
	}
	if err := validateDefaults(parameters); err != nil {
		return err
	}
	used := make(map[string]struct{}, len(parameters))
	mainReferences, err := ReferencedParameters(statement, parameters, mode)
	if err != nil {
		return err
	}
	for _, key := range mainReferences {
		used[key] = struct{}{}
	}
	byKey := make(map[string]Parameter, len(parameters))
	for _, parameter := range parameters {
		byKey[parameter.Key] = parameter
	}
	for _, parameter := range parameters {
		if effectiveOptionSource(parameter) != OptionSourceDynamic {
			continue
		}
		if strings.TrimSpace(parameter.DynamicOptionSQL) == "" {
			return fmt.Errorf("%w: dynamic option SQL for %q must not be empty", ErrInvalid, parameter.Key)
		}
		references, err := ReferencedParameters(parameter.DynamicOptionSQL, parameters, mode)
		if err != nil {
			return fmt.Errorf("dynamic option SQL for %q: %w", parameter.Key, err)
		}
		for _, key := range references {
			if byKey[key].DisplayOrder >= parameter.DisplayOrder {
				return fmt.Errorf("%w: dynamic parameter %q references non-upstream parameter %q", ErrInvalid, parameter.Key, key)
			}
			used[key] = struct{}{}
		}
	}
	for _, parameter := range parameters {
		if _, found := used[parameter.Key]; !found {
			return fmt.Errorf("%w: parameter %q is not used by report SQL", ErrInvalid, parameter.Key)
		}
	}
	return nil
}

func validateDefaults(parameters []Parameter) error {
	for _, parameter := range parameters {
		if len(parameter.DefaultValue) == 0 || bytes.Equal(parameter.DefaultValue, []byte("null")) {
			continue
		}
		values, err := defaultStrings(parameter.DefaultValue)
		if err != nil {
			return fmt.Errorf("%w: parameter %q has invalid default: %v", ErrInvalid, parameter.Key, err)
		}
		if effectiveOptionSource(parameter) == OptionSourceDynamic {
			if parameter.Type == ParameterSingleOption && len(values) > 1 {
				return fmt.Errorf("%w: parameter %q default accepts one value", ErrInvalid, parameter.Key)
			}
			seen := make(map[string]struct{}, len(values))
			for _, value := range values {
				if value == "" {
					return fmt.Errorf("%w: parameter %q has an empty default", ErrInvalid, parameter.Key)
				}
				if _, found := seen[value]; found {
					return fmt.Errorf("%w: parameter %q has duplicate default value", ErrInvalid, parameter.Key)
				}
				seen[value] = struct{}{}
			}
			continue
		}
		if _, err := normalizeParameter(parameter, values); err != nil {
			return fmt.Errorf("%w: parameter %q has invalid default: %v", ErrInvalid, parameter.Key, err)
		}
	}
	return nil
}

// DriverNamedValues is deliberately stricter than database/sql conversion.
// Raw execution only transports the canonical binder types without precision loss.
func DriverNamedValues(arguments []any) ([]driver.NamedValue, error) {
	result := make([]driver.NamedValue, len(arguments))
	for index, value := range arguments {
		switch value.(type) {
		case nil, string, int64, bool:
		default:
			return nil, fmt.Errorf("raw report argument %d has unsupported type %T", index+1, value)
		}
		result[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return result, nil
}
