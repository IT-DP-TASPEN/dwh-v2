package ingestionstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	databasepkg "github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

const defaultMaintenanceLockTimeout = 30 * time.Second

type MaintenanceRepository struct {
	db          *sqlx.DB
	lockTimeout time.Duration
}

type MaintenanceSnapshot struct {
	RequestedDate ingestion.CalendarDate
	FileName      string
	Parsed        ingestion.ParsedMaintenanceCSV
}

func NewMaintenanceRepository(db *sqlx.DB) *MaintenanceRepository {
	return &MaintenanceRepository{db: db, lockTimeout: defaultMaintenanceLockTimeout}
}

func (repository *MaintenanceRepository) SaveSnapshot(ctx context.Context, runID uint64, ownerID string, snapshot MaintenanceSnapshot) error {
	return repository.saveSnapshot(ctx, runID, ownerID, snapshot, true)
}

// saveSnapshotWithoutRunFence exercises storage replacement independently in
// integration tests. Production publication must use SaveSnapshot.
func (repository *MaintenanceRepository) saveSnapshotWithoutRunFence(ctx context.Context, snapshot MaintenanceSnapshot) error {
	return repository.saveSnapshot(ctx, 0, "", snapshot, false)
}

func (repository *MaintenanceRepository) saveSnapshot(ctx context.Context, runID uint64, ownerID string, snapshot MaintenanceSnapshot, fenced bool) (err error) {
	if repository == nil || repository.db == nil {
		return fmt.Errorf("maintenance repository is not configured")
	}
	if fenced && (runID == 0 || ownerID == "") {
		return fmt.Errorf("complete maintenance publication ownership is required")
	}
	definition := snapshot.Parsed.Definition
	if snapshot.RequestedDate.IsZero() || snapshot.FileName == "" || snapshot.Parsed.AsOfDate != snapshot.RequestedDate || definition.SchemaMode != ingestion.DynamicAdditive || len(snapshot.Parsed.Columns) == 0 {
		return fmt.Errorf("complete dynamic-additive maintenance snapshot is required")
	}
	if !canonicalMaintenanceDefinition(definition) {
		return fmt.Errorf("maintenance definition %q is not canonical", definition.Key)
	}
	if err := validateMaintenanceSnapshot(snapshot); err != nil {
		return err
	}
	if _, err := quoteIdentifier(definition.TableName); err != nil {
		return err
	}
	connection, err := repository.db.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin maintenance connection: %w", err)
	}
	defer connection.Close()
	var databaseName string
	if err := connection.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&databaseName); err != nil || databaseName == "" {
		return fmt.Errorf("identify maintenance database: %w", err)
	}
	lockName := maintenanceLockName(databaseName, definition.TableName)
	seconds := int64(math.Ceil(repository.lockTimeout.Seconds()))
	var acquired sql.NullInt64
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, seconds).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire maintenance schema lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("maintenance schema lock timed out after %s", repository.lockTimeout)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		releaseErr := connection.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
		if releaseErr != nil || !released.Valid || released.Int64 != 1 {
			discardErr := discardPinnedConnection(connection)
			err = errors.Join(err, fmt.Errorf("release maintenance schema lock was uncertain: result=%v error=%w", released, releaseErr), discardErr)
		}
	}()
	proveOwnership := func() error {
		if !fenced {
			return nil
		}
		var owned bool
		if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_runs
			WHERE id=? AND status='running' AND owner_id=? AND cancel_requested_at IS NULL)`, runID, ownerID).Scan(&owned); err != nil {
			return fmt.Errorf("check maintenance ownership: %w", err)
		}
		if !owned {
			return ingestionrun.ErrOwnershipLost
		}
		return nil
	}
	if err := syncMaintenanceSchema(ctx, connection, databaseName, snapshot, proveOwnership); err != nil {
		return err
	}
	return replaceMaintenanceSnapshot(ctx, connection, runID, ownerID, snapshot, fenced)
}

func maintenanceLockName(databaseName, tableName string) string {
	sum := sha256.Sum256([]byte(databaseName + "." + tableName))
	return "dwh-ddl:" + hex.EncodeToString(sum[:])[:56]
}

func discardPinnedConnection(connection *sql.Conn) error {
	if connection == nil {
		return nil
	}
	err := connection.Raw(func(any) error { return driver.ErrBadConn })
	if err != nil && !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("discard lock-owning connection: %w", err)
	}
	return nil
}

type physicalColumn struct {
	Name       string `db:"COLUMN_NAME"`
	ColumnType string `db:"COLUMN_TYPE"`
	Nullable   string `db:"IS_NULLABLE"`
}

func syncMaintenanceSchema(ctx context.Context, connection *sql.Conn, databaseName string, snapshot MaintenanceSnapshot, proveOwnership func() error) error {
	registered := map[string]string{}
	rows, err := connection.QueryContext(ctx, `SELECT physical_column, original_header FROM dynamic_csv_source_columns WHERE source_id = ?`, snapshot.Parsed.Definition.Key)
	if err != nil {
		return fmt.Errorf("read dynamic column registry: %w", err)
	}
	for rows.Next() {
		var physical, original string
		if err := rows.Scan(&physical, &original); err != nil {
			rows.Close()
			return err
		}
		registered[physical] = original
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range snapshot.Parsed.Columns {
		if original, found := registered[column.PhysicalName]; found && original != column.OriginalHeader {
			return fmt.Errorf("header %q conflicts with registered %q for %s", column.OriginalHeader, original, column.PhysicalName)
		}
	}
	physical, err := maintenanceColumns(ctx, connection, databaseName, snapshot.Parsed.Definition.TableName)
	if err != nil {
		return err
	}
	if len(physical) == 0 {
		query, err := createMaintenanceTableSQL(snapshot.Parsed)
		if err != nil {
			return err
		}
		// MySQL DDL may implicitly commit. It is additive, idempotent and
		// non-destructive; an already-started CREATE may finish after lease loss.
		if err := proveOwnership(); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("create maintenance table: %w", err)
		}
		return nil
	}
	if err := validateMaintenancePhysicalSchema(physical, snapshot.Parsed.Definition); err != nil {
		return err
	}
	if err := validateMaintenancePrimaryKey(ctx, connection, databaseName, snapshot.Parsed.Definition); err != nil {
		return err
	}
	missing := make([]string, 0)
	for _, column := range snapshot.Parsed.Columns {
		if _, found := physical[column.PhysicalName]; !found {
			quoted, _ := quoteIdentifier(column.PhysicalName)
			missing = append(missing, "ADD COLUMN "+quoted+" TEXT NULL")
		}
	}
	if len(missing) > 0 {
		quotedTable, _ := quoteIdentifier(snapshot.Parsed.Definition.TableName)
		// Same MySQL DDL fencing limit as CREATE above. Authoritative snapshot
		// data and success remain protected by the later transactional fence.
		if err := proveOwnership(); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, "ALTER TABLE "+quotedTable+" "+strings.Join(missing, ", ")); err != nil {
			return fmt.Errorf("add maintenance columns: %w", err)
		}
	}
	return nil
}

func maintenanceColumns(ctx context.Context, connection *sql.Conn, databaseName, tableName string) (map[string]physicalColumn, error) {
	rows, err := connection.QueryContext(ctx, `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, databaseName, tableName)
	if err != nil {
		return nil, fmt.Errorf("inspect maintenance table: %w", err)
	}
	defer rows.Close()
	columns := map[string]physicalColumn{}
	for rows.Next() {
		var column physicalColumn
		if err := rows.Scan(&column.Name, &column.ColumnType, &column.Nullable); err != nil {
			return nil, err
		}
		columns[column.Name] = column
	}
	return columns, rows.Err()
}

