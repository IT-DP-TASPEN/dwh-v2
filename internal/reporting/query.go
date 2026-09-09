package reporting

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type RowSink interface {
	Columns([]Column) error
	Row([]driver.Value) error
}

type QueryEngine struct{}

// Prepared variants are a bounded safety net; lexical validation remains exhaustive.
const maxPreparedTemplateVariants = 64

func (QueryEngine) Validate(ctx context.Context, database *sql.DB, statement string, parameters []Parameter) error {
	mode, err := (QueryEngine{}).SQLMode(ctx, database)
	if err != nil {
		return err
	}
	return ValidateBinding(statement, parameters, mode)
}

func (QueryEngine) ValidateTemplate(ctx context.Context, database *sql.DB, statement string, parameters []Parameter) error {
	_, err := (QueryEngine{}).validateTemplate(ctx, database, statement, parameters)
	return err
}

func (QueryEngine) validateTemplate(ctx context.Context, database *sql.DB, statement string, parameters []Parameter) (SQLMode, error) {
	if database == nil {
		return SQLMode{}, fmt.Errorf("report database is required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return SQLMode{}, fmt.Errorf("acquire report connection: %w", err)
	}
	defer connection.Close()
	mode, err := sqlMode(ctx, connection)
	if err != nil {
		return SQLMode{}, err
	}
	if err := ValidateTemplateBinding(statement, parameters, mode); err != nil {
		return mode, err
	}
	type validationStatement struct{ label, sql string }
	statements := []validationStatement{{label: "report SQL", sql: statement}}
	for _, parameter := range parameters {
		if effectiveOptionSource(parameter) == OptionSourceDynamic {
			statements = append(statements, validationStatement{label: fmt.Sprintf("dynamic option SQL for %q", parameter.Key), sql: parameter.DynamicOptionSQL})
		}
	}
	seen := make(map[string]struct{})
	for _, candidate := range statements {
		queries, err := templateValidationQueries(candidate.sql, parameters, mode, maxPreparedTemplateVariants-len(seen))
		if err != nil {
			return mode, err
		}
		for _, query := range queries {
			if _, found := seen[query]; found {
				continue
			}
			if len(seen) == maxPreparedTemplateVariants {
				return mode, nil
			}
			if err := prepareRaw(ctx, connection, query); err != nil {
				return mode, fmt.Errorf("%w: %s shape validation failed: %v", ErrInvalid, candidate.label, err)
			}
			seen[query] = struct{}{}
		}
	}
	return mode, nil
}

func (QueryEngine) SQLMode(ctx context.Context, database *sql.DB) (SQLMode, error) {
	if database == nil {
		return SQLMode{}, fmt.Errorf("report database is required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return SQLMode{}, fmt.Errorf("acquire report connection: %w", err)
	}
	defer connection.Close()
	return sqlMode(ctx, connection)
}

func sqlMode(ctx context.Context, connection *sql.Conn) (SQLMode, error) {
	var value string
	if err := connection.QueryRowContext(ctx, `SELECT @@SESSION.sql_mode`).Scan(&value); err != nil {
		return SQLMode{}, fmt.Errorf("inspect report SQL mode: %w", err)
	}
	return ParseSQLMode(value), nil
}

func (QueryEngine) Stream(ctx context.Context, database *sql.DB, statement string, parameters []Parameter, input map[string]InputValue, sink RowSink) error {
	if database == nil || sink == nil {
		return fmt.Errorf("report database and row sink are required")
	}
	normalized, err := NormalizeParameters(parameters, input)
	if err != nil {
		return err
	}
	return (QueryEngine{}).StreamNormalized(ctx, database, statement, parameters, normalized, sink)
}

func (QueryEngine) StreamNormalized(ctx context.Context, database *sql.DB, statement string, parameters []Parameter, normalized map[string]NormalizedValue, sink RowSink) error {
	if database == nil || sink == nil {
		return fmt.Errorf("report database and row sink are required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire report connection: %w", err)
	}
	defer connection.Close()
	mode, err := sqlMode(ctx, connection)
	if err != nil {
		return err
	}
	query, arguments, err := Bind(statement, parameters, normalized, mode)
	if err != nil {
		return err
	}
	named, err := DriverNamedValues(arguments)
	if err != nil {
		return err
	}
	return streamRaw(ctx, connection, query, named, sink)
}

func prepareRaw(ctx context.Context, connection *sql.Conn, query string) error {
	var operationErr error
	rawErr := connection.Raw(func(raw any) error {
		preparer, ok := raw.(driver.ConnPrepareContext)
		if !ok {
			operationErr = fmt.Errorf("report driver does not support context-aware prepare")
			return operationErr
		}
		statement, err := preparer.PrepareContext(ctx, query)
		if err != nil {
			operationErr = err
			return err
		}
		operationErr = statement.Close()
		return operationErr
	})
	if operationErr != nil {
		return operationErr
	}
	return rawErr
}

func streamRaw(ctx context.Context, connection *sql.Conn, query string, arguments []driver.NamedValue, sink RowSink) error {
	var operationErr error
	rawErr := connection.Raw(func(raw any) error {
		preparer, ok := raw.(driver.ConnPrepareContext)
		if !ok {
			operationErr = fmt.Errorf("report driver does not support context-aware prepare")
			return operationErr
		}
		queryContext, cancel := context.WithCancel(ctx)
		defer cancel()
		statement, err := preparer.PrepareContext(queryContext, query)
		if err != nil {
			operationErr = err
			return err
		}
		querier, ok := statement.(driver.StmtQueryContext)
		if !ok {
			_ = statement.Close()
			operationErr = fmt.Errorf("report driver does not support context-aware query")
			return operationErr
		}
		rows, err := querier.QueryContext(queryContext, arguments)
		if err != nil {
			_ = statement.Close()
			operationErr = err
			return err
		}
		columns := rows.Columns()
		metadata := make([]Column, len(columns))
		types, _ := rows.(driver.RowsColumnTypeDatabaseTypeName)
		nullability, _ := rows.(driver.RowsColumnTypeNullable)
		for index, name := range columns {
			metadata[index].Name = name
			if types != nil {
				metadata[index].DatabaseType = types.ColumnTypeDatabaseTypeName(index)
			}
			if nullability != nil {
				if nullable, known := nullability.ColumnTypeNullable(index); known {
					metadata[index].Nullable = &nullable
				}
			}
		}
		if err := sink.Columns(metadata); err != nil {
			operationErr = err
			cancel()
			return driver.ErrBadConn
		}
		values := make([]driver.Value, len(columns))
		for {
			err = rows.Next(values)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					operationErr = context.Cause(ctx)
				} else {
					operationErr = fmt.Errorf("read report row: %w", err)
				}
				cancel()
				return driver.ErrBadConn
			}
			copyValues := make([]driver.Value, len(values))
			for index, value := range values {
				if binary, ok := value.([]byte); ok {
					copyValues[index] = append([]byte(nil), binary...)
				} else {
					copyValues[index] = value
				}
			}
			if err := sink.Row(copyValues); err != nil {
				operationErr = err
				cancel()
				// Do not close rows here: mysql.Rows.Close drains unread packets.
				// ErrBadConn makes database/sql discard the canceled physical connection.
				return driver.ErrBadConn
			}
		}
		if next, ok := rows.(driver.RowsNextResultSet); ok && next.HasNextResultSet() {
			operationErr = ErrMultipleResultSets
			_ = rows.Close() // first set is already at EOF; drain only the rejected later sets
			_ = statement.Close()
			return operationErr
		}
		if err := rows.Close(); err != nil {
			operationErr = err
			_ = statement.Close()
			return err
		}
		if err := statement.Close(); err != nil {
			operationErr = err
			return err
		}
		return nil
	})
	if operationErr != nil {
		return operationErr
	}
	if rawErr != nil {
		return rawErr
	}
	return nil
}

