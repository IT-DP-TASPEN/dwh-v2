//go:build integration

package dwhschema

import (
	"context"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestFixedPublicationRuntimeSchemaContract(t *testing.T) {
	db := integrationdb.Open(t)
	ctx := context.Background()
	if err := VerifyRuntime(ctx, db); err != nil {
		t.Fatalf("canonical schema rejected: %v", err)
	}

	for _, test := range []struct {
		name, damage, restore, diagnostic string
	}{
		{
			"missing coverage index",
			`ALTER TABLE fincloud_cif_opening_reports DROP INDEX idx_fixed_coverage_date`,
			`ALTER TABLE fincloud_cif_opening_reports ADD INDEX idx_fixed_coverage_date (coverage_date)`,
			"fincloud_cif_opening_reports.idx_fixed_coverage_date",
		},
		{
			"wrong coverage column type",
			`ALTER TABLE stg_fincloud_cif_opening_reports MODIFY coverage_date DATETIME NOT NULL`,
			`ALTER TABLE stg_fincloud_cif_opening_reports MODIFY coverage_date DATE NOT NULL`,
			"stg_fincloud_cif_opening_reports.coverage_date",
		},
		{
			"nullable final coverage column",
			`ALTER TABLE fincloud_teller_mutation_reports MODIFY coverage_date DATE NULL`,
			`ALTER TABLE fincloud_teller_mutation_reports MODIFY coverage_date DATE NOT NULL`,
			"fincloud_teller_mutation_reports.coverage_date",
		},
		{
			"interval result with coverage column",
			`ALTER TABLE fincloud_profit_loss_statements ADD COLUMN coverage_date DATE NULL`,
			`ALTER TABLE fincloud_profit_loss_statements DROP COLUMN coverage_date`,
			"fincloud_profit_loss_statements must not have coverage_date",
		},
		{
			"missing publication mutex",
			`DELETE FROM fixed_report_publication_locks WHERE job_key='cif_opening_report'`,
			`INSERT INTO fixed_report_publication_locks (job_key) VALUES ('cif_opening_report')`,
			"publication mutex rows",
		},
		{
			"wrong date publication identity",
			`ALTER TABLE fixed_report_date_publications DROP PRIMARY KEY, ADD PRIMARY KEY (coverage_date,job_key)`,
			`ALTER TABLE fixed_report_date_publications DROP PRIMARY KEY, ADD PRIMARY KEY (job_key,coverage_date)`,
			"fixed_report_date_publications.PRIMARY",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec(test.damage); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec(test.restore); err != nil {
					t.Errorf("restore schema: %v", err)
					return
				}
				if err := VerifyRuntime(ctx, db); err != nil {
					t.Errorf("restored schema rejected: %v", err)
				}
			})
			if err := VerifyRuntime(ctx, db); err == nil || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("schema corruption diagnostic=%v; want %q", err, test.diagnostic)
			}
		})
	}
}
