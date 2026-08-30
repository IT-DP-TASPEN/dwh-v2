//go:build integration

package reportexport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/audit"
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
	report := reporting.Template{ID: uint64(reportID), Name: "test", SQLText: "SELECT 1", DatasourceID: uint64(datasourceID), DatasourceName: "test", Revision: 1,
		Parameters: []reporting.Parameter{{Key: "branch", Label: "Branch", Type: reporting.ParameterSingleOption, Options: []reporting.ParameterOption{{Value: "001", Label: "KC Jakarta"}}}}}
	normalized, err := reporting.NormalizeParameters(report.Parameters, map[string]reporting.InputValue{"branch": {Present: true, Values: []string{"001"}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := repository.Submit(context.Background(), requester, report, normalized, now)
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
	completed, err := repository.Find(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordDownload(context.Background(), requester, completed, reportexport.DownloadAccessOwner, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_datasources SET status='disabled' WHERE id=?`, datasourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Submit(context.Background(), requester, report, normalized, now); !errors.Is(err, reporting.ErrForbidden) {
		t.Fatalf("disabled datasource submission error=%v", err)
	}
	if _, accessPath, err := repository.AuthorizeDownload(context.Background(), job.ID, user.ID, false); err != nil || accessPath != reportexport.DownloadAccessOwner {
		t.Fatalf("download access=%q error=%v", accessPath, err)
	}
	if _, err := database.Exec(`DELETE FROM report_template_user_access WHERE report_id=? AND user_id=?`, reportID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, accessPath, err := repository.AuthorizeDownload(context.Background(), job.ID, user.ID, false); err != nil || accessPath != "" {
		t.Fatalf("revoked download access=%q error=%v", accessPath, err)
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
	if _, err := repository.Submit(context.Background(), requester, report, normalized, now); !errors.Is(err, reporting.ErrForbidden) {
		t.Fatalf("disabled report submission error=%v", err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='active' WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	second, err := repository.Submit(context.Background(), requester, report, normalized, now.Add(4*time.Second))
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
	health, err := repository.HealthForUser(context.Background(), user.ID)
	if err != nil || health.Queued != 0 || health.Running != 1 || health.Failed != 0 || health.Processing() != 1 {
		t.Fatalf("running health=%+v error=%v", health, err)
	}
	if failed, err := repository.Fail(context.Background(), second.ID, owner('c'), 2, "source", "failed", now.Add(7*time.Second)); err != nil || !failed {
		t.Fatalf("fail export=%t error=%v", failed, err)
	}
	health, err = repository.HealthForUser(context.Background(), user.ID)
	failures, failureErr := repository.RecentFailuresForUser(context.Background(), user.ID, 8)
	if err != nil || failureErr != nil || health.Running != 0 || health.Failed != 1 || len(failures) != 1 || failures[0].ID != second.ID {
		t.Fatalf("failed health=%+v failures=%+v errors=%v/%v", health, failures, err, failureErr)
	}
	otherHealth, err := repository.HealthForUser(context.Background(), user.ID+1000)
	if err != nil || otherHealth != (reportexport.Health{}) {
		t.Fatalf("other user health=%+v error=%v", otherHealth, err)
	}

	var audits []struct {
		Action   string `db:"action"`
		Metadata []byte `db:"metadata"`
	}
	if err := database.Select(&audits, `SELECT action,metadata FROM audit_logs WHERE resource_type='report_export' AND resource_id IN (?,?) ORDER BY id`, job.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 3 || audits[0].Action != string(audit.ActionReportExportSubmitted) || audits[1].Action != string(audit.ActionReportExportDownloaded) || audits[2].Action != string(audit.ActionReportExportSubmitted) {
		t.Fatalf("export audit actions=%+v", audits)
	}
	var submitted audit.ReportExportSubmittedMetadata
	if err := json.Unmarshal(audits[0].Metadata, &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.ExportJobID != job.ID || submitted.ReportTemplateID != uint64(reportID) || len(submitted.Parameters.Items) != 1 || submitted.Parameters.Items[0].Values[0].Value != "001" || submitted.Parameters.Items[0].Values[0].Label != "KC Jakarta" {
		t.Fatalf("submit metadata=%+v", submitted)
	}
	var downloaded audit.ReportExportDownloadedMetadata
	if err := json.Unmarshal(audits[1].Metadata, &downloaded); err != nil {
		t.Fatal(err)
	}
	if downloaded.ArtifactName != "test.xlsx" || downloaded.ExportJobID != job.ID || downloaded.ReportTemplateID != uint64(reportID) {
		t.Fatalf("download metadata=%+v", downloaded)
	}
}

func TestExportOversightScopesHistoricalAccessAndAudit(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	now := integrationdb.Now()
	var registeredPermission int
	if err := database.Get(&registeredPermission, `SELECT COUNT(*) FROM permissions WHERE `+"`key`"+`=?`, reports.PermissionViewAllExports); err != nil || registeredPermission != 1 {
		t.Fatalf("registered view_all permission=%d err=%v", registeredPermission, err)
	}
	exporterRole := integrationdb.CustomRole(t, database, "Exporter", "exporter")
	opsRole := integrationdb.CustomRole(t, database, "Export Operations", "export-operations")
	for roleID, permission := range map[uint64]string{exporterRole.ID: reports.PermissionExport, opsRole.ID: reports.PermissionViewAllExports} {
		if _, err := database.Exec(`INSERT INTO role_permissions (role_id,permission_id) SELECT ?,id FROM permissions WHERE key=?`, roleID, permission); err != nil {
			t.Fatal(err)
		}
	}
	userA := integrationdb.User(t, database, "user-a", exporterRole.ID, true)
	userB := integrationdb.User(t, database, "user-b", exporterRole.ID, true)
	ops := integrationdb.User(t, database, "ops", opsRole.ID, true)
	datasourceResult, err := database.Exec(`INSERT INTO report_datasources (name,host,port,database_name,username,password_ciphertext,tls_policy,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('oversight','127.0.0.1',3306,'test','test',X'01','disabled','active',?,?,?,?)`, userA.ID, userA.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	datasourceID, _ := datasourceResult.LastInsertId()
	reportResult, err := database.Exec(`INSERT INTO report_templates (name,datasource_id,sql_text,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES ('Oversight Report',?,'SELECT 1','active',?,?,?,?)`, datasourceID, userA.ID, userA.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	reportID, _ := reportResult.LastInsertId()
	for _, found := range []uint64{userA.ID, userB.ID, ops.ID} {
		if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, reportID, found, userA.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := reportexport.NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	report := reporting.Template{ID: uint64(reportID), Name: "Oversight Report", SQLText: "SELECT 1", DatasourceID: uint64(datasourceID), Revision: 1}
	jobA, err := repository.Submit(context.Background(), integrationdb.Requester(userA, exporterRole), report, map[string]reporting.NormalizedValue{}, now)
	if err != nil {
		t.Fatal(err)
	}
	jobB, err := repository.Submit(context.Background(), integrationdb.Requester(userB, exporterRole), report, map[string]reporting.NormalizedValue{}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for index, job := range []reportexport.Job{jobA, jobB} {
		ownerValue := owner(byte('x' + index))
		claimed, err := repository.Claim(context.Background(), ownerValue, now.Add(time.Duration(index+2)*time.Second))
		if err != nil || claimed == nil || claimed.ID != job.ID {
			t.Fatalf("claim %d=%+v err=%v", job.ID, claimed, err)
		}
		artifact := reportexport.Artifact{RelativePath: fmt.Sprintf("final/%d/1/token/report.xlsx", job.ID), Name: "report.xlsx", Type: "xlsx", Size: 10, Parts: 1, Rows: 1}
		if succeeded, err := repository.Succeed(context.Background(), job.ID, ownerValue, claimed.Attempt, artifact, now.Add(time.Hour), now.Add(time.Duration(index+4)*time.Second)); err != nil || !succeeded {
			t.Fatalf("succeed %d=%t err=%v", job.ID, succeeded, err)
		}
	}

	mineTotal, err := repository.CountVisible(context.Background(), reportexport.ScopeMine, userA.ID)
	if err != nil || mineTotal != 1 {
		t.Fatalf("mine total=%d err=%v", mineTotal, err)
	}
	allTotal, err := repository.CountVisible(context.Background(), reportexport.ScopeAll, ops.ID)
	if err != nil || allTotal != 2 {
		t.Fatalf("all total=%d err=%v", allTotal, err)
	}
	all, err := repository.ListVisible(context.Background(), reportexport.ScopeAll, ops.ID, 100, 0)
	if err != nil || len(all) != 2 || all[0].ID != jobB.ID || all[0].RequesterUsername == nil || *all[0].RequesterUsername != userB.Username {
		t.Fatalf("all rows=%+v err=%v", all, err)
	}
	if _, err := repository.FindVisible(context.Background(), jobB.ID, reportexport.ScopeMine, userA.ID); !errors.Is(err, reporting.ErrNotFound) {
		t.Fatalf("cross-user mine detail=%v", err)
	}
	if visible, err := repository.FindVisible(context.Background(), jobB.ID, reportexport.ScopeAll, ops.ID); err != nil || visible.ID != jobB.ID {
		t.Fatalf("all detail=%+v err=%v", visible, err)
	}
	if _, err := repository.Submit(context.Background(), integrationdb.Requester(ops, opsRole), report, map[string]reporting.NormalizedValue{}, now); !errors.Is(err, reporting.ErrForbidden) {
		t.Fatalf("view_all submission=%v", err)
	}

	if _, err := database.Exec(`DELETE FROM report_template_user_access WHERE report_id=? AND user_id=?`, reportID, userB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='disabled' WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	if _, accessPath, err := repository.AuthorizeDownload(context.Background(), jobB.ID, userB.ID, false); err != nil || accessPath != "" {
		t.Fatalf("revoked owner path=%q err=%v", accessPath, err)
	}
	if _, accessPath, err := repository.AuthorizeDownload(context.Background(), jobB.ID, userB.ID, true); err != nil || accessPath != reportexport.DownloadAccessViewAll {
		t.Fatalf("owner true-OR path=%q err=%v", accessPath, err)
	}
	completed, accessPath, err := repository.AuthorizeDownload(context.Background(), jobB.ID, ops.ID, true)
	if err != nil || accessPath != reportexport.DownloadAccessViewAll {
		t.Fatalf("cross-user path=%q err=%v", accessPath, err)
	}
	if err := repository.RecordDownload(context.Background(), integrationdb.Requester(ops, opsRole), completed, accessPath, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	var event struct {
		EffectiveUserID uint64 `db:"effective_user_id"`
		Metadata        []byte `db:"metadata"`
	}
	if err := database.Get(&event, `SELECT effective_user_id,metadata FROM audit_logs WHERE action=? AND resource_id=? ORDER BY id DESC LIMIT 1`, audit.ActionReportExportDownloaded, jobB.ID); err != nil {
		t.Fatal(err)
	}
	var metadata audit.ReportExportDownloadedMetadata
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if event.EffectiveUserID != ops.ID || metadata.ExportJobID != jobB.ID || metadata.SubmittedByUserID != userB.ID || metadata.AccessPath != string(reportexport.DownloadAccessViewAll) {
		t.Fatalf("download audit event=%+v metadata=%+v", event, metadata)
	}
	if err := repository.MarkArtifactDeleted(context.Background(), jobB.ID, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	visible, err := repository.FindVisible(context.Background(), jobB.ID, reportexport.ScopeAll, ops.ID)
	if err != nil || visible.ArtifactDeletedAt == nil {
		t.Fatalf("retained metadata=%+v err=%v", visible, err)
	}

	for index, want := range map[string]string{
		"idx_report_export_jobs_created":   "created_at,id",
		"idx_report_export_jobs_submitter": "submitted_by_user_id,created_at,id",
	} {
		var columns string
		if err := database.Get(&columns, `SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='report_export_jobs' AND INDEX_NAME=?`, index); err != nil || columns != want {
			t.Fatalf("index %s columns=%q want=%q err=%v", index, columns, want, err)
		}
	}
	for _, statement := range []string{
		`EXPLAIN SELECT id FROM report_export_jobs ORDER BY created_at DESC,id DESC LIMIT 100`,
		`EXPLAIN SELECT id FROM report_export_jobs WHERE submitted_by_user_id=1 ORDER BY created_at DESC,id DESC LIMIT 100`,
	} {
		rows, err := database.Queryx(statement)
		if err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
}

func owner(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