type interactiveSink struct {
	result          InteractiveResult
	maxRows         int
	payloadCap      int64
	cellPreview     int
	approximateSize int64
}

func RunInteractive(ctx context.Context, engine QueryEngine, database *sql.DB, statement string, parameters []Parameter, input map[string]InputValue, maxRows int, payloadCap int64, cellPreview int) (InteractiveResult, error) {
	normalized, err := NormalizeParameters(parameters, input)
	if err != nil {
		return InteractiveResult{}, err
	}
	return RunInteractiveNormalized(ctx, engine, database, statement, parameters, normalized, maxRows, payloadCap, cellPreview)
}

func RunInteractiveNormalized(ctx context.Context, engine QueryEngine, database *sql.DB, statement string, parameters []Parameter, normalized map[string]NormalizedValue, maxRows int, payloadCap int64, cellPreview int) (InteractiveResult, error) {
	if maxRows <= 0 || payloadCap < 4096 || cellPreview <= 0 {
		return InteractiveResult{}, fmt.Errorf("invalid interactive bounds")
	}
	sink := &interactiveSink{maxRows: maxRows, payloadCap: payloadCap, cellPreview: cellPreview, approximateSize: 1024}
	err := engine.StreamNormalized(ctx, database, statement, parameters, normalized, sink)
	if err != nil && !errors.Is(err, errInteractiveBound) {
		return InteractiveResult{}, err
	}
	if fitErr := sink.fit(); fitErr != nil {
		return InteractiveResult{}, fitErr
	}
	return sink.result, nil
}

