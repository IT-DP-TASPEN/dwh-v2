package reportexport

import (
	"archive/zip"
	"context"
	"database/sql/driver"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/reporting"
)

func TestWorkbookUsesLiteralStringsAndSplitsParts(t *testing.T) {
	workspace := t.TempDir()
	sink, err := NewWorkbookSink(context.Background(), workspace, "Formula report", WorkbookConfig{RowsPerPart: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Columns([]reporting.Column{{Name: "value", DatabaseType: "VARCHAR"}}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"=2+2", "+SUM(A1:A2)", "@command"} {
		if err := sink.Row([]driver.Value{[]byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	path, _, kind, parts, rows, err := sink.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if kind != "zip" || parts != 2 || rows != 3 {
		t.Fatalf("kind=%s parts=%d rows=%d", kind, parts, rows)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 {
		t.Fatalf("zip entries=%d", len(archive.File))
	}
	for _, part := range archive.File {
		partReader, err := part.Open()
		if err != nil {
			t.Fatal(err)
		}
		partBytes, err := io.ReadAll(partReader)
		partReader.Close()
		if err != nil {
			t.Fatal(err)
		}
		partPath := filepath.Join(workspace, "inspect.xlsx")
		if err := os.WriteFile(partPath, partBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		xlsx, err := zip.OpenReader(partPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range xlsx.File {
			if !strings.HasSuffix(entry.Name, "sheet1.xml") {
				continue
			}
			reader, _ := entry.Open()
			xml, _ := io.ReadAll(reader)
			reader.Close()
			if strings.Contains(string(xml), "<f") {
				t.Fatalf("formula emitted in %s", entry.Name)
			}
		}
		xlsx.Close()
	}
}

func TestWorkbookZeroRowsAndLimits(t *testing.T) {
	if ExcelMaxDataRows != 1048575 || ExcelMaxColumns != 16384 {
		t.Fatal("Excel boundary constants changed")
	}
	sink, err := NewWorkbookSink(context.Background(), t.TempDir(), "empty", WorkbookConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Columns([]reporting.Column{{Name: "header"}}); err != nil {
		t.Fatal(err)
	}
	path, _, kind, parts, rows, err := sink.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if kind != "xlsx" || parts != 1 || rows != 0 {
		t.Fatalf("path=%s kind=%s parts=%d rows=%d", path, kind, parts, rows)
	}
	if _, err := workbookValue([]byte(strings.Repeat("x", ExcelMaxCellRunes+1)), "TEXT"); err == nil {
		t.Fatal("oversized cell accepted")
	}
	columns := make([]reporting.Column, ExcelMaxColumns+1)
	over, _ := NewWorkbookSink(context.Background(), t.TempDir(), "wide", WorkbookConfig{})
	if err := over.Columns(columns); err == nil {
		t.Fatal("oversized column set accepted")
	}
}
