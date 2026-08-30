package auditlogs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
)

const PageSize = 50

type Record struct {
	ID                uint64
	ActorUserID       *uint64
	ActorUsername     string
	EffectiveUserID   *uint64
	EffectiveUsername string
	Action            string
	ResourceType      string
	ResourceID        *uint64
	Metadata          []byte
	CreatedAt         time.Time
}

type Page struct {
	Rows       []Record
	Action     string
	Pagination pagination.Page
}

type IdentityView struct {
	Label    string
	UserID   *uint64
	Username string
}

type ReportingView struct {
	ReportTemplateID      uint64
	ReportName            string
	ReportRevision        uint64
	DatasourceID          uint64
	DatasourceName        string
	ExecutionMode         string
	Draft                 bool
	Parameters            audit.ReportParametersMetadata
	HasParameters         bool
	ParametersTitle       string
	Outcome               string
	FailureStage          string
	FailureClass          string
	ReturnedRowCount      *int
	ResultTruncated       *bool
	ResultState           string
	TruncationReason      string
	DurationMS            int64
	TargetKey             string
	TargetLabel           string
	OptionState           string
	OptionCount           *int
	ExportJobID           uint64
	ExportRequesterUserID uint64
	ExportAccessPath      string
	ArtifactName          string
	ArtifactType          string
}

func (record Record) Actor() IdentityView {
	return identityView(record.Action, true, record.ActorUserID, record.ActorUsername)
}

func (record Record) Effective() IdentityView {
	return identityView(record.Action, false, record.EffectiveUserID, record.EffectiveUsername)
}

func (record Record) IsImpersonated() bool {
	if record.ActorUserID != nil && record.EffectiveUserID != nil {
		return *record.ActorUserID != *record.EffectiveUserID
	}
	return record.ActorUsername != "" && record.EffectiveUsername != "" && record.ActorUsername != record.EffectiveUsername
}

func (record Record) ResourceLabel() string {
	if record.ResourceType == "" || record.ResourceID == nil {
		return "—"
	}
	return fmt.Sprintf("%s #%d", record.ResourceType, *record.ResourceID)
}

func (record Record) MetadataText() string {
	if len(record.Metadata) == 0 {
		return "—"
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, record.Metadata, "", "  ") == nil {
		return pretty.String()
	}
	return string(record.Metadata)
}

func (record Record) Reporting() *ReportingView {
	view := &ReportingView{ParametersTitle: "Parameters"}
	switch audit.Action(record.Action) {
	case audit.ActionReportExecuted, audit.ActionReportTemplateQueryTested:
		var metadata audit.ReportExecutionMetadata
		if json.Unmarshal(record.Metadata, &metadata) != nil {
			return nil
		}
		if metadata.ReportTemplateID == 0 {
			return nil
		}
		view.ReportTemplateID, view.ReportName, view.ReportRevision = metadata.ReportTemplateID, metadata.ReportName, metadata.ReportRevision
		view.DatasourceID, view.DatasourceName = metadata.DatasourceID, metadata.DatasourceName
		view.ExecutionMode, view.Draft, view.Parameters, view.HasParameters = metadata.ExecutionMode, metadata.Draft, metadata.Parameters, true
		view.Outcome, view.FailureStage, view.FailureClass = metadata.Outcome, metadata.FailureStage, metadata.FailureClass
		view.ReturnedRowCount, view.ResultTruncated, view.TruncationReason = metadata.ReturnedRowCount, metadata.ResultTruncated, metadata.TruncationReason
		if metadata.ResultTruncated != nil {
			view.ResultState = "Complete"
			if *metadata.ResultTruncated {
				view.ResultState = "Truncated"
			}
		}
		view.DurationMS = metadata.ExecutionDuration
	case audit.ActionReportTemplateOptionsTested:
		var metadata audit.ReportOptionsTestMetadata
		if json.Unmarshal(record.Metadata, &metadata) != nil {
			return nil
		}
		if metadata.ReportTemplateID == 0 {
			return nil
		}
		view.ReportTemplateID, view.ReportName, view.ReportRevision = metadata.ReportTemplateID, metadata.ReportName, metadata.ReportRevision
		view.DatasourceID, view.DatasourceName = metadata.DatasourceID, metadata.DatasourceName
		view.Draft, view.Parameters, view.HasParameters, view.ParametersTitle = metadata.Draft, metadata.Parameters, true, "Upstream parameters"
		view.Outcome, view.FailureStage, view.FailureClass = metadata.Outcome, metadata.FailureStage, metadata.FailureClass
		view.TargetKey, view.TargetLabel, view.OptionState, view.OptionCount = metadata.TargetKey, metadata.TargetLabel, metadata.OptionState, metadata.OptionCount
		view.DurationMS = metadata.ExecutionDuration
	case audit.ActionReportExportSubmitted:
		var metadata audit.ReportExportSubmittedMetadata
		if json.Unmarshal(record.Metadata, &metadata) != nil {
			return nil
		}
		if metadata.ReportTemplateID == 0 {
			return nil
		}
		view.ReportTemplateID, view.ReportName, view.ReportRevision = metadata.ReportTemplateID, metadata.ReportName, metadata.ReportRevision
		view.DatasourceID, view.DatasourceName = metadata.DatasourceID, metadata.DatasourceName
		view.Parameters, view.HasParameters, view.Outcome, view.ExportJobID = metadata.Parameters, true, metadata.Outcome, metadata.ExportJobID
	case audit.ActionReportExportDownloaded:
		var metadata audit.ReportExportDownloadedMetadata
		if json.Unmarshal(record.Metadata, &metadata) != nil {
			return nil
		}
		if metadata.ReportTemplateID == 0 {
			return nil
		}
		view.ReportTemplateID, view.ReportName, view.DatasourceID = metadata.ReportTemplateID, metadata.ReportName, metadata.DatasourceID
		view.ExportJobID, view.ExportRequesterUserID, view.ExportAccessPath = metadata.ExportJobID, metadata.SubmittedByUserID, metadata.AccessPath
		view.ArtifactName, view.ArtifactType = metadata.ArtifactName, metadata.ArtifactType
	default:
		return nil
	}
	return view
}

func identityView(action string, actor bool, id *uint64, username string) IdentityView {
	if username != "" {
		return IdentityView{Label: "@" + username, UserID: id, Username: username}
	}
	if actor {
		switch audit.Action(action) {
		case audit.ActionAuthRegistration:
			return IdentityView{Label: "Public"}
		case audit.ActionAdminBootstrap:
			return IdentityView{Label: "System"}
		}
	}
	return IdentityView{Label: "—", UserID: id}
}
