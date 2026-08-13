//go:build integration

package dwhschema

import (
	"testing"

	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestOpaqueSourceKeysUseExactDatabaseSemantics(t *testing.T) {
	db := integrationdb.Open(t)
	defer db.Exec(`DELETE FROM source_settings WHERE source_id IN ('phase3_case_probe','Phase3_case_probe')`)
	if _, err := db.Exec(`INSERT INTO source_settings (source_id,enabled) VALUES ('phase3_case_probe',TRUE),('Phase3_case_probe',TRUE)`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM source_settings WHERE BINARY source_id IN (BINARY 'phase3_case_probe',BINARY 'Phase3_case_probe')`); err != nil || count != 2 {
		t.Fatalf("exact source key count=%d error=%v", count, err)
	}
}
