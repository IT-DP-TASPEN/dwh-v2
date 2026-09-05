package customdataset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

type DDL struct{ db *sqlx.DB }

func NewDDL(db *sqlx.DB) (*DDL, error) {
	if db == nil {
		return nil, fmt.Errorf("custom dataset DDL database is required")
	}
	return &DDL{db: db}, nil
}

func (ddl *DDL) Ensure(ctx context.Context, dataset Dataset, columns []Column, importID uint64) error {
	connection, err := ddl.db.Connx(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	lockName := fmt.Sprintf("custom_dataset_ddl:%d", dataset.ID)
	var acquired sql.NullInt64
	if err := connection.GetContext(ctx, &acquired, `SELECT GET_LOCK(?,30)`, lockName); err != nil || !acquired.Valid || acquired.Int64 != 1 {
		if err == nil {
			err = fmt.Errorf("DDL lock timed out")
		}
		return fmt.Errorf("acquire custom dataset DDL lock: %w", err)
	}
	defer connection.ExecContext(context.WithoutCancel(ctx), `SELECT RELEASE_LOCK(?)`, lockName)

	exact, exists, err := tableIsExact(ctx, connection, dataset, columns)
	if err != nil {
		return err
	}
	if exists && !exact {
		if dataset.Status != DatasetProvisioning {
			return fmt.Errorf("active custom dataset table %s does not match frozen metadata", dataset.TableName())
		}
		if err := resetAllowed(ctx, connection, dataset.ID, importID); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `DROP VIEW IF EXISTS `+quoteIdentifier(dataset.ViewName())); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `DROP TABLE `+quoteIdentifier(dataset.TableName())); err != nil {
			return err
		}
		exists = false
	}
	if !exists {
		if _, err := connection.ExecContext(ctx, createTableSQL(dataset, columns)); err != nil {
			return fmt.Errorf("create custom dataset table: %w", err)
		}
	}
	if dataset.Status == DatasetProvisioning {
		if err := resetAllowed(ctx, connection, dataset.ID, importID); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `DROP VIEW IF EXISTS `+quoteIdentifier(dataset.ViewName())); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, createViewSQL(dataset, columns)); err != nil {
			return fmt.Errorf("create custom dataset view: %w", err)
		}
		return nil
	}
	return verifyView(ctx, connection, dataset, columns)
}

func createTableSQL(dataset Dataset, columns []Column) string {
	definitions := []string{
		"`_row_id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY",
		"`_import_id` BIGINT UNSIGNED NOT NULL",
		"`_import_attempt` INT UNSIGNED NOT NULL",
		"`_source_record_number` BIGINT UNSIGNED NOT NULL",
	}
	for _, column := range columns {
		typeName, _ := sqlType(column)
		definitions = append(definitions, quoteIdentifier(column.PhysicalName)+" "+typeName+" NULL")
	}
	definitions = append(definitions, "UNIQUE KEY `uq_import_attempt_source` (`_import_id`,`_import_attempt`,`_source_record_number`)")
	return "CREATE TABLE " + quoteIdentifier(dataset.TableName()) + " (\n" + strings.Join(definitions, ",\n") + "\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
}

func createViewSQL(dataset Dataset, columns []Column) string {
	selects := make([]string, len(columns))
	for index, column := range columns {
		selects[index] = "r." + quoteIdentifier(column.PhysicalName) + " AS " + quoteIdentifier(column.QueryName)
	}
	return "CREATE SQL SECURITY INVOKER VIEW " + quoteIdentifier(dataset.ViewName()) + " AS SELECT " + strings.Join(selects, ",") +
		" FROM `custom_dataset_imports` i JOIN `custom_datasets` d ON d.id=i.dataset_id AND d.current_generation_id=i.generation_id JOIN " + quoteIdentifier(dataset.TableName()) +
		" r ON r._import_id=i.id AND r._import_attempt=i.published_attempt WHERE i.dataset_id=" + strconv.FormatUint(dataset.ID, 10) + " AND i.status='succeeded'"
}

