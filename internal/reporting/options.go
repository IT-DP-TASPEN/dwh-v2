package reporting

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ibldzn/go-admin/internal/securityctx"
)

type optionSink struct {
	columns    []Column
	options    []OptionItem
	seen       map[string]struct{}
	maxRows    int
	payloadCap int64
	payload    int64
}

func RunDynamicOptions(ctx context.Context, engine QueryEngine, database *sql.DB, statement string, parameters []Parameter, normalized map[string]NormalizedValue, maxRows int, payloadCap int64) ([]OptionItem, error) {
	if maxRows <= 0 || payloadCap < 4096 {
		return nil, fmt.Errorf("invalid dynamic option bounds")
	}
	sink := &optionSink{maxRows: maxRows, payloadCap: payloadCap, seen: make(map[string]struct{}), options: make([]OptionItem, 0), payload: 2}
	if err := engine.StreamNormalized(ctx, database, statement, parameters, normalized, sink); err != nil {
		return nil, err
	}
	return sink.options, nil
}

func (sink *optionSink) Columns(columns []Column) error {
	if len(columns) != 2 || columns[0].Name != "value" || columns[1].Name != "label" {
		return fmt.Errorf("%w: dynamic option query must return exactly value,label", ErrInvalid)
	}
	sink.columns = append([]Column(nil), columns...)
	return nil
}

func (sink *optionSink) Row(values []driver.Value) error {
	if len(values) != 2 || len(sink.columns) != 2 {
		return fmt.Errorf("%w: dynamic option row has invalid shape", ErrInvalid)
	}
	if len(sink.options) >= sink.maxRows {
		return fmt.Errorf("%w: dynamic option query exceeds %d rows", ErrInvalid, sink.maxRows)
	}
	value, err := dynamicOptionString(values[0], sink.columns[0].DatabaseType)
	if err != nil {
		return fmt.Errorf("%w: dynamic option value: %v", ErrInvalid, err)
	}
	label, err := dynamicOptionString(values[1], sink.columns[1].DatabaseType)
	if err != nil {
		return fmt.Errorf("%w: dynamic option label: %v", ErrInvalid, err)
	}
	if value == "" || strings.TrimSpace(label) == "" {
		return fmt.Errorf("%w: dynamic option value and label must not be empty", ErrInvalid)
	}
	if _, found := sink.seen[value]; found {
		return fmt.Errorf("%w: dynamic option query returned a duplicate value", ErrInvalid)
	}
	encoded, fits := optionEncodedBytes(value, label, sink.payloadCap-sink.payload)
	if len(sink.options) != 0 {
		encoded++
	}
	if !fits || encoded > sink.payloadCap-sink.payload {
		return fmt.Errorf("%w: dynamic option payload exceeds %d bytes", ErrInvalid, sink.payloadCap)
	}
	sink.payload += encoded
	sink.seen[value] = struct{}{}
	sink.options = append(sink.options, OptionItem{Value: value, Label: label})
	return nil
}

func optionEncodedBytes(value, label string, limit int64) (int64, bool) {
	encoded := int64(len(`{"value":`) + len(`,"label":`) + len(`}`))
	if encoded > limit {
		return encoded, false
	}
	for _, text := range []string{value, label} {
		encoded += 2 // JSON string quotes.
		if encoded > limit {
			return encoded, false
		}
		for _, character := range text {
			width := int64(utf8.RuneLen(character))
			switch character {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				width = 2
			case '<', '>', '&':
				width = 6
			default:
				if character < 0x20 || character == '\u2028' || character == '\u2029' {
					width = 6
				}
			}
			encoded += width
			if encoded > limit {
				return encoded, false
			}
		}
	}
	return encoded, true
}

func dynamicOptionString(value driver.Value, databaseType string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("must not be NULL")
	}
	var result string
	switch typed := value.(type) {
	case string:
		result = typed
	case []byte:
		if !utf8.Valid(typed) {
			return "", fmt.Errorf("must be valid UTF-8")
		}
		result = string(bytes.Clone(typed))
	case int64:
		result = strconv.FormatInt(typed, 10)
	case float64:
		result = strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		result = strconv.FormatBool(typed)
	case time.Time:
		if strings.EqualFold(databaseType, "DATE") {
			result = typed.Format("2006-01-02")
		} else {
			result = typed.Format("2006-01-02 15:04:05.999999")
		}
	default:
		return "", fmt.Errorf("has unsupported type %T", value)
	}
	if !utf8.ValidString(result) {
		return "", fmt.Errorf("must be valid UTF-8")
	}
	return result, nil
}

func optionParameters(options []OptionItem) []ParameterOption {
	result := make([]ParameterOption, len(options))
	for index, option := range options {
		result[index] = ParameterOption{Value: option.Value, Label: option.Label, DisplayOrder: uint16(index)}
	}
	return result
}

