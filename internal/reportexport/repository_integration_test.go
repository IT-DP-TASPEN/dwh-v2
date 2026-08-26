//go:build integration

package reportexport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestExportAuthorizationClaimFencingAndDownloadRules(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Exporter", "exporter")
	if _, err := database.Exec(`INSERT INTO role_permissions (role_id,permission_id) SELECT ?,id FROM permissions WHERE key=?`, role.ID, reports.PermissionExport); err != nil {
		t.Fatal(err)
	}
	user := integrationdb.User(t, database, "exporter", role.ID, true)
	requester := integrationdb.Requester(user, role)
	now := integrationdb.Now()
	datasourceResult, err := database.Exec(`INSERT INTO report_datasources (name,host,port,database_name,username,password_ciphertext,tls_policy,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('test','127.0.0.1',3306,'test','test',X'01','disabled','active',?,?,?,?)`, user.ID, user.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	datasourceID, _ := datasourceResult.LastInsertId()
	reportResult, err := database.Exec(`INSERT INTO report_templates (name,datasource_id,sql_text,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('test',?,'SELECT 1','active',?,?,?,?)`, datasourceID, user.ID, user.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	reportID, _ := reportResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, reportID, user.ID, user.ID, now); err != nil {
		t.Fatal(err)
	}
	repository, err := reportexport.NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	report := reporting.Template{ID: uint64(reportID), Name: "test", SQLText: "SELECT 1", DatasourceID: uint64(datasourceID), Revision: 1}
	job, err := repository.Submit(context.Background(), requester, report, map[string]reporting.NormalizedValue{}, now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(context.Background(), owner('a'), now.Add(time.Second))
	if err != nil || claimed == nil || claimed.ID != job.ID || claimed.Attempt != 1 {
		t.Fatalf("claimed=%+v error=%v", claimed, err)
	}
	owned, err := repository.Heartbeat(context.Background(), job.ID, owner('a'), 1, now.Add(2*time.Second))
	if err != nil || !owned {
		t.Fatalf("owned=%v error=%v", owned, err)
	}
	succeeded, err := repository.Succeed(context.Background(), job.ID, owner('a'), 1, reportexport.Artifact{RelativePath: "final/1/1/token/test.xlsx", Name: "test.xlsx", Type: "xlsx", Size: 1, Parts: 1, Rows: 1}, now.Add(time.Hour), now.Add(3*time.Second))
	if err != nil || !succeeded {
		t.Fatalf("succeeded=%v error=%v", succeeded, err)
	}
	if _, err := database.Exec(`UPDATE report_datasources SET status='disabled' WHERE id=?`, datasourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Submit(context.Background(), requester, report, map[string]reporting.NormalizedValue{}, now); !errors.Is(err, reporting.ErrForbidden) {
		t.Fatalf("disabled datasource submission error=%v", err)
	}
	if _, allowed, err := repository.Downloadable(context.Background(), job.ID, user.ID); err != nil || !allowed {
		t.Fatalf("download allowed=%v error=%v", allowed, err)
	}
	if _, err := database.Exec(`DELETE FROM report_template_user_access WHERE report_id=? AND user_id=?`, reportID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, allowed, err := repository.Downloadable(context.Background(), job.ID, user.ID); err != nil || allowed {
		t.Fatalf("revoked download allowed=%v error=%v", allowed, err)
	}

	if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, reportID, user.ID, user.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_datasources SET status='active' WHERE id=?`, datasourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='disabled' WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Submit(context.Background(), requester, report, map[string]reporting.NormalizedValue{}, now); !errors.Is(err, reporting.ErrForbidden) {
		t.Fatalf("disabled report submission error=%v", err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='active' WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	second, err := repository.Submit(context.Background(), requester, report, map[string]reporting.NormalizedValue{}, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = repository.Claim(context.Background(), owner('b'), now.Add(5*time.Second))
	if err != nil || claimed == nil || claimed.ID != second.ID {
		t.Fatalf("second claim=%+v error=%v", claimed, err)
	}
	if _, err := database.Exec(`UPDATE report_export_jobs SET heartbeat_at=? WHERE id=?`, now.Add(-time.Minute), second.ID); err != nil {
		t.Fatal(err)
	}
	if recovered, err := repository.RequeueStale(context.Background(), now); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d error=%v", recovered, err)
	}
	retried, err := repository.Claim(context.Background(), owner('c'), now.Add(6*time.Second))
	if err != nil || retried == nil || retried.ID != second.ID || retried.Attempt != 2 {
		t.Fatalf("retried=%+v error=%v", retried, err)
	}
	owned, err = repository.Heartbeat(context.Background(), second.ID, owner('b'), 1, now.Add(6*time.Second))
	if err != nil || owned {
		t.Fatalf("lost claim owned=%v error=%v", owned, err)
	}
}

func owner(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
