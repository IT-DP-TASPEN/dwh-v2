package ingestionstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

const maxInsertParameters = 60000
const maxInsertBytes = 4 << 20

var safeIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

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
			return fmt.Errorf("insert %s rows %d-%d: %w", table, start+1, end, err)
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

func rollbackUnlessCommitted(tx *sqlx.Tx, committed *bool) {
	if !*committed {
		_ = tx.Rollback()
	}
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

func retryTransaction(ctx context.Context, operation string, transaction func() error) error {
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := transaction()
		if err == nil {
			if attempt > 1 {
				slog.InfoContext(ctx, "database transaction recovered", "operation", operation, "attempts", attempt)
			}
			return nil
		}
		if !isMySQLDeadlock(err) || attempt == maxAttempts {
			return err
		}
		slog.WarnContext(ctx, "retrying database transaction", "operation", operation, "attempt", attempt, "mysql_error", 1213)
		delay := time.Duration(attempt*25+rand.IntN(26)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

func isMySQLDeadlock(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1213
}