func sqlType(column Column) (string, error) {
	switch column.LogicalType {
	case TypeText:
		if column.DateFormat != nil {
			break
		}
		return "LONGTEXT", nil
	case TypeInteger:
		if column.DateFormat != nil {
			break
		}
		return "BIGINT", nil
	case TypeDecimal:
		if column.DateFormat != nil {
			break
		}
		return "DECIMAL(65,30)", nil
	case TypeDate:
		if validDateFormat(column.DateFormat, false) {
			return "DATE", nil
		}
	case TypeDateTime:
		if validDateFormat(column.DateFormat, true) {
			return "DATETIME", nil
		}
	case TypeBoolean:
		if column.DateFormat != nil {
			break
		}
		return "BOOLEAN", nil
	}
	return "", fmt.Errorf("%w: invalid type/date format for column %d", ErrInvalid, column.Ordinal)
}

func validDateFormat(format *string, dateTime bool) bool {
	if format == nil {
		return false
	}
	for _, candidate := range []string{"YYYY-MM-DD", "DD/MM/YYYY", "MM/DD/YYYY"} {
		if dateTime {
			candidate += " HH:mm:ss"
		}
		if *format == candidate {
			return true
		}
	}
	return false
}

type physicalColumn struct {
	Name       string  `db:"name"`
	ColumnType string  `db:"column_type"`
	Nullable   string  `db:"nullable"`
	Extra      string  `db:"extra"`
	Key        string  `db:"column_key"`
	Collation  *string `db:"collation"`
}

