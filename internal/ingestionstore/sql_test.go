package ingestionstore

import (
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
)

func TestDecimalStringRejectsRounding(t *testing.T) {
	for _, test := range []struct {
		value            string
		precision, scale int
		valid            bool
	}{
		{"1234567890.123456", 24, 6, true},
		{"123456789012345678.123456", 24, 6, true},
		{"1234567890123456789.1", 24, 6, false},
		{"1.1234567", 24, 6, false},
		{"12.34", 20, 2, true},
		{"12.345", 20, 2, false},
	} {
		_, err := decimalString(decimal.RequireFromString(test.value), test.precision, test.scale)
		if (err == nil) != test.valid {
			t.Fatalf("decimal %s valid=%v error=%v", test.value, test.valid, err)
		}
	}
}

func TestQuoteIdentifier(t *testing.T) {
	if _, err := quoteIdentifier("safe_name"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "Bad", "x` DROP TABLE users", "1bad"} {
		if _, err := quoteIdentifier(value); err == nil {
			t.Fatalf("unsafe identifier %q accepted", value)
		}
	}
}

func TestTechnicalDiagnosticPreservesTypedMySQLMetadata(t *testing.T) {
	mysqlError := &mysql.MySQLError{Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}, Message: "deadlock"}
	diagnostic := TechnicalDiagnostic(&DatabaseError{Operation: "insert_child_rows", QueryName: "replace_saving_mutations",
		Table: "fincloud_saving_mutations", BatchStart: 1, BatchEnd: 61, Attempt: 2, MaxAttempts: 3, Cause: mysqlError})
	if diagnostic.MySQLNumber != 1213 || diagnostic.SQLState != "40001" || diagnostic.Table != "fincloud_saving_mutations" || diagnostic.TxAttempt != 2 {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}
