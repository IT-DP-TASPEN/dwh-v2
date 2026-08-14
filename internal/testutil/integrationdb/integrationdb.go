//go:build integration

package integrationdb

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
)

func Open(t *testing.T) *sqlx.DB {
	t.Helper()
	root := Root(t)
	databaseConfig := Config(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	db, err := database.OpenMigrations(ctx, databaseConfig)
	if err != nil {
		t.Fatalf("open disposable integration database: %v", err)
	}
	lock, err := db.Connx(ctx)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	var acquired int
	if err := lock.GetContext(ctx, &acquired, `SELECT GET_LOCK('goment-integration-suite', 120)`); err != nil || acquired != 1 {
		lock.Close()
		db.Close()
		t.Fatalf("lock disposable integration database: acquired=%d err=%v", acquired, err)
	}
	t.Cleanup(func() {
		_, _ = lock.ExecContext(context.Background(), `SELECT RELEASE_LOCK('goment-integration-suite')`)
		_ = lock.Close()
		_ = db.Close()
	})
	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db.DB, filepath.Join(root, "migrations")); err != nil {
		t.Fatalf("apply integration migrations: %v", err)
	}
	return db
}

func Config(t *testing.T) config.DatabaseConfig {
	t.Helper()
	return testConfig(t, Root(t))
}

func Reset(t *testing.T, db *sqlx.DB, definitions []access.PermissionDefinition) {
	t.Helper()
	connection, err := db.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS = 0`); err != nil {
		t.Fatal(err)
	}
	defer connection.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS = 1`)
	for _, table := range []string{"audit_logs", "sessions", "role_permissions", "users", "permissions", "roles"} {
		if _, err := connection.ExecContext(context.Background(), `TRUNCATE TABLE `+table); err != nil {
			t.Fatalf("truncate integration table %s: %v", table, err)
		}
	}
	if _, err := connection.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS = 1`); err != nil {
		t.Fatal(err)
	}
	if err := access.Bootstrap(context.Background(), db, definitions, Now()); err != nil {
		t.Fatal(err)
	}
}

func Now() time.Time {
	return time.Date(2026, 8, 9, 12, 0, 0, 123456000, time.UTC)
}

func Role(t *testing.T, db *sqlx.DB, slug string) access.Role {
	t.Helper()
	role, err := access.NewRepository(db).FindRoleBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return role
}

func CustomRole(t *testing.T, db *sqlx.DB, name, slug string) access.Role {
	t.Helper()
	now := Now()
	result, err := db.Exec(`INSERT INTO roles (name, slug, is_system, created_at, updated_at) VALUES (?, ?, FALSE, ?, ?)`, name, slug, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return access.Role{ID: uint64(id), Name: name, Slug: slug, CreatedAt: now, UpdatedAt: now}
}

func User(t *testing.T, db *sqlx.DB, username string, roleID uint64, active bool) user.User {
	t.Helper()
	found, err := user.NewRepository(db).Create(context.Background(), user.CreateParams{
		Username: username, Name: username, PasswordHash: "integration-hash", RoleID: roleID, IsActive: active,
	}, Now())
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func Requester(found user.User, role access.Role) securityctx.Requester {
	identity := securityctx.Identity{UserID: found.ID, Username: found.Username}
	return securityctx.Requester{Actor: identity, Effective: identity, EffectiveRoleID: role.ID, EffectiveRoleSlug: role.Slug}
}

func Session(t *testing.T, repository *auth.SessionRepository, userID uint64, remember bool, token string, now time.Time) auth.Session {
	t.Helper()
	session, err := repository.Create(context.Background(), auth.CreateSessionParams{
		UserID: userID, TokenHash: auth.HashToken(token), RememberMe: remember,
		ExpiresAt: now.Add(12 * time.Hour), LastSeenAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func Root(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration helper")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("locate project root: %v", err)
	}
	return root
}

func testConfig(t *testing.T, root string) config.DatabaseConfig {
	t.Helper()
	keys := []string{"TEST_DB_HOST", "TEST_DB_PORT", "TEST_DB_NAME", "TEST_DB_USER", "TEST_DB_PASSWORD"}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		if !ok {
			t.Fatalf("%s must be explicitly set; integration database must be disposable", key)
		}
		values[key] = value
	}
	if values["TEST_DB_NAME"] == "" {
		t.Fatal("TEST_DB_NAME must not be empty")
	}
	if values["TEST_DB_NAME"] == "dwh2" || values["TEST_DB_NAME"] == "dwh3" {
		t.Fatalf("TEST_DB_NAME %q is reserved; refusing destructive integration tests", values["TEST_DB_NAME"])
	}
	port, err := strconv.Atoi(values["TEST_DB_PORT"])
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("TEST_DB_PORT must be an integer between 1 and 65535")
	}
	if values["TEST_DB_HOST"] == "" || values["TEST_DB_USER"] == "" {
		t.Fatal("TEST_DB_HOST and TEST_DB_USER must not be empty")
	}
	runtime := map[string]string{}
	if environment, err := godotenv.Read(filepath.Join(root, ".env")); err == nil {
		runtime = environment
	}
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_NAME"} {
		if value, ok := os.LookupEnv(key); ok {
			runtime[key] = value
		}
	}
	if values["TEST_DB_HOST"] == runtime["DB_HOST"] && values["TEST_DB_PORT"] == runtime["DB_PORT"] && values["TEST_DB_NAME"] == runtime["DB_NAME"] {
		t.Fatalf("TEST_DB connection matches the normal runtime database; refusing destructive integration tests")
	}
	return config.DatabaseConfig{Host: values["TEST_DB_HOST"], Port: port, Name: values["TEST_DB_NAME"], User: values["TEST_DB_USER"], Password: values["TEST_DB_PASSWORD"]}
}