func (service *Service) resolveAll(ctx context.Context, database *sql.DB, parameters []Parameter, input map[string]InputValue, mode SQLMode) (map[string]NormalizedValue, error) {
	if err := ValidateParameters(parameters); err != nil {
		return nil, withFailureStage(failureStageParameterValidation, err)
	}
	if err := validateKnownInput(parameters, input); err != nil {
		return nil, withFailureStage(failureStageParameterValidation, err)
	}
	normalized := make(map[string]NormalizedValue, len(parameters))
	for _, index := range parameterIndexesByDisplayOrder(parameters) {
		parameter := parameters[index]
		values, fromDefault, err := parameterValues(parameter, input[parameter.Key])
		if err != nil {
			return normalized, withFailureStage(failureStageParameterValidation, err)
		}
		var value NormalizedValue
		if effectiveOptionSource(parameter) == OptionSourceDynamic {
			if dynamicValuesUnset(parameter, values) {
				if parameter.Required {
					return normalized, withFailureStage(failureStageParameterValidation, fmt.Errorf("%w: %s is required", ErrInvalid, parameter.Label))
				}
				value, err = normalizeDynamicSnapshot(parameter, values)
				normalized[parameter.Key] = value
				continue
			}
			options, err := RunDynamicOptions(ctx, service.engine, database, parameter.DynamicOptionSQL, parameters, normalized, service.config.DynamicOptionMaxRows, service.config.DynamicOptionPayloadBytes)
			if err != nil {
				return normalized, withFailureStage(failureStageDynamicOptionResolution, fmt.Errorf("dynamic options for %s: %w", parameter.Label, err))
			}
			value, err = normalizeWithOptions(parameter, values, options)
			if err != nil && fromDefault && !parameter.Required {
				value, err = NormalizedValue{}, nil
			}
			if err != nil {
				return normalized, withFailureStage(failureStageParameterValidation, fmt.Errorf("%w: %s: %v", ErrInvalid, parameter.Label, err))
			}
		} else {
			value, err = normalizeParameter(parameter, values)
			if err != nil {
				return normalized, withFailureStage(failureStageParameterValidation, fmt.Errorf("%w: %s: %v", ErrInvalid, parameter.Label, err))
			}
		}
		if parameter.Required && value.Scalar == nil && len(value.Multi) == 0 {
			return normalized, withFailureStage(failureStageParameterValidation, fmt.Errorf("%w: %s is required", ErrInvalid, parameter.Label))
		}
		normalized[parameter.Key] = value
	}
	return normalized, nil
}

