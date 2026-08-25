package reportexport

import (
	"archive/zip"
	"context"
	"database/sql/driver"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/ibldzn/go-admin/internal/reporting"
)

const (
	ExcelMaxColumns   = 16384
	ExcelMaxDataRows  = 1048575
	ExcelMaxCellRunes = 32767
)

type WorkbookConfig struct {
	RowsPerPart int
	Progress    func(rows uint64, part uint32) error
}

type WorkbookSink struct {
	ctx          context.Context
	workspace    string
	name         string
	config       WorkbookConfig
	columns      []reporting.Column
	file         *excelize.File
	stream       *excelize.StreamWriter
	part         uint32
	partRows     int
	totalRows    uint64
	parts        []string
	lastProgress time.Time
}

func NewWorkbookSink(ctx context.Context, workspace, reportName string, config WorkbookConfig) (*WorkbookSink, error) {
	if config.RowsPerPart == 0 {
		config.RowsPerPart = ExcelMaxDataRows
	}
	if config.RowsPerPart < 1 || config.RowsPerPart > ExcelMaxDataRows {
		return nil, fmt.Errorf("invalid workbook row limit")
	}
	return &WorkbookSink{ctx: ctx, workspace: workspace, name: safeArtifactName(reportName), config: config}, nil
}

func (sink *WorkbookSink) Columns(columns []reporting.Column) error {
	if len(columns) == 0 {
		return fmt.Errorf("report query returned no columns")
	}
	if len(columns) > ExcelMaxColumns {
		return fmt.Errorf("report has %d columns; XLSX supports at most %d", len(columns), ExcelMaxColumns)
	}
	sink.columns = append([]reporting.Column(nil), columns...)
	return sink.openPart()
}

func (sink *WorkbookSink) Row(values []driver.Value) error {
	if err := sink.ctx.Err(); err != nil {
		return context.Cause(sink.ctx)
	}
	if len(values) != len(sink.columns) {
		return fmt.Errorf("report row column count changed")
	}
	if sink.partRows == sink.config.RowsPerPart {
		if err := sink.closePart(); err != nil {
			return err
		}
		if err := sink.openPart(); err != nil {
			return err
		}
	}
	row := make([]interface{}, len(values))
	for index, value := range values {
		converted, err := workbookValue(value, sink.columns[index].DatabaseType)
		if err != nil {
			return fmt.Errorf("column %d: %w", index+1, err)
		}
		row[index] = excelize.Cell{Value: converted}
	}
	cell, _ := excelize.CoordinatesToCellName(1, sink.partRows+2)
	if err := sink.stream.SetRow(cell, row); err != nil {
		return err
	}
	sink.partRows++
	sink.totalRows++
	if sink.config.Progress != nil && (sink.totalRows%1000 == 0 || time.Since(sink.lastProgress) >= 2*time.Second) {
		if err := sink.config.Progress(sink.totalRows, sink.part); err != nil {
			return err
		}
		sink.lastProgress = time.Now()
	}
	return nil
}

func (sink *WorkbookSink) Finish() (string, string, string, uint32, uint64, error) {
	if sink.stream == nil {
		return "", "", "", 0, 0, fmt.Errorf("workbook has no columns")
	}
	if err := sink.closePart(); err != nil {
		return "", "", "", 0, 0, err
	}
	if len(sink.parts) == 1 {
		name := sink.name + ".xlsx"
		path := filepath.Join(sink.workspace, name)
		if err := os.Rename(sink.parts[0], path); err != nil {
			return "", "", "", 0, 0, err
		}
		return path, name, "xlsx", 1, sink.totalRows, nil
	}
	name := sink.name + ".zip"
	path := filepath.Join(sink.workspace, name)
	if err := zipParts(sink.ctx, path, sink.parts, sink.name); err != nil {
		return "", "", "", 0, 0, err
	}
	return path, name, "zip", uint32(len(sink.parts)), sink.totalRows, nil
}

func (sink *WorkbookSink) Abort() {
	if sink.file != nil {
		_ = sink.file.Close()
		sink.file = nil
		sink.stream = nil
	}
}

