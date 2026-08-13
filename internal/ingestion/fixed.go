package ingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

type LocationStrategy string
type AccountCodeStrategy string

const (
	SingleRequestAllLocationsEmpty LocationStrategy = "single_request_all_locations_empty"
	PerLocation                    LocationStrategy = "per_location"

	NoAccountCodeStrategy AccountCodeStrategy = "none"
	AllAccountCodes       AccountCodeStrategy = "all_account_codes"
)

type FixedDefinition struct {
	Key                 string
	Name                string
	FincloudReportName  string
	RequiredHeaders     []string
	LocationStrategy    LocationStrategy
	AccountCodeStrategy AccountCodeStrategy
	SnapshotDate        bool
	SourceLocationID    bool
	MaxChunkDays        int
}

type RequestDescriptor struct {
	ReportName       string
	Parameters       []string
	SourceLocationID string
	AccountCode      string
}

type FixedPlan struct {
	JobKey            string
	Range             FixedDateRangeParams
	ReplacementScope  ReplacementScope
	Members           []RequestDescriptor
	RequireAllMembers bool
}

type ReplacementScope struct {
	JobKey string
	From   CalendarDate
	To     CalendarDate
}

type FrozenLocations struct{ values []string }
type FrozenAccountCodes struct{ values []string }

func FreezeLocations(values []string) (FrozenLocations, error) {
	frozen, err := freezeNonblank(values, "location")
	return FrozenLocations{values: frozen}, err
}

func FreezeAccountCodes(values []string) (FrozenAccountCodes, error) {
	frozen, err := freezeNonblank(values, "account code")
	return FrozenAccountCodes{values: frozen}, err
}

func (frozen FrozenLocations) Values() []string    { return append([]string(nil), frozen.values...) }
func (frozen FrozenAccountCodes) Values() []string { return append([]string(nil), frozen.values...) }

