//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestMigrationPreflightUsesSelectedKnownLineage(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	var name string
	if err := db.Get(&name, `SELECT DATABASE()`); err != nil {
		t.Fatal(err)
	}
	if err := verifySelectedDatabase(ctx, db, name, name); err != nil {
		t.Fatal(err)
	}
	if err := preflightUp(ctx, db); err != nil {
		t.Fatalf("canonical lineage rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id,is_applied,tstamp) VALUES (999,TRUE,NOW())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM goose_db_version WHERE version_id=999`) })
	if err := preflightUp(ctx, db); err == nil {
		t.Fatal("unknown Goose history accepted")
	}
}
