//go:build integration

package ingestionexec

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/fincloudauth"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
)

func integrationAuth(t *testing.T, db *sqlx.DB, baseURL, username, password, locationID, roleID string) (*fincloud.SessionCoordinator, *fincloudauth.Repository) {
	t.Helper()
	name := strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())
	roleResult, err := db.Exec(`INSERT INTO roles (name,slug,is_system,created_at,updated_at) VALUES (?,CONCAT('ingestionexec-',UUID()),FALSE,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, name)
	if err != nil {
		t.Fatal(err)
	}
	databaseRoleID, _ := roleResult.LastInsertId()
	userResult, err := db.Exec(`INSERT INTO users (username,name,password_hash,role_id,is_active,created_at,updated_at)
		VALUES (CONCAT('ingestionexec-',UUID()),?,'test',?,TRUE,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, name, databaseRoleID)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	profileResult, err := db.Exec(`INSERT INTO fincloud_auth_profiles
		(name,username,role_id,location_id,password_ciphertext,status,created_by_user_id,updated_by_user_id)
		VALUES (?,?,?,?,NULL,'active',?,?)`, fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), username, roleID, locationID, userID, userID)
	if err != nil {
		t.Fatal(err)
	}
	profileID, _ := profileResult.LastInsertId()
	var key [32]byte
	key[0] = 91
	cipher := secretcrypto.New(key)
	ciphertext, err := cipher.Encrypt(secretcrypto.PurposeFincloudAuthPassword, uint64(profileID), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fincloud_auth_profiles SET password_ciphertext=? WHERE id=?`, ciphertext, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=?`, profileID); err != nil {
		t.Fatal(err)
	}
	repository, err := fincloudauth.NewRepository(db, cipher)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := fincloud.NewSessionCoordinator(fincloud.SessionCoordinatorConfig{BaseURL: baseURL, HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sessions.Close()
		_, _ = db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=NULL WHERE fincloud_auth_profile_id=?`, profileID)
		_, _ = db.Exec(`DELETE FROM fincloud_auth_profiles WHERE id=?`, profileID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, userID)
		_, _ = db.Exec(`DELETE FROM roles WHERE id=?`, databaseRoleID)
	})
	return sessions, repository
}
