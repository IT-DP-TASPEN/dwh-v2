//go:build integration

package sources

import (
	"context"
	"database/sql"
	"errors"
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

func TestSourceAuthProfileAssignmentAuditAndLifecycleRules(t *testing.T) {
	db := integrationdb.Open(t)
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, fmt.Sprintf("sourceprofileactor%d", time.Now().UnixNano()), role.ID, true)
	requester := integrationdb.Requester(actor, role)
	key := "cif_detail"
	var original struct {
		ProfileID sql.NullInt64 `db:"fincloud_auth_profile_id"`
		UpdatedBy sql.NullInt64 `db:"updated_by_user_id"`
	}
	if err := db.Get(&original, `SELECT fincloud_auth_profile_id,updated_by_user_id FROM source_settings WHERE source_id=?`, key); err != nil {
		t.Fatal(err)
	}
	ids := map[string]uint64{}
	for _, status := range []string{"active", "disabled", "archived"} {
		result, err := db.Exec(`INSERT INTO fincloud_auth_profiles
			(name,username,role_id,location_id,password_ciphertext,status,created_by_user_id,updated_by_user_id)
			VALUES (?,?,?,?,?,?,?,?)`, fmt.Sprintf("source-%s-%d", status, time.Now().UnixNano()), "user", "role", "location", []byte{2}, status, actor.ID, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		ids[status] = uint64(id)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=?,updated_by_user_id=? WHERE source_id=?`, nullableSQLID(original.ProfileID), nullableSQLID(original.UpdatedBy), key)
		_, _ = db.Exec(`DELETE FROM audit_logs WHERE actor_user_id=?`, actor.ID)
		_, _ = db.Exec(`DELETE FROM fincloud_auth_profiles WHERE created_by_user_id=?`, actor.ID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, actor.ID)
	})
	if _, err := db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=NULL WHERE source_id=?`, key); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(db)
	active, disabled, archived := ids["active"], ids["disabled"], ids["archived"]
	if err := service.SetAuthProfile(context.Background(), key, nil, &active, actor.ID, requester); err != nil {
		t.Fatal(err)
	}
	if err := service.SetAuthProfile(context.Background(), key, &active, &disabled, actor.ID, requester); err != nil {
		t.Fatalf("disabled profile assignment rejected: %v", err)
	}
	if err := service.SetAuthProfile(context.Background(), key, &disabled, &archived, actor.ID, requester); !errors.Is(err, ErrInvalidAuthProfile) {
		t.Fatalf("archived profile assignment error=%v", err)
	}
	var bound uint64
	if err := db.Get(&bound, `SELECT fincloud_auth_profile_id FROM source_settings WHERE source_id=?`, key); err != nil || bound != disabled {
		t.Fatalf("bound=%d error=%v", bound, err)
	}
	var audits int
	if err := db.Get(&audits, `SELECT COUNT(*) FROM audit_logs WHERE actor_user_id=? AND action='source.auth_profile_changed'`, actor.ID); err != nil || audits != 2 {
		t.Fatalf("audit count=%d error=%v", audits, err)
	}
}

func nullableSQLID(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}
