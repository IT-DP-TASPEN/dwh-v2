package scheduler

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

const (
	DefaultTimezone                  = "Asia/Jakarta"
	PolicyPreviousCalendarDay        = "previous_calendar_day_jakarta"
	PolicyDetailLiveSnapshot         = "detail_live_snapshot"
	policyVersionV1           uint16 = 1
	defaultLookbackDays              = 3
)

var (
	ErrConflict          = errors.New("schedule revision conflict")
	ErrBacklog           = errors.New("schedule has unresolved backlog")
	ErrArchived          = errors.New("schedule is archived")
	ErrInvalidDefinition = errors.New("invalid schedule definition")
	strictParser         = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

type Policy struct {
	Kind     string
	Version  uint16
	Payload  []byte
	Checksum [32]byte
}

type Definition struct {
	Name, JobKey, CronExpression, Timezone string
	Policy                                 Policy
}

type CreateInput struct {
	Definition
	Enabled bool
	ActorID *uint64
}

type UpdateInput struct {
	Definition
	ExpectedRevision uint64
	ActorID          *uint64
}

type Schedule struct {
	ID                                        uint64
	Definition                                Definition
	Enabled                                   bool
	NextRunAt, SchedulerNotBefore, ArchivedAt *time.Time
	Revision                                  uint64
}

type cronDefinition struct {
	schedule cron.Schedule
	location *time.Location
}

func PreviousCalendarDayPolicy() Policy {
	policy, err := canonicalPolicy(PolicyPreviousCalendarDay, policyVersionV1, []byte("{}"))
	if err != nil {
		panic(err)
	}
	return policy
}

func DetailLiveSnapshotPolicy() Policy {
	policy, err := canonicalPolicy(PolicyDetailLiveSnapshot, policyVersionV1, []byte("{}"))
	if err != nil {
		panic(err)
	}
	return policy
}

func canonicalPolicy(kind string, version uint16, payload []byte) (Policy, error) {
	if (kind != PolicyPreviousCalendarDay && kind != PolicyDetailLiveSnapshot) || version != policyVersionV1 {
		return Policy{}, fmt.Errorf("%w: unsupported policy %q version %d", ErrInvalidDefinition, kind, version)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value struct{}
	if err := decoder.Decode(&value); err != nil {
		return Policy{}, fmt.Errorf("%w: policy payload: %v", ErrInvalidDefinition, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("%w: policy payload has trailing data", ErrInvalidDefinition)
	}
	canonicalPayload, _ := json.Marshal(value)
	envelope, _ := json.Marshal(struct {
		Kind    string          `json:"kind"`
		Version uint16          `json:"version"`
		Payload json.RawMessage `json:"payload"`
	}{kind, version, canonicalPayload})
	return Policy{Kind: kind, Version: version, Payload: canonicalPayload, Checksum: sha256.Sum256(envelope)}, nil
}

func validatePolicy(policy Policy) (Policy, error) {
	canonical, err := canonicalPolicy(policy.Kind, policy.Version, policy.Payload)
	if err != nil {
		return Policy{}, err
	}
	if policy.Checksum != ([32]byte{}) && policy.Checksum != canonical.Checksum {
		return Policy{}, fmt.Errorf("%w: policy checksum mismatch", ErrInvalidDefinition)
	}
	return canonical, nil
}

func parseCron(expression, timezone string, reference time.Time) (cronDefinition, error) {
	expression, timezone = strings.TrimSpace(expression), strings.TrimSpace(timezone)
	if expression == "" || len(expression) > 128 || strings.HasPrefix(expression, "@") || strings.Contains(expression, "CRON_TZ=") || strings.Contains(expression, "TZ=") {
		return cronDefinition{}, fmt.Errorf("%w: strict five-field cron is required", ErrInvalidDefinition)
	}
	if timezone == "" || len(timezone) > 64 || timezone == "Local" || strings.HasPrefix(timezone, "+") || strings.HasPrefix(timezone, "-") {
		return cronDefinition{}, fmt.Errorf("%w: IANA timezone is required", ErrInvalidDefinition)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return cronDefinition{}, fmt.Errorf("%w: load timezone %q: %v", ErrInvalidDefinition, timezone, err)
	}
	schedule, err := strictParser.Parse(expression)
	if err != nil {
		return cronDefinition{}, fmt.Errorf("%w: parse cron: %v", ErrInvalidDefinition, err)
	}
	definition := cronDefinition{schedule: schedule, location: location}
	if definition.Next(reference).IsZero() {
		return cronDefinition{}, fmt.Errorf("%w: cron has no future occurrence", ErrInvalidDefinition)
	}
	return definition, nil
}

func (definition cronDefinition) Next(after time.Time) time.Time {
	next := definition.schedule.Next(after.In(definition.location))
	if next.IsZero() {
		return time.Time{}
	}
	return next.UTC()
}

func (definition cronDefinition) IsOccurrence(value time.Time) bool {
	value = value.UTC()
	return value.Nanosecond() == 0 && definition.Next(value.Add(-time.Minute)).Equal(value)
}

func validateDefinition(catalog ingestion.Catalog, definition Definition, reference time.Time) (Definition, cronDefinition, error) {
	definition.Name = strings.TrimSpace(definition.Name)
	definition.JobKey = strings.TrimSpace(definition.JobKey)
	definition.CronExpression = strings.TrimSpace(definition.CronExpression)
	definition.Timezone = strings.TrimSpace(definition.Timezone)
	if definition.Name == "" || len([]rune(definition.Name)) > 128 {
		return Definition{}, cronDefinition{}, fmt.Errorf("%w: schedule name is required and limited to 128 characters", ErrInvalidDefinition)
	}
	job, found := catalog.Find(definition.JobKey)
	if !found {
		return Definition{}, cronDefinition{}, fmt.Errorf("%w: unknown job %q", ErrInvalidDefinition, definition.JobKey)
	}
	policy, err := validatePolicy(definition.Policy)
	if err != nil {
		return Definition{}, cronDefinition{}, err
	}
	wantPolicy := PolicyPreviousCalendarDay
	if job.DateStrategy == ingestion.NoDate {
		wantPolicy = PolicyDetailLiveSnapshot
	}
	if policy.Kind != wantPolicy {
		return Definition{}, cronDefinition{}, fmt.Errorf("%w: job %q requires policy %q", ErrInvalidDefinition, job.Key, wantPolicy)
	}
	parsed, err := parseCron(definition.CronExpression, definition.Timezone, reference)
	if err != nil {
		return Definition{}, cronDefinition{}, err
	}
	definition.Policy = policy
	return definition, parsed, nil
}

func parametersForOccurrence(job ingestion.JobDefinition, scheduledFor time.Time) (ingestionrun.Parameters, error) {
	date := ingestion.ResolvePreviousCalendarDayJakarta(scheduledFor)
	switch job.DateStrategy {
	case ingestion.RangeCapable:
		return ingestionrun.NewRangeExecution(job.Key, date, date)
	case ingestion.SingleDate:
		if job.Category == ingestion.CategoryFixed {
			return ingestionrun.NewDateSeriesExecution(job.Key, date, date)
		}
		return ingestionrun.NewMaintenanceSeriesExecution(job.Key, date, date, defaultLookbackDays)
	case ingestion.NoDate:
		// Fincloud exposes current detail only; missed historical detail states
		// cannot be reconstructed and are therefore one live obligation.
		return ingestionrun.NewLiveSnapshotExecution(job.Key)
	default:
		return ingestionrun.Parameters{}, fmt.Errorf("job %s has no scheduler date contract", job.Key)
	}
}

func retryDelay(attempt uint32) time.Duration {
	if attempt == 0 {
		return 0
	}
	if attempt >= 6 {
		return 30 * time.Minute
	}
	return time.Minute << (attempt - 1)
}
