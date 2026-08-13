package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing/fstest"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	"github.com/ibldzn/go-admin/internal/dwhschema"
)

const legacyMaximumVersion int64 = 20260808090000

var gooseFilesystemMu sync.Mutex

type Config struct {
	ExpectedDatabase string
	MigrationDir     string
}

type Engine struct {
	db     *sqlx.DB
	config Config
}

type Result struct {
	Database            string          `json:"database"`
	MySQLVersion        string          `json:"mysql_version"`
	Fingerprint         string          `json:"fingerprint"`
	CurrentGooseVersion int64           `json:"current_goose_version"`
	SourceSettings      []SourceSetting `json:"source_settings"`
	UnexpectedSources   []string        `json:"unexpected_sources,omitempty"`
	Advisories          []string        `json:"advisories,omitempty"`
	Actions             []string        `json:"actions"`
}

type SourceSetting struct {
	Key             string  `db:"source_id" json:"key"`
	Enabled         bool    `db:"enabled" json:"enabled"`
	UpdatedByUserID *uint64 `db:"updated_by_user_id" json:"updated_by_user_id"`
}

func New(db *sqlx.DB, config Config) (*Engine, error) {
	if db == nil || config.ExpectedDatabase == "" || config.MigrationDir == "" {
		return nil, fmt.Errorf("database, expected identity, and migration directory are required")
	}
	return &Engine{db: db, config: config}, nil
}

func (engine *Engine) Preflight(ctx context.Context) (Result, error) {
	var databaseName, mysqlVersion string
	if err := engine.db.QueryRowxContext(ctx, "SELECT DATABASE(), VERSION()").Scan(&databaseName, &mysqlVersion); err != nil {
		return Result{}, fmt.Errorf("identify adoption database: %w", err)
	}
	if databaseName != engine.config.ExpectedDatabase {
		return Result{}, fmt.Errorf("refusing adoption: database is %q, expected exact %q", databaseName, engine.config.ExpectedDatabase)
	}
	gooseRows, err := engine.gooseRows(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := validateGooseRows(gooseRows); err != nil {
		return Result{}, err
	}
	sources, unexpected, err := engine.sourceSettings(ctx)
	if err != nil {
		return Result{}, err
	}
	counts, err := engine.protectedCounts(ctx)
	if err != nil {
		return Result{}, err
	}
	for table, count := range counts {
		if count != 0 {
			return Result{}, fmt.Errorf("protected table %s has %d rows; adoption requires an explicit data decision", table, count)
		}
	}
	if err := engine.addFingerprintOnlyCounts(ctx, counts, "users", "sessions"); err != nil {
		return Result{}, err
	}
	if err := engine.validateUserReferences(ctx); err != nil {
		return Result{}, err
	}
	fingerprintData, err := engine.fingerprintData(ctx, gooseRows, sources, counts)
	if err != nil {
		return Result{}, err
	}
	encoded, _ := json.Marshal(fingerprintData)
	hash := sha256.Sum256(encoded)
	current := int64(0)
	for _, row := range gooseRows {
		if row.Applied && row.Version > current {
			current = row.Version
		}
	}
	result := Result{
		Database: databaseName, MySQLVersion: mysqlVersion, Fingerprint: hex.EncodeToString(hash[:]),
		CurrentGooseVersion: current, SourceSettings: sources, UnexpectedSources: unexpected,
		Actions: []string{"replace approved legacy authentication", "apply seven allowlisted bootstrap migrations", "replace zero-gated DWH tables", "execute Phase 3 migrations normally"},
	}
	var otherConnections int
	if err := engine.db.GetContext(ctx, &otherConnections, `SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE DB = ? AND ID <> CONNECTION_ID()`, databaseName); err == nil && otherConnections > 0 {
		result.Advisories = append(result.Advisories, fmt.Sprintf("%d other database connections observed; this does not prove writer activity or quiescence", otherConnections))
	}
	return result, nil
}

func (engine *Engine) Apply(ctx context.Context, confirmation string) error {
	if confirmation == "" {
		return fmt.Errorf("preflight fingerprint confirmation is required")
	}
	connection, err := engine.db.Connx(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	var acquired int
	lockName := "dwh-adopt:" + engine.config.ExpectedDatabase
	if err := connection.GetContext(ctx, &acquired, "SELECT GET_LOCK(?, 0)", lockName); err != nil || acquired != 1 {
		return fmt.Errorf("another adoption process holds the lock")
	}
	defer connection.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName)
	preflight, err := engine.Preflight(ctx)
	if err != nil {
		return err
	}
	if preflight.Fingerprint != confirmation {
		return fmt.Errorf("adoption fingerprint changed: got %s", preflight.Fingerprint)
	}
	if err := engine.replaceLegacyAuthentication(ctx); err != nil {
		return err
	}
	if err := engine.applyBootstrap(ctx); err != nil {
		return err
	}
	if err := engine.restoreUserReferences(ctx); err != nil {
		return err
	}
	if err := engine.dropZeroGatedObjects(ctx); err != nil {
		return err
	}
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, engine.db.DB, engine.config.MigrationDir); err != nil {
		return fmt.Errorf("apply Phase 3 migrations: %w", err)
	}
	return engine.verifyFinal(ctx)
}

