//go:build integration

package ingestionstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestRuntimePrivilegesSupportDynamicAdditiveWithoutDrop(t *testing.T) {
	admin := integrationdb.Open(t)
	config := integrationdb.Config(t)
	if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(config.Name) {
		t.Fatalf("disposable database name %q is not safe for privilege test", config.Name)
	}
	accountHash := sha256.Sum256([]byte(config.Name))
	accountName := fmt.Sprintf("phase7_rt_%x", accountHash[:4])
	account := fmt.Sprintf("'%s'@'%%'", accountName)
	_, _ = admin.Exec(`DROP USER IF EXISTS ` + account)
	if _, err := admin.Exec(`CREATE USER ` + account + ` IDENTIFIED BY 'integration-only-password'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP USER IF EXISTS ` + account) })
	if _, err := admin.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER ON ` + "`" + config.Name + "`.* TO " + account); err != nil {
		t.Fatal(err)
	}

	runtimeConfig := config
	runtimeConfig.User = accountName
	runtimeConfig.Password = "integration-only-password"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtimeDB, err := database.Open(ctx, runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeDB.Close()

	definition := findMaintenance(t, "cbr_customer")
	_, _ = admin.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
	_, _ = admin.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
	_, _ = admin.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP TABLE IF EXISTS `" + definition.TableName + "`")
		_, _ = admin.Exec(`DELETE FROM dynamic_csv_source_columns WHERE source_id=?`, definition.Key)
		_, _ = admin.Exec(`DELETE FROM dynamic_csv_sources WHERE source_id=?`, definition.Key)
	})

	date, _ := ingestion.ParseCalendarDate("2026-08-12")
	repository := NewMaintenanceRepository(runtimeDB)
	for _, content := range []string{"One|Two\na|b\n", "One|Two|Three\nc|d|e\n"} {
		parsed, err := ingestion.ParseMaintenanceCSV(ctx, definition, date, content)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveSnapshot(ctx, MaintenanceSnapshot{RequestedDate: date, FileName: "cbrcustomer.csv", Parsed: parsed}); err != nil {
			t.Fatalf("runtime dynamic additive load: %v", err)
		}
	}
	if _, err := runtimeDB.ExecContext(ctx, "DROP TABLE `"+definition.TableName+"`"); err == nil {
		t.Fatal("runtime account unexpectedly has DROP privilege")
	}
}
