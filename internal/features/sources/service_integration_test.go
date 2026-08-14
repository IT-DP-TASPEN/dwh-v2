//go:build integration

package sources

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestSourceStateCASPersistsActor(t *testing.T) {
	db := integrationdb.Open(t)
	if err := access.Bootstrap(context.Background(), db, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	actor := integrationdb.User(t, db, fmt.Sprintf("sourceactor%d", time.Now().UnixNano()), role.ID, true)
	var enabled bool
	if err := db.Get(&enabled, `SELECT enabled FROM source_settings WHERE source_id='cif_detail'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=?,updated_by_user_id=NULL WHERE source_id='cif_detail'`, enabled)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, actor.ID)
	})
	service, _ := NewService(db)
	if err := service.SetEnabled(context.Background(), "cif_detail", enabled, !enabled, actor.ID); err != nil {
		t.Fatal(err)
	}
	var updatedBy uint64
	if err := db.Get(&updatedBy, `SELECT updated_by_user_id FROM source_settings WHERE source_id='cif_detail'`); err != nil || updatedBy != actor.ID {
		t.Fatalf("updated_by=%d error=%v", updatedBy, err)
	}
	if err := service.SetEnabled(context.Background(), "cif_detail", enabled, !enabled, actor.ID); err != ErrConflict {
		t.Fatalf("stale CAS error=%v", err)
	}
}
