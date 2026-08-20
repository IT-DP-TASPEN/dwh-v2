package ingestionrun

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

type Kind string
type Status string
type Trigger string

const (
	KindJob          Kind = "job"
	KindRunAllParent Kind = "run_all_parent"
	KindRunAllChild  Kind = "run_all_child"

	StatusPlanned            Status = "planned"
	StatusQueued             Status = "queued"
	StatusRunning            Status = "running"
	StatusSucceeded          Status = "succeeded"
	StatusFailed             Status = "failed"
	StatusSkipped            Status = "skipped"
	StatusCancelled          Status = "cancelled"
	StatusAbandoned          Status = "abandoned"
	StatusCompleted          Status = "completed"
	StatusCompletedWithSkips Status = "completed_with_skips"

	TriggerDirect    Trigger = "direct"
	TriggerScheduler Trigger = "scheduler"
	TriggerRunAll    Trigger = "run_all"
)

var (
	ErrJobBusy               = errors.New("job already queued or running")
	ErrSourceDisabled        = errors.New("source is disabled")
	ErrTransition            = errors.New("run state transition rejected")
	ErrCancellationRequested = errors.New("durable run cancellation requested")
	ErrCoordinatorShutdown   = errors.New("ingestion coordinator shutting down")
)

type Run struct {
	ID                uint64
	Kind              Kind
	ParentRunID       *uint64
	ChildPosition     *uint16
	JobKey            string
	Status            Status
	Parameters        Parameters
	Trigger           Trigger
	TriggerReference  string
	RequestedByUserID *uint64
	OwnerID           string
	HeartbeatAt       *time.Time
	SnapshotDate      ingestion.CalendarDate
	CancelRequested   bool
	Progress          Progress
	MapperDiagnostics *MapperDiagnostics
}

type Progress struct {
	Total, Started, Succeeded, Failed, Rows uint64
	Step                                    string
}

type SafeError struct {
	Class, Message, Step string
}

const MaxMapperDiagnosticGroups = 16

type MapperDiagnosticGroup struct {
	Class       string `json:"class"`
	Field       string `json:"field"`
	Category    string `json:"category"`
	Reason      string `json:"reason"`
	SafeMessage string `json:"safe_message"`
	Count       uint64 `json:"count"`
}

type MapperDiagnostics struct {
	TotalCount    uint64                  `json:"total_count"`
	OverflowCount uint64                  `json:"overflow_count"`
	Groups        []MapperDiagnosticGroup `json:"groups"`
}

func (diagnostics *MapperDiagnostics) Add(metadata ingestion.MapperMetadata) error {
	group := MapperDiagnosticGroup{
		Class: metadata.Class(), Field: metadata.Field(), Category: metadata.Category(), Reason: metadata.Reason(), SafeMessage: metadata.SafeMessage(),
	}
	if !ingestion.IsSafeMapperDiagnostic(group.Class, group.Field, group.Category, group.Reason, group.SafeMessage) {
		return fmt.Errorf("invalid mapper diagnostic metadata")
	}
	if diagnostics.TotalCount == math.MaxUint64 {
		return fmt.Errorf("mapper diagnostic count overflow")
	}
	diagnostics.TotalCount++
	for index := range diagnostics.Groups {
		if sameMapperDiagnostic(diagnostics.Groups[index], group) {
			if diagnostics.Groups[index].Count == math.MaxUint64 {
				return fmt.Errorf("mapper diagnostic group count overflow")
			}
			diagnostics.Groups[index].Count++
			diagnostics.sort()
			return diagnostics.validate()
		}
	}
	if len(diagnostics.Groups) < MaxMapperDiagnosticGroups {
		group.Count = 1
		diagnostics.Groups = append(diagnostics.Groups, group)
		diagnostics.sort()
	} else {
		if diagnostics.OverflowCount == math.MaxUint64 {
			return fmt.Errorf("mapper diagnostic overflow count overflow")
		}
		diagnostics.OverflowCount++
	}
	return diagnostics.validate()
}

func (diagnostics MapperDiagnostics) Marshal() ([]byte, error) {
	diagnostics.sort()
	if err := diagnostics.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(diagnostics)
}

func decodeMapperDiagnostics(data []byte) (*MapperDiagnostics, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var diagnostics MapperDiagnostics
	if err := json.Unmarshal(data, &diagnostics); err != nil {
		return nil, fmt.Errorf("decode mapper diagnostics: %w", err)
	}
	diagnostics.sort()
	if err := diagnostics.validate(); err != nil {
		return nil, err
	}
	return &diagnostics, nil
}

func (diagnostics *MapperDiagnostics) sort() {
	sort.Slice(diagnostics.Groups, func(left, right int) bool {
		a, b := diagnostics.Groups[left], diagnostics.Groups[right]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.SafeMessage < b.SafeMessage
	})
}

func (diagnostics MapperDiagnostics) validate() error {
	if len(diagnostics.Groups) > MaxMapperDiagnosticGroups {
		return fmt.Errorf("too many mapper diagnostic groups")
	}
	total := diagnostics.OverflowCount
	for index, group := range diagnostics.Groups {
		if !ingestion.IsSafeMapperDiagnostic(group.Class, group.Field, group.Category, group.Reason, group.SafeMessage) || group.Count == 0 {
			return fmt.Errorf("invalid mapper diagnostic group")
		}
		if index > 0 && sameMapperDiagnostic(diagnostics.Groups[index-1], group) {
			return fmt.Errorf("duplicate mapper diagnostic group")
		}
		if math.MaxUint64-total < group.Count {
			return fmt.Errorf("mapper diagnostic total overflow")
		}
		total += group.Count
	}
	if total != diagnostics.TotalCount {
		return fmt.Errorf("mapper diagnostic total mismatch")
	}
	return nil
}

func sameMapperDiagnostic(left, right MapperDiagnosticGroup) bool {
	return left.Class == right.Class && left.Field == right.Field && left.Category == right.Category && left.Reason == right.Reason && left.SafeMessage == right.SafeMessage
}

func NewOwnerID() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate coordinator owner identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func IsTerminal(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusSkipped, StatusCancelled, StatusAbandoned, StatusCompleted, StatusCompletedWithSkips:
		return true
	default:
		return false
	}
}
