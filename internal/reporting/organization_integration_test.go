//go:build integration

package reporting_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestPersonalReportOrganizationPersistenceAndVisibility(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Report user", "report-user")
	userA := integrationdb.User(t, database, "report-a", role.ID, true)
	userB := integrationdb.User(t, database, "report-b", role.ID, true)
	now := integrationdb.Now()

	datasourceResult, err := database.Exec(`INSERT INTO report_datasources
		(name,host,port,database_name,username,password_ciphertext,tls_policy,status,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES ('organization','127.0.0.1',3306,'test','test',X'01','disabled','active',?,?,?,?)`, userA.ID, userA.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	datasourceID, _ := datasourceResult.LastInsertId()
	reportIDs := make([]uint64, 4)
	for index, name := range []string{"NPL 100%", "Nominatif Kredit", "Dormant lifecycle", "Other"} {
		result, err := database.Exec(`INSERT INTO report_templates
			(name,description,datasource_id,sql_text,status,created_by_user_id,updated_by_user_id,created_at,updated_at)
			VALUES (?,CONCAT(?,' description'),?,'SELECT 1','active',?,?,?,?)`, name, name, datasourceID, userA.ID, userA.ID, now, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		reportIDs[index] = uint64(id)
		for _, userID := range []uint64{userA.ID, userB.ID} {
			if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, id, userID, userA.ID, now); err != nil {
				t.Fatal(err)
			}
		}
	}

	var key [32]byte
	repository, err := reporting.NewRepository(database, reporting.NewCipher(key))
	if err != nil {
		t.Fatal(err)
	}
	kredit, err := repository.CreateUserReportFolder(context.Background(), userA.ID, " Kredit ", now)
	if err != nil || kredit.Name != "Kredit" {
		t.Fatalf("create folder=%+v error=%v", kredit, err)
	}
	if _, err := repository.CreateUserReportFolder(context.Background(), userA.ID, "kREDIT", now); !errors.Is(err, reporting.ErrFolderNameTaken) {
		t.Fatalf("case-insensitive duplicate error=%v", err)
	}
	funding, err := repository.CreateUserReportFolder(context.Background(), userA.ID, "Funding", now)
	if err != nil {
		t.Fatal(err)
	}
	otherFolder, err := repository.CreateUserReportFolder(context.Background(), userB.ID, "Kredit", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RenameUserReportFolder(context.Background(), userA.ID, funding.ID, "KREDIT", now); !errors.Is(err, reporting.ErrFolderNameTaken) {
		t.Fatalf("duplicate rename error=%v", err)
	}

	if err := repository.SetReportStarred(context.Background(), userA.ID, reportIDs[0], true, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetReportStarred(context.Background(), userA.ID, reportIDs[0], true, now); err != nil {
		t.Fatalf("idempotent star: %v", err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportIDs[0], &kredit.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportIDs[0], &funding.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportIDs[0], &funding.ID, now); err != nil {
		t.Fatalf("idempotent move: %v", err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportIDs[0], nil, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportIDs[0], &kredit.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportIDs[1], &otherFolder.ID, now); !errors.Is(err, reporting.ErrNotFound) {
		t.Fatalf("cross-owner move error=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO report_user_preferences (user_id,report_id,folder_id,starred) VALUES (?,?,?,FALSE)`, userA.ID, reportIDs[3], otherFolder.ID); err == nil {
		t.Fatal("database accepted cross-owner folder")
	}

	if err := repository.SetReportStarred(context.Background(), userB.ID, reportIDs[0], false, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.MoveReportToFolder(context.Background(), userB.ID, reportIDs[0], &otherFolder.ID, now); err != nil {
		t.Fatal(err)
	}
	userBOrganization, err := repository.ListRuntimeReportOrganization(context.Background(), userB.ID, reporting.RuntimeReportFilter{FolderID: &otherFolder.ID})
	if err != nil || len(userBOrganization.Reports) != 1 || userBOrganization.Reports[0].Starred {
		t.Fatalf("independent user organization=%+v error=%v", userBOrganization, err)
	}

	organization, err := repository.ListRuntimeReportOrganization(context.Background(), userA.ID, reporting.RuntimeReportFilter{})
	if err != nil || organization.StarredVisibleCount != 1 || len(organization.Reports) != 4 || visibleCount(organization.Folders, kredit.ID) != 1 {
		t.Fatalf("organization=%+v error=%v", organization, err)
	}
	search, err := repository.ListRuntimeReportOrganization(context.Background(), userA.ID, reporting.RuntimeReportFilter{Query: "100%"})
	if err != nil || len(search.Reports) != 1 || search.Reports[0].ID != reportIDs[0] {
		t.Fatalf("escaped search=%+v error=%v", search.Reports, err)
	}
	starred, err := repository.ListRuntimeReportOrganization(context.Background(), userA.ID, reporting.RuntimeReportFilter{Starred: true})
	if err != nil || len(starred.Reports) != 1 || starred.Reports[0].ID != reportIDs[0] {
		t.Fatalf("starred scope=%+v error=%v", starred.Reports, err)
	}

	for _, reportID := range reportIDs[1:3] {
		if err := repository.MoveReportToFolder(context.Background(), userA.ID, reportID, &kredit.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SetReportStarred(context.Background(), userA.ID, reportIDs[1], true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM report_template_user_access WHERE report_id=? AND user_id=?`, reportIDs[1], userA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='archived' WHERE id=?`, reportIDs[2]); err != nil {
		t.Fatal(err)
	}
	organization, err = repository.ListRuntimeReportOrganization(context.Background(), userA.ID, reporting.RuntimeReportFilter{})
	if err != nil || visibleCount(organization.Folders, kredit.ID) != 1 || organization.StarredVisibleCount != 1 {
		t.Fatalf("dormant rows leaked into counts: %+v error=%v", organization, err)
	}
	var dormant int
	if err := database.Get(&dormant, `SELECT COUNT(*) FROM report_user_preferences WHERE user_id=? AND folder_id=?`, userA.ID, kredit.ID); err != nil || dormant != 3 {
		t.Fatalf("stored memberships=%d error=%v", dormant, err)
	}
	if err := repository.DeleteUserReportFolder(context.Background(), userA.ID, kredit.ID); err != nil {
		t.Fatal(err)
	}
	var unfiled, stillStarred int
	if err := database.Get(&unfiled, `SELECT COUNT(*) FROM report_user_preferences WHERE user_id=? AND report_id IN (?,?,?) AND folder_id IS NULL`, userA.ID, reportIDs[0], reportIDs[1], reportIDs[2]); err != nil || unfiled != 3 {
		t.Fatalf("unfiled memberships=%d error=%v", unfiled, err)
	}
	if err := database.Get(&stillStarred, `SELECT COUNT(*) FROM report_user_preferences WHERE user_id=? AND report_id=? AND starred=TRUE`, userA.ID, reportIDs[1]); err != nil || stillStarred != 1 {
		t.Fatalf("preserved dormant star=%d error=%v", stillStarred, err)
	}
	if _, err := database.Exec(`INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?)`, reportIDs[1], userA.ID, userA.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE report_templates SET status='active' WHERE id=?`, reportIDs[2]); err != nil {
		t.Fatal(err)
	}
	organization, err = repository.ListRuntimeReportOrganization(context.Background(), userA.ID, reporting.RuntimeReportFilter{})
	if err != nil || reportFolder(organization.Reports, reportIDs[1]) != nil || reportFolder(organization.Reports, reportIDs[2]) != nil {
		t.Fatalf("restored reports were not unfiled: %+v error=%v", organization.Reports, err)
	}

	empty, err := repository.CreateUserReportFolder(context.Background(), userA.ID, "Empty", now)
	if err != nil || repository.DeleteUserReportFolder(context.Background(), userA.ID, empty.ID) != nil {
		t.Fatalf("empty folder lifecycle=%+v error=%v", empty, err)
	}
	var auditRows int
	if err := database.Get(&auditRows, `SELECT COUNT(*) FROM audit_logs`); err != nil || auditRows != 0 {
		t.Fatalf("personal organization audit rows=%d error=%v", auditRows, err)
	}
	if _, err := database.Exec(`UPDATE users SET is_active=FALSE WHERE id=?`, userA.ID); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := database.Get(&retained, `SELECT COUNT(*) FROM report_user_preferences WHERE user_id=?`, userA.ID); err != nil || retained == 0 {
		t.Fatalf("deactivation retained preferences=%d error=%v", retained, err)
	}
	organization, err = repository.ListRuntimeReportOrganization(context.Background(), userA.ID, reporting.RuntimeReportFilter{})
	if err != nil || len(organization.Reports) != 0 || organization.StarredVisibleCount != 0 {
		t.Fatalf("inactive user visibility=%+v error=%v", organization, err)
	}
}

func TestConcurrentReportFolderNameCreation(t *testing.T) {
	database := integrationdb.Open(t)
	integrationdb.Reset(t, database, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, database, "Concurrent", "concurrent-folder")
	user := integrationdb.User(t, database, "concurrent-folder", role.ID, true)
	var key [32]byte
	repository, err := reporting.NewRepository(database, reporting.NewCipher(key))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsFound := make([]error, 2)
	var group sync.WaitGroup
	for index, name := range []string{"Treasury", "treasury"} {
		group.Add(1)
		go func(index int, name string) {
			defer group.Done()
			<-start
			_, errorsFound[index] = repository.CreateUserReportFolder(context.Background(), user.ID, name, integrationdb.Now())
		}(index, name)
	}
	close(start)
	group.Wait()
	successes, duplicates := 0, 0
	for _, err := range errorsFound {
		if err == nil {
			successes++
		} else if errors.Is(err, reporting.ErrFolderNameTaken) {
			duplicates++
		} else {
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if successes != 1 || duplicates != 1 {
		t.Fatalf("successes=%d duplicates=%d errors=%v", successes, duplicates, errorsFound)
	}
}

func visibleCount(folders []reporting.UserReportFolder, id uint64) int {
	for _, folder := range folders {
		if folder.ID == id {
			return folder.VisibleReportCount
		}
	}
	return -1
}

func reportFolder(reports []reporting.RuntimeReport, id uint64) *uint64 {
	for _, report := range reports {
		if report.ID == id {
			return report.FolderID
		}
	}
	return nil
}
