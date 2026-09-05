package customdataset

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const delimiterPrefixBytes = 256 << 10

var (
	integerPattern = regexp.MustCompile(`^[+-]?[0-9]+$`)
	decimalPattern = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)
)

type ReservedWord func(string) bool

func QueryNames(headers []string, reserved ReservedWord) ([]string, error) {
	if len(headers) == 0 || len(headers) > MaxColumns {
		return nil, fmt.Errorf("%w: header must contain 1 to %d columns", ErrInvalid, MaxColumns)
	}
	names := make([]string, len(headers))
	used := make(map[string]struct{}, len(headers))
	for index, header := range headers {
		if strings.TrimSpace(header) == "" {
			return nil, fmt.Errorf("%w: header column %d is empty", ErrInvalid, index+1)
		}
		var builder strings.Builder
		underscore := false
		overflow := false
		for _, value := range header {
			value = unicode.ToLower(value)
			if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
				if builder.Len() < 64 {
					builder.WriteRune(value)
				} else {
					overflow = true
				}
				underscore = false
			} else if builder.Len() != 0 && !underscore {
				if builder.Len() < 64 {
					builder.WriteByte('_')
				} else {
					overflow = true
				}
				underscore = true
			}
		}
		base := strings.Trim(builder.String(), "_")
		if base == "" {
			base = fmt.Sprintf("column_%03d", index+1)
		}
		if base[0] >= '0' && base[0] <= '9' {
			base = "column_" + base
		}
		if !overflow && reserved != nil && reserved(base) {
			base += "_column"
		}
		base = truncateASCII(base, 64)
		name := base
		for collision := 2; ; collision++ {
			if _, exists := used[name]; !exists {
				break
			}
			suffix := "_" + strconv.Itoa(collision)
			name = truncateASCII(base, 64-len(suffix)) + suffix
		}
		used[name] = struct{}{}
		names[index] = name
	}
	return names, nil
}

func truncateASCII(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return strings.TrimRight(value[:maximum], "_")
}

func DetectDelimiter(source io.Reader) (Delimiter, bool, error) {
	prefix := make([]byte, delimiterPrefixBytes+1)
	read, err := io.ReadFull(source, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false, err
	}
	truncated := read > delimiterPrefixBytes
	if truncated {
		read = delimiterPrefixBytes
	}
	prefix = stripBOM(prefix[:read])
	if truncated {
		if end := bytes.LastIndexByte(prefix, '\n'); end >= 0 {
			prefix = prefix[:end+1]
		} else {
			prefix = nil
		}
	}
	type score struct {
		delimiter     Delimiter
		support       int
		inconsistency int
		eligible      bool
	}
	scores := make([]score, 0, 3)
	for _, delimiter := range []Delimiter{DelimiterComma, DelimiterSemicolon, DelimiterTab} {
		runeValue, _ := delimiter.Rune()
		reader := csv.NewReader(strings.NewReader(string(prefix)))
		reader.Comma, reader.FieldsPerRecord, reader.ReuseRecord = runeValue, -1, true
		widths := make([]int, 0, 64)
		for len(widths) < 64 {
			record, readErr := reader.Read()
			if readErr != nil {
				var parseError *csv.ParseError
				if truncated && errors.As(readErr, &parseError) && errors.Is(parseError.Err, csv.ErrQuote) {
					break
				}
				if !errors.Is(readErr, io.EOF) {
					widths = nil
				}
				break
			}
			widths = append(widths, len(record))
		}
		counts := make(map[int]int)
		mode, support := 0, 0
		for _, width := range widths {
			if width < 2 || width > MaxColumns {
				continue
			}
			counts[width]++
			if counts[width] > support || counts[width] == support && width < mode {
				mode, support = width, counts[width]
			}
		}
		inconsistent := 0
		for _, width := range widths {
			if width != mode {
				inconsistent++
			}
		}
		scores = append(scores, score{delimiter: delimiter, support: support, inconsistency: inconsistent,
			eligible: support >= 2 && len(widths) > 0 && support*100 >= len(widths)*80})
	}
	var best, runner score
	for _, candidate := range scores {
		if !candidate.eligible {
			continue
		}
		if candidate.support > best.support || candidate.support == best.support && candidate.inconsistency < best.inconsistency {
			runner, best = best, candidate
		} else if candidate.support > runner.support || candidate.support == runner.support && candidate.inconsistency < runner.inconsistency {
			runner = candidate
		}
	}
	clear := best.eligible && (!runner.eligible || best.support >= runner.support+2)
	return best.delimiter, clear, nil
}

