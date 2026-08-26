package dwhschema

import (
	"context"
	"fmt"
	"sort"

	"github.com/jmoiron/sqlx"
)

var ApplicationVersions = []int64{
	202608090001, 202608090002, 202608090003, 202608090004,
	202608090005, 202608090006, 202608090007,
	20260813033006, 20260813033031, 20260813033039, 20260813033045,
	20260813033056, 20260813084201, 20260814025546, 20260814042806,
	20260814161323, 20260821120000, 20260822120000,
	20260823120000,
	20260825090000,
	20260826120000,
}

const CurrentVersion int64 = 20260826120000

type MigrationRecord struct {
	Version int64 `db:"version_id"`
	Applied bool  `db:"is_applied"`
}

type MigrationState struct {
	TableExists bool
	Records     []MigrationRecord
}

func ReadMigrationState(ctx context.Context, db *sqlx.DB) (MigrationState, error) {
	var tableCount int
	if err := db.GetContext(ctx, &tableCount, `SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='goose_db_version'`); err != nil {
		return MigrationState{}, fmt.Errorf("inspect Goose metadata: %w", err)
	}
	if tableCount == 0 {
		return MigrationState{}, nil
	}
	var records []MigrationRecord
	if err := db.SelectContext(ctx, &records, `SELECT version_id,is_applied FROM goose_db_version ORDER BY id`); err != nil {
		return MigrationState{}, fmt.Errorf("read Goose metadata: %w", err)
	}
	return MigrationState{TableExists: true, Records: records}, nil
}

// ValidateMigrationPrefix returns the number of currently applied canonical
// migrations. Historical up/down rows are reduced to their latest state.
func ValidateMigrationPrefix(state MigrationState) (int, error) {
	if !state.TableExists {
		return 0, nil
	}
	known := make(map[int64]int, len(ApplicationVersions))
	for index, version := range ApplicationVersions {
		known[version] = index
	}
	latest := make(map[int64]bool, len(state.Records))
	for _, record := range state.Records {
		if record.Version == 0 {
			continue
		}
		if _, found := known[record.Version]; !found {
			return 0, fmt.Errorf("Goose history contains unknown version %d", record.Version)
		}
		latest[record.Version] = record.Applied
	}
	applied := 0
	missing := false
	for _, version := range ApplicationVersions {
		if latest[version] {
			if missing {
				return 0, fmt.Errorf("Goose history is not a canonical migration prefix")
			}
			applied++
		} else {
			missing = true
		}
	}
	return applied, nil
}

func VerifyRuntime(ctx context.Context, db *sqlx.DB) error {
	state, err := ReadMigrationState(ctx, db)
	if err != nil {
		return err
	}
	applied, err := ValidateMigrationPrefix(state)
	if err != nil {
		return err
	}
	if !state.TableExists || applied != len(ApplicationVersions) {
		return fmt.Errorf("database schema is not at required Goose version %d", CurrentVersion)
	}

	wantKeys, err := CanonicalSourceKeys()
	if err != nil {
		return fmt.Errorf("load canonical source catalog: %w", err)
	}
	var gotKeys []string
	if err := db.SelectContext(ctx, &gotKeys, `SELECT source_id FROM source_settings ORDER BY BINARY source_id`); err != nil {
		return fmt.Errorf("verify source settings: %w", err)
	}
	sort.Strings(wantKeys)
	if len(gotKeys) != len(wantKeys) {
		return fmt.Errorf("source settings contain %d keys, want %d", len(gotKeys), len(wantKeys))
	}
	for index := range wantKeys {
		if gotKeys[index] != wantKeys[index] {
			return fmt.Errorf("source settings do not match the canonical catalog")
		}
	}

	type runtimeSettings struct {
		ID                     uint8 `db:"id"`
		MaxRunningJobs         uint  `db:"max_running_jobs"`
		FixedMemberConcurrency uint  `db:"fixed_member_concurrency"`
		DetailConcurrency      uint  `db:"detail_concurrency"`
	}
	var settings []runtimeSettings
	if err := db.SelectContext(ctx, &settings, `SELECT id,max_running_jobs,fixed_member_concurrency,detail_concurrency FROM ingestion_runtime_settings`); err != nil {
		return fmt.Errorf("verify ingestion runtime settings: %w", err)
	}
	if len(settings) != 1 || settings[0].ID != 1 || !validRuntimeLimit(settings[0].MaxRunningJobs) ||
		!validRuntimeLimit(settings[0].FixedMemberConcurrency) || !validRuntimeLimit(settings[0].DetailConcurrency) {
		return fmt.Errorf("ingestion runtime settings singleton is invalid")
	}

	for _, required := range []struct{ table, index string }{
		{"ingestion_runs", "uq_ingestion_runs_active_job"},
		{"fixed_report_publications", "PRIMARY"},
		{"schedule_occurrences", "uq_schedule_occurrences_active"},
	} {
		var count int
		if err := db.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND INDEX_NAME=? AND NON_UNIQUE=0`, required.table, required.index); err != nil {
			return fmt.Errorf("verify runtime safety index: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("required runtime safety index %s.%s is missing", required.table, required.index)
		}
	}
	for _, table := range []string{"schedules", "schedule_attempts", "report_datasources", "report_templates", "report_parameters", "report_parameter_options", "report_template_user_access", "report_export_jobs"} {
		var count int
		if err := db.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.TABLES
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, table); err != nil || count != 1 {
			return fmt.Errorf("required runtime table %s is missing", table)
		}
	}
	var exportClaimIndex int
	if err := db.GetContext(ctx, &exportClaimIndex, `SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='report_export_jobs' AND INDEX_NAME='idx_report_export_jobs_claim'`); err != nil || exportClaimIndex == 0 {
		return fmt.Errorf("required report export claim index is missing")
	}
	var diagnosticsIndex int
	if err := db.GetContext(ctx, &diagnosticsIndex, `SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ingestion_run_errors' AND INDEX_NAME='idx_ingestion_run_errors_run_time'`); err != nil || diagnosticsIndex == 0 {
		return fmt.Errorf("required runtime diagnostic index ingestion_run_errors.idx_ingestion_run_errors_run_time is missing")
	}
	var diagnosticDeleteRule string
	if err := db.GetContext(ctx, &diagnosticDeleteRule, `SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME='fk_ingestion_run_errors_run'`); err != nil || diagnosticDeleteRule != "CASCADE" {
		return fmt.Errorf("required runtime diagnostic run cascade is missing")
	}
	return nil
}

func validRuntimeLimit(value uint) bool { return value >= 1 && value <= 64 }