func validateMaintenancePhysicalSchema(columns map[string]physicalColumn, definition ingestion.MaintenanceDefinition) error {
	type expectedColumn struct{ columnType, nullable string }
	businessNullable := "YES"
	if definition.Identity == ingestion.BusinessKeyIdentity {
		businessNullable = "NO"
	}
	required := map[string]expectedColumn{
		"requested_date": {"date", "NO"}, "as_of_date": {"date", "NO"}, "source_file_name": {"varchar(255)", "NO"},
		"source_row_number": {"bigint unsigned", "NO"}, "source_row_checksum": {"char(64)", "NO"},
		"business_key_hash": {"char(64)", businessNullable}, "created_at": {"datetime(6)", "NO"}, "updated_at": {"datetime(6)", "NO"},
	}
	for name, expected := range required {
		column, found := columns[name]
		if !found || strings.ToLower(column.ColumnType) != expected.columnType || column.Nullable != expected.nullable {
			return fmt.Errorf("maintenance metadata column %s must be %s nullable=%s", name, expected.columnType, expected.nullable)
		}
	}
	for name, column := range columns {
		if _, metadata := required[name]; !metadata && (strings.ToLower(column.ColumnType) != "text" || column.Nullable != "YES") {
			return fmt.Errorf("historical maintenance column %s must be TEXT NULL", name)
		}
	}
	return nil
}