func ParsePreview(ctx context.Context, source io.Reader, delimiter Delimiter, headerRecord uint64, reserved ReservedWord) (Preview, error) {
	if headerRecord == 0 {
		return Preview{}, fmt.Errorf("%w: header record must be positive", ErrInvalid)
	}
	reader, err := newReader(source, delimiter)
	if err != nil {
		return Preview{}, err
	}
	var preview Preview
	inferers := make([]inferer, 0)
	for recordNumber := uint64(1); ; recordNumber++ {
		if err := ctx.Err(); err != nil {
			return Preview{}, err
		}
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Preview{}, recordError(recordNumber, readErr)
		}
		if !validRecordUTF8(record) {
			return Preview{}, fmt.Errorf("%w: CSV record %d contains invalid UTF-8", ErrInvalid, recordNumber)
		}
		if recordNumber < headerRecord {
			continue
		}
		if recordNumber == headerRecord {
			preview.Header, err = normalizeHeader(record)
			if err != nil {
				return Preview{}, err
			}
			preview.QueryNames, err = QueryNames(preview.Header, reserved)
			if err != nil {
				return Preview{}, err
			}
			inferers = make([]inferer, len(record))
			continue
		}
		if len(record) != len(preview.Header) {
			return Preview{}, fmt.Errorf("%w: CSV record %d has %d fields; expected %d", ErrInvalid, recordNumber, len(record), len(preview.Header))
		}
		if allEmpty(record) {
			continue
		}
		preview.ScannedRows++
		for index, value := range record {
			inferers[index].observe(value)
		}
		if len(preview.Rows) < PreviewRows {
			row := make([]string, len(record))
			for index, value := range record {
				row[index] = excerpt(value, 256)
			}
			preview.Rows = append(preview.Rows, row)
		}
		if preview.ScannedRows >= InferenceRows {
			break
		}
	}
	if preview.Header == nil {
		return Preview{}, fmt.Errorf("%w: header record %d does not exist", ErrInvalid, headerRecord)
	}
	preview.Suggestions = make([]ColumnSuggestion, len(inferers))
	for index := range inferers {
		preview.Suggestions[index] = inferers[index].suggestion()
	}
	return preview, nil
}

func Stream(ctx context.Context, source io.Reader, delimiter Delimiter, headerRecord uint64, columns []Column, consume func(uint64, []any) error) (uint64, uint64, []Diagnostic, bool, error) {
	reader, err := newReader(&contextReader{ctx: ctx, source: source}, delimiter)
	if err != nil {
		return 0, 0, nil, false, err
	}
	var sourceRecords, rows uint64
	diagnostics := make([]Diagnostic, 0, MaxDiagnostics)
	truncated := false
	headerSeen := false
	for recordNumber := uint64(1); ; recordNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return sourceRecords, rows, diagnostics, truncated, recordError(recordNumber, readErr)
		}
		if !validRecordUTF8(record) {
			return sourceRecords, rows, diagnostics, truncated, fmt.Errorf("%w: CSV record %d contains invalid UTF-8", ErrInvalid, recordNumber)
		}
		if recordNumber < headerRecord {
			continue
		}
		if recordNumber == headerRecord {
			header, headerErr := normalizeHeader(record)
			if headerErr != nil {
				return sourceRecords, rows, diagnostics, truncated, headerErr
			}
			if len(header) != len(columns) {
				return sourceRecords, rows, diagnostics, truncated, fmt.Errorf("%w: header has %d fields; expected %d", ErrInvalid, len(header), len(columns))
			}
			for index := range header {
				if header[index] != columns[index].DisplayName {
					return sourceRecords, rows, diagnostics, truncated, fmt.Errorf("%w: header column %d is %q; expected %q", ErrInvalid, index+1, excerpt(header[index], 128), excerpt(columns[index].DisplayName, 128))
				}
			}
			headerSeen = true
			continue
		}
		sourceRecords++
		if len(record) != len(columns) {
			diagnostics = appendDiagnostic(diagnostics, Diagnostic{Record: recordNumber, Reason: fmt.Sprintf("record has %d fields; expected %d", len(record), len(columns))})
			if len(diagnostics) == MaxDiagnostics {
				return sourceRecords, rows, diagnostics, true, nil
			}
			continue
		}
		if allEmpty(record) {
			continue
		}
		if rows == MaxDataRows {
			diagnostics = appendDiagnostic(diagnostics, Diagnostic{Record: recordNumber, Reason: fmt.Sprintf("data row limit %d exceeded", MaxDataRows)})
			return sourceRecords, rows, diagnostics, false, nil
		}
		converted := make([]any, len(columns))
		valid := true
		for index := range columns {
			value, conversionErr := convert(record[index], columns[index])
			if conversionErr != nil {
				valid = false
				diagnostics = appendDiagnostic(diagnostics, Diagnostic{Record: recordNumber, Column: excerpt(columns[index].DisplayName, 128),
					Expected: string(columns[index].LogicalType), Value: excerpt(record[index], 128), Reason: conversionErr.Error()})
				if len(diagnostics) == MaxDiagnostics {
					return sourceRecords, rows, diagnostics, true, nil
				}
				continue
			}
			converted[index] = value
		}
		if !valid || len(diagnostics) != 0 {
			continue
		}
		if err := consume(recordNumber, converted); err != nil {
			return sourceRecords, rows, diagnostics, truncated, err
		}
		rows++
	}
	if !headerSeen {
		return sourceRecords, rows, diagnostics, truncated, fmt.Errorf("%w: header record %d does not exist", ErrInvalid, headerRecord)
	}
	return sourceRecords, rows, diagnostics, truncated, nil
}

