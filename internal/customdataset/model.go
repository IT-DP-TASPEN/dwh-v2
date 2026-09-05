package customdataset

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	MaxUploadBytes = int64(150_000_000)
	MaxDataRows    = uint64(1_000_000)
	MaxColumns     = 50
	MaxDiagnostics = 100
	PreviewRows    = 25
	InferenceRows  = 1000
)

var (
	ErrNotFound      = errors.New("custom dataset not found")
	ErrConflict      = errors.New("custom dataset changed")
	ErrInvalid       = errors.New("invalid custom dataset")
	ErrInactive      = errors.New("custom dataset is not active")
	ErrClaimLost     = errors.New("custom dataset import ownership lost")
	ErrLeaseUnproven = errors.New("custom dataset import ownership could not be verified")
)

type InfrastructureError struct{ Reason string }

func (err InfrastructureError) Error() string { return err.Reason }

type DatasetStatus string

const (
	DatasetProvisioning DatasetStatus = "provisioning"
	DatasetActive       DatasetStatus = "active"
	DatasetArchived     DatasetStatus = "archived"
)

type ImportStatus string

const (
	ImportQueued    ImportStatus = "queued"
	ImportRunning   ImportStatus = "running"
	ImportSucceeded ImportStatus = "succeeded"
	ImportFailed    ImportStatus = "failed"
)

type ImportMode string

const (
	ModeReplace ImportMode = "replace"
	ModeAppend  ImportMode = "append"
)

type LogicalType string

const (
	TypeText     LogicalType = "text"
	TypeInteger  LogicalType = "integer"
	TypeDecimal  LogicalType = "decimal"
	TypeDate     LogicalType = "date"
	TypeDateTime LogicalType = "datetime"
	TypeBoolean  LogicalType = "boolean"
)

type Delimiter string

const (
	DelimiterComma     Delimiter = "comma"
	DelimiterSemicolon Delimiter = "semicolon"
	DelimiterTab       Delimiter = "tab"
)

func (delimiter Delimiter) Rune() (rune, error) {
	switch delimiter {
	case DelimiterComma:
		return ',', nil
	case DelimiterSemicolon:
		return ';', nil
	case DelimiterTab:
		return '\t', nil
	default:
		return 0, fmt.Errorf("%w: unsupported delimiter", ErrInvalid)
	}
}

type Upload struct {
	ID               uint64     `db:"id"`
	StorageKey       string     `db:"storage_key"`
	OriginalFilename string     `db:"original_filename"`
	ByteSize         uint64     `db:"byte_size"`
	SHA256           []byte     `db:"sha256"`
	Status           string     `db:"status"`
	Revision         uint64     `db:"revision"`
	CreatedByUserID  uint64     `db:"created_by_user_id"`
	RetainedAt       *time.Time `db:"retained_at"`
	ExpiresAt        *time.Time `db:"expires_at"`
	CreatedAt        time.Time  `db:"created_at"`
}

type Dataset struct {
	ID                  uint64        `db:"id"`
	Name                string        `db:"name"`
	Description         string        `db:"description"`
	Status              DatasetStatus `db:"status"`
	Revision            uint64        `db:"revision"`
	SchemaRevision      uint64        `db:"schema_revision"`
	CurrentGenerationID *uint64       `db:"current_generation_id"`
	RowCount            uint64        `db:"row_count"`
	CreatedByUserID     uint64        `db:"created_by_user_id"`
	UpdatedByUserID     uint64        `db:"updated_by_user_id"`
	ActivatedAt         *time.Time    `db:"activated_at"`
	ArchivedAt          *time.Time    `db:"archived_at"`
	CreatedAt           time.Time     `db:"created_at"`
	UpdatedAt           time.Time     `db:"updated_at"`
}

func (dataset Dataset) TableName() string { return fmt.Sprintf("custom_dataset_%d", dataset.ID) }
func (dataset Dataset) ViewName() string  { return fmt.Sprintf("custom_dataset_view_%d", dataset.ID) }

type Column struct {
	DatasetID    uint64      `db:"dataset_id"`
	Ordinal      uint16      `db:"ordinal"`
	DisplayName  string      `db:"display_name"`
	QueryName    string      `db:"query_name"`
	PhysicalName string      `db:"physical_name"`
	LogicalType  LogicalType `db:"logical_type"`
	DateFormat   *string     `db:"date_format"`
}

type Import struct {
	ID                   uint64       `db:"id"`
	DatasetID            uint64       `db:"dataset_id"`
	UploadID             uint64       `db:"upload_id"`
	Delimiter            Delimiter    `db:"delimiter"`
	HeaderRecordNumber   uint64       `db:"header_record_number"`
	Mode                 ImportMode   `db:"mode"`
	SchemaRevision       uint64       `db:"schema_revision"`
	GenerationID         *uint64      `db:"generation_id"`
	Status               ImportStatus `db:"status"`
	Phase                string       `db:"phase"`
	Attempt              uint32       `db:"attempt"`
	PublishedAttempt     *uint32      `db:"published_attempt"`
	OwnerID              *string      `db:"owner_id"`
	ClaimedAt            *time.Time   `db:"claimed_at"`
	HeartbeatAt          *time.Time   `db:"heartbeat_at"`
	SourceRecords        uint64       `db:"source_records"`
	StagedRows           uint64       `db:"staged_rows"`
	FailureClass         *string      `db:"failure_class"`
	FailureMessage       *string      `db:"failure_message"`
	Diagnostics          []byte       `db:"diagnostics"`
	DiagnosticsTruncated bool         `db:"diagnostics_truncated"`
	SubmittedByUserID    uint64       `db:"submitted_by_user_id"`
	StartedAt            *time.Time   `db:"started_at"`
	FinishedAt           *time.Time   `db:"finished_at"`
	CreatedAt            time.Time    `db:"created_at"`
	UpdatedAt            time.Time    `db:"updated_at"`
}

func (value Import) DiagnosticRows() []Diagnostic {
	var rows []Diagnostic
	_ = json.Unmarshal(value.Diagnostics, &rows)
	return rows
}

type Diagnostic struct {
	Record   uint64 `json:"record"`
	Column   string `json:"column,omitempty"`
	Expected string `json:"expected,omitempty"`
	Value    string `json:"value,omitempty"`
	Reason   string `json:"reason"`
}

type Preview struct {
	Header      []string
	QueryNames  []string
	Suggestions []ColumnSuggestion
	Rows        [][]string
	ScannedRows uint64
}

type ColumnSuggestion struct {
	Type       LogicalType
	DateFormat string
}

type StoredFile struct {
	Key, OriginalName string
	Size              int64
	SHA256            [32]byte
}

type SampleCell struct {
	Value string
	Null  bool
}
