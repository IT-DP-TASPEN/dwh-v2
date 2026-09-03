//go:build integration

package adoption

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	"github.com/ibldzn/go-admin/internal/dwhschema"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestControlledBootstrapMatchesDWH2Topology(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	t.Cleanup(func() { resetToCanonicalTopology(t, db) })
	resetToLegacyTopology(t, db)
	var databaseName string
	if err := db.Get(&databaseName, "SELECT DATABASE()"); err != nil {
		t.Fatal(err)
	}
	migrationDir := filepath.Join(integrationdb.Root(t), "migrations")
	bootstrap, err := BootstrapFilesystem(migrationDir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(bootstrap, ".")
	if err != nil || len(entries) != 7 {
		t.Fatalf("allowlisted bootstrap entries=%d error=%v", len(entries), err)
	}
	for _, entry := range entries {
		if entry.Name() == "20260808090000_missing.sql" {
			t.Fatal("missing legacy source exposed to bootstrap")
		}
	}
	engine, err := New(db, Config{ExpectedDatabase: databaseName, MigrationDir: migrationDir})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := engine.Preflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.CurrentGooseVersion != legacyMaximumVersion {
		t.Fatalf("current version=%d", preflight.CurrentGooseVersion)
	}
	if len(preflight.UnexpectedSources) != 1 || preflight.UnexpectedSources[0] != "legacy_unknown_job" {
		t.Fatalf("unexpected source audit=%v", preflight.UnexpectedSources)
	}
	if err := engine.Apply(ctx, preflight.Fingerprint); err != nil {
		t.Fatal(err)
	}
	for _, version := range append(legacyVersions(), dwhschema.BootstrapVersions...) {
		var count int
		if err := db.Get(&count, `SELECT COUNT(*) FROM goose_db_version WHERE version_id=? AND is_applied=1`, version); err != nil || count != 1 {
			t.Fatalf("version %d count=%d error=%v", version, count, err)
		}
	}
	var enabled bool
	if err := db.Get(&enabled, `SELECT enabled FROM source_settings WHERE source_id='cif_opening_report'`); err != nil || enabled {
		t.Fatalf("persisted disabled state was overwritten: enabled=%v error=%v", enabled, err)
	}
	var unknownCount int
	if err := db.Get(&unknownCount, `SELECT COUNT(*) FROM source_settings WHERE source_id='legacy_unknown_job'`); err != nil || unknownCount != 1 {
		t.Fatalf("unknown source was not preserved: count=%d error=%v", unknownCount, err)
	}
	var masterSources int
	if err := db.Get(&masterSources, `SELECT COUNT(*) FROM source_settings WHERE enabled=TRUE AND source_id IN
		('cif_reference_master','saving_reference_master','time_deposit_reference_master','loan_reference_master','marketing_master')`); err != nil || masterSources != 5 {
		t.Fatalf("Master source settings=%d error=%v", masterSources, err)
	}
	schedulerVersion, err := migrationVersion(migrationDir, "create_ingestion_scheduler.sql")
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := db.Get(&applied, `SELECT COUNT(*) FROM goose_db_version WHERE version_id=? AND is_applied=1`, schedulerVersion); err != nil || applied != 1 {
		t.Fatalf("scheduler migration applied=%d error=%v", applied, err)
	}
	var canonicalObjects int
	if err := db.Get(&canonicalObjects, `SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN ('schedules','schedule_occurrences','schedule_attempts')`); err != nil || canonicalObjects != 3 {
		t.Fatalf("canonical scheduler tables=%d error=%v", canonicalObjects, err)
	}
	var legacyObjects int
	if err := db.Get(&legacyObjects, `SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='schedule_executions'`); err != nil || legacyObjects != 0 {
		t.Fatalf("legacy scheduler tables=%d error=%v", legacyObjects, err)
	}
	var generatedTrigger int
	if err := db.Get(&generatedTrigger, `SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ingestion_runs' AND COLUMN_NAME='scheduler_trigger_reference'
		AND EXTRA LIKE '%STORED GENERATED%'`); err != nil || generatedTrigger != 1 {
		t.Fatalf("scheduler trigger guard=%d error=%v", generatedTrigger, err)
	}
	var adminIndexes int
	if err := db.Get(&adminIndexes, `SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ingestion_runs'
		AND INDEX_NAME IN ('idx_ingestion_runs_admin_job','idx_ingestion_runs_admin_status','idx_ingestion_runs_admin_trigger')`); err != nil || adminIndexes != 3 {
		t.Fatalf("ingestion admin indexes=%d error=%v", adminIndexes, err)
	}
}

func TestLiveSnapshotNamingMigrationMovesExecutableStateAndPreservesHistory(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	t.Cleanup(func() { resetToCanonicalTopology(t, db) })
	migrationDir := filepath.Join(integrationdb.Root(t), "migrations")
	resetToVersion(t, db, migrationDir, 20260830130000)

	const legacyChecksum = "39344a5541ef427ed51b38da8f8c6ba67a719cd4517ed44cdc95af0c5c8334e4"
	const canonicalChecksum = "6aabf8e855d1dd0a653754257a4e4bce0380ccf0bb1734b4dc50de2b1f5ec60a"
	result, err := db.ExecContext(ctx, `INSERT INTO schedules
		(name,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,enabled,next_run_at,revision)
		VALUES ('active legacy','cif_detail','0 1 * * *','Asia/Jakarta','detail_live_snapshot',1,JSON_OBJECT(),UNHEX(?),TRUE,UTC_TIMESTAMP(6),7)`, legacyChecksum)
	if err != nil {
		t.Fatal(err)
	}
	activeSchedule, _ := result.LastInsertId()
	result, err = db.ExecContext(ctx, `INSERT INTO schedules
		(name,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,enabled,next_run_at,revision,archived_at)
		VALUES ('archived legacy','saving_detail','0 2 * * *','Asia/Jakarta','detail_live_snapshot',1,JSON_OBJECT(),UNHEX(?),FALSE,NULL,9,UTC_TIMESTAMP(6))`, legacyChecksum)
	if err != nil {
		t.Fatal(err)
	}
	archivedSchedule, _ := result.LastInsertId()
	if _, err := db.ExecContext(ctx, `INSERT INTO schedule_occurrences
		(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum)
		VALUES (?,UTC_TIMESTAMP(6),'validated_cron','live_coalesced','unresolved',7,'cif_detail','0 1 * * *','Asia/Jakarta','detail_live_snapshot',1,JSON_OBJECT(),UNHEX(?))`, activeSchedule, legacyChecksum); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schedule_occurrences
		(schedule_id,scheduled_for,identity_source,resolution_mode,status,schedule_revision,job_key,cron_expression,timezone,policy_kind,policy_version,policy_json,policy_checksum,closed_at)
		VALUES (?,UTC_TIMESTAMP(6),'validated_cron','live_coalesced','resolved',9,'saving_detail','0 2 * * *','Asia/Jakarta','detail_live_snapshot',1,JSON_OBJECT(),UNHEX(?),UTC_TIMESTAMP(6))`, archivedSchedule, legacyChecksum); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type)
		VALUES ('job','loan_detail','queued','detail_live_snapshot_v1',1,JSON_OBJECT(),UNHEX(SHA2('{}',256)),'direct'),
		       ('job','time_deposit_detail','succeeded','detail_live_snapshot_v1',1,JSON_OBJECT(),UNHEX(SHA2('{}',256)),'direct')`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db.DB, migrationDir, 20260903090000); err != nil {
		t.Fatal(err)
	}

	var activeKind, activeHash, archivedKind string
	var activeRevision uint64
	if err := db.QueryRowxContext(ctx, `SELECT policy_kind,HEX(policy_checksum),revision FROM schedules WHERE id=?`, activeSchedule).Scan(&activeKind, &activeHash, &activeRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.GetContext(ctx, &archivedKind, `SELECT policy_kind FROM schedules WHERE id=?`, archivedSchedule); err != nil {
		t.Fatal(err)
	}
	if activeKind != "live_snapshot" || activeHash != strings.ToUpper(canonicalChecksum) || activeRevision != 7 || archivedKind != "detail_live_snapshot" {
		t.Fatalf("schedule migration active=%s/%s/r%d archived=%s", activeKind, activeHash, activeRevision, archivedKind)
	}
	var unresolvedKind, resolvedKind string
	if err := db.GetContext(ctx, &unresolvedKind, `SELECT policy_kind FROM schedule_occurrences WHERE schedule_id=?`, activeSchedule); err != nil {
		t.Fatal(err)
	}
	if err := db.GetContext(ctx, &resolvedKind, `SELECT policy_kind FROM schedule_occurrences WHERE schedule_id=?`, archivedSchedule); err != nil {
		t.Fatal(err)
	}
	if unresolvedKind != "live_snapshot" || resolvedKind != "detail_live_snapshot" {
		t.Fatalf("occurrence migration unresolved=%s resolved=%s", unresolvedKind, resolvedKind)
	}
	var activeParameter, terminalParameter string
	if err := db.GetContext(ctx, &activeParameter, `SELECT parameter_kind FROM ingestion_runs WHERE status='queued'`); err != nil {
		t.Fatal(err)
	}
	if err := db.GetContext(ctx, &terminalParameter, `SELECT parameter_kind FROM ingestion_runs WHERE status='succeeded'`); err != nil {
		t.Fatal(err)
	}
	if activeParameter != "live_snapshot_v1" || terminalParameter != "detail_live_snapshot_v1" {
		t.Fatalf("run migration active=%s terminal=%s", activeParameter, terminalParameter)
	}
}

func resetToCanonicalTopology(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	dropAllTables(t, db)
	if err := goose.SetDialect("mysql"); err != nil {
		t.Error(err)
		return
	}
	if err := goose.UpContext(ctx, db.DB, filepath.Join(integrationdb.Root(t), "migrations")); err != nil {
		t.Errorf("restore canonical integration topology: %v", err)
	}
}

func dropAllTables(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	connection, err := db.Connx(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS=0`); err != nil {
		t.Error(err)
		return
	}
	defer func() {
		if _, err := connection.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS=1`); err != nil {
			t.Error(err)
		}
	}()
	var tables []string
	if err := connection.SelectContext(ctx, &tables, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()`); err != nil {
		t.Error(err)
		return
	}
	for _, table := range tables {
		if _, err := connection.ExecContext(ctx, "DROP TABLE `"+table+"`"); err != nil {
			t.Error(err)
			return
		}
	}
}

func resetToVersion(t *testing.T, db *sqlx.DB, migrationDir string, version int64) {
	t.Helper()
	ctx := context.Background()
	dropAllTables(t, db)
	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db.DB, migrationDir, version); err != nil {
		t.Fatal(err)
	}
}

func resetToLegacyTopology(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	dropAllTables(t, db)
	statements := []string{
		`CREATE TABLE goose_db_version (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
			version_id BIGINT NOT NULL, is_applied BOOLEAN NOT NULL,
			tstamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE users (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
			username VARCHAR(128) NOT NULL, password_hash VARCHAR(255) NOT NULL,
			role VARCHAR(32) NOT NULL, enabled BOOLEAN NOT NULL)`,
		`CREATE TABLE sessions (
			token_hash CHAR(64) NOT NULL PRIMARY KEY, user_id BIGINT UNSIGNED NOT NULL,
			expires_at DATETIME(6) NOT NULL,
			CONSTRAINT fk_legacy_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE)`,
		`CREATE TABLE source_settings (
			source_id VARCHAR(128) NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			updated_by_user_id BIGINT UNSIGNED NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (source_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE schedules (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(128) NOT NULL,
			cron_expression VARCHAR(128) NOT NULL,
			created_by_user_id BIGINT UNSIGNED NULL) ENGINE=InnoDB`,
		`CREATE TABLE schedule_executions (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
			schedule_id BIGINT UNSIGNED NOT NULL,
			CONSTRAINT fk_legacy_schedule_execution FOREIGN KEY (schedule_id) REFERENCES schedules(id)) ENGINE=InnoDB`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range legacyVersions() {
		if _, err := db.ExecContext(ctx, `INSERT INTO goose_db_version (version_id,is_applied) VALUES (?,TRUE)`, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (username,password_hash,role,enabled) VALUES ('legacy','x','admin',TRUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions (token_hash,user_id,expires_at) VALUES (REPEAT('0',64),1,'2020-01-01')`); err != nil {
		t.Fatal(err)
	}
	keys, err := dwhschema.PreMasterSourceKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		enabled := key != "cif_opening_report"
		if _, err := db.ExecContext(ctx, `INSERT INTO source_settings (source_id,enabled,updated_by_user_id) VALUES (?,?,NULL)`, key, enabled); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO source_settings (source_id,enabled,updated_by_user_id) VALUES ('legacy_unknown_job',FALSE,NULL)`); err != nil {
		t.Fatal(err)
	}
}

func legacyVersions() []int64 {
	return append([]int64(nil), dwhschema.LegacyVersions...)
}
