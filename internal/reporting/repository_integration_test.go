//go:build integration

package reporting_test

import (
	"context"
	"testing"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDynamicOptionDefinitionPersistsCanonicalState(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Author", "author")
	user := integrationdb.User(t, database, "author", role.ID, true)
	requester := integrationdb.Requester(user, role)
	now := integrationdb.Now()
	datasourceResult, err := database.Exec(`INSERT INTO report_datasources (name,host,port,database_name,username,password_ciphertext,tls_policy,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('dynamic','127.0.0.1',3306,'test','test',X'01','disabled','active',?,?,?,?)`, user.ID, user.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	datasourceID, _ := datasourceResult.LastInsertId()
	var key [32]byte
	repository, err := reporting.NewRepository(database, reporting.NewCipher(key))
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateTemplate(context.Background(), requester, reporting.TemplateInput{
		Name: "Dynamic", DatasourceID: uint64(datasourceID), SQLText: "SELECT :city",
		Parameters: []reporting.Parameter{
			{Key: "province", Label: "Province", Type: reporting.ParameterText, DisplayOrder: 0},
			{Key: "city", Label: "City", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceDynamic, DynamicOptionSQL: "SELECT code AS value,name AS label FROM cities WHERE province=:province", DisplayOrder: 1},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := created.Parameters[1]; got.OptionSource != reporting.OptionSourceDynamic || got.DynamicOptionSQL == "" || len(got.Options) != 0 {
		t.Fatalf("parameter=%+v", got)
	}
	var options int
	if err := database.Get(&options, `SELECT COUNT(*) FROM report_parameter_options o JOIN report_parameters p ON p.id=o.parameter_id WHERE p.report_id=?`, created.ID); err != nil || options != 0 {
		t.Fatalf("options=%d error=%v", options, err)
	}
	updated, err := repository.UpdateTemplate(context.Background(), requester, created.ID, created.Revision, reporting.TemplateInput{
		Name: "Static", DatasourceID: uint64(datasourceID), SQLText: "SELECT :city",
		Parameters: []reporting.Parameter{{Key: "city", Label: "City", Type: reporting.ParameterSingleOption, OptionSource: reporting.OptionSourceStatic, Options: []reporting.ParameterOption{{Value: "001", Label: "One"}}}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Parameters[0]; got.OptionSource != reporting.OptionSourceStatic || got.DynamicOptionSQL != "" || len(got.Options) != 1 {
		t.Fatalf("source switch retained inactive state: %+v", got)
	}
	var state struct {
		Source     string  `db:"option_source"`
		DynamicSQL *string `db:"dynamic_option_sql"`
		Options    int     `db:"options"`
	}
	if err := database.Get(&state, `SELECT p.option_source,p.dynamic_option_sql,(SELECT COUNT(*) FROM report_parameter_options o WHERE o.parameter_id=p.id) AS options FROM report_parameters p WHERE p.report_id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if state.Source != "static" || state.DynamicSQL != nil || state.Options != 1 {
		t.Fatalf("database canonical state=%+v", state)
	}
}

func TestParameterWithoutDefaultStoresSQLNullAndReadsBackAsJSONNull(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Author", "author-null-default")
	user := integrationdb.User(t, database, "author-null-default", role.ID, true)
	requester := integrationdb.Requester(user, role)
	now := integrationdb.Now()
	datasourceResult, err := database.Exec(`INSERT INTO report_datasources (name,host,port,database_name,username,password_ciphertext,tls_policy,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('null-default','127.0.0.1',3306,'test','test',X'01','disabled','active',?,?,?,?)`, user.ID, user.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	datasourceID, _ := datasourceResult.LastInsertId()
	repository, err := reporting.NewRepository(database, reporting.NewCipher([32]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateTemplate(context.Background(), requester, reporting.TemplateInput{
		Name: "No default", DatasourceID: uint64(datasourceID), SQLText: "SELECT :branch",
		Parameters: []reporting.Parameter{{Key: "branch", Label: "Branch", Type: reporting.ParameterText}},
	}, now)
	if err != nil {
		t.Fatalf("create with empty default: %v", err)
	}
	var stored *string
	if err := database.Get(&stored, `SELECT default_value FROM report_parameters WHERE report_id=?`, created.ID); err != nil || stored != nil {
		t.Fatalf("stored default=%v error=%v, want SQL NULL", stored, err)
	}
	found, err := repository.FindTemplate(context.Background(), created.ID)
	if err != nil || len(found.Parameters) != 1 || string(found.Parameters[0].DefaultValue) != "null" {
		t.Fatalf("found=%+v error=%v", found.Parameters, err)
	}
}
