package ingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	SingleFixedMemberKey = "__single__"

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
	MemberKey        string
	ReportName       string
	Parameters       []string
	RequestedFrom    CalendarDate
	RequestedTo      CalendarDate
	SourceLocationID string
	AccountCode      string
}

func FixedManifestChecksum(definition FixedDefinition, plan FixedPlan) ([32]byte, error) {
	if plan.JobKey != definition.Key || len(plan.Members) == 0 || !plan.RequireAllMembers {
		return [32]byte{}, fmt.Errorf("complete fixed plan for %s is required", definition.Key)
	}
	members := make([]string, len(plan.Members))
	seen := make(map[string]struct{}, len(members))
	for index, member := range plan.Members {
		if member.MemberKey == "" {
			return [32]byte{}, fmt.Errorf("fixed plan member key is required")
		}
		if _, duplicate := seen[member.MemberKey]; duplicate {
			return [32]byte{}, fmt.Errorf("duplicate fixed plan member %q", member.MemberKey)
		}
		seen[member.MemberKey] = struct{}{}
		members[index] = member.MemberKey
	}
	sort.Strings(members)
	var encoded bytes.Buffer
	encoded.WriteString("DWH-FIXED-MANIFEST\x00")
	_ = binary.Write(&encoded, binary.BigEndian, uint16(1))
	for _, value := range []string{plan.JobKey, string(definition.LocationStrategy), string(definition.AccountCodeStrategy), plan.Range.From.String(), plan.Range.To.String()} {
		writeManifestString(&encoded, value)
	}
	_ = binary.Write(&encoded, binary.BigEndian, uint32(len(members)))
	for _, member := range members {
		writeManifestString(&encoded, member)
	}
	return sha256.Sum256(encoded.Bytes()), nil
}

func writeManifestString(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len([]byte(value))))
	buffer.WriteString(value)
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
			members = append(members, descriptor(definition, requested, location, "", location))
		}
	case definition.AccountCodeStrategy == AllAccountCodes:
		if len(accounts) == 0 {
			return FixedPlan{}, fmt.Errorf("%s requires a frozen account-code set", definition.Key)
		}
		for _, account := range accounts {
			members = append(members, descriptor(definition, requested, "", account, account))
		}
	default:
		members = []RequestDescriptor{descriptor(definition, requested, "", "", SingleFixedMemberKey)}
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

// BuildFixedDateSeriesPlan creates one complete Balance Sheet load spanning
// every frozen date and location. Enumeration remains external to this pure builder.
func BuildFixedDateSeriesPlan(definition FixedDefinition, dates []CalendarDate, frozenLocations FrozenLocations) (FixedPlan, error) {
	if definition.Key != "balance_sheet_report" || !definition.SnapshotDate || len(dates) == 0 {
		return FixedPlan{}, fmt.Errorf("balance sheet date series is required")
	}
	for index, date := range dates {
		if date.IsZero() || (index > 0 && date != dates[index-1].AddDays(1)) {
			return FixedPlan{}, fmt.Errorf("fixed date series must be ascending and contiguous")
		}
	}
	locations := frozenLocations.Values()
	if len(locations) == 0 {
		return FixedPlan{}, fmt.Errorf("%s requires a frozen location set", definition.Key)
	}
	members := make([]RequestDescriptor, 0, len(dates)*len(locations))
	for _, date := range dates {
		requested := FixedDateRangeParams{From: date, To: date}
		for _, location := range locations {
			members = append(members, descriptor(definition, requested, location, "", dateLocationMemberKey(date, location)))
		}
	}
	from, to := dates[0], dates[len(dates)-1]
	return FixedPlan{
		JobKey: definition.Key, Range: FixedDateRangeParams{From: from, To: to},
		ReplacementScope: ReplacementScope{JobKey: definition.Key, From: from, To: to},
		Members:          members, RequireAllMembers: true,
	}, nil
}

func descriptor(definition FixedDefinition, requested FixedDateRangeParams, location, account, memberKey string) RequestDescriptor {
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
	return RequestDescriptor{MemberKey: memberKey, ReportName: definition.FincloudReportName, Parameters: parameters, RequestedFrom: requested.From, RequestedTo: requested.To, SourceLocationID: location, AccountCode: account}
}

