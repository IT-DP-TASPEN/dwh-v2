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
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestScheduleManagementDerivesLiveDetailPolicy(t *testing.T) {
	db := integrationdb.Open(t)
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, fmt.Sprintf("scheduleactor%d", time.Now().UnixNano()), role.ID, true)
	requester := integrationdb.Requester(actor, role)
	domainService, err := domain.New(db, func(context.Context, *sqlx.Tx, string, ingestionrun.Parameters, ingestionrun.Trigger, string, *uint64) (uint64, error) {
		return 0, nil
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(db, domainService)
	created, err := service.Create(context.Background(), FormData{Name: "Live CIF", JobKey: "cif_detail", CronExpression: "0 1 * * *", Timezone: domain.DefaultTimezone}, actor.ID, requester)
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
		CronExpression: "0 2 * * *", Timezone: domain.DefaultTimezone}, actor.ID, requester)
	if err != nil || bulk.Selected != 2 || bulk.Created != 2 || bulk.Skipped != 0 {
		t.Fatalf("bulk result=%+v error=%v", bulk, err)
	}
	secondCIF, err := service.CreateMany(context.Background(), BulkFormData{JobKeys: []string{"cif_detail"},
		CronExpression: " 0 3 * * * "}, actor.ID, requester)
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
	badRequester := securityctx.Requester{Actor: securityctx.Identity{UserID: actor.ID}, Effective: securityctx.Identity{UserID: actor.ID}}
	if _, err := service.Enable(context.Background(), created.ID, created.Revision, actor.ID, badRequester); err == nil {
		t.Fatal("schedule mutation committed without required audit identity")
	}
	unchanged, err := service.domain.Get(context.Background(), created.ID)
	if err != nil || unchanged.Enabled || unchanged.Revision != created.Revision {
		t.Fatalf("failed audit did not roll back schedule: %+v error=%v", unchanged, err)
	}
	enabled, err := service.Enable(context.Background(), created.ID, created.Revision, actor.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := service.Disable(context.Background(), created.ID, enabled.Revision, actor.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Archive(context.Background(), created.ID, disabled.Revision, actor.ID, requester); err != nil {
		t.Fatal(err)
	}
	var events []struct {
		Action, Metadata string
	}
	if err := db.Select(&events, `SELECT action,CAST(metadata AS CHAR) metadata FROM audit_logs WHERE actor_user_id=? AND action LIKE 'schedule.%' ORDER BY id`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if len(events) != 7 || events[0].Action != "schedule.created" || events[4].Metadata != `{"from":"disabled","to":"enabled"}` || events[5].Metadata != `{"from":"enabled","to":"disabled"}` || events[6].Metadata != `{"from":"disabled","to":"archived"}` {
		t.Fatalf("schedule events=%+v", events)
	}
}
