//go:build integration

package reporting_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/config"
	appdatabase "github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDatasourceConnectionModePersistence(t *testing.T) {
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, db, "Datasource modes", "datasource-modes")
	owner := integrationdb.User(t, db, "datasource-modes", role.ID, true)
	requester, connection := integrationdb.Requester(owner, role), integrationdb.Config(t)
	cipher := reporting.NewCipher([32]byte{})
	repository, err := reporting.NewRepository(db, cipher)
	if err != nil {
		t.Fatal(err)
	}
	input := reporting.DatasourceInput{Name: "Modes", Host: connection.Host, Port: uint16(connection.Port), DatabaseName: connection.Name, Username: connection.User, Password: connection.Password, TLSPolicy: reporting.TLSDisabled}
	tcp, err := repository.CreateDatasource(context.Background(), requester, input, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if tcp.Network != "tcp" || tcp.SocketPath != "" {
		t.Fatal("legacy datasource did not default to TCP")
	}
	password, err := cipher.Decrypt(tcp.ID, tcp.PasswordCiphertext)
	if err != nil || password != connection.Password {
		t.Fatalf("legacy credential changed: %v", err)
	}
	input.Password = ""
	preserved, err := repository.UpdateDatasource(context.Background(), requester, tcp.ID, tcp.Revision, input, integrationdb.Now())
	if err != nil || !bytes.Equal(preserved.PasswordCiphertext, tcp.PasswordCiphertext) {
		t.Fatalf("TCP edit lost credential: %v", err)
	}
	input.Network, input.SocketPath = "unix", filepath.Join(t.TempDir(), "missing.sock")
	unix, err := repository.UpdateDatasource(context.Background(), requester, tcp.ID, preserved.Revision, input, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if unix.Network != "unix" || unix.Host != "" || unix.Port != 0 || unix.PasswordCiphertext != nil || unix.TLSPolicy != reporting.TLSDisabled {
		t.Fatal("Unix retained TCP connection state")
	}
	unixCreate := input
	unixCreate.Name = "No password"
	created, err := repository.CreateDatasource(context.Background(), requester, unixCreate, integrationdb.Now())
	if err != nil || created.PasswordCiphertext != nil {
		t.Fatalf("Unix creation stored a credential: %v", err)
	}
	input.Network = "tcp"
	if _, err := repository.UpdateDatasource(context.Background(), requester, tcp.ID, unix.Revision, input, integrationdb.Now()); !errors.Is(err, reporting.ErrInvalid) {
		t.Fatalf("Unix to TCP accepted no password: %v", err)
	}
	unchanged, err := repository.FindDatasource(context.Background(), tcp.ID)
	if err != nil || unchanged.Network != "unix" || unchanged.Revision != unix.Revision {
		t.Fatalf("failed switch changed datasource: %v", err)
	}
	pools, err := reporting.NewPoolManager(cipher, reporting.PoolConfig{ConnectTimeout: time.Second, MySQLMaxPacketBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pools.Close() })
	service, err := reporting.NewService(repository, pools, reporting.ServiceConfig{ConnectTimeout: time.Second, InteractiveTimeout: time.Second, InteractiveMaxRows: 10, InteractivePayloadBytes: 4096, DynamicOptionMaxRows: 10, DynamicOptionPayloadBytes: 4096, CellPreviewBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	err = service.TestDatasource(context.Background(), requester, unix.ID)
	var socketError *net.OpError
	if !errors.As(err, &socketError) || socketError.Net != "unix" {
		t.Fatalf("TestDatasource did not use Unix builder: %v", err)
	}
	input.Password = connection.Password
	back, err := repository.UpdateDatasource(context.Background(), requester, tcp.ID, unix.Revision, input, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if back.Network != "tcp" || back.SocketPath != "" || back.PasswordCiphertext == nil {
		t.Fatal("TCP switch did not replace connection state")
	}
	if err := service.TestDatasource(context.Background(), requester, back.ID); err != nil {
		t.Fatalf("restored TCP TestDatasource: %v", err)
	}
	var updates [][]byte
	if err := db.Select(&updates, `SELECT metadata FROM audit_logs WHERE action=? AND resource_id=? ORDER BY id`, audit.ActionReportDatasourceUpdated, tcp.ID); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 3 {
		t.Fatalf("mode switch audit incomplete: %s", updates)
	}
	for index, metadata := range updates {
		var decoded audit.DatasourceUpdatedMetadata
		if err := json.Unmarshal(metadata, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.CredentialsChanged != (index > 0) || decoded.ConnectionChanged != (index > 0) || (index > 0 && !slices.Contains(decoded.ChangedFields, "network")) {
			t.Fatalf("mode switch audit incomplete: %+v", decoded)
		}
		if connection.Password != "" && bytes.Contains(metadata, []byte(connection.Password)) {
			t.Fatal("audit disclosed a password")
		}
	}
}

func TestUnixDatasourceUsesProcessIdentity(t *testing.T) {
	socket := os.Getenv("TEST_DB_SOCKET")
	if socket == "" {
		t.Skip("TEST_DB_SOCKET is required for real Unix/auth_socket validation")
	}
	identity, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, app.PermissionDefinitions())
	role := integrationdb.CustomRole(t, db, "Socket identity", "socket-identity")
	owner := integrationdb.User(t, db, "socket-identity", role.ID, true)
	requester, connection := integrationdb.Requester(owner, role), integrationdb.Config(t)
	for key, value := range map[string]string{"APP_ENV": "production", "APP_URL": "https://dwh.example.test", "SESSION_SECURE": "true", "ALLOW_REGISTRATION": "false", "DB_NETWORK": "unix", "DB_SOCKET": socket, "DB_NAME": connection.Name, "DB_USER": identity.Username, "DB_PASSWORD": "", "DB_HOST": "", "DB_PORT": "ignored"} {
		t.Setenv(key, value)
	}
	primaryConfig, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	primary, err := appdatabase.Open(ctx, primaryConfig.Database)
	if err != nil {
		t.Fatalf("passwordless production primary connection: %v", err)
	}
	defer primary.Close()
	cipher := reporting.NewCipher([32]byte{})
	repository, err := reporting.NewRepository(db, cipher)
	if err != nil {
		t.Fatal(err)
	}
	datasource, err := repository.CreateDatasource(ctx, requester, reporting.DatasourceInput{Name: "Actual socket identity", Network: "unix", SocketPath: socket, DatabaseName: connection.Name, Username: identity.Username}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	pools, err := reporting.NewPoolManager(cipher, reporting.PoolConfig{ConnectTimeout: time.Second, MySQLMaxPacketBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	service, err := reporting.NewService(repository, pools, reporting.ServiceConfig{ConnectTimeout: time.Second, InteractiveTimeout: time.Second, InteractiveMaxRows: 10, InteractivePayloadBytes: 4096, DynamicOptionMaxRows: 10, DynamicOptionPayloadBytes: 4096, CellPreviewBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TestDatasource(ctx, requester, datasource.ID); err != nil {
		t.Fatalf("passwordless Unix TestDatasource: %v", err)
	}
	if err := service.SetDatasourceStatus(ctx, requester, datasource.ID, datasource.Revision, reporting.StatusActive); err != nil {
		t.Fatal(err)
	}
	datasource, err = repository.FindDatasource(ctx, datasource.ID)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pools.Database(ctx, datasource, false)
	if err != nil {
		t.Fatal(err)
	}
	var primaryUser, datasourceUser string
	if err := primary.GetContext(ctx, &primaryUser, `SELECT CURRENT_USER()`); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&datasourceUser); err != nil {
		t.Fatal(err)
	}
	if primaryUser != datasourceUser || !strings.HasPrefix(datasourceUser, identity.Username+"@") {
		t.Fatal("connections did not authenticate as process identity")
	}
	if mismatch := os.Getenv("TEST_DB_SOCKET_MISMATCH_USER"); mismatch != "" {
		datasource.Username, datasource.Revision = mismatch, datasource.Revision+1
		unexpected, err := pools.Database(ctx, datasource, false)
		var authenticationError *mysql.MySQLError
		if unexpected != nil || !errors.As(err, &authenticationError) || string(authenticationError.SQLState[:]) != "28000" {
			t.Fatal("mismatched auth_socket identity unexpectedly authenticated")
		}
	}
}
