package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type Action string

const MaxMetadataBytes = 64 << 10

const (
	ActionAuthLogin                      Action = "auth.login"
	ActionAuthLogout                     Action = "auth.logout"
	ActionAuthRegistration               Action = "auth.registration"
	ActionImpersonationStarted           Action = "impersonation.started"
	ActionImpersonationStopped           Action = "impersonation.stopped"
	ActionUserCreated                    Action = "user.created"
	ActionUserProfileUpdated             Action = "user.profile_updated"
	ActionUserRoleChanged                Action = "user.role_changed"
	ActionUserActivated                  Action = "user.activated"
	ActionUserDeactivated                Action = "user.deactivated"
	ActionUserPasswordReset              Action = "user.password_reset"
	ActionRoleCreated                    Action = "role.created"
	ActionRoleUpdated                    Action = "role.updated"
	ActionRoleDeleted                    Action = "role.deleted"
	ActionRolePermissionsUpdated         Action = "role.permissions_updated"
	ActionAdminBootstrap                 Action = "admin.bootstrap"
	ActionIngestionRunSubmitted          Action = "ingestion.run_submitted"
	ActionIngestionRunAllSubmitted       Action = "ingestion.run_all_submitted"
	ActionIngestionCancellationRequested Action = "ingestion.cancellation_requested"
	ActionIngestionAbandonedRecovered    Action = "ingestion.abandoned_recovered"
	ActionSourceStateChanged             Action = "source.state_changed"
	ActionScheduleCreated                Action = "schedule.created"
	ActionScheduleUpdated                Action = "schedule.updated"
	ActionScheduleStateChanged           Action = "schedule.state_changed"
	ActionReportDatasourceCreated        Action = "report_datasource.created"
	ActionReportDatasourceUpdated        Action = "report_datasource.updated"
	ActionReportDatasourceStateChanged   Action = "report_datasource.state_changed"
	ActionReportDatasourceTested         Action = "report_datasource.tested"
	ActionReportTemplateCreated          Action = "report_template.created"
	ActionReportTemplateUpdated          Action = "report_template.updated"
	ActionReportTemplateStateChanged     Action = "report_template.state_changed"
	ActionReportTemplateAccessChanged    Action = "report_template.access_changed"
	ActionReportExecuted                 Action = "report.executed"
	ActionReportTemplateQueryTested      Action = "report_template.query_tested"
	ActionReportTemplateOptionsTested    Action = "report_template.options_tested"
	ActionReportExportSubmitted          Action = "report_export.submitted"
	ActionReportExportDownloaded         Action = "report_export.downloaded"
)

type ResourceType string

const (
	ResourceUser             ResourceType = "user"
	ResourceRole             ResourceType = "role"
	ResourceIngestionRun     ResourceType = "ingestion_run"
	ResourceSchedule         ResourceType = "schedule"
	ResourceReportDatasource ResourceType = "report_datasource"
	ResourceReportTemplate   ResourceType = "report_template"
	ResourceReportExport     ResourceType = "report_export"
)

type Identity struct {
	UserID   uint64
	Username string
}

type Attribution struct {
	Actor     *Identity
	Effective *Identity
}

type Metadata interface {
	auditMetadata()
}

type RoleChangeMetadata struct {
	FromRole string `json:"from_role"`
	ToRole   string `json:"to_role"`
}

func (RoleChangeMetadata) auditMetadata() {}

type StatusChangeMetadata struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (StatusChangeMetadata) auditMetadata() {}

type PermissionsUpdatedMetadata struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

func (PermissionsUpdatedMetadata) auditMetadata() {}

type ImpersonationStartedMetadata struct {
	TargetRole string `json:"target_role"`
}

func (ImpersonationStartedMetadata) auditMetadata() {}

type AccessChangeMetadata struct {
	UserID  uint64 `json:"user_id"`
	Granted bool   `json:"granted"`
}

func (AccessChangeMetadata) auditMetadata() {}

type OutcomeMetadata struct {
	Outcome string `json:"outcome"`
}

func (OutcomeMetadata) auditMetadata() {}

type ReportIdentityMetadata struct {
	ReportTemplateID uint64 `json:"report_template_id"`
	ReportName       string `json:"report_name"`
	ReportRevision   uint64 `json:"report_revision,omitempty"`
	DatasourceID     uint64 `json:"datasource_id"`
	DatasourceName   string `json:"datasource_name,omitempty"`
}

