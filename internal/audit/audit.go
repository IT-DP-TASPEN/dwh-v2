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
	ActionIngestionCancellationRequested Action = "ingestion.cancellation_requested"
	ActionIngestionAbandonedRecovered    Action = "ingestion.abandoned_recovered"
	ActionReportDatasourceCreated        Action = "report_datasource.created"
	ActionReportDatasourceUpdated        Action = "report_datasource.updated"
	ActionReportDatasourceStateChanged   Action = "report_datasource.state_changed"
	ActionReportDatasourceTested         Action = "report_datasource.tested"
	ActionReportTemplateCreated          Action = "report_template.created"
	ActionReportTemplateUpdated          Action = "report_template.updated"
	ActionReportTemplateStateChanged     Action = "report_template.state_changed"
	ActionReportTemplateAccessChanged    Action = "report_template.access_changed"
	ActionReportExecuted                 Action = "report.executed"
	ActionReportExportSubmitted          Action = "report_export.submitted"
	ActionReportExportDownloaded         Action = "report_export.downloaded"
)

type ResourceType string

const (
	ResourceUser             ResourceType = "user"
	ResourceRole             ResourceType = "role"
	ResourceIngestionRun     ResourceType = "ingestion_run"
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
		event.Resource != ResourceReportDatasource && event.Resource != ResourceReportTemplate && event.Resource != ResourceReportExport {
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
		ActionIngestionCancellationRequested,
		ActionIngestionAbandonedRecovered,
		ActionReportDatasourceCreated,
		ActionReportDatasourceUpdated,
		ActionReportDatasourceStateChanged,
		ActionReportDatasourceTested,
		ActionReportTemplateCreated,
		ActionReportTemplateUpdated,
		ActionReportTemplateStateChanged,
		ActionReportTemplateAccessChanged,
		ActionReportExecuted,
		ActionReportExportSubmitted,
		ActionReportExportDownloaded:
		return true
	default:
		return false
	}
}
