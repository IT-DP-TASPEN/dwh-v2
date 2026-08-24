//go:build integration

package schedules

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	domain "github.com/ibldzn/go-admin/internal/scheduler"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestScheduleManagementDerivesLiveDetailPolicy(t *testing.T) {
	db := integrationdb.Open(t)
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, fmt.Sprintf("scheduleactor%d", time.Now().UnixNano()), role.ID, true)
	domainService, err := domain.New(db, func(context.Context, *sqlx.Tx, string, ingestionrun.Parameters, ingestionrun.Trigger, string, *uint64) (uint64, error) {
		return 0, nil
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(db, domainService)
	created, err := service.Create(context.Background(), FormData{Name: "Live CIF", JobKey: "cif_detail", CronExpression: "0 1 * * *", Timezone: domain.DefaultTimezone}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM schedules WHERE id=?`, created.ID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, actor.ID)
	})
	if created.Definition.Policy.Kind != domain.PolicyDetailLiveSnapshot {
		t.Fatalf("policy=%s", created.Definition.Policy.Kind)
	}
	page, err := service.List(context.Background(), Filter{Job: "cif_detail"}, 1)
	if err != nil || len(page.Rows) == 0 {
		t.Fatalf("schedule list rows=%d error=%v", len(page.Rows), err)
	}

	bulk, err := service.CreateMany(context.Background(), BulkFormData{JobKeys: []string{"cif_detail", "saving_detail"},
		CronExpression: "0 2 * * *", Timezone: domain.DefaultTimezone}, actor.ID)
	if err != nil || bulk.Selected != 2 || bulk.Created != 2 || bulk.Skipped != 0 {
		t.Fatalf("bulk result=%+v error=%v", bulk, err)
	}
	secondCIF, err := service.CreateMany(context.Background(), BulkFormData{JobKeys: []string{"cif_detail"},
		CronExpression: " 0 3 * * * "}, actor.ID)
	if err != nil || secondCIF.Created != 1 || secondCIF.CronExpression != "0 3 * * *" || secondCIF.Timezone != domain.DefaultTimezone {
		t.Fatalf("second CIF result=%+v error=%v", secondCIF, err)
	}
	bulkIDs := []uint64{bulk.CreatedSchedules[0].ID, bulk.CreatedSchedules[1].ID, secondCIF.CreatedSchedules[0].ID}
	for _, id := range bulkIDs {
		id := id
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM schedules WHERE id=?`, id) })
	}
	var names []string
	if err := db.Select(&names, `SELECT name FROM schedules WHERE id IN (?,?,?) ORDER BY id`, bulkIDs[0], bulkIDs[1], bulkIDs[2]); err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || names[0] != "CIF Detail" || names[1] != "Saving Detail" || names[2] != "CIF Detail" {
		t.Fatalf("generated names=%v", names)
	}
}