type ReportParameterValueMetadata struct {
	Value              string `json:"value"`
	Label              string `json:"label,omitempty"`
	ValueTruncated     bool   `json:"value_truncated,omitempty"`
	ValueOriginalBytes int    `json:"value_original_bytes,omitempty"`
	ValueIncludedBytes int    `json:"value_included_bytes,omitempty"`
	LabelTruncated     bool   `json:"label_truncated,omitempty"`
	LabelOriginalBytes int    `json:"label_original_bytes,omitempty"`
	LabelIncludedBytes int    `json:"label_included_bytes,omitempty"`
}

type ReportParameterMetadata struct {
	Key           string                         `json:"key"`
	Label         string                         `json:"label"`
	Type          string                         `json:"type"`
	Unset         bool                           `json:"unset"`
	Values        []ReportParameterValueMetadata `json:"values"`
	Truncated     bool                           `json:"truncated,omitempty"`
	OriginalCount int                            `json:"original_count"`
	IncludedCount int                            `json:"included_count"`
	OmittedCount  int                            `json:"omitted_count"`
}

type ReportParametersMetadata struct {
	Items         []ReportParameterMetadata `json:"items"`
	Complete      bool                      `json:"complete"`
	Truncated     bool                      `json:"truncated,omitempty"`
	OriginalCount int                       `json:"original_count"`
	IncludedCount int                       `json:"included_count"`
	OmittedCount  int                       `json:"omitted_count"`
}

type ReportExecutionMetadata struct {
	ReportIdentityMetadata
	ExecutionMode     string                   `json:"execution_mode"`
	Draft             bool                     `json:"draft,omitempty"`
	Parameters        ReportParametersMetadata `json:"parameters"`
	Outcome           string                   `json:"outcome"`
	FailureStage      string                   `json:"failure_stage,omitempty"`
	FailureClass      string                   `json:"failure_class,omitempty"`
	ReturnedRowCount  *int                     `json:"returned_row_count,omitempty"`
	ResultTruncated   *bool                    `json:"result_truncated,omitempty"`
	TruncationReason  string                   `json:"truncation_reason,omitempty"`
	ExecutionDuration int64                    `json:"execution_duration_ms"`
}

func (ReportExecutionMetadata) auditMetadata() {}

type ReportOptionsTestMetadata struct {
	ReportIdentityMetadata
	Draft             bool                     `json:"draft"`
	TargetKey         string                   `json:"target_parameter_key,omitempty"`
	TargetLabel       string                   `json:"target_parameter_label,omitempty"`
	Parameters        ReportParametersMetadata `json:"upstream_parameters"`
	Outcome           string                   `json:"outcome"`
	FailureStage      string                   `json:"failure_stage,omitempty"`
	FailureClass      string                   `json:"failure_class,omitempty"`
	OptionState       string                   `json:"option_state,omitempty"`
	OptionCount       *int                     `json:"option_count,omitempty"`
	ExecutionDuration int64                    `json:"execution_duration_ms"`
}

func (ReportOptionsTestMetadata) auditMetadata() {}

type ReportExportSubmittedMetadata struct {
	ReportIdentityMetadata
	ExportJobID uint64                   `json:"export_job_id"`
	Parameters  ReportParametersMetadata `json:"parameters"`
	Outcome     string                   `json:"outcome"`
}

func (ReportExportSubmittedMetadata) auditMetadata() {}

type ReportExportDownloadedMetadata struct {
	ReportTemplateID  uint64 `json:"report_template_id"`
	ReportName        string `json:"report_name"`
	DatasourceID      uint64 `json:"datasource_id"`
	ExportJobID       uint64 `json:"export_job_id"`
	SubmittedByUserID uint64 `json:"submitted_by_user_id,omitempty"`
	AccessPath        string `json:"access_path,omitempty"`
	ArtifactName      string `json:"artifact_name"`
	ArtifactType      string `json:"artifact_type,omitempty"`
}

func (ReportExportDownloadedMetadata) auditMetadata() {}

type DatasourceUpdatedMetadata struct {
	CredentialsChanged bool `json:"credentials_changed"`
}

func (DatasourceUpdatedMetadata) auditMetadata() {}

type SourceStateChangeMetadata struct {
	SourceKey string `json:"source_key"`
	From      string `json:"from"`
	To        string `json:"to"`
}

func (SourceStateChangeMetadata) auditMetadata() {}