func freezeNonblank(values []string, name string) ([]string, error) {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("captured %s set is empty", name)
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// BuildFixedPlan is pure: callers fetch and freeze completeness dimensions
// before calling it. It never performs source I/O.
func BuildFixedPlan(definition FixedDefinition, requested FixedDateRangeParams, frozenLocations FrozenLocations, frozenAccountCodes FrozenAccountCodes) (FixedPlan, error) {
	if requested.From.IsZero() || requested.To.IsZero() || requested.From.String() > requested.To.String() {
		return FixedPlan{}, fmt.Errorf("valid fixed report date range is required")
	}
	if definition.SnapshotDate && requested.From != requested.To {
		return FixedPlan{}, fmt.Errorf("%s requires one fixed snapshot date", definition.Key)
	}
	locations := frozenLocations.Values()
	accounts := frozenAccountCodes.Values()
	var members []RequestDescriptor
	switch {
	case definition.LocationStrategy == PerLocation:
		if len(locations) == 0 {
			return FixedPlan{}, fmt.Errorf("%s requires a frozen location set", definition.Key)
		}
		for _, location := range locations {
			members = append(members, descriptor(definition, requested, location, ""))
		}
	case definition.AccountCodeStrategy == AllAccountCodes:
		if len(accounts) == 0 {
			return FixedPlan{}, fmt.Errorf("%s requires a frozen account-code set", definition.Key)
		}
		for _, account := range accounts {
			members = append(members, descriptor(definition, requested, "", account))
		}
	default:
		members = []RequestDescriptor{descriptor(definition, requested, "", "")}
	}
	return FixedPlan{
		JobKey: definition.Key, Range: requested,
		ReplacementScope: ReplacementScope{JobKey: definition.Key, From: requested.From, To: requested.To},
		Members:          members, RequireAllMembers: true,
	}, nil
}

func BuildFixedSnapshotPlan(definition FixedDefinition, requested FixedSnapshotDateParams, frozenLocations FrozenLocations) (FixedPlan, error) {
	if !definition.SnapshotDate {
		return FixedPlan{}, fmt.Errorf("%s is not a fixed snapshot report", definition.Key)
	}
	return BuildFixedPlan(definition, FixedDateRangeParams{From: requested.Date, To: requested.Date}, frozenLocations, FrozenAccountCodes{})
}

func descriptor(definition FixedDefinition, requested FixedDateRangeParams, location, account string) RequestDescriptor {
	from, to := requested.From.String(), requested.To.String()
	var parameters []string
	switch definition.Key {
	case "cif_opening_report":
		parameters = []string{location, from, to}
	case "journal_transaction_report":
		parameters = []string{location, "%", from, to, "", ""}
	case "balance_sheet_report":
		parameters = []string{location, to}
	case "profit_loss_statement":
		parameters = []string{location, from, to}
	case "coa_movement_report":
		parameters = []string{account, from, to, location}
	case "fund_distribution_report":
		parameters = []string{location, "", "", "", from, to}
	case "vault_mutation_report":
		parameters = []string{location, from, to}
	case "teller_mutation_report":
		parameters = []string{location, "ALL", from, to}
	}
	return RequestDescriptor{ReportName: definition.FincloudReportName, Parameters: parameters, SourceLocationID: location, AccountCode: account}
}

type DateChunk struct{ From, To CalendarDate }

func ChunkDateRange(from, to CalendarDate, maxDays int) ([]DateChunk, error) {
	if from.IsZero() || to.IsZero() || from.String() > to.String() {
		return nil, fmt.Errorf("valid date range is required")
	}
	if maxDays <= 0 {
		maxDays = 30
	}
	var chunks []DateChunk
	for start := from; start.String() <= to.String(); {
		end := start.AddDays(maxDays - 1)
		if end.String() > to.String() {
			end = to
		}
		chunks = append(chunks, DateChunk{From: start, To: end})
		start = end.AddDays(1)
	}
	return chunks, nil
}

type FixedCSVRow struct {
	SourceRowNumber   int
	SourceRowChecksum string
	SourceLocationID  string
	Values            map[string]string
}

func ParseFixedCSV(ctx context.Context, definition FixedDefinition, sourceLocationID, content string) ([]FixedCSVRow, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(content, "\uFEFF")))
	reader.Comma = '|'
	headers, err := reader.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("%s: no CSV header", definition.Key)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: read header: %w", definition.Key, err)
	}
	if err := validateFixedHeaders(definition, headers); err != nil {
		return nil, err
	}
	var rows []FixedCSVRow
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("%s: malformed CSV: %w", definition.Key, readErr)
		}
		line, _ := reader.FieldPos(0)
		row := FixedCSVRow{SourceRowNumber: line, Values: make(map[string]string, len(headers))}
		if definition.SourceLocationID {
			row.SourceLocationID = sourceLocationID
		}
		for index, header := range headers {
			row.Values[header] = values[index]
		}
		raw := bytes.Join(func() [][]byte {
			parts := make([][]byte, len(values))
			for index := range values {
				parts[index] = []byte(values[index])
			}
			return parts
		}(), []byte{0})
		sum := sha256.Sum256(raw)
		row.SourceRowChecksum = hex.EncodeToString(sum[:])
		rows = append(rows, row)
	}
	return rows, nil
}

func validateFixedHeaders(definition FixedDefinition, headers []string) error {
	expected := make(map[string]struct{}, len(definition.RequiredHeaders))
	for _, header := range definition.RequiredHeaders {
		expected[header] = struct{}{}
	}
	seen, normalized := map[string]struct{}{}, map[string]string{}
	for _, header := range headers {
		if _, duplicate := seen[header]; duplicate {
			return fmt.Errorf("%s: duplicate CSV header %q", definition.Key, header)
		}
		seen[header] = struct{}{}
		column := toSnakeCase(header)
		if previous, collision := normalized[column]; collision {
			return fmt.Errorf("%s: CSV headers %q and %q normalize to duplicate column %q", definition.Key, previous, header, column)
		}
		normalized[column] = header
		if _, allowed := expected[header]; !allowed {
			return fmt.Errorf("%s: unexpected CSV header %q", definition.Key, header)
		}
	}
	for _, header := range definition.RequiredHeaders {
		if _, found := seen[header]; !found {
			return fmt.Errorf("%s: missing required CSV header %q", definition.Key, header)
		}
	}
	return nil
}
