package ingestionstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	databasepkg "github.com/ibldzn/go-admin/internal/database"
)

const maxInsertParameters = 60000
const maxInsertBytes = 4 << 20

var safeIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

type DatabaseError struct {
	Operation, QueryName, Table string
	BatchStart, BatchEnd        int
	Attempt, MaxAttempts        int
	Cause                       error
}

func (err *DatabaseError) Error() string {
	if err == nil || err.Cause == nil {
		return "database operation failed"
	}
	return err.Cause.Error()
}

func (err *DatabaseError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

type DatabaseDiagnostic struct {
	Operation   string `json:"operation,omitempty"`
	QueryName   string `json:"query_name,omitempty"`
	Table       string `json:"table,omitempty"`
	BatchStart  int    `json:"batch_start,omitempty"`
	BatchEnd    int    `json:"batch_end,omitempty"`
	TxAttempt   int    `json:"tx_attempt,omitempty"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	MySQLNumber uint16 `json:"mysql_number,omitempty"`
	SQLState    string `json:"sqlstate,omitempty"`
	Message     string `json:"message,omitempty"`
}

func TechnicalDiagnostic(err error) DatabaseDiagnostic {
	var result DatabaseDiagnostic
	var databaseError *DatabaseError
	if errors.As(err, &databaseError) {
		result = DatabaseDiagnostic{Operation: databaseError.Operation, QueryName: databaseError.QueryName, Table: databaseError.Table,
			BatchStart: databaseError.BatchStart, BatchEnd: databaseError.BatchEnd, TxAttempt: databaseError.Attempt, MaxAttempts: databaseError.MaxAttempts}
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		result.MySQLNumber, result.Message = mysqlError.Number, mysqlError.Message
		if mysqlError.SQLState != [5]byte{} {
			result.SQLState = string(mysqlError.SQLState[:])
		}
	}
	return result
}

func wrapDatabaseError(err error, operation, queryName, table string, start, end int) error {
	if err == nil {
		return nil
	}
	var existing *DatabaseError
	if errors.As(err, &existing) {
		copy := *existing
		if copy.Operation == "" {
			copy.Operation = operation
		}
		if copy.QueryName == "" {
			copy.QueryName = queryName
		}
		if copy.Table == "" {
			copy.Table = table
		}
		return &copy
	}
	return &DatabaseError{Operation: operation, QueryName: queryName, Table: table, BatchStart: start, BatchEnd: end, Cause: err}
}

func withDatabaseAttempt(err error, operation string, attempt, maximum int) error {
	err = wrapDatabaseError(err, operation, operation, "", 0, 0)
	var databaseError *DatabaseError
	if !errors.As(err, &databaseError) {
		return err
	}
	copy := *databaseError
	copy.Attempt, copy.MaxAttempts = attempt, maximum
	return &copy
}

func quoteIdentifier(value string) (string, error) {
	if len(value) == 0 || len(value) > 64 || !safeIdentifier.MatchString(value) {
		return "", fmt.Errorf("unsafe SQL identifier %q", value)
	}
	return "`" + value + "`", nil
}

func decimalString(value any, precision, scale int) (any, error) {
	if value == nil {
		return nil, nil
	}
	number, ok := value.(decimal.Decimal)
	if !ok {
		return nil, fmt.Errorf("decimal value has type %T", value)
	}
	exponent := int(number.Exponent())
	fractional := 0
	if exponent < 0 {
		fractional = -exponent
	}
	integerDigits := len(number.Abs().Truncate(0).String())
	if number.Abs().LessThan(decimal.NewFromInt(1)) {
		integerDigits = 1
	}
	if fractional > scale || integerDigits > precision-scale {
		return nil, fmt.Errorf("decimal does not fit DECIMAL(%d,%d)", precision, scale)
	}
	return number.String(), nil
}

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertRows(ctx context.Context, tx contextExecer, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	quotedTable, err := quoteIdentifier(table)
	if err != nil {
		return err
	}
	quotedColumns := make([]string, len(columns))
	for index, column := range columns {
		quotedColumns[index], err = quoteIdentifier(column)
		if err != nil {
			return err
		}
	}
	maxRows := min(500, max(1, maxInsertParameters/len(columns)))
	for start := 0; start < len(rows); {
		end, estimatedBytes := start, 0
		for end < len(rows) && end-start < maxRows {
			rowBytes := estimateRowBytes(rows[end])
			if rowBytes > maxInsertBytes {
				return fmt.Errorf("%s row %d exceeds bounded insert size", table, end+1)
			}
			if end > start && estimatedBytes+rowBytes > maxInsertBytes {
				break
			}
			estimatedBytes += rowBytes
			end++
		}
		placeholders := make([]string, end-start)
		arguments := make([]any, 0, (end-start)*len(columns))
		for rowIndex, row := range rows[start:end] {
			if len(row) != len(columns) {
				return fmt.Errorf("%s row %d has %d values, want %d", table, start+rowIndex, len(row), len(columns))
			}
			placeholders[rowIndex] = "(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
			arguments = append(arguments, row...)
		}
		query := "INSERT INTO " + quotedTable + " (" + strings.Join(quotedColumns, ",") + ") VALUES " + strings.Join(placeholders, ",")
		if _, err := tx.ExecContext(ctx, query, arguments...); err != nil {
			return wrapDatabaseError(fmt.Errorf("insert %s rows %d-%d: %w", table, start+1, end, err), "", "insert_rows", table, start+1, end)
		}
		start = end
	}
	return nil
}

func estimateRowBytes(row []any) int {
	bytes := len(row) * 8
	for _, value := range row {
		switch typed := value.(type) {
		case string:
			bytes += len(typed)
		case []byte:
			bytes += len(typed)
		default:
			bytes += 32
		}
	}
	return bytes
}

func lockRow(ctx context.Context, tx *sqlx.Tx, query string, args ...any) error {
	var marker int
	if err := tx.GetContext(ctx, &marker, query, args...); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("required row does not exist")
		}
		return err
	}
	return nil
}

func retryReplaySafeTx(ctx context.Context, db *sqlx.DB, operation string, work func(*sqlx.Tx) error) error {
	attempt, err := databasepkg.RetryReplaySafeTx(ctx, db, work)
	if err == nil {
		return nil
	}
	return withDatabaseAttempt(err, operation, attempt, databasepkg.ReplaySafeMaxAttempts)
}