func (sink *WorkbookSink) openPart() error {
	sink.part++
	sink.partRows = 0
	sink.file = excelize.NewFile()
	if err := sink.file.SetSheetName("Sheet1", "Report"); err != nil {
		_ = sink.file.Close()
		return err
	}
	stream, err := sink.file.NewStreamWriter("Report")
	if err != nil {
		_ = sink.file.Close()
		return err
	}
	sink.stream = stream
	header := make([]interface{}, len(sink.columns))
	for index, column := range sink.columns {
		header[index] = excelize.Cell{Value: column.Name}
	}
	if err := sink.stream.SetRow("A1", header); err != nil {
		return err
	}
	return nil
}

func (sink *WorkbookSink) closePart() error {
	if err := sink.ctx.Err(); err != nil {
		sink.Abort()
		return context.Cause(sink.ctx)
	}
	if err := sink.stream.Flush(); err != nil {
		sink.Abort()
		return err
	}
	path := filepath.Join(sink.workspace, fmt.Sprintf("part-%06d.xlsx", sink.part))
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		sink.Abort()
		return err
	}
	writeErr := sink.file.Write(contextWriter{ctx: sink.ctx, writer: output})
	closeErr := output.Close()
	fileCloseErr := sink.file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	if fileCloseErr != nil {
		_ = os.Remove(path)
		return fileCloseErr
	}
	sink.parts = append(sink.parts, path)
	sink.stream, sink.file = nil, nil
	return nil
}

func workbookValue(value driver.Value, databaseType string) (any, error) {
	if value == nil {
		return "", nil
	}
	var result any
	switch typed := value.(type) {
	case []byte:
		if isBinary(databaseType) {
			result = base64.StdEncoding.EncodeToString(typed)
		} else {
			result = string(typed)
		}
	case int64:
		if typed > 999999999999999 || typed < -999999999999999 {
			result = strconv.FormatInt(typed, 10)
		} else {
			result = typed
		}
	case float64, bool:
		result = typed
	case time.Time:
		if strings.EqualFold(databaseType, "DATE") {
			result = typed.Format("2006-01-02")
		} else {
			result = typed.Format("2006-01-02 15:04:05.999999")
		}
	case string:
		result = typed
	default:
		return nil, fmt.Errorf("unsupported result value %T", value)
	}
	if text, ok := result.(string); ok && excelCharacterCount(text) > ExcelMaxCellRunes {
		return nil, fmt.Errorf("cell exceeds Excel's %d-character limit", ExcelMaxCellRunes)
	}
	return result, nil
}

func excelCharacterCount(value string) int {
	count := 0
	for _, character := range value {
		count++
		if character > 0xffff {
			count++
		}
	}
	return count
}

func isBinary(databaseType string) bool {
	typeName := strings.ToUpper(databaseType)
	return strings.Contains(typeName, "BLOB") || strings.Contains(typeName, "BINARY") || typeName == "BIT"
}

func zipParts(ctx context.Context, destination string, parts []string, reportName string) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(contextWriter{ctx: ctx, writer: file})
	for index, path := range parts {
		entry, err := archive.Create(fmt.Sprintf("%s-part-%03d.xlsx", reportName, index+1))
		if err != nil {
			archive.Close()
			file.Close()
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			archive.Close()
			file.Close()
			return err
		}
		_, copyErr := io.Copy(entry, contextReader{ctx: ctx, reader: source})
		closeErr := source.Close()
		if copyErr != nil {
			archive.Close()
			file.Close()
			return copyErr
		}
		if closeErr != nil {
			archive.Close()
			file.Close()
			return closeErr
		}
	}
	if err := archive.Close(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer contextWriter) Write(value []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		if cause := context.Cause(writer.ctx); cause != nil {
			return 0, cause
		}
		return 0, err
	}
	return writer.writer.Write(value)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(value []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		if cause := context.Cause(reader.ctx); cause != nil {
			return 0, cause
		}
		return 0, err
	}
	return reader.reader.Read(value)
}

var unsafeArtifact = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeArtifactName(value string) string {
	value = strings.Trim(unsafeArtifact.ReplaceAllString(strings.TrimSpace(value), "-"), "-._")
	if value == "" {
		return "report"
	}
	if len(value) > 120 {
		value = value[:120]
	}
	return value
}
