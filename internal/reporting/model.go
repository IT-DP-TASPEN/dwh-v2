package reporting

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound           = errors.New("reporting record not found")
	ErrConflict           = errors.New("reporting record changed")
	ErrForbidden          = errors.New("report access denied")
	ErrInactive           = errors.New("reporting record is not active")
	ErrInvalid            = errors.New("invalid report definition")
	ErrMultipleResultSets = errors.New("query returned more than one result set")
	ErrClaimLost          = errors.New("export claim ownership lost")
	ErrLeaseUnproven      = errors.New("export lease could not be proven")
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusArchived Status = "archived"
)

type TLSPolicy string

const (
	TLSRequired TLSPolicy = "required"
	TLSDisabled TLSPolicy = "disabled"
)

type Datasource struct {
	ID                 uint64    `db:"id"`
	Name               string    `db:"name"`
	Description        string    `db:"description"`
	Host               string    `db:"host"`
	Port               uint16    `db:"port"`
	DatabaseName       string    `db:"database_name"`
	Username           string    `db:"username"`
	PasswordCiphertext []byte    `db:"password_ciphertext"`
	TLSPolicy          TLSPolicy `db:"tls_policy"`
	Status             Status    `db:"status"`
	Revision           uint64    `db:"revision"`
	CreatedByUserID    uint64    `db:"created_by_user_id"`
	UpdatedByUserID    uint64    `db:"updated_by_user_id"`
	CreatedAt          time.Time `db:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"`
}

type ParameterType string

const (
	ParameterText           ParameterType = "text"
	ParameterInteger        ParameterType = "integer"
	ParameterDecimal        ParameterType = "decimal"
	ParameterDate           ParameterType = "date"
	ParameterDatetime       ParameterType = "datetime"
	ParameterBoolean        ParameterType = "boolean"
	ParameterSingleOption   ParameterType = "single_option"
	ParameterMultipleOption ParameterType = "multiple_option"
)

type ParameterOption struct {
	ID           uint64 `db:"id"`
	ParameterID  uint64 `db:"parameter_id"`
	Value        string `db:"option_value"`
	Label        string `db:"label"`
	DisplayOrder uint16 `db:"display_order"`
}

type Parameter struct {
	ID           uint64          `db:"id"`
	ReportID     uint64          `db:"report_id"`
	Key          string          `db:"parameter_key"`
	Label        string          `db:"label"`
	Type         ParameterType   `db:"parameter_type"`
	Required     bool            `db:"required"`
	DefaultValue json.RawMessage `db:"default_value"`
	DisplayOrder uint16          `db:"display_order"`
	Options      []ParameterOption
}

type Template struct {
	ID               uint64    `db:"id"`
	Name             string    `db:"name"`
	Description      string    `db:"description"`
	DatasourceID     uint64    `db:"datasource_id"`
	DatasourceName   string    `db:"datasource_name"`
	DatasourceStatus Status    `db:"datasource_status"`
	SQLText          string    `db:"sql_text"`
	Status           Status    `db:"status"`
	Revision         uint64    `db:"revision"`
	CreatedByUserID  uint64    `db:"created_by_user_id"`
	UpdatedByUserID  uint64    `db:"updated_by_user_id"`
	CreatedAt        time.Time `db:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"`
	Parameters       []Parameter
}

type InputValue struct {
	Present bool
	Values  []string
}

type NormalizedValue struct {
	Scalar any
	Multi  []any
}

type Column struct {
	Name         string `json:"name"`
	DatabaseType string `json:"database_type,omitempty"`
	Nullable     *bool  `json:"nullable,omitempty"`
}

type Cell struct {
	Text          string `json:"text,omitempty"`
	Null          bool   `json:"null,omitempty"`
	Binary        bool   `json:"binary,omitempty"`
	Preview       bool   `json:"preview,omitempty"`
	OriginalBytes int    `json:"original_bytes,omitempty"`
}

type InteractiveResult struct {
	Columns          []Column `json:"columns"`
	Rows             [][]Cell `json:"rows"`
	Truncated        bool     `json:"truncated"`
	TruncationReason string   `json:"truncation_reason,omitempty"`
	CellPreviews     int      `json:"cell_previews,omitempty"`
	EncodedBytes     int      `json:"encoded_bytes"`
}