var errInteractiveBound = errors.New("interactive result bound reached")

func (sink *interactiveSink) Columns(columns []Column) error {
	sink.result.Columns = append([]Column(nil), columns...)
	encoded, _ := json.Marshal(columns)
	sink.approximateSize += int64(len(encoded))
	if sink.approximateSize > sink.payloadCap {
		sink.truncate("payload_limit")
		return errInteractiveBound
	}
	return nil
}

func (sink *interactiveSink) Row(values []driver.Value) error {
	if len(sink.result.Rows) >= sink.maxRows {
		sink.truncate("row_limit")
		return errInteractiveBound
	}
	row := make([]Cell, len(values))
	for index, value := range values {
		row[index] = previewCell(value, sink.result.Columns[index].DatabaseType, sink.cellPreview)
		if row[index].Preview {
			sink.result.CellPreviews++
		}
	}
	encoded, _ := json.Marshal(row)
	if sink.approximateSize+int64(len(encoded))+1 > sink.payloadCap {
		sink.truncate("payload_limit")
		return errInteractiveBound
	}
	sink.approximateSize += int64(len(encoded)) + 1
	sink.result.Rows = append(sink.result.Rows, row)
	return nil
}

func (sink *interactiveSink) truncate(reason string) {
	sink.result.Truncated = true
	sink.result.TruncationReason = reason
}

func (sink *interactiveSink) fit() error {
	for {
		encoded, err := json.Marshal(sink.result)
		if err != nil {
			return fmt.Errorf("encode interactive result: %w", err)
		}
		sink.result.EncodedBytes = len(encoded)
		encoded, err = json.Marshal(sink.result)
		if err != nil {
			return fmt.Errorf("encode interactive result: %w", err)
		}
		if int64(len(encoded)) <= sink.payloadCap {
			sink.result.EncodedBytes = len(encoded)
			return nil
		}
		if len(sink.result.Rows) == 0 {
			return fmt.Errorf("interactive payload cap is too small for result metadata")
		}
		sink.result.Rows = sink.result.Rows[:len(sink.result.Rows)-1]
		sink.truncate("payload_limit")
	}
}

func previewCell(value driver.Value, databaseType string, limit int) Cell {
	if value == nil {
		return Cell{Null: true}
	}
	var text string
	binary := isBinaryDatabaseType(databaseType)
	switch typed := value.(type) {
	case []byte:
		if binary {
			return Cell{Text: fmt.Sprintf("[binary: %d bytes]", len(typed)), Binary: true, OriginalBytes: len(typed)}
		}
		text = string(typed)
	case time.Time:
		if strings.EqualFold(databaseType, "DATE") {
			text = typed.Format("2006-01-02")
		} else {
			text = typed.Format("2006-01-02 15:04:05.999999")
		}
	case bool:
		if typed {
			text = "true"
		} else {
			text = "false"
		}
	default:
		text = fmt.Sprint(typed)
	}
	original := len(text)
	if original <= limit {
		return Cell{Text: text, OriginalBytes: original}
	}
	end := limit
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return Cell{Text: text[:end], Preview: true, OriginalBytes: original}
}

func isBinaryDatabaseType(value string) bool {
	value = strings.ToUpper(value)
	return strings.Contains(value, "BLOB") || strings.Contains(value, "BINARY") || value == "BIT"
}
