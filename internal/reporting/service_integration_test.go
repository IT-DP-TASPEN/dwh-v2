//go:build integration

package reporting_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestAuthorTestsUsePersistedDatasource(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Author", "author-test-datasource")
	user := integrationdb.User(t, database, "author-test-datasource", role.ID, true)
	requester := integrationdb.Requester(user, role)
	connection := integrationdb.Config(t)
	var key [32]byte
	cipher := reporting.NewCipher(key)
	repository, err := reporting.NewRepository(database, cipher)
	if err != nil {
		t.Fatal(err)
	}
	datasource, err := repository.CreateDatasource(context.Background(), requester, reporting.DatasourceInput{
		Name: "Persisted", Host: connection.Host, Port: uint16(connection.Port), DatabaseName: connection.Name,
		Username: connection.User, Password: connection.Password, TLSPolicy: reporting.TLSDisabled,
	}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_datasources SET status='active' WHERE id=?`, datasource.ID); err != nil {
		t.Fatal(err)
	}
	report, err := repository.CreateTemplate(context.Background(), requester, reporting.TemplateInput{Name: "Saved", DatasourceID: datasource.ID, SQLText: "SELECT 1"}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, report.ID, user.ID, user.ID, integrationdb.Now()); err != nil {
		t.Fatal(err)
	}
	pools, err := reporting.NewPoolManager(cipher, reporting.PoolConfig{ConnectTimeout: 5 * time.Second, MySQLMaxPacketBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pools.Close() })
	service, err := reporting.NewService(repository, pools, reporting.ServiceConfig{
		ConnectTimeout: 5 * time.Second, InteractiveTimeout: 20 * time.Second, InteractiveMaxRows: 100,
		InteractivePayloadBytes: 1 << 20, DynamicOptionMaxRows: 1000, DynamicOptionPayloadBytes: 1 << 20, CellPreviewBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	unsavedDatasourceID := datasource.ID + 999999
	queryDraft := reporting.TemplateInput{Name: "Unsaved", DatasourceID: unsavedDatasourceID, SQLText: "SELECT 1 AS tested"}
	result, err := service.TestQuery(context.Background(), requester, report.ID, queryDraft, nil)
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("test query used unsaved datasource: result=%+v error=%v", result, err)
	}
	optionDraft := reporting.TemplateInput{
		Name: "Unsaved", DatasourceID: unsavedDatasourceID, SQLText: "SELECT 1",
		Parameters: []reporting.Parameter{{Key: "city", Label: "City", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT '001' AS value,'One' AS label"}},
	}
	options, err := service.TestOptions(context.Background(), requester, report.ID, optionDraft, 0, nil)
	if err != nil || options.State != "ready" || len(options.Options) != 1 || options.Options[0].Value != "001" {
		t.Fatalf("test options used unsaved datasource: result=%+v error=%v", options, err)
	}
	defaultDraft := reporting.TemplateInput{
		Name: "Defaults", DatasourceID: unsavedDatasourceID, SQLText: "SELECT 1",
		Parameters: []reporting.Parameter{
			{Key: "province", Label: "Province", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT 'valid' AS value,'Valid' AS label", DefaultValue: json.RawMessage(`"missing"`), DisplayOrder: 0},
			{Key: "city", Label: "City", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT '001' AS value,'One' AS label WHERE :province IS NULL", DisplayOrder: 1},
		},
	}
	options, err = service.TestOptions(context.Background(), requester, report.ID, defaultDraft, 1, nil)
	if err != nil || options.State != "ready" || options.Warning == "" || len(options.Options) != 1 {
		t.Fatalf("optional invalid upstream default did not become unset: result=%+v error=%v", options, err)
	}
	defaultDraft.Parameters[0].Required = true
	options, err = service.TestOptions(context.Background(), requester, report.ID, defaultDraft, 1, nil)
	if err != nil || options.State != "waiting" || options.WaitingFor != "province" {
		t.Fatalf("required invalid upstream default did not wait: result=%+v error=%v", options, err)
	}
}
