package ingestionrun

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	ErrJobBusy        = errors.New("job already queued or running")
	ErrSourceDisabled = errors.New("source is disabled")
	ErrTransition     = errors.New("run state transition rejected")
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
}

type Progress struct {
	Total, Started, Succeeded, Failed, Rows uint64
	Step                                    string
}

type SafeError struct {
	Class, Message, Step string
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