func newReader(source io.Reader, delimiter Delimiter) (*csv.Reader, error) {
	value, err := delimiter.Rune()
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReader(source)
	if bom, _ := buffered.Peek(3); len(bom) == 3 && bom[0] == 0xef && bom[1] == 0xbb && bom[2] == 0xbf {
		_, _ = buffered.Discard(3)
	}
	reader := csv.NewReader(buffered)
	reader.Comma = value
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	return reader, nil
}

func normalizeHeader(record []string) ([]string, error) {
	if len(record) == 0 || len(record) > MaxColumns {
		return nil, fmt.Errorf("%w: header must contain 1 to %d columns", ErrInvalid, MaxColumns)
	}
	header := make([]string, len(record))
	for index, value := range record {
		header[index] = strings.TrimSpace(value)
		if header[index] == "" {
			return nil, fmt.Errorf("%w: header column %d is empty", ErrInvalid, index+1)
		}
	}
	return header, nil
}

func convert(value string, column Column) (any, error) {
	if value == "" {
		return nil, nil
	}
	switch column.LogicalType {
	case TypeText:
		return value, nil
	case TypeInteger:
		if !integerPattern.MatchString(value) {
			return nil, errors.New("value is not canonical integer syntax")
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, errors.New("value is outside signed BIGINT range")
		}
		return parsed, nil
	case TypeDecimal:
		if !decimalPattern.MatchString(value) {
			return nil, errors.New("value is not canonical decimal syntax")
		}
		unsigned := strings.TrimPrefix(strings.TrimPrefix(value, "+"), "-")
		parts := strings.SplitN(unsigned, ".", 2)
		integerDigits := len(parts[0])
		fractionalDigits := 0
		if len(parts) == 2 {
			fractionalDigits = len(parts[1])
		}
		if integerDigits > 35 || fractionalDigits > 30 || integerDigits+fractionalDigits > 65 {
			return nil, errors.New("value exceeds DECIMAL(65,30)")
		}
		parsed, err := decimal.NewFromString(value)
		if err != nil {
			return nil, errors.New("value is not a valid decimal")
		}
		return parsed, nil
	case TypeDate, TypeDateTime:
		if column.DateFormat == nil {
			return nil, errors.New("column date format is missing")
		}
		layout, ok := dateLayout(column.LogicalType, *column.DateFormat)
		if !ok {
			return nil, errors.New("column date format is invalid")
		}
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err != nil || parsed.Format(layout) != value {
			return nil, fmt.Errorf("value does not match %s", *column.DateFormat)
		}
		return parsed, nil
	case TypeBoolean:
		if strings.EqualFold(value, "true") {
			return true, nil
		}
		if strings.EqualFold(value, "false") {
			return false, nil
		}
		return nil, errors.New("value must be TRUE or FALSE")
	default:
		return nil, errors.New("column type is invalid")
	}
}

