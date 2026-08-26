package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

const (
	auditParameterBudget    = 60 << 10
	auditParameterReserve   = 512
	auditParameterTextLimit = 4 << 10
)

const (
	failureStageAuthorization           = "authorization"
	failureStageParameterValidation     = "parameter_validation"
	failureStageDynamicOptionResolution = "dynamic_option_resolution"
	failureStageQueryExecution          = "query_execution"
)

type stagedError struct {
	stage string
	err   error
}

func (value stagedError) Error() string { return value.err.Error() }
func (value stagedError) Unwrap() error { return value.err }

func withFailureStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing stagedError
	if errors.As(err, &existing) {
		return err
	}
	return stagedError{stage: stage, err: err}
}

func safeFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	stage := failureStageQueryExecution
	var staged stagedError
	if errors.As(err, &staged) {
		stage = staged.stage
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return stage, "timed_out"
	}
	switch {
	case errors.Is(err, ErrForbidden):
		return stage, "access_denied"
	case errors.Is(err, ErrInactive):
		return stage, "inactive"
	case stage == failureStageParameterValidation:
		return stage, "invalid_parameters"
	case stage == failureStageDynamicOptionResolution:
		return stage, "option_resolution_failed"
	case stage == failureStageAuthorization:
		return stage, "authorization_failed"
	default:
		return stage, "query_failed"
	}
}

func AuditIdentity(report Template) audit.ReportIdentityMetadata {
	return audit.ReportIdentityMetadata{
		ReportTemplateID: report.ID,
		ReportName:       report.Name,
		ReportRevision:   report.Revision,
		DatasourceID:     report.DatasourceID,
		DatasourceName:   report.DatasourceName,
	}
}

func (service *Service) appendExecutionAudit(ctx context.Context, requester securityctx.Requester, action audit.Action, mode string, draft bool, report Template, parameters []Parameter, normalized map[string]NormalizedValue, result InteractiveResult, runErr error, duration time.Duration) error {
	outcome := "succeeded"
	stage, class := safeFailure(runErr)
	metadata := audit.ReportExecutionMetadata{
		ReportIdentityMetadata: AuditIdentity(report),
		ExecutionMode:          mode, Draft: draft, Parameters: AuditParameters(parameters, normalized),
		Outcome: outcome, FailureStage: stage, FailureClass: class, ExecutionDuration: duration.Milliseconds(),
	}
	if runErr != nil {
		metadata.Outcome = "failed"
	} else {
		rows, truncated := len(result.Rows), result.Truncated
		metadata.ReturnedRowCount, metadata.ResultTruncated = &rows, &truncated
		metadata.TruncationReason = result.TruncationReason
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return service.repository.AppendEvent(auditContext, requester, action, audit.ResourceReportTemplate, report.ID, metadata, time.Now().UTC())
}

func (service *Service) appendOptionsTestAudit(ctx context.Context, requester securityctx.Requester, report Template, draft TemplateInput, target int, normalized map[string]NormalizedValue, result OptionLoad, runErr error, duration time.Duration) error {
	parameters := make([]Parameter, 0, len(normalized))
	for _, parameter := range draft.Parameters {
		if _, found := normalized[parameter.Key]; found {
			parameters = append(parameters, parameter)
		}
	}
	stage, class := safeFailure(runErr)
	metadata := audit.ReportOptionsTestMetadata{
		ReportIdentityMetadata: AuditIdentity(report), Draft: true,
		Parameters: AuditParameters(parameters, normalized), Outcome: "succeeded",
		FailureStage: stage, FailureClass: class, OptionState: result.State, ExecutionDuration: duration.Milliseconds(),
	}
	if target >= 0 && target < len(draft.Parameters) {
		metadata.TargetKey, metadata.TargetLabel = draft.Parameters[target].Key, draft.Parameters[target].Label
	}
	if runErr != nil {
		metadata.Outcome = "failed"
	} else if result.State == "ready" {
		count := len(result.Options)
		metadata.OptionCount = &count
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return service.repository.AppendEvent(auditContext, requester, audit.ActionReportTemplateOptionsTested, audit.ResourceReportTemplate, report.ID, metadata, time.Now().UTC())
}

// AuditParameters incrementally retains normalized values until the global
// parameter budget is exhausted. It never allocates from the submitted value count.
func AuditParameters(parameters []Parameter, normalized map[string]NormalizedValue) audit.ReportParametersMetadata {
	ordered := append([]Parameter(nil), parameters...)
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].DisplayOrder < ordered[right].DisplayOrder })
	eligible := ordered[:0]
	for _, parameter := range ordered {
		if _, found := normalized[parameter.Key]; found {
			eligible = append(eligible, parameter)
		}
	}
	result := audit.ReportParametersMetadata{
		Items:         make([]audit.ReportParameterMetadata, 0),
		Complete:      len(eligible) == len(parameters),
		OriginalCount: len(eligible),
	}
	empty, _ := json.Marshal(result)
	used := len(empty)
	contentLimit := auditParameterBudget - auditParameterReserve
	for _, parameter := range eligible {
		value := normalized[parameter.Key]
		count := normalizedValueCount(parameter, value)
		item := audit.ReportParameterMetadata{
			Key: parameter.Key, Label: parameter.Label, Type: string(parameter.Type),
			Unset: count == 0, Values: make([]audit.ReportParameterValueMetadata, 0, min(count, 8)),
			OriginalCount: count, OmittedCount: count,
		}
		header, _ := json.Marshal(item)
		if used+len(header)+1 > contentLimit {
			break
		}
		used += len(header) + 1
		for index := 0; index < count; index++ {
			selection := auditParameterValue(normalizedValueAt(parameter, value, index), value.OptionLabels)
			encoded, _ := json.Marshal(selection)
			separator := 0
			if len(item.Values) != 0 {
				separator = 1
			}
			if used+len(encoded)+separator > contentLimit {
				break
			}
			used += len(encoded) + separator
			item.Values = append(item.Values, selection)
		}
		item.IncludedCount = len(item.Values)
		item.OmittedCount = item.OriginalCount - item.IncludedCount
		item.Truncated = item.OmittedCount != 0
		result.Items = append(result.Items, item)
		if item.Truncated {
			break
		}
	}
	result.IncludedCount = len(result.Items)
	result.OmittedCount = result.OriginalCount - result.IncludedCount
	result.Truncated = result.OmittedCount != 0
	return fitAuditParameters(result)
}

