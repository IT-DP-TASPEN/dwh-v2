package reportexport

import (
	"encoding/json"
	"time"

	"github.com/ibldzn/go-admin/internal/reporting"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

type Job struct {
	ID                uint64          `db:"id"`
	ReportID          uint64          `db:"report_id"`
	ReportName        string          `db:"report_name"`
	SQLText           string          `db:"sql_text"`
	DatasourceID      uint64          `db:"datasource_id"`
	ParameterVersion  uint16          `db:"parameter_version"`
	ParametersJSON    json.RawMessage `db:"parameters_json"`
	SubmittedByUserID uint64          `db:"submitted_by_user_id"`
	Status            Status          `db:"status"`
	Attempt           uint32          `db:"attempt"`
	OwnerID           *string         `db:"owner_id"`
	ClaimedAt         *time.Time      `db:"claimed_at"`
	HeartbeatAt       *time.Time      `db:"heartbeat_at"`
	ProgressRows      uint64          `db:"progress_rows"`
	CurrentPart       uint32          `db:"current_part"`
	FinalParts        *uint32         `db:"final_parts"`
	TotalRows         *uint64         `db:"total_rows"`
	ArtifactPath      *string         `db:"artifact_path"`
	ArtifactName      *string         `db:"artifact_name"`
	ArtifactType      *string         `db:"artifact_type"`
	ArtifactSize      *uint64         `db:"artifact_size"`
	ArtifactExpiresAt *time.Time      `db:"artifact_expires_at"`
	ArtifactDeletedAt *time.Time      `db:"artifact_deleted_at"`
	FailureClass      *string         `db:"failure_class"`
	FailureMessage    *string         `db:"failure_message"`
	CreatedAt         time.Time       `db:"created_at"`
	StartedAt         *time.Time      `db:"started_at"`
	FinishedAt        *time.Time      `db:"finished_at"`
	UpdatedAt         time.Time       `db:"updated_at"`
}

type Snapshot struct {
	Version    uint16                          `json:"version"`
	Parameters []reporting.Parameter           `json:"parameters"`
	Input      map[string]reporting.InputValue `json:"input"`
}

type Artifact struct {
	RelativePath string
	Name         string
	Type         string
	Size         uint64
	Parts        uint32
	Rows         uint64
}

func (job Job) FinishedAtValue() time.Time {
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	return job.UpdatedAt
}