func BuildFixedRequestDescriptor(definition FixedDefinition, requested FixedDateRangeParams, location, account, memberKey string) (RequestDescriptor, error) {
	if requested.From.IsZero() || requested.To.IsZero() || requested.From.String() > requested.To.String() || memberKey == "" {
		return RequestDescriptor{}, fmt.Errorf("valid fixed request descriptor is required")
	}
	canonical := false
	for _, candidate := range FixedDefinitions() {
		if candidate.Key == definition.Key {
			canonical = true
			break
		}
	}
	if !canonical {
		return RequestDescriptor{}, fmt.Errorf("unknown fixed definition %q", definition.Key)
	}
	return descriptor(definition, requested, location, account, memberKey), nil
}

func dateLocationMemberKey(date CalendarDate, location string) string {
	var encoded bytes.Buffer
	writeManifestString(&encoded, date.String())
	writeManifestString(&encoded, location)
	sum := sha256.Sum256(encoded.Bytes())
	return hex.EncodeToString(sum[:])
}

func FixedColumnName(header string) string { return toSnakeCase(header) }

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

type FixedHeaderError struct {
	Report, Kind, Expected, ReceivedRaw, ReceivedNormalized string
	Column                                                  int
	cause                                                   error
}

func (err *FixedHeaderError) Error() string {
	if err == nil {
		return "fixed CSV header mismatch"
	}
	switch err.Kind {
	case "missing":
		return fmt.Sprintf("%s: missing required CSV header %q", err.Report, err.Expected)
	case "duplicate":
		return fmt.Sprintf("%s: duplicate CSV header %q", err.Report, err.ReceivedNormalized)
	case "normalization_collision":
		return fmt.Sprintf("%s: CSV header normalization collision at column %d", err.Report, err.Column)
	default:
		return fmt.Sprintf("%s: unexpected CSV header %q", err.Report, err.ReceivedNormalized)
	}
}

func (err *FixedHeaderError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func ParseFixedCSV(ctx context.Context, definition FixedDefinition, sourceLocationID, content string) ([]FixedCSVRow, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(content, "\uFEFF")))
	reader.Comma = '|'
	reader.LazyQuotes = true
	headers, err := reader.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("%s: no CSV header", definition.Key)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: read header: %w", definition.Key, err)
	}
	rawHeaders := append([]string(nil), headers...)
	for index := range headers {
		headers[index] = strings.TrimSpace(rawHeaders[index])
	}
	if err := validateFixedHeaders(definition, rawHeaders, headers); err != nil {
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

func validateFixedHeaders(definition FixedDefinition, rawHeaders, headers []string) error {
	expected := make(map[string]struct{}, len(definition.RequiredHeaders))
	for _, header := range definition.RequiredHeaders {
		expected[header] = struct{}{}
	}
	seen, normalized := map[string]struct{}{}, map[string]string{}
	for index, header := range headers {
		expectedHeader := ""
		if index < len(definition.RequiredHeaders) {
			expectedHeader = definition.RequiredHeaders[index]
		}
		if _, duplicate := seen[header]; duplicate {
			return &FixedHeaderError{Report: definition.Key, Kind: "duplicate", Column: index + 1, Expected: expectedHeader,
				ReceivedRaw: rawHeaders[index], ReceivedNormalized: header}
		}
		seen[header] = struct{}{}
		column := toSnakeCase(header)
		if previous, collision := normalized[column]; collision {
			return &FixedHeaderError{Report: definition.Key, Kind: "normalization_collision", Column: index + 1, Expected: previous,
				ReceivedRaw: rawHeaders[index], ReceivedNormalized: header, cause: fmt.Errorf("normalized column %s", column)}
		}
		normalized[column] = header
		if _, allowed := expected[header]; !allowed {
			return &FixedHeaderError{Report: definition.Key, Kind: "unexpected", Column: index + 1, Expected: expectedHeader,
				ReceivedRaw: rawHeaders[index], ReceivedNormalized: header}
		}
	}
	for index, header := range definition.RequiredHeaders {
		if _, found := seen[header]; !found {
			return &FixedHeaderError{Report: definition.Key, Kind: "missing", Column: index + 1, Expected: header}
		}
	}
	return nil
}