func fitAuditParameters(result audit.ReportParametersMetadata) audit.ReportParametersMetadata {
	for {
		encoded, _ := json.Marshal(result)
		if len(encoded) <= auditParameterBudget {
			return result
		}
		last := len(result.Items) - 1
		if last < 0 {
			return result
		}
		item := &result.Items[last]
		if len(item.Values) != 0 {
			item.Values = item.Values[:len(item.Values)-1]
			item.IncludedCount = len(item.Values)
			item.OmittedCount = item.OriginalCount - item.IncludedCount
			item.Truncated = true
			continue
		}
		result.Items = result.Items[:last]
		result.IncludedCount = len(result.Items)
		result.OmittedCount = result.OriginalCount - result.IncludedCount
		result.Truncated = true
	}
}

func normalizedValueCount(parameter Parameter, value NormalizedValue) int {
	if parameter.Type == ParameterMultipleOption {
		return len(value.Multi)
	}
	if value.Scalar == nil {
		return 0
	}
	return 1
}

func normalizedValueAt(parameter Parameter, value NormalizedValue, index int) string {
	var item any
	if parameter.Type == ParameterMultipleOption {
		item = value.Multi[index]
	} else {
		item = value.Scalar
	}
	switch typed := item.(type) {
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func auditParameterValue(value string, labels map[string]string) audit.ReportParameterValueMetadata {
	text, valueTruncated, valueOriginal, valueIncluded := boundedAuditText(value)
	result := audit.ReportParameterValueMetadata{Value: text}
	if valueTruncated {
		result.ValueTruncated, result.ValueOriginalBytes, result.ValueIncludedBytes = true, valueOriginal, valueIncluded
	}
	if label, found := labels[value]; found {
		result.Label, result.LabelTruncated, result.LabelOriginalBytes, result.LabelIncludedBytes = boundedAuditText(label)
		if !result.LabelTruncated {
			result.LabelOriginalBytes, result.LabelIncludedBytes = 0, 0
		}
	}
	return result
}

func boundedAuditText(value string) (string, bool, int, int) {
	originalBytes := len(value)
	valid := strings.ToValidUTF8(value, "\uFFFD")
	if len(valid) <= auditParameterTextLimit && valid == value {
		return value, false, originalBytes, originalBytes
	}
	end := min(len(valid), auditParameterTextLimit)
	for end > 0 && !utf8.ValidString(valid[:end]) {
		end--
	}
	return valid[:end], true, originalBytes, end
}
