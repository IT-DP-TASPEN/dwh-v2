//go:build integration

package reporting_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/audit"
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

	executionReport, err := repository.CreateTemplate(context.Background(), requester, reporting.TemplateInput{
		Name: "Audited execution", DatasourceID: datasource.ID,
		SQLText: "SELECT :as_of_date AS as_of_date,:branch AS branch,:amount AS amount WHERE :products__count>=0",
		Parameters: []reporting.Parameter{
			{Key: "as_of_date", Label: "As of Date", Type: reporting.ParameterDate, Required: true, DisplayOrder: 0},
			{Key: "branch", Label: "Branch", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT '001' AS value,'KC Jakarta' AS label", Required: true, DisplayOrder: 1},
			{Key: "products", Label: "Products", Type: reporting.ParameterMultipleOption, OptionSource: reporting.OptionSourceStatic, Options: []reporting.ParameterOption{{Value: "TAB002", Label: "Tabungan B", DisplayOrder: 2}, {Value: "TAB001", Label: "Tabungan A", DisplayOrder: 1}}, DisplayOrder: 2},
			{Key: "amount", Label: "Amount", Type: reporting.ParameterDecimal, Required: true, DisplayOrder: 3},
			{Key: "optional", Label: "Optional", Type: reporting.ParameterText, DisplayOrder: 4},
		},
	}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='active' WHERE id=?`, executionReport.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, executionReport.ID, user.ID, user.ID, integrationdb.Now()); err != nil {
		t.Fatal(err)
	}
	input := map[string]reporting.InputValue{
		"as_of_date": {Present: true, Values: []string{"2026-08-26"}},
		"branch":     {Present: true, Values: []string{"001"}},
		"products":   {Present: true, Values: []string{"TAB002", "TAB001"}},
		"amount":     {Present: true, Values: []string{"12345678901234567890.1200"}},
		"optional":   {Present: true},
	}
	if _, result, err := service.Run(context.Background(), requester, executionReport.ID, input); err != nil || len(result.Rows) != 1 {
		t.Fatalf("manual run result=%+v error=%v", result, err)
	}
	input["branch"] = reporting.InputValue{Present: true, Values: []string{"999"}}
	if _, _, err := service.Run(context.Background(), requester, executionReport.ID, input); err == nil {
		t.Fatal("invalid manual run succeeded")
	}
	var executionAudits []struct {
		Metadata []byte `db:"metadata"`
	}
	if err := database.Select(&executionAudits, `SELECT metadata FROM audit_logs WHERE action=? AND resource_type='report_template' AND resource_id=? ORDER BY id`, audit.ActionReportExecuted, executionReport.ID); err != nil {
		t.Fatal(err)
	}
	if len(executionAudits) != 2 {
		t.Fatalf("report.executed count=%d", len(executionAudits))
	}
	var succeeded, failed audit.ReportExecutionMetadata
	if err := json.Unmarshal(executionAudits[0].Metadata, &succeeded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(executionAudits[1].Metadata, &failed); err != nil {
		t.Fatal(err)
	}
	if succeeded.Outcome != "succeeded" || succeeded.ReportTemplateID != executionReport.ID || succeeded.DatasourceID != datasource.ID || succeeded.ReturnedRowCount == nil || *succeeded.ReturnedRowCount != 1 {
		t.Fatalf("success metadata=%+v", succeeded)
	}
	if failed.Outcome != "failed" || failed.FailureStage != "parameter_validation" || failed.FailureClass != "invalid_parameters" {
		t.Fatalf("failure metadata=%+v", failed)
	}
	if bytes.Contains(executionAudits[0].Metadata, []byte(`"rows"`)) || bytes.Contains(executionAudits[1].Metadata, []byte("invalid option")) {
		t.Fatalf("result data or raw error leaked: %s %s", executionAudits[0].Metadata, executionAudits[1].Metadata)
	}
	parameter := func(key string) audit.ReportParameterMetadata {
		for _, value := range succeeded.Parameters.Items {
			if value.Key == key {
				return value
			}
		}
		return audit.ReportParameterMetadata{}
	}
	if got := parameter("branch").Values[0]; got.Value != "001" || got.Label != "KC Jakarta" {
		t.Fatalf("branch audit=%+v", got)
	}
	products := parameter("products").Values
	if len(products) != 2 || products[0].Value != "TAB001" || products[1].Value != "TAB002" || products[0].Label != "Tabungan A" {
		t.Fatalf("products audit=%+v", products)
	}
	if got := parameter("amount").Values[0].Value; got != "12345678901234567890.12" {
		t.Fatalf("amount audit=%q", got)
	}
	if !parameter("optional").Unset {
		t.Fatal("unset parameter not preserved")
	}

	var testActions []string
	if err := database.Select(&testActions, `SELECT action FROM audit_logs WHERE resource_type='report_template' AND resource_id=? AND action IN (?,?) ORDER BY id`, report.ID, audit.ActionReportTemplateQueryTested, audit.ActionReportTemplateOptionsTested); err != nil {
		t.Fatal(err)
	}
	if len(testActions) != 4 || testActions[0] != string(audit.ActionReportTemplateQueryTested) {
		t.Fatalf("test audit actions=%v", testActions)
	}
	updatedDatasource, err := repository.UpdateDatasource(context.Background(), requester, datasource.ID, datasource.Revision, reporting.DatasourceInput{
		Name: datasource.Name, Description: datasource.Description, Host: connection.Host, Port: uint16(connection.Port), DatabaseName: connection.Name,
		Username: connection.User, Password: connection.Password, TLSPolicy: reporting.TLSDisabled,
	}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateDatasource(context.Background(), requester, datasource.ID, updatedDatasource.Revision, reporting.DatasourceInput{
		Name: datasource.Name, Description: datasource.Description, Host: connection.Host, Port: uint16(connection.Port), DatabaseName: connection.Name,
		Username: connection.User, TLSPolicy: reporting.TLSDisabled,
	}, integrationdb.Now()); err != nil {
		t.Fatal(err)
	}
	var datasourceAudits [][]byte
	if err := database.Select(&datasourceAudits, `SELECT metadata FROM audit_logs WHERE action=? AND resource_id=? ORDER BY id`, audit.ActionReportDatasourceUpdated, datasource.ID); err != nil {
		t.Fatal(err)
	}
	if len(datasourceAudits) != 2 || !bytes.Contains(datasourceAudits[0], []byte(`"credentials_changed":true`)) || !bytes.Contains(datasourceAudits[1], []byte(`"credentials_changed":false`)) || bytes.Contains(datasourceAudits[0], []byte(connection.Password)) {
		t.Fatalf("datasource update audits=%s", datasourceAudits)
	}
}
