//go:build integration

package adoption

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/dwhschema"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestControlledBootstrapMatchesDWH2Topology(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
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

func resetToLegacyTopology(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS=0`); err != nil {
		t.Fatal(err)
	}
	var tables []string
	if err := db.SelectContext(ctx, &tables, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if _, err := db.ExecContext(ctx, "DROP TABLE `"+table+"`"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS=1`); err != nil {
		t.Fatal(err)
	}
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
	keys, err := dwhschema.CanonicalSourceKeys()
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
