//go:build integration

package sources

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestSourceStateCASPersistsActor(t *testing.T) {
	db := integrationdb.Open(t)
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, fmt.Sprintf("sourceactor%d", time.Now().UnixNano()), role.ID, true)
	requester := integrationdb.Requester(actor, role)
	var enabled bool
	if err := db.Get(&enabled, `SELECT enabled FROM source_settings WHERE source_id='cif_detail'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=?,updated_by_user_id=NULL WHERE source_id='cif_detail'`, enabled)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, actor.ID)
	})
	service, _ := NewService(db)
	if err := service.SetEnabled(context.Background(), "cif_detail", enabled, !enabled, actor.ID, requester); err != nil {
		t.Fatal(err)
	}
	var updatedBy uint64
	if err := db.Get(&updatedBy, `SELECT updated_by_user_id FROM source_settings WHERE source_id='cif_detail'`); err != nil || updatedBy != actor.ID {
		t.Fatalf("updated_by=%d error=%v", updatedBy, err)
	}
	if err := service.SetEnabled(context.Background(), "cif_detail", enabled, !enabled, actor.ID, requester); err != ErrConflict {
		t.Fatalf("stale CAS error=%v", err)
	}
	badRequester := securityctx.Requester{Actor: securityctx.Identity{UserID: actor.ID}, Effective: securityctx.Identity{UserID: actor.ID}}
	if err := service.SetEnabled(context.Background(), "cif_detail", !enabled, enabled, actor.ID, badRequester); err == nil {
		t.Fatal("source mutation committed without required audit identity")
	}
	var afterFailedAudit bool
	if err := db.Get(&afterFailedAudit, `SELECT enabled FROM source_settings WHERE source_id='cif_detail'`); err != nil || afterFailedAudit != !enabled {
		t.Fatalf("failed audit did not roll back source: enabled=%v error=%v", afterFailedAudit, err)
	}
	var event struct {
		Action   string `db:"action"`
		Metadata string `db:"metadata"`
		Count    int    `db:"count"`
	}
	if err := db.Get(&event, `SELECT MIN(action) action,MIN(CAST(metadata AS CHAR)) metadata,COUNT(*) count FROM audit_logs WHERE actor_user_id=? AND action='source.state_changed'`, actor.ID); err != nil {
		t.Fatal(err)
	}
	from, to := "disabled", "enabled"
	if enabled {
		from, to = "enabled", "disabled"
	}
	want := fmt.Sprintf(`{"source_key":"cif_detail","from":"%s","to":"%s"}`, from, to)
	if event.Count != 1 || event.Action != "source.state_changed" || event.Metadata != want {
		t.Fatalf("source event=%+v want=%s", event, want)
	}
}
