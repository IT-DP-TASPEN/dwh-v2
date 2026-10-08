//go:build integration

package ingestionstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/dwhschema"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

// The application database is rebuilt from zero; Fixed must publish immediately
// after the canonical migration chain with no preparation step.
func TestFixedPublishesOnDatabaseMigratedFromZero(t *testing.T) {
	admin := integrationdb.Open(t)
	ctx := context.Background()
	databaseConfig := integrationdb.Config(t)
	databaseConfig.Name += "_from_zero"
	if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + databaseConfig.Name + "`"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec("CREATE DATABASE `" + databaseConfig.Name + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS `" + databaseConfig.Name + "`") })
	db, err := database.OpenMigrations(ctx, databaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := goose.UpContext(ctx, db.DB, filepath.Join(integrationdb.Root(t), "migrations")); err != nil {
		t.Fatalf("migrate from zero: %v", err)
	}
	if err := dwhschema.VerifyRuntime(ctx, db); err != nil {
		t.Fatalf("fresh schema rejected: %v", err)
	}

	for _, definition := range ingestion.FixedDefinitions() {
		table, _ := ingestion.FixedTableName(definition.Key)
		for _, storage := range []string{table, "stg_" + table} {
			var nullable []string
			if err := db.Select(&nullable, `SELECT IS_NULLABLE FROM information_schema.COLUMNS
				WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='coverage_date' AND DATA_TYPE='date'`, storage); err != nil {
				t.Fatal(err)
			}
			want := []string{"NO"}
			if definition.PublicationMode == ingestion.IntervalResult {
				want = nil
			}
			if len(nullable) != len(want) || (len(want) == 1 && nullable[0] != "NO") {
				t.Fatalf("%s coverage_date=%v want %v", storage, nullable, want)
			}
		}
	}

	definition := ingestion.FixedDefinitions()[0]
	repository := NewFixedRepository(db)
	_, a := publicationCandidate(t, db, definition, "2026-01-01", "2026-06-30", nil)
	publishCandidate(t, repository, definition, a)
	_, b := publicationCandidate(t, db, definition, "2026-02-01", "2026-07-31", nil)
	publishCandidate(t, repository, definition, b)
	assertDatePublication(t, db, definition, "2026-01-01", "2026-07-31", func(date string) uint64 {
		if date < "2026-02-01" {
			return a
		}
		return b
	}, 2)
}