type gooseRow struct {
	ID      int64 `db:"id" json:"id"`
	Version int64 `db:"version_id" json:"version"`
	Applied bool  `db:"is_applied" json:"applied"`
}

func (engine *Engine) gooseRows(ctx context.Context) ([]gooseRow, error) {
	rows := []gooseRow{}
	if err := engine.db.SelectContext(ctx, &rows, `SELECT id, version_id, is_applied FROM goose_db_version ORDER BY id`); err != nil {
		return nil, fmt.Errorf("read Goose history: %w", err)
	}
	return rows, nil
}

func validateGooseRows(rows []gooseRow) error {
	seen := map[int64]bool{}
	for _, row := range rows {
		if !row.Applied {
			return fmt.Errorf("Goose history contains unapplied row for %d", row.Version)
		}
		if seen[row.Version] {
			return fmt.Errorf("Goose history contains duplicate applied version %d", row.Version)
		}
		seen[row.Version] = true
	}
	for _, version := range dwhschema.LegacyVersions {
		if !seen[version] {
			return fmt.Errorf("required legacy Goose version %d is absent", version)
		}
	}
	return nil
}

func (engine *Engine) sourceSettings(ctx context.Context) ([]SourceSetting, []string, error) {
	if err := engine.validateSourceSettingsSchema(ctx); err != nil {
		return nil, nil, err
	}
	settings := []SourceSetting{}
	if err := engine.db.SelectContext(ctx, &settings, `SELECT source_id, enabled, updated_by_user_id FROM source_settings ORDER BY BINARY source_id`); err != nil {
		return nil, nil, fmt.Errorf("read source_settings: %w", err)
	}
	canonical, err := dwhschema.CanonicalSourceKeys()
	if err != nil {
		return nil, nil, err
	}
	want := make(map[string]bool, len(canonical))
	for _, key := range canonical {
		want[key] = true
	}
	folded := map[string]string{}
	unexpected := []string{}
	for _, setting := range settings {
		fold := strings.ToLower(setting.Key)
		if previous, exists := folded[fold]; exists && previous != setting.Key {
			return nil, nil, fmt.Errorf("source setting keys %q and %q collide under deployed collation", previous, setting.Key)
		}
		folded[fold] = setting.Key
		if setting.UpdatedByUserID != nil {
			return nil, nil, fmt.Errorf("source setting %s references legacy user %d", setting.Key, *setting.UpdatedByUserID)
		}
		if want[setting.Key] {
			delete(want, setting.Key)
		} else {
			unexpected = append(unexpected, setting.Key)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for key := range want {
			missing = append(missing, key)
		}
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("source_settings is missing canonical keys: %s", strings.Join(missing, ", "))
	}
	return settings, unexpected, nil
}

func (engine *Engine) validateSourceSettingsSchema(ctx context.Context) error {
	type column struct {
		Name      string `db:"COLUMN_NAME"`
		Type      string `db:"COLUMN_TYPE"`
		Nullable  string `db:"IS_NULLABLE"`
		Collation string `db:"COLLATION_NAME"`
	}
	var columns []column
	if err := engine.db.SelectContext(ctx, &columns, `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLLATION_NAME,'') COLLATION_NAME
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='source_settings' ORDER BY ORDINAL_POSITION`); err != nil {
		return err
	}
	want := map[string]struct{ dataType, nullable string }{
		"source_id": {"varchar(128)", "NO"}, "enabled": {"tinyint(1)", "NO"},
		"updated_by_user_id": {"bigint unsigned", "YES"}, "created_at": {"datetime(6)", "NO"}, "updated_at": {"datetime(6)", "NO"},
	}
	if len(columns) != len(want) {
		return fmt.Errorf("source_settings has %d columns, want %d", len(columns), len(want))
	}
	for _, column := range columns {
		expected, found := want[column.Name]
		if !found || strings.ToLower(column.Type) != expected.dataType || column.Nullable != expected.nullable {
			return fmt.Errorf("source_settings column %s is incompatible", column.Name)
		}
		if column.Name == "source_id" && column.Collation != "utf8mb4_unicode_ci" && column.Collation != "utf8mb4_0900_bin" {
			return fmt.Errorf("source_settings source_id collation %s is not an approved predecessor", column.Collation)
		}
	}
	return nil
}

func (engine *Engine) protectedCounts(ctx context.Context) (map[string]int64, error) {
	tables, err := engine.tableSet(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, table := range dwhschema.EmptyBeforeAdoption {
		if !tables[table] {
			continue
		}
		var count int64
		if err := engine.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM `"+table+"`"); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = count
	}
	return counts, nil
}

func (engine *Engine) tableSet(ctx context.Context) (map[string]bool, error) {
	var tables []string
	if err := engine.db.SelectContext(ctx, &tables, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()`); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(tables))
	for _, table := range tables {
		set[table] = true
	}
	return set, nil
}

func (engine *Engine) validateUserReferences(ctx context.Context) error {
	tables, err := engine.tableSet(ctx)
	if err != nil {
		return err
	}
	for _, reference := range dwhschema.UserReferences {
		if !tables[reference.Table] {
			continue
		}
		var count int64
		if err := engine.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM `"+reference.Table+"` WHERE `"+reference.Column+"` IS NOT NULL"); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%s.%s contains %d legacy-user references", reference.Table, reference.Column, count)
		}
	}
	return nil
}

type fingerprint struct {
	Database string          `json:"database"`
	Version  string          `json:"version"`
	Goose    []gooseRow      `json:"goose"`
	Tables   []schemaTable   `json:"tables"`
	Columns  []schemaColumn  `json:"columns"`
	Indexes  []schemaIndex   `json:"indexes"`
	FKs      []schemaFK      `json:"foreign_keys"`
	Counts   []tableCount    `json:"protected_counts"`
	Sources  []SourceSetting `json:"source_settings"`
}

type schemaTable struct {
	Name      string `db:"TABLE_NAME"`
	Engine    string `db:"ENGINE"`
	Collation string `db:"TABLE_COLLATION"`
}

type schemaColumn struct {
	Table      string `db:"TABLE_NAME"`
	Name       string `db:"COLUMN_NAME"`
	Type       string `db:"COLUMN_TYPE"`
	Nullable   string `db:"IS_NULLABLE"`
	Default    string `db:"COLUMN_DEFAULT"`
	Extra      string `db:"EXTRA"`
	Charset    string `db:"CHARACTER_SET_NAME"`
	Collation  string `db:"COLLATION_NAME"`
	Generation string `db:"GENERATION_EXPRESSION"`
}
type schemaIndex struct {
	Table     string `db:"TABLE_NAME"`
	Name      string `db:"INDEX_NAME"`
	Column    string `db:"COLUMN_NAME"`
	NonUnique int    `db:"NON_UNIQUE"`
	Sequence  int    `db:"SEQ_IN_INDEX"`
}
type schemaFK struct {
	Table            string `db:"TABLE_NAME"`
	Name             string `db:"CONSTRAINT_NAME"`
	Column           string `db:"COLUMN_NAME"`
	ReferencedTable  string `db:"REFERENCED_TABLE_NAME"`
	ReferencedColumn string `db:"REFERENCED_COLUMN_NAME"`
	DeleteRule       string `db:"DELETE_RULE"`
	UpdateRule       string `db:"UPDATE_RULE"`
}
type tableCount struct {
	Table string
	Count int64
}

func (engine *Engine) fingerprintData(ctx context.Context, gooseRows []gooseRow, sources []SourceSetting, counts map[string]int64) (fingerprint, error) {
	var value fingerprint
	if err := engine.db.QueryRowxContext(ctx, "SELECT DATABASE(), VERSION()").Scan(&value.Database, &value.Version); err != nil {
		return value, err
	}
	value.Goose, value.Sources = gooseRows, sources
	if err := engine.db.SelectContext(ctx, &value.Tables, `SELECT TABLE_NAME, COALESCE(ENGINE,'') ENGINE,
		COALESCE(TABLE_COLLATION,'') TABLE_COLLATION FROM information_schema.TABLES
		WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME`); err != nil {
		return value, err
	}
	if err := engine.db.SelectContext(ctx, &value.Columns, `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE,
		COALESCE(COLUMN_DEFAULT, '<NULL>') AS COLUMN_DEFAULT, EXTRA,
		COALESCE(CHARACTER_SET_NAME,'') CHARACTER_SET_NAME, COALESCE(COLLATION_NAME,'') COLLATION_NAME,
		COALESCE(GENERATION_EXPRESSION,'') GENERATION_EXPRESSION FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME, ORDINAL_POSITION`); err != nil {
		return value, err
	}
	if err := engine.db.SelectContext(ctx, &value.Indexes, `SELECT TABLE_NAME, INDEX_NAME, COLUMN_NAME, NON_UNIQUE, SEQ_IN_INDEX
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`); err != nil {
		return value, err
	}
	if err := engine.db.SelectContext(ctx, &value.FKs, `SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
		k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.DELETE_RULE, r.UPDATE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		ON r.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME=k.CONSTRAINT_NAME AND r.TABLE_NAME=k.TABLE_NAME
		WHERE k.CONSTRAINT_SCHEMA=DATABASE() AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`); err != nil {
		return value, err
	}
	for table, count := range counts {
		value.Counts = append(value.Counts, tableCount{table, count})
	}
	sort.Slice(value.Counts, func(i, j int) bool { return value.Counts[i].Table < value.Counts[j].Table })
	return value, nil
}

func (engine *Engine) addFingerprintOnlyCounts(ctx context.Context, counts map[string]int64, names ...string) error {
	tables, err := engine.tableSet(ctx)
	if err != nil {
		return err
	}
	for _, table := range names {
		if !tables[table] {
			continue
		}
		var count int64
		if err := engine.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM `"+table+"`"); err != nil {
			return err
		}
		counts[table] = count
	}
	return nil
}

func (engine *Engine) replaceLegacyAuthentication(ctx context.Context) error {
	if err := engine.validateUserReferences(ctx); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, reference := range dwhschema.UserReferences {
		allowed[reference.Table+"\x00"+reference.Column] = true
	}
	var references []schemaFK
	if err := engine.db.SelectContext(ctx, &references, `SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
		k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.DELETE_RULE, r.UPDATE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		ON r.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME=k.CONSTRAINT_NAME AND r.TABLE_NAME=k.TABLE_NAME
		WHERE k.CONSTRAINT_SCHEMA=DATABASE() AND k.REFERENCED_TABLE_NAME='users'
		ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME`); err != nil {
		return err
	}
	for _, reference := range references {
		if reference.Table == "sessions" || reference.Table == "audit_logs" {
			continue
		}
		if !allowed[reference.Table+"\x00"+reference.Column] {
			return fmt.Errorf("unapproved table %s.%s references legacy users", reference.Table, reference.Column)
		}
		if _, err := engine.db.ExecContext(ctx, "ALTER TABLE `"+reference.Table+"` DROP FOREIGN KEY `"+reference.Name+"`"); err != nil {
			return fmt.Errorf("drop legacy user reference %s: %w", reference.Name, err)
		}
	}
	tables, err := engine.tableSet(ctx)
	if err != nil {
		return err
	}
	if tables["users"] {
		var targetColumns int
		if err := engine.db.GetContext(ctx, &targetColumns, `SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME IN ('role_id','is_active','name')`); err != nil {
			return err
		}
		if targetColumns != 3 {
			if tables["sessions"] {
				if _, err := engine.db.ExecContext(ctx, "DROP TABLE sessions"); err != nil {
					return fmt.Errorf("drop legacy sessions: %w", err)
				}
			}
			if _, err := engine.db.ExecContext(ctx, "DROP TABLE users"); err != nil {
				return fmt.Errorf("drop legacy users: %w", err)
			}
		}
	}
	return nil
}

func (engine *Engine) applyBootstrap(ctx context.Context) error {
	filesystem, err := BootstrapFilesystem(engine.config.MigrationDir)
	if err != nil {
		return err
	}
	gooseFilesystemMu.Lock()
	defer gooseFilesystemMu.Unlock()
	goose.SetBaseFS(filesystem)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, engine.db.DB, ".", goose.WithAllowMissing()); err != nil {
		return fmt.Errorf("apply allowlisted target bootstrap: %w", err)
	}
	return nil
}

func (engine *Engine) restoreUserReferences(ctx context.Context) error {
	tables, err := engine.tableSet(ctx)
	if err != nil {
		return err
	}
	for _, reference := range dwhschema.UserReferences {
		if !tables[reference.Table] {
			continue
		}
		var existing int
		if err := engine.db.GetContext(ctx, &existing, `SELECT COUNT(*) FROM information_schema.KEY_COLUMN_USAGE
			WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=? AND REFERENCED_TABLE_NAME='users' AND REFERENCED_COLUMN_NAME='id'`, reference.Table, reference.Column); err != nil {
			return err
		}
		if existing > 0 {
			continue
		}
		query := "ALTER TABLE `" + reference.Table + "` ADD CONSTRAINT `" + reference.Constraint + "` FOREIGN KEY (`" + reference.Column + "`) REFERENCES `users` (`id`) ON DELETE SET NULL"
		if _, err := engine.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("restore user reference %s: %w", reference.Constraint, err)
		}
	}
	return nil
}

func (engine *Engine) dropZeroGatedObjects(ctx context.Context) error {
	counts, err := engine.protectedCounts(ctx)
	if err != nil {
		return err
	}
	for table, count := range counts {
		if count != 0 {
			return fmt.Errorf("protected table %s gained %d rows during adoption", table, count)
		}
	}
	tables, err := engine.tableSet(ctx)
	if err != nil {
		return err
	}
	for _, group := range dwhschema.AdoptionMigrationGroups {
		version, err := migrationVersion(engine.config.MigrationDir, group.Suffix)
		if err != nil {
			return err
		}
		var applied int
		if err := engine.db.GetContext(ctx, &applied, `SELECT COUNT(*) FROM goose_db_version WHERE version_id=? AND is_applied=1`, version); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		for _, table := range group.Tables {
			if tables[table] {
				if _, err := engine.db.ExecContext(ctx, "DROP TABLE `"+table+"`"); err != nil {
					return fmt.Errorf("remove empty incompatible table %s: %w", table, err)
				}
			}
		}
	}
	return nil
}

func migrationVersion(directory, suffix string) (int64, error) {
	matches, err := filepath.Glob(filepath.Join(directory, "*_"+strings.TrimPrefix(suffix, "_")))
	if err != nil || len(matches) != 1 {
		return 0, fmt.Errorf("migration %s must resolve to exactly one file", suffix)
	}
	var version int64
	if _, err := fmt.Sscanf(filepath.Base(matches[0]), "%d_", &version); err != nil {
		return 0, fmt.Errorf("parse migration version for %s: %w", suffix, err)
	}
	return version, nil
}

func (engine *Engine) verifyFinal(ctx context.Context) error {
	settings, _, err := engine.sourceSettings(ctx)
	if err != nil {
		return err
	}
	if len(settings) < 36 {
		return fmt.Errorf("source settings postcondition failed")
	}
	for _, table := range []string{"roles", "permissions", "role_permissions", "users", "sessions", "audit_logs", "fixed_report_loads", "fincloud_cifs", "dynamic_csv_sources"} {
		var count int
		if err := engine.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, table); err != nil || count != 1 {
			return fmt.Errorf("adoption postcondition missing table %s", table)
		}
	}
	return nil
}

func BootstrapFilesystem(migrationDir string) (fs.FS, error) {
	filesystem := fstest.MapFS{}
	for _, version := range dwhschema.BootstrapVersions {
		matches, err := filepath.Glob(filepath.Join(migrationDir, fmt.Sprintf("%d_*.sql", version)))
		if err != nil || len(matches) != 1 {
			return nil, fmt.Errorf("bootstrap migration %d must resolve to exactly one file", version)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil {
			return nil, err
		}
		filesystem[filepath.Base(matches[0])] = &fstest.MapFile{Data: data, Mode: 0o444}
	}
	return filesystem, nil
}
