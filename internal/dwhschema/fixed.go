package dwhschema

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/jmoiron/sqlx"
)

// verifyFixedPublicationSchema checks the current Fixed publication contract.
func verifyFixedPublicationSchema(ctx context.Context, db *sqlx.DB) error {
	type column struct {
		Table    string `db:"TABLE_NAME"`
		Name     string `db:"COLUMN_NAME"`
		Type     string `db:"DATA_TYPE"`
		Nullable string `db:"IS_NULLABLE"`
		Length   *int64 `db:"CHARACTER_MAXIMUM_LENGTH"`
	}
	var columns []column
	if err := db.SelectContext(ctx, &columns, `SELECT TABLE_NAME,COLUMN_NAME,DATA_TYPE,IS_NULLABLE,CHARACTER_MAXIMUM_LENGTH
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE()
		AND (TABLE_NAME LIKE 'fixed_report_%' OR TABLE_NAME LIKE 'fincloud_%' OR TABLE_NAME LIKE 'stg_fincloud_%')`); err != nil {
		return fmt.Errorf("inspect Fixed publication columns: %w", err)
	}
	byColumn := make(map[string]column, len(columns))
	for _, column := range columns {
		byColumn[column.Table+"."+column.Name] = column
	}
	requireColumn := func(table, name, dataType, nullable string, length int64) error {
		got, found := byColumn[table+"."+name]
		if !found || got.Type != dataType || (nullable != "" && got.Nullable != nullable) ||
			(length > 0 && (got.Length == nil || *got.Length != length)) {
			return fmt.Errorf("required Fixed column %s.%s has an invalid definition", table, name)
		}
		return nil
	}
	type index struct {
		Table     string `db:"TABLE_NAME"`
		Name      string `db:"INDEX_NAME"`
		Columns   string `db:"columns_list"`
		NonUnique int    `db:"NON_UNIQUE"`
		Prefixes  int    `db:"prefixes"`
	}
	var indexes []index
	if err := db.SelectContext(ctx, &indexes, `SELECT TABLE_NAME,INDEX_NAME,NON_UNIQUE,
		GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') AS columns_list,
		SUM(SUB_PART IS NOT NULL) AS prefixes FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA=DATABASE() AND (TABLE_NAME LIKE 'fixed_report_%' OR TABLE_NAME LIKE 'fincloud_%')
		GROUP BY TABLE_NAME,INDEX_NAME,NON_UNIQUE`); err != nil {
		return fmt.Errorf("inspect Fixed publication indexes: %w", err)
	}
	byIndex := make(map[string]index, len(indexes))
	for _, index := range indexes {
		byIndex[index.Table+"."+index.Name] = index
	}
	requireIndex := func(table, name, columns string, unique bool) error {
		got, found := byIndex[table+"."+name]
		if !found || got.Columns != columns || got.Prefixes != 0 ||
			(unique && got.NonUnique != 0) || (!unique && got.NonUnique != 1) {
			return fmt.Errorf("required Fixed index %s.%s has an invalid definition", table, name)
		}
		return nil
	}
	wantLocks := make([]string, 0, 8)
	for _, definition := range ingestion.FixedDefinitions() {
		wantLocks = append(wantLocks, definition.Key)
		table, err := ingestion.FixedTableName(definition.Key)
		if err != nil {
			return err
		}
		for _, storage := range []string{table, "stg_" + table} {
			if definition.PublicationMode == ingestion.DateAddressable {
				if err := requireColumn(storage, "coverage_date", "date", "NO", 0); err != nil {
					return err
				}
			} else if _, found := byColumn[storage+".coverage_date"]; found {
				return fmt.Errorf("interval-result table %s must not have coverage_date", storage)
			}
		}
		if definition.PublicationMode == ingestion.DateAddressable {
			if err := requireIndex(table, "idx_fixed_coverage_date", "coverage_date", false); err != nil {
				return err
			}
		}
	}
	for _, required := range []struct{ table, columns string }{
		{"fixed_report_publications", "job_key,period_from,period_to"},
		{"fixed_report_date_publications", "job_key,coverage_date"},
		{"fixed_report_publication_locks", "job_key"},
		{"fixed_report_load_segments", "load_id,member_key,segment_index"},
	} {
		if err := requireIndex(required.table, "PRIMARY", required.columns, true); err != nil {
			return err
		}
	}
	for _, required := range []struct {
		table, name, dataType, nullable string
		length                          int64
	}{
		{"fixed_report_date_publications", "job_key", "varchar", "NO", 128},
		{"fixed_report_date_publications", "coverage_date", "date", "NO", 0},
		{"fixed_report_date_publications", "active_load_id", "bigint", "NO", 0},
		{"fixed_report_date_publications", "published_at", "datetime", "NO", 0},
		{"fixed_report_publication_locks", "job_key", "varchar", "NO", 128},
		{"fixed_report_load_segments", "load_id", "bigint", "NO", 0},
		{"fixed_report_load_segments", "member_key", "varchar", "NO", 191},
		{"fixed_report_load_segments", "segment_index", "int", "NO", 0},
		{"fixed_report_load_segments", "source_period_from", "date", "NO", 0},
		{"fixed_report_load_segments", "source_period_to", "date", "NO", 0},
		{"fixed_report_load_segments", "as_of_date", "date", "NO", 0},
		{"fixed_report_load_segments", "row_count", "bigint", "NO", 0},
		{"fixed_report_load_segments", "request_variant", "varchar", "YES", 191},
		{"fixed_report_load_segments", "segment_checksum", "binary", "NO", 32},
		{"fixed_report_load_members", "staged_segment_count", "int", "NO", 0},
		{"fixed_report_load_members", "source_location_id", "varchar", "YES", 191},
		{"fixed_report_load_members", "account_code", "varchar", "YES", 191},
		{"fixed_report_load_members", "source_period_from", "date", "YES", 0},
		{"fixed_report_load_members", "source_period_to", "date", "YES", 0},
		{"fixed_report_load_members", "expected_segment_count", "int", "YES", 0},
		{"fixed_report_loads", "publication_mode", "varchar", "YES", 32},
		{"fixed_report_loads", "source_request_mode", "varchar", "YES", 32},
		{"fixed_report_loads", "source_max_chunk_days", "smallint", "YES", 0},
		{"fixed_report_loads", "source_variants", "json", "", 0},
		{"fixed_report_loads", "contract_version", "smallint", "YES", 0},
	} {
		if err := requireColumn(required.table, required.name, required.dataType, required.nullable, required.length); err != nil {
			return err
		}
	}
	var gotLocks []string
	if err := db.SelectContext(ctx, &gotLocks, `SELECT job_key FROM fixed_report_publication_locks ORDER BY BINARY job_key`); err != nil {
		return fmt.Errorf("verify Fixed publication mutex rows: %w", err)
	}
	sort.Strings(wantLocks)
	if strings.Join(gotLocks, "\x00") != strings.Join(wantLocks, "\x00") {
		return fmt.Errorf("Fixed publication mutex rows do not match the canonical eight jobs")
	}
	for _, required := range []struct{ table, columns, reference, referenceColumns string }{
		{"fixed_report_date_publications", "active_load_id", "fixed_report_loads", "id"},
		{"fixed_report_load_segments", "load_id,member_key", "fixed_report_load_members", "load_id,member_key"},
	} {
		var matching int
		if err := db.GetContext(ctx, &matching, `SELECT COUNT(*) FROM (
			SELECT k.CONSTRAINT_NAME,r.DELETE_RULE,k.REFERENCED_TABLE_NAME,
			GROUP_CONCAT(k.COLUMN_NAME ORDER BY k.ORDINAL_POSITION SEPARATOR ',') AS local_columns,
			GROUP_CONCAT(k.REFERENCED_COLUMN_NAME ORDER BY k.ORDINAL_POSITION SEPARATOR ',') AS referenced_columns
			FROM information_schema.KEY_COLUMN_USAGE k
			JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA
			AND r.TABLE_NAME=k.TABLE_NAME AND r.CONSTRAINT_NAME=k.CONSTRAINT_NAME
			WHERE k.TABLE_SCHEMA=DATABASE() AND k.TABLE_NAME=?
			GROUP BY k.CONSTRAINT_NAME,r.DELETE_RULE,k.REFERENCED_TABLE_NAME
		) f WHERE local_columns=? AND REFERENCED_TABLE_NAME=? AND referenced_columns=? AND DELETE_RULE='RESTRICT'`,
			required.table, required.columns, required.reference, required.referenceColumns); err != nil || matching != 1 {
			return fmt.Errorf("required Fixed provenance foreign key on %s is invalid", required.table)
		}
	}
	return nil
}