type IngestionSubmissionMetadata struct {
	JobKey string `json:"job_key,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

func (IngestionSubmissionMetadata) auditMetadata() {}

type Event struct {
	Attribution Attribution
	Action      Action
	Resource    ResourceType
	ResourceID  uint64
	Metadata    Metadata
	CreatedAt   time.Time
}

type AppendFunc func(context.Context, sqlx.ExtContext, Event) error

func Append(ctx context.Context, executor sqlx.ExtContext, event Event) error {
	if executor == nil {
		return fmt.Errorf("audit executor must not be nil")
	}
	if !knownAction(event.Action) {
		return fmt.Errorf("unknown audit action %q", event.Action)
	}
	if err := validateIdentity("actor", event.Attribution.Actor); err != nil {
		return err
	}
	if err := validateIdentity("effective", event.Attribution.Effective); err != nil {
		return err
	}
	if (event.Resource == "") != (event.ResourceID == 0) {
		return fmt.Errorf("audit resource type and ID must be set together")
	}
	if event.Resource != "" && event.Resource != ResourceUser && event.Resource != ResourceRole && event.Resource != ResourceIngestionRun &&
		event.Resource != ResourceSchedule && event.Resource != ResourceReportDatasource && event.Resource != ResourceReportTemplate && event.Resource != ResourceReportExport {
		return fmt.Errorf("unknown audit resource type %q", event.Resource)
	}
	if event.CreatedAt.IsZero() {
		return fmt.Errorf("audit creation time must not be zero")
	}

	var metadata any
	if event.Metadata != nil {
		encoded, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("encode audit metadata: %w", err)
		}
		if len(encoded) > MaxMetadataBytes {
			return fmt.Errorf("audit metadata exceeds %d bytes", MaxMetadataBytes)
		}
		metadata = encoded
	}

	var actorID, actorUsername, effectiveID, effectiveUsername any
	if event.Attribution.Actor != nil {
		actorID = event.Attribution.Actor.UserID
		actorUsername = event.Attribution.Actor.Username
	}
	if event.Attribution.Effective != nil {
		effectiveID = event.Attribution.Effective.UserID
		effectiveUsername = event.Attribution.Effective.Username
	}
	var resourceType, resourceID any
	if event.Resource != "" {
		resourceType = event.Resource
		resourceID = event.ResourceID
	}

	const statement = `
		INSERT INTO audit_logs (
			actor_user_id, actor_username, effective_user_id, effective_username,
			action, resource_type, resource_id, metadata, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := executor.ExecContext(ctx, statement,
		actorID, actorUsername, effectiveID, effectiveUsername,
		event.Action, resourceType, resourceID, metadata, event.CreatedAt.UTC(),
	); err != nil {
		return fmt.Errorf("append audit event %q: %w", event.Action, err)
	}
	return nil
}

func validateIdentity(label string, identity *Identity) error {
	if identity == nil {
		return nil
	}
	if identity.UserID == 0 || strings.TrimSpace(identity.Username) == "" {
		return fmt.Errorf("audit %s identity must have user ID and username", label)
	}
	return nil
}

func knownAction(action Action) bool {
	switch action {
	case ActionAuthLogin,
		ActionAuthLogout,
		ActionAuthRegistration,
		ActionImpersonationStarted,
		ActionImpersonationStopped,
		ActionUserCreated,
		ActionUserProfileUpdated,
		ActionUserRoleChanged,
		ActionUserActivated,
		ActionUserDeactivated,
		ActionUserPasswordReset,
		ActionRoleCreated,
		ActionRoleUpdated,
		ActionRoleDeleted,
		ActionRolePermissionsUpdated,
		ActionAdminBootstrap,
		ActionIngestionRunSubmitted,
		ActionIngestionRunAllSubmitted,
		ActionIngestionCancellationRequested,
		ActionIngestionAbandonedRecovered,
		ActionSourceStateChanged,
		ActionScheduleCreated,
		ActionScheduleUpdated,
		ActionScheduleStateChanged,
		ActionReportDatasourceCreated,
		ActionReportDatasourceUpdated,
		ActionReportDatasourceStateChanged,
		ActionReportDatasourceTested,
		ActionReportTemplateCreated,
		ActionReportTemplateUpdated,
		ActionReportTemplateStateChanged,
		ActionReportTemplateAccessChanged,
		ActionReportExecuted,
		ActionReportTemplateQueryTested,
		ActionReportTemplateOptionsTested,
		ActionReportExportSubmitted,
		ActionReportExportDownloaded:
		return true
	default:
		return false
	}
}