func dateLayout(kind LogicalType, format string) (string, bool) {
	if kind == TypeDateTime {
		if !strings.HasSuffix(format, " HH:mm:ss") {
			return "", false
		}
		format = strings.TrimSuffix(format, " HH:mm:ss")
	}
	dateLayouts := map[string]string{"YYYY-MM-DD": "2006-01-02", "DD/MM/YYYY": "02/01/2006", "MM/DD/YYYY": "01/02/2006"}
	layout, ok := dateLayouts[format]
	if !ok {
		return "", false
	}
	if kind == TypeDateTime {
		layout += " 15:04:05"
	}
	return layout, true
}

func validRecordUTF8(record []string) bool {
	for _, value := range record {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func allEmpty(record []string) bool {
	for _, value := range record {
		if value != "" {
			return false
		}
	}
	return true
}

func stripBOM(value []byte) []byte {
	if len(value) >= 3 && value[0] == 0xef && value[1] == 0xbb && value[2] == 0xbf {
		return value[3:]
	}
	return value
}

func appendDiagnostic(values []Diagnostic, value Diagnostic) []Diagnostic {
	if len(values) < MaxDiagnostics {
		return append(values, value)
	}
	return values
}

func excerpt(value string, maximum int) string {
	seen := 0
	for offset := range value {
		if seen == maximum {
			return value[:offset] + "…"
		}
		seen++
	}
	return value
}

func recordError(record uint64, err error) error {
	return fmt.Errorf("%w: CSV record %d: %v", ErrInvalid, record, err)
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

type inferer struct {
	seen, boolean, integer, decimal int
	dates                           map[string]int
	datetimes                       map[string]int
	leadingZero                     bool
}

func (value *inferer) observe(raw string) {
	if raw == "" {
		return
	}
	value.seen++
	if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false") {
		value.boolean++
	}
	if integerPattern.MatchString(raw) {
		value.integer++
		unsigned := strings.TrimPrefix(strings.TrimPrefix(raw, "+"), "-")
		value.leadingZero = value.leadingZero || len(unsigned) > 1 && unsigned[0] == '0'
	}
	if decimalPattern.MatchString(raw) {
		value.decimal++
	}
	if value.dates == nil {
		value.dates, value.datetimes = map[string]int{}, map[string]int{}
	}
	for _, format := range []string{"YYYY-MM-DD", "DD/MM/YYYY", "MM/DD/YYYY"} {
		if layout, _ := dateLayout(TypeDate, format); parsesExactly(raw, layout) {
			value.dates[format]++
		}
		dateTimeFormat := format + " HH:mm:ss"
		if layout, _ := dateLayout(TypeDateTime, dateTimeFormat); parsesExactly(raw, layout) {
			value.datetimes[dateTimeFormat]++
		}
	}
}

func (value inferer) suggestion() ColumnSuggestion {
	if value.seen == 0 {
		return ColumnSuggestion{Type: TypeText}
	}
	if value.boolean == value.seen {
		return ColumnSuggestion{Type: TypeBoolean}
	}
	if value.integer == value.seen && !value.leadingZero {
		return ColumnSuggestion{Type: TypeInteger}
	}
	if value.decimal == value.seen {
		return ColumnSuggestion{Type: TypeDecimal}
	}
	for format, count := range value.datetimes {
		if count == value.seen && unambiguousFormat(value.datetimes, count) {
			return ColumnSuggestion{Type: TypeDateTime, DateFormat: format}
		}
	}
	for format, count := range value.dates {
		if count == value.seen && unambiguousFormat(value.dates, count) {
			return ColumnSuggestion{Type: TypeDate, DateFormat: format}
		}
	}
	return ColumnSuggestion{Type: TypeText}
}

func parsesExactly(value, layout string) bool {
	parsed, err := time.ParseInLocation(layout, value, time.UTC)
	return err == nil && parsed.Format(layout) == value
}

func unambiguousFormat(values map[string]int, expected int) bool {
	matches := 0
	for _, count := range values {
		if count == expected {
			matches++
		}
	}
	return matches == 1
}