func validateMaintenancePrimaryKey(ctx context.Context, connection *sql.Conn, databaseName string, definition ingestion.MaintenanceDefinition) error {
	var columns []string
	rows, err := connection.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		WHERE CONSTRAINT_SCHEMA=? AND TABLE_NAME=? AND CONSTRAINT_NAME='PRIMARY' ORDER BY ORDINAL_POSITION`, databaseName, definition.TableName)
	if err != nil {
		return err
	}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, column)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	want := []string{"as_of_date", "source_row_number"}
	if definition.Identity == ingestion.BusinessKeyIdentity {
		want = []string{"as_of_date", "business_key_hash"}
	}
	if !slices.Equal(columns, want) {
		return fmt.Errorf("maintenance table %s primary key is %v, want %v", definition.TableName, columns, want)
	}
	return nil
}

func canonicalMaintenanceDefinition(definition ingestion.MaintenanceDefinition) bool {
	if definition.FilePattern == nil {
		return false
	}
	for _, candidate := range ingestion.MaintenanceDefinitions() {
		if candidate.Key == definition.Key && candidate.Name == definition.Name && candidate.Kind == definition.Kind &&
			candidate.TableName == definition.TableName && candidate.Identity == definition.Identity && candidate.SchemaMode == definition.SchemaMode &&
			candidate.FixtureGapAccepted == definition.FixtureGapAccepted && candidate.FilePattern.String() == definition.FilePattern.String() &&
			slices.Equal(candidate.BusinessKeyColumns, definition.BusinessKeyColumns) {
			return true
		}
	}
	return false
}

func validateMaintenanceSnapshot(snapshot MaintenanceSnapshot) error {
	seen := map[string]bool{}
	for _, column := range snapshot.Parsed.Columns {
		if _, err := quoteIdentifier(column.PhysicalName); err != nil {
			return err
		}
		if seen[column.PhysicalName] {
			return fmt.Errorf("duplicate maintenance column %s", column.PhysicalName)
		}
		seen[column.PhysicalName] = true
	}
	identities := map[string]bool{}
	for _, row := range snapshot.Parsed.Rows {
		if row.SourceRowNumber < 2 || len(row.RowChecksum) != 64 || len(row.Values) != len(snapshot.Parsed.Columns) {
			return fmt.Errorf("invalid prepared maintenance row %d", row.SourceRowNumber)
		}
		identity := fmt.Sprintf("row:%d", row.SourceRowNumber)
		if snapshot.Parsed.Definition.Identity == ingestion.BusinessKeyIdentity {
			if len(row.BusinessKeyHash) != 64 {
				return fmt.Errorf("maintenance row %d is missing business-key hash", row.SourceRowNumber)
			}
			identity = "key:" + row.BusinessKeyHash
		} else if row.BusinessKeyHash != "" {
			return fmt.Errorf("row-number maintenance row %d has a business-key hash", row.SourceRowNumber)
		}
		if identities[identity] {
			return fmt.Errorf("duplicate prepared maintenance identity %s", identity)
		}
		identities[identity] = true
	}
	return nil
}

func createMaintenanceTableSQL(parsed ingestion.ParsedMaintenanceCSV) (string, error) {
	quotedTable, err := quoteIdentifier(parsed.Definition.TableName)
	if err != nil {
		return "", err
	}
	businessNullable := "NULL"
	primary := "PRIMARY KEY (`as_of_date`, `source_row_number`)"
	if parsed.Definition.Identity == ingestion.BusinessKeyIdentity {
		businessNullable = "NOT NULL"
		primary = "PRIMARY KEY (`as_of_date`, `business_key_hash`)"
	}
	definitions := []string{
		"`requested_date` DATE NOT NULL", "`as_of_date` DATE NOT NULL", "`source_file_name` VARCHAR(255) NOT NULL",
		"`source_row_number` BIGINT UNSIGNED NOT NULL", "`source_row_checksum` CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
		"`business_key_hash` CHAR(64) CHARACTER SET ascii COLLATE ascii_bin " + businessNullable,
	}
	for _, column := range parsed.Columns {
		quoted, err := quoteIdentifier(column.PhysicalName)
		if err != nil {
			return "", err
		}
		definitions = append(definitions, quoted+" TEXT NULL")
	}
	definitions = append(definitions,
		"`created_at` DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)",
		"`updated_at` DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)", primary,
		"KEY `idx_maintenance_source_file` (`source_file_name`)")
	return "CREATE TABLE " + quotedTable + " (" + strings.Join(definitions, ",") + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci", nil
}

func replaceMaintenanceSnapshot(ctx context.Context, connection *sql.Conn, runID uint64, ownerID string, snapshot MaintenanceSnapshot, fenced bool) error {
	_, err := databasepkg.RetryReplaySafeConnTx(ctx, connection, func(tx *sql.Tx) error {
		table, _ := quoteIdentifier(snapshot.Parsed.Definition.TableName)
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE as_of_date = ?", snapshot.RequestedDate.String()); err != nil {
			return fmt.Errorf("delete maintenance snapshot: %w", err)
		}
		columns := []string{"requested_date", "as_of_date", "source_file_name", "source_row_number", "source_row_checksum", "business_key_hash"}
		for _, column := range snapshot.Parsed.Columns {
			columns = append(columns, column.PhysicalName)
		}
		values := make([][]any, len(snapshot.Parsed.Rows))
		for index, row := range snapshot.Parsed.Rows {
			businessKey := any(nil)
			if row.BusinessKeyHash != "" {
				businessKey = row.BusinessKeyHash
			}
			values[index] = []any{snapshot.RequestedDate.String(), snapshot.RequestedDate.String(), snapshot.FileName, row.SourceRowNumber, row.RowChecksum, businessKey}
			for _, value := range row.Values {
				values[index] = append(values[index], value)
			}
		}
		if err := insertRows(ctx, tx, snapshot.Parsed.Definition.TableName, columns, values); err != nil {
			return err
		}
		if err := upsertDynamicRegistry(ctx, tx, snapshot); err != nil {
			return err
		}
		if fenced {
			result, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET heartbeat_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='running' AND owner_id=? AND cancel_requested_at IS NULL`, runID, ownerID)
			if err != nil {
				return fmt.Errorf("fence maintenance publication: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return errors.Join(ingestionrun.ErrOwnershipLost, err)
			}
		}
		return nil
	})
	return err
}

