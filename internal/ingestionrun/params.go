package ingestionrun

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

type ParameterKind string

const (
	FixedRangeV1            ParameterKind = "fixed_range_v1"
	FixedDateSeriesV1       ParameterKind = "fixed_date_series_v1"
	MaintenanceDateSeriesV1 ParameterKind = "maintenance_date_series_v1"
	DetailLiveSnapshotV1    ParameterKind = "detail_live_snapshot_v1"
	RunAllRangeV1           ParameterKind = "run_all_range_v1"
)

type Parameters struct {
	Kind     ParameterKind
	Version  uint16
	JSON     []byte
	Checksum [32]byte
}

type Range struct {
	From ingestion.CalendarDate `json:"from"`
	To   ingestion.CalendarDate `json:"to"`
}

type DateSeries struct {
	Dates []ingestion.CalendarDate `json:"dates"`
}

type MaintenanceSeries struct {
	Dates        []ingestion.CalendarDate `json:"dates"`
	LookbackDays int                      `json:"lookback_days"`
}

func NewRangeExecution(jobKey string, from, to ingestion.CalendarDate) (Parameters, error) {
	job, err := requireJob(jobKey, ingestion.RangeCapable, ingestion.CategoryFixed)
	if err != nil {
		return Parameters{}, err
	}
	_ = job
	value, err := validRange(from, to)
	if err != nil {
		return Parameters{}, err
	}
	return encode(FixedRangeV1, value)
}

func NewDateSeriesExecution(jobKey string, from, to ingestion.CalendarDate) (Parameters, error) {
	if _, err := requireJob(jobKey, ingestion.SingleDate, ingestion.CategoryFixed); err != nil {
		return Parameters{}, err
	}
	dates, err := inclusiveDates(from, to)
	if err != nil {
		return Parameters{}, err
	}
	return encode(FixedDateSeriesV1, DateSeries{Dates: dates})
}

func NewMaintenanceSeriesExecution(jobKey string, from, to ingestion.CalendarDate, lookback int) (Parameters, error) {
	job, err := requireJob(jobKey, ingestion.SingleDate, "")
	if err != nil {
		return Parameters{}, err
	}
	if job.Category != ingestion.CategoryEOD && job.Category != ingestion.CategoryCBR {
		return Parameters{}, fmt.Errorf("job %s is not maintenance", jobKey)
	}
	if lookback < 0 || lookback > 3 {
		return Parameters{}, fmt.Errorf("maintenance lookback must be between 0 and 3")
	}
	dates, err := inclusiveDates(from, to)
	if err != nil {
		return Parameters{}, err
	}
	return encode(MaintenanceDateSeriesV1, MaintenanceSeries{Dates: dates, LookbackDays: lookback})
}

func NewLiveSnapshotExecution(jobKey string) (Parameters, error) {
	if _, err := requireJob(jobKey, ingestion.NoDate, ingestion.CategoryDetail); err != nil {
		return Parameters{}, err
	}
	return encode(DetailLiveSnapshotV1, struct{}{})
}

func NewRunAllRange(from, to ingestion.CalendarDate) (Parameters, error) {
	value, err := validRange(from, to)
	if err != nil {
		return Parameters{}, err
	}
	return encode(RunAllRangeV1, value)
}

func (parameters Parameters) Validate(job ingestion.JobDefinition) error {
	if parameters.Version != 1 || len(parameters.JSON) == 0 || sha256.Sum256(parameters.JSON) != parameters.Checksum || !json.Valid(parameters.JSON) {
		return fmt.Errorf("invalid canonical parameter envelope")
	}
	switch parameters.Kind {
	case FixedRangeV1:
		if job.Category != ingestion.CategoryFixed || job.DateStrategy != ingestion.RangeCapable {
			return fmt.Errorf("job %s does not accept range parameters", job.Key)
		}
		var value Range
		return decodeCanonical(parameters.JSON, &value, func() error { _, err := validRange(value.From, value.To); return err })
	case FixedDateSeriesV1:
		if job.Category != ingestion.CategoryFixed || job.DateStrategy != ingestion.SingleDate {
			return fmt.Errorf("job %s does not accept date-series parameters", job.Key)
		}
		var value DateSeries
		return decodeCanonical(parameters.JSON, &value, func() error { return validateDateSeries(value.Dates) })
	case MaintenanceDateSeriesV1:
		if job.Category != ingestion.CategoryEOD && job.Category != ingestion.CategoryCBR {
			return fmt.Errorf("job %s does not accept maintenance parameters", job.Key)
		}
		var value MaintenanceSeries
		return decodeCanonical(parameters.JSON, &value, func() error {
			if value.LookbackDays < 0 || value.LookbackDays > 3 {
				return fmt.Errorf("maintenance lookback must be between 0 and 3")
			}
			return validateDateSeries(value.Dates)
		})
	case DetailLiveSnapshotV1:
		if job.Category != ingestion.CategoryDetail || job.DateStrategy != ingestion.NoDate || !bytes.Equal(parameters.JSON, []byte("{}")) {
			return fmt.Errorf("job %s does not accept live-snapshot parameters", job.Key)
		}
		return nil
	default:
		return fmt.Errorf("unsupported parameter kind %q", parameters.Kind)
	}
}

func DecodeRange(parameters Parameters) (Range, error) {
	var value Range
	if parameters.Kind != FixedRangeV1 && parameters.Kind != RunAllRangeV1 {
		return value, fmt.Errorf("parameters are not a range")
	}
	err := decodeCanonical(parameters.JSON, &value, func() error { _, err := validRange(value.From, value.To); return err })
	return value, err
}

func DecodeDateSeries(parameters Parameters) (DateSeries, error) {
	var value DateSeries
	if parameters.Kind != FixedDateSeriesV1 {
		return value, fmt.Errorf("parameters are not a date series")
	}
	err := decodeCanonical(parameters.JSON, &value, func() error { return validateDateSeries(value.Dates) })
	return value, err
}

func DecodeMaintenanceSeries(parameters Parameters) (MaintenanceSeries, error) {
	var value MaintenanceSeries
	if parameters.Kind != MaintenanceDateSeriesV1 {
		return value, fmt.Errorf("parameters are not a maintenance date series")
	}
	err := decodeCanonical(parameters.JSON, &value, func() error {
		if value.LookbackDays < 0 || value.LookbackDays > 3 {
			return fmt.Errorf("invalid lookback")
		}
		return validateDateSeries(value.Dates)
	})
	return value, err
}

func encode(kind ParameterKind, value any) (Parameters, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Parameters{}, err
	}
	return Parameters{Kind: kind, Version: 1, JSON: data, Checksum: sha256.Sum256(data)}, nil
}

func decodeCanonical(data []byte, target any, validate func() error) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode canonical parameters: %w", err)
	}
	if err := validate(); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(data, canonical) {
		return fmt.Errorf("parameters are not canonical")
	}
	return nil
}

func requireJob(key string, strategy ingestion.DateStrategy, category ingestion.JobCategory) (ingestion.JobDefinition, error) {
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		return ingestion.JobDefinition{}, err
	}
	job, found := catalog.Find(key)
	if !found {
		return ingestion.JobDefinition{}, fmt.Errorf("unknown job %q", key)
	}
	if job.DateStrategy != strategy || (category != "" && job.Category != category) {
		return ingestion.JobDefinition{}, fmt.Errorf("job %s has incompatible date contract", key)
	}
	return job, nil
}

func validRange(from, to ingestion.CalendarDate) (Range, error) {
	if from.IsZero() || to.IsZero() || from.String() > to.String() {
		return Range{}, fmt.Errorf("valid inclusive date range is required")
	}
	return Range{From: from, To: to}, nil
}

func inclusiveDates(from, to ingestion.CalendarDate) ([]ingestion.CalendarDate, error) {
	if _, err := validRange(from, to); err != nil {
		return nil, err
	}
	dates := make([]ingestion.CalendarDate, 0)
	for date := from; date.String() <= to.String(); date = date.AddDays(1) {
		dates = append(dates, date)
	}
	return dates, nil
}

func validateDateSeries(dates []ingestion.CalendarDate) error {
	if len(dates) == 0 {
		return fmt.Errorf("date series is empty")
	}
	for index, date := range dates {
		if date.IsZero() || (index > 0 && date != dates[index-1].AddDays(1)) {
			return fmt.Errorf("date series must be ascending and contiguous")
		}
	}
	return nil
}