func (service *Service) LoadOptions(ctx context.Context, requester securityctx.Requester, reportID uint64, targetKey string, input map[string]InputValue) (OptionLoad, error) {
	report, err := service.repository.FindTemplate(ctx, reportID)
	if err != nil {
		return OptionLoad{}, err
	}
	if report.Status != StatusActive || report.DatasourceStatus != StatusActive {
		return OptionLoad{}, ErrInactive
	}
	allowed, err := service.repository.HasAccess(ctx, reportID, requester.Effective.UserID)
	if err != nil || !allowed {
		if err != nil {
			return OptionLoad{}, err
		}
		return OptionLoad{}, ErrForbidden
	}
	target := -1
	for index := range report.Parameters {
		if report.Parameters[index].Key == targetKey {
			target = index
			break
		}
	}
	if target < 0 || effectiveOptionSource(report.Parameters[target]) != OptionSourceDynamic {
		return OptionLoad{}, fmt.Errorf("%w: dynamic option parameter not found", ErrInvalid)
	}
	datasource, err := service.repository.FindDatasource(ctx, report.DatasourceID)
	if err != nil {
		return OptionLoad{}, err
	}
	database, err := service.pools.Database(ctx, datasource, false)
	if err != nil {
		return OptionLoad{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, service.config.InteractiveTimeout)
	defer cancel()
	mode, err := service.engine.SQLMode(runContext, database)
	if err != nil {
		return OptionLoad{}, err
	}
	return service.loadTargetOptions(runContext, database, report.Parameters, target, input, mode)
}

func (service *Service) TestOptions(ctx context.Context, requester securityctx.Requester, savedReportID uint64, draft TemplateInput, target int, input map[string]InputValue) (result OptionLoad, err error) {
	started := time.Now()
	saved, err := service.repository.FindTemplate(ctx, savedReportID)
	if err != nil {
		return result, err
	}
	var normalized map[string]NormalizedValue
	defer func() {
		err = errors.Join(err, service.appendOptionsTestAudit(ctx, requester, saved, draft, target, normalized, result, err, time.Since(started)))
	}()
	allowed, err := service.repository.HasAccess(ctx, savedReportID, requester.Effective.UserID)
	if err != nil {
		return result, withFailureStage(failureStageAuthorization, err)
	}
	if !allowed {
		return result, withFailureStage(failureStageAuthorization, ErrForbidden)
	}
	if err := validateTemplateInput(draft); err != nil {
		return result, withFailureStage(failureStageParameterValidation, err)
	}
	if target < 0 || target >= len(draft.Parameters) || effectiveOptionSource(draft.Parameters[target]) != OptionSourceDynamic {
		return result, withFailureStage(failureStageParameterValidation, fmt.Errorf("%w: choose a dynamic option parameter", ErrInvalid))
	}
	datasource, err := service.repository.FindDatasource(ctx, saved.DatasourceID)
	if err != nil {
		return result, withFailureStage(failureStageQueryExecution, err)
	}
	if datasource.Status != StatusActive {
		return result, withFailureStage(failureStageAuthorization, ErrInactive)
	}
	database, err := service.pools.Database(ctx, datasource, false)
	if err != nil {
		return result, withFailureStage(failureStageQueryExecution, err)
	}
	runContext, cancel := context.WithTimeout(ctx, service.config.InteractiveTimeout)
	defer cancel()
	mode, err := service.engine.SQLMode(runContext, database)
	if err != nil {
		return result, withFailureStage(failureStageQueryExecution, err)
	}
	result, normalized, err = service.loadTargetOptionsWithAudit(runContext, database, draft.Parameters, target, input, mode)
	return result, err
}

func (service *Service) loadTargetOptions(ctx context.Context, database *sql.DB, parameters []Parameter, target int, input map[string]InputValue, mode SQLMode) (OptionLoad, error) {
	result, _, err := service.loadTargetOptionsWithAudit(ctx, database, parameters, target, input, mode)
	return result, err
}

func (service *Service) loadTargetOptionsWithAudit(ctx context.Context, database *sql.DB, parameters []Parameter, target int, input map[string]InputValue, mode SQLMode) (OptionLoad, map[string]NormalizedValue, error) {
	if err := validateKnownInput(parameters, input); err != nil {
		return OptionLoad{}, nil, withFailureStage(failureStageParameterValidation, err)
	}
	direct, needed, err := dependencyClosure(parameters, target, input, mode)
	if err != nil {
		return OptionLoad{}, nil, withFailureStage(failureStageParameterValidation, err)
	}
	if direct == nil {
		direct = []string{}
	}
	normalized := make(map[string]NormalizedValue, len(needed))
	warning := ""
	for _, index := range parameterIndexesByDisplayOrder(parameters) {
		parameter := parameters[index]
		if !needed[index] {
			continue
		}
		values, fromDefault, err := parameterValues(parameter, input[parameter.Key])
		if err != nil {
			return OptionLoad{}, normalized, withFailureStage(failureStageParameterValidation, err)
		}
		if parameter.Required && dynamicValuesUnset(parameter, values) {
			return OptionLoad{State: "waiting", Dependencies: direct, WaitingFor: parameter.Key}, normalized, nil
		}
		var value NormalizedValue
		if effectiveOptionSource(parameter) == OptionSourceDynamic && !dynamicValuesUnset(parameter, values) {
			options, err := RunDynamicOptions(ctx, service.engine, database, parameter.DynamicOptionSQL, parameters, normalized, service.config.DynamicOptionMaxRows, service.config.DynamicOptionPayloadBytes)
			if err != nil {
				return OptionLoad{}, normalized, withFailureStage(failureStageDynamicOptionResolution, fmt.Errorf("dynamic options for %s: %w", parameter.Label, err))
			}
			value, err = normalizeWithOptions(parameter, values, options)
		} else if effectiveOptionSource(parameter) == OptionSourceDynamic {
			value, err = normalizeDynamicSnapshot(parameter, values)
		} else {
			value, err = normalizeParameter(parameter, values)
		}
		if err != nil && fromDefault && effectiveOptionSource(parameter) == OptionSourceDynamic {
			if parameter.Required {
				return OptionLoad{State: "waiting", Dependencies: direct, WaitingFor: parameter.Key}, normalized, nil
			}
			value, err = NormalizedValue{}, nil
			warning = "An upstream saved default is not available; it was left unset."
		}
		if err != nil {
			return OptionLoad{}, normalized, withFailureStage(failureStageParameterValidation, fmt.Errorf("%w: %s: %v", ErrInvalid, parameter.Label, err))
		}
		if parameter.Required && value.Scalar == nil && len(value.Multi) == 0 {
			return OptionLoad{State: "waiting", Dependencies: direct, WaitingFor: parameter.Key}, normalized, nil
		}
		normalized[parameter.Key] = value
	}
	targetParameter := parameters[target]
	options, err := RunDynamicOptions(ctx, service.engine, database, targetParameter.DynamicOptionSQL, parameters, normalized, service.config.DynamicOptionMaxRows, service.config.DynamicOptionPayloadBytes)
	if err != nil {
		return OptionLoad{}, normalized, withFailureStage(failureStageDynamicOptionResolution, fmt.Errorf("dynamic options for %s: %w", targetParameter.Label, err))
	}
	result := OptionLoad{State: "ready", Dependencies: direct, Options: options, Warning: warning}
	if len(targetParameter.DefaultValue) != 0 && !bytes.Equal(targetParameter.DefaultValue, []byte("null")) {
		values, defaultErr := defaultStrings(targetParameter.DefaultValue)
		if defaultErr == nil {
			_, defaultErr = normalizeWithOptions(targetParameter, values, options)
		}
		if defaultErr != nil {
			result.Warning = "Saved default is not available. Choose a current option."
		}
	}
	return result, normalized, nil
}

func dependencyClosure(parameters []Parameter, target int, input map[string]InputValue, mode SQLMode) ([]string, map[int]bool, error) {
	byKey := make(map[string]int, len(parameters))
	for index, parameter := range parameters {
		byKey[parameter.Key] = index
	}
	needed := make(map[int]bool)
	visiting := make(map[int]bool)
	var visitSQL func(int) ([]string, error)
	visitSQL = func(index int) ([]string, error) {
		if visiting[index] {
			return nil, fmt.Errorf("%w: dynamic option dependency cycle", ErrInvalid)
		}
		visiting[index] = true
		parameter := parameters[index]
		if strings.TrimSpace(parameter.DynamicOptionSQL) == "" {
			return nil, fmt.Errorf("%w: dynamic option SQL for %q must not be empty", ErrInvalid, parameter.Key)
		}
		references, err := ReferencedParameters(parameter.DynamicOptionSQL, parameters, mode)
		if err != nil {
			return nil, fmt.Errorf("dynamic option SQL for %q: %w", parameter.Key, err)
		}
		for _, key := range references {
			dependency := byKey[key]
			dependencyParameter := parameters[dependency]
			if dependencyParameter.DisplayOrder >= parameter.DisplayOrder {
				return nil, fmt.Errorf("%w: dynamic parameter %q references non-upstream parameter %q", ErrInvalid, parameter.Key, key)
			}
			needed[dependency] = true
			if effectiveOptionSource(dependencyParameter) != OptionSourceDynamic {
				continue
			}
			values, _, valueErr := parameterValues(dependencyParameter, input[dependencyParameter.Key])
			if valueErr != nil {
				return nil, valueErr
			}
			if !dynamicValuesUnset(dependencyParameter, values) {
				if _, err := visitSQL(dependency); err != nil {
					return nil, err
				}
			}
		}
		visiting[index] = false
		return references, nil
	}
	direct, err := visitSQL(target)
	return direct, needed, err
}

func parameterValues(parameter Parameter, provided InputValue) ([]string, bool, error) {
	values := append([]string(nil), provided.Values...)
	if provided.Present || len(parameter.DefaultValue) == 0 || bytes.Equal(parameter.DefaultValue, []byte("null")) {
		return values, false, nil
	}
	values, err := defaultStrings(parameter.DefaultValue)
	if err != nil {
		return nil, true, fmt.Errorf("%w: parameter %q has invalid default: %v", ErrInvalid, parameter.Key, err)
	}
	return values, true, nil
}

func dynamicValuesUnset(parameter Parameter, values []string) bool {
	if parameter.Type == ParameterSingleOption {
		return len(values) == 0 || (len(values) == 1 && values[0] == "")
	}
	return len(values) == 0
}

func parameterIndexesByDisplayOrder(parameters []Parameter) []int {
	indexes := make([]int, len(parameters))
	for index := range parameters {
		indexes[index] = index
	}
	sort.Slice(indexes, func(left, right int) bool {
		return parameters[indexes[left]].DisplayOrder < parameters[indexes[right]].DisplayOrder
	})
	return indexes
}

func normalizeWithOptions(parameter Parameter, values []string, options []OptionItem) (NormalizedValue, error) {
	parameter.Options = optionParameters(options)
	return normalizeParameter(parameter, values)
}

func validateKnownInput(parameters []Parameter, input map[string]InputValue) error {
	known := make(map[string]struct{}, len(parameters))
	for _, parameter := range parameters {
		known[parameter.Key] = struct{}{}
	}
	for key := range input {
		if _, found := known[key]; !found {
			return fmt.Errorf("%w: unknown parameter %q", ErrInvalid, key)
		}
	}
	return nil
}