func upsertDynamicRegistry(ctx context.Context, tx *sql.Tx, snapshot MaintenanceSnapshot) error {
	definition := snapshot.Parsed.Definition
	if _, err := tx.ExecContext(ctx, `INSERT INTO dynamic_csv_sources
		(source_id, source_kind, table_name, identity_mode, schema_mode, first_seen_at, last_seen_at,
		 first_seen_filename, last_seen_filename, first_seen_as_of_date, last_seen_as_of_date)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP(6), CURRENT_TIMESTAMP(6), ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		 last_seen_at=VALUES(last_seen_at), last_seen_filename=VALUES(last_seen_filename), last_seen_as_of_date=VALUES(last_seen_as_of_date),
		 source_kind=VALUES(source_kind), table_name=VALUES(table_name), identity_mode=VALUES(identity_mode), schema_mode=VALUES(schema_mode)`,
		definition.Key, definition.Kind, definition.TableName, definition.Identity, definition.SchemaMode,
		snapshot.FileName, snapshot.FileName, snapshot.RequestedDate.String(), snapshot.RequestedDate.String()); err != nil {
		return fmt.Errorf("upsert dynamic source registry: %w", err)
	}
	for _, column := range snapshot.Parsed.Columns {
		if _, err := tx.ExecContext(ctx, `INSERT INTO dynamic_csv_source_columns
			(source_id, original_header, physical_column, ordinal_position, first_seen_at, last_seen_at,
			 first_seen_filename, last_seen_filename, first_seen_as_of_date, last_seen_as_of_date, seen_count)
			VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP(6), CURRENT_TIMESTAMP(6), ?, ?, ?, ?, 1)
			ON DUPLICATE KEY UPDATE
			 last_seen_at=VALUES(last_seen_at), last_seen_filename=VALUES(last_seen_filename),
			 last_seen_as_of_date=VALUES(last_seen_as_of_date), seen_count=seen_count+1`,
			definition.Key, column.OriginalHeader, column.PhysicalName, column.Ordinal,
			snapshot.FileName, snapshot.FileName, snapshot.RequestedDate.String(), snapshot.RequestedDate.String()); err != nil {
			return fmt.Errorf("upsert dynamic column registry: %w", err)
		}
	}
	return nil
}