func tableIsExact(ctx context.Context, connection *sqlx.Conn, dataset Dataset, columns []Column) (bool, bool, error) {
	var table struct{ Engine, Collation string }
	if err := connection.GetContext(ctx, &table, `SELECT ENGINE engine,TABLE_COLLATION collation FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND TABLE_TYPE='BASE TABLE'`, dataset.TableName()); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	if table.Engine == "" {
		return false, false, nil
	}
	if table.Engine != "InnoDB" || table.Collation != "utf8mb4_unicode_ci" {
		return false, true, nil
	}
	var got []physicalColumn
	if err := connection.SelectContext(ctx, &got, `SELECT COLUMN_NAME name,COLUMN_TYPE column_type,IS_NULLABLE nullable,EXTRA extra,COLUMN_KEY column_key,COLLATION_NAME collation FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, dataset.TableName()); err != nil {
		return false, true, err
	}
	expected := []physicalColumn{
		{Name: "_row_id", ColumnType: "bigint unsigned", Nullable: "NO", Extra: "auto_increment", Key: "PRI"},
		{Name: "_import_id", ColumnType: "bigint unsigned", Nullable: "NO", Key: "MUL"},
		{Name: "_import_attempt", ColumnType: "int unsigned", Nullable: "NO"},
		{Name: "_source_record_number", ColumnType: "bigint unsigned", Nullable: "NO"},
	}
	for _, column := range columns {
		typeName, err := sqlType(column)
		if err != nil {
			return false, true, err
		}
		columnType := strings.ToLower(typeName)
		if column.LogicalType == TypeBoolean {
			columnType = "tinyint(1)"
		}
		var collation *string
		if column.LogicalType == TypeText {
			value := "utf8mb4_unicode_ci"
			collation = &value
		}
		expected = append(expected, physicalColumn{Name: column.PhysicalName, ColumnType: columnType, Nullable: "YES", Collation: collation})
	}
	if len(got) != len(expected) {
		return false, true, nil
	}
	for index := range got {
		if got[index].Name != expected[index].Name || got[index].ColumnType != expected[index].ColumnType || got[index].Nullable != expected[index].Nullable || got[index].Extra != expected[index].Extra || got[index].Key != expected[index].Key || !sameStringPointer(got[index].Collation, expected[index].Collation) {
			return false, true, nil
		}
	}
	var indexes []struct {
		Name      string `db:"index_name"`
		NonUnique int    `db:"non_unique"`
		Sequence  int    `db:"seq_in_index"`
		Column    string `db:"column_name"`
	}
	if err := connection.SelectContext(ctx, &indexes, `SELECT INDEX_NAME index_name,NON_UNIQUE non_unique,SEQ_IN_INDEX seq_in_index,COLUMN_NAME column_name FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY INDEX_NAME,SEQ_IN_INDEX`, dataset.TableName()); err != nil {
		return false, true, err
	}
	if len(indexes) != 4 {
		return false, true, nil
	}
	wantNames := []string{"PRIMARY", "uq_import_attempt_source", "uq_import_attempt_source", "uq_import_attempt_source"}
	wantColumns := []string{"_row_id", "_import_id", "_import_attempt", "_source_record_number"}
	for index := range indexes {
		if indexes[index].Name != wantNames[index] || indexes[index].Column != wantColumns[index] || indexes[index].NonUnique != 0 {
			return false, true, nil
		}
	}
	return true, true, nil
}

func resetAllowed(ctx context.Context, connection *sqlx.Conn, datasetID, currentImportID uint64) error {
	var row struct {
		Status      DatasetStatus `db:"status"`
		Current     *uint64       `db:"current_generation_id"`
		Successful  int           `db:"successful"`
		OtherActive int           `db:"other_active"`
	}
	if err := connection.GetContext(ctx, &row, `SELECT d.status,d.current_generation_id,(SELECT COUNT(*) FROM custom_dataset_imports s WHERE s.dataset_id=d.id AND s.status='succeeded') successful,(SELECT COUNT(*) FROM custom_dataset_imports a WHERE a.dataset_id=d.id AND a.status IN ('queued','running') AND a.id<>?) other_active FROM custom_datasets d WHERE d.id=?`, currentImportID, datasetID); err != nil {
		return err
	}
	if row.Status != DatasetProvisioning || row.Current != nil || row.Successful != 0 || row.OtherActive != 0 {
		return fmt.Errorf("custom dataset provisioning objects cannot be reset after publication or during another import")
	}
	return nil
}

func verifyView(ctx context.Context, connection *sqlx.Conn, dataset Dataset, columns []Column) error {
	var view struct {
		Security   string `db:"security"`
		Definition string `db:"definition"`
		Schema     string `db:"schema_name"`
	}
	if err := connection.GetContext(ctx, &view, `SELECT SECURITY_TYPE security,VIEW_DEFINITION definition,TABLE_SCHEMA schema_name FROM information_schema.VIEWS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, dataset.ViewName()); err != nil {
		return fmt.Errorf("inspect custom dataset view: %w", err)
	}
	if view.Security != "INVOKER" {
		return fmt.Errorf("custom dataset view %s is not SQL SECURITY INVOKER", dataset.ViewName())
	}
	var names []string
	if err := connection.SelectContext(ctx, &names, `SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, dataset.ViewName()); err != nil {
		return err
	}
	if len(names) != len(columns) {
		return fmt.Errorf("custom dataset view %s does not match frozen metadata", dataset.ViewName())
	}
	for index := range names {
		if names[index] != columns[index].QueryName {
			return fmt.Errorf("custom dataset view %s does not match frozen metadata", dataset.ViewName())
		}
	}
	expected := createViewSQL(dataset, columns)
	selectAt := strings.Index(expected, "SELECT ")
	if selectAt < 0 || normalizeViewDefinition(view.Definition, view.Schema) != normalizeViewDefinition(expected[selectAt:], view.Schema) {
		return fmt.Errorf("custom dataset view %s definition does not match frozen metadata", dataset.ViewName())
	}
	return nil
}

func normalizeViewDefinition(value, schema string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "`", "")
	value = strings.ReplaceAll(value, strings.ToLower(schema)+".", "")
	for _, remove := range []string{" ", "\n", "\r", "\t", "(", ")"} {
		value = strings.ReplaceAll(value, remove, "")
	}
	return value
}

func quoteIdentifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }
