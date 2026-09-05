package customdataset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestQueryNamesContract(t *testing.T) {
	headers := []string{" Account ID ", "Account-ID", "123 value", "!!!", "select", strings.Repeat("A", 70), strings.Repeat("A", 70)}
	got, err := QueryNames(headers, func(value string) bool { return value == "select" })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"account_id", "account_id_2", "column_123_value", "column_004", "select_column", strings.Repeat("a", 64), strings.Repeat("a", 62) + "_2"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("name %d=%q want %q", index, got[index], want[index])
		}
	}
	if _, err := QueryNames([]string{" "}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty header error=%v", err)
	}
}

func TestDetectDelimiter(t *testing.T) {
	for name, source := range map[string]struct {
		source string
		want   Delimiter
	}{
		"comma":         {"a,b\n1,2\n3,4\n", DelimiterComma},
		"semicolon":     {"a;b\n1;2\n3;4\n", DelimiterSemicolon},
		"tab multiline": {"a\tb\n\"one\nline\"\t2\nthree\t4\n", DelimiterTab},
	} {
		t.Run(name, func(t *testing.T) {
			got, clear, err := DetectDelimiter(strings.NewReader(source.source))
			if err != nil || !clear || got != source.want {
				t.Fatalf("delimiter=%q clear=%v err=%v", got, clear, err)
			}
		})
	}
	for _, source := range []string{"value\none\ntwo\n", "a,b;c\n1,2;3\n4,5;6\n"} {
		if _, clear, err := DetectDelimiter(strings.NewReader(source)); err != nil || clear {
			t.Fatalf("ambiguous source clear=%v err=%v", clear, err)
		}
	}
	mostlySingleColumn := "a,b\n1,2\n3,4\n5\n6\n7\n8\n9\n10\n11\n"
	if _, clear, err := DetectDelimiter(strings.NewReader(mostlySingleColumn)); err != nil || clear {
		t.Fatalf("inconsistent source clear=%v err=%v", clear, err)
	}
}

func TestPreviewCSVContract(t *testing.T) {
	source := "\ufeffignored\r\n First ,First,Notes\r\n1,2,\"hello\r\nworld\"\r\n,,\r\n3,4,last"
	preview, err := ParsePreview(context.Background(), strings.NewReader(source), DelimiterComma, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(preview.Header) != "[First First Notes]" || fmt.Sprint(preview.QueryNames) != "[first first_2 notes]" || preview.ScannedRows != 2 || len(preview.Rows) != 2 || preview.Rows[0][2] != "hello\nworld" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if _, err := ParsePreview(context.Background(), strings.NewReader("a,b\n1\n"), DelimiterComma, 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("width error=%v", err)
	}
	if _, err := ParsePreview(context.Background(), strings.NewReader("a,b\n\"bad"), DelimiterComma, 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("quote error=%v", err)
	}
}

func TestStreamTypesEmptyRowsAndReuseRecord(t *testing.T) {
	date := "YYYY-MM-DD"
	datetime := "DD/MM/YYYY HH:mm:ss"
	columns := []Column{
		{Ordinal: 1, DisplayName: "text", LogicalType: TypeText},
		{Ordinal: 2, DisplayName: "int", LogicalType: TypeInteger},
		{Ordinal: 3, DisplayName: "decimal", LogicalType: TypeDecimal},
		{Ordinal: 4, DisplayName: "date", LogicalType: TypeDate, DateFormat: &date},
		{Ordinal: 5, DisplayName: "datetime", LogicalType: TypeDateTime, DateFormat: &datetime},
		{Ordinal: 6, DisplayName: "bool", LogicalType: TypeBoolean},
	}
	source := "preamble\ntext,int,decimal,date,datetime,bool\nexact,+2,-12.340,2026-09-05,05/09/2026 01:02:03,TrUe\n,,,,,\nsecond,-3,0.1,2026-09-06,06/09/2026 04:05:06,FALSE\n"
	var records []uint64
	var values [][]any
	sourceRecords, rows, diagnostics, truncated, err := Stream(context.Background(), strings.NewReader(source), DelimiterComma, 2, columns, func(record uint64, row []any) error {
		records = append(records, record)
		values = append(values, append([]any(nil), row...))
		return nil
	})
	if err != nil || truncated || len(diagnostics) != 0 || sourceRecords != 3 || rows != 2 || fmt.Sprint(records) != "[3 5]" {
		t.Fatalf("source=%d rows=%d records=%v diagnostics=%v truncated=%v err=%v", sourceRecords, rows, records, diagnostics, truncated, err)
	}
	if values[0][0] != "exact" || values[1][0] != "second" || values[0][1] != int64(2) || !values[0][2].(decimal.Decimal).Equal(decimal.RequireFromString("-12.340")) || !values[0][3].(time.Time).Equal(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)) || values[0][5] != true {
		t.Fatalf("converted values=%#v", values)
	}
}

func TestStreamValidationDiagnostics(t *testing.T) {
	columns := []Column{{Ordinal: 1, DisplayName: "number", LogicalType: TypeInteger}}
	_, _, diagnostics, _, err := Stream(context.Background(), strings.NewReader("number\n 1\n1e3\n1,2\n"), DelimiterComma, 1, columns, func(uint64, []any) error { t.Fatal("invalid rows must not stage"); return nil })
	if err != nil || len(diagnostics) != 3 || diagnostics[0].Value != " 1" {
		t.Fatalf("diagnostics=%+v err=%v", diagnostics, err)
	}
	if _, _, _, _, err := Stream(context.Background(), strings.NewReader("before\n"), DelimiterComma, 2, columns, func(uint64, []any) error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing header error=%v", err)
	}
	if _, err := ParsePreview(context.Background(), strings.NewReader(string([]byte{'a', ',', 'b', '\n', 0xff})), DelimiterComma, 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("utf8 error=%v", err)
	}
}

func TestTypeBoundaries(t *testing.T) {
	decimalColumn := Column{Ordinal: 1, LogicalType: TypeDecimal}
	valid := strings.Repeat("9", 35) + "." + strings.Repeat("8", 30)
	if _, err := convert(valid, decimalColumn); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{strings.Repeat("9", 36), "1." + strings.Repeat("1", 31), "1e2", "1,000", " 1"} {
		if _, err := convert(value, decimalColumn); err == nil {
			t.Fatalf("decimal %q accepted", value)
		}
	}
	if _, err := convert("9223372036854775808", Column{LogicalType: TypeInteger}); err == nil {
		t.Fatal("overflow integer accepted")
	}
	if _, err := convert("true ", Column{LogicalType: TypeBoolean}); err == nil {
		t.Fatal("trimmed boolean accepted")
	}
	for _, candidate := range []struct {
		kind   LogicalType
		format string
		value  string
	}{
		{TypeDate, "YYYY-MM-DD", "2026-09-05"},
		{TypeDate, "DD/MM/YYYY", "05/09/2026"},
		{TypeDate, "MM/DD/YYYY", "09/05/2026"},
		{TypeDateTime, "YYYY-MM-DD HH:mm:ss", "2026-09-05 01:02:03"},
		{TypeDateTime, "DD/MM/YYYY HH:mm:ss", "05/09/2026 01:02:03"},
		{TypeDateTime, "MM/DD/YYYY HH:mm:ss", "09/05/2026 01:02:03"},
	} {
		if _, err := convert(candidate.value, Column{LogicalType: candidate.kind, DateFormat: &candidate.format}); err != nil {
			t.Fatalf("%s %s: %v", candidate.format, candidate.value, err)
		}
	}
}

func TestColumnAndPublishedRowLimits(t *testing.T) {
	headers := make([]string, MaxColumns)
	for index := range headers {
		headers[index] = fmt.Sprintf("column %d", index+1)
	}
	if names, err := QueryNames(headers, nil); err != nil || len(names) != MaxColumns {
		t.Fatalf("50 columns names=%d err=%v", len(names), err)
	}
	if _, err := QueryNames(append(headers, "too many"), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("51-column error=%v", err)
	}

	columns := []Column{{Ordinal: 1, DisplayName: "value", LogicalType: TypeText}}
	source := "value\n" + strings.Repeat("x\n", int(MaxDataRows)) + "\"\"\nextra\n"
	sourceRecords, rows, diagnostics, _, err := Stream(context.Background(), strings.NewReader(source), DelimiterComma, 1, columns, func(uint64, []any) error { return nil })
	if err != nil || sourceRecords != MaxDataRows+2 || rows != MaxDataRows || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Reason, "limit") {
		t.Fatalf("source=%d rows=%d diagnostics=%v err=%v", sourceRecords, rows, diagnostics, err)
	}
}
