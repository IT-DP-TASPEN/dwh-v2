package database

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/config"
)

func TestMySQLConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  config.DatabaseConfig
		network string
		address string
	}{
		{"legacy TCP", config.DatabaseConfig{Host: "127.0.0.1", Port: 3306, Name: "dwh", User: "app", Password: "secret"}, "tcp", "127.0.0.1:3306"},
		{"explicit TCP", config.DatabaseConfig{Network: "tcp", Host: "mysql.example.test", Port: 3307, Socket: "/ignored.sock", Name: "dwh", User: "app", Password: "secret"}, "tcp", "mysql.example.test:3307"},
		{"IPv6 TCP", config.DatabaseConfig{Network: "tcp", Host: "::1", Port: 3306, Name: "dwh", User: "app", Password: "p@ss:/?#&"}, "tcp", "[::1]:3306"},
		{"Unix socket", config.DatabaseConfig{Network: "unix", Socket: "/var/run/mysqld/mysqld.sock", Name: "dwh", User: "dwhadmin"}, "unix", "/var/run/mysqld/mysqld.sock"},
		{"Unix ignores TCP", config.DatabaseConfig{Network: "unix", Host: "ignored.example.test", Port: 3306, Socket: "/var/run/mysqld/mysqld.sock", Name: "dwh", User: "dwhadmin"}, "unix", "/var/run/mysqld/mysqld.sock"},
		{"Unix password", config.DatabaseConfig{Network: "unix", Socket: "/var/run/mysqld/mysqld.sock", Name: "dwh/archive", User: "app", Password: "secret"}, "unix", "/var/run/mysqld/mysqld.sock"},
	}
	for _, test := range tests {
		for _, migrations := range []bool{false, true} {
			name := test.name + "/runtime"
			if migrations {
				name = test.name + "/migrations"
			}
			t.Run(name, func(t *testing.T) {
				got, err := mysqlConfig(test.config, migrations)
				if err != nil {
					t.Fatal(err)
				}
				if got.Net != test.network || got.Addr != test.address {
					t.Fatalf("network/address = %s/%s, want %s/%s", got.Net, got.Addr, test.network, test.address)
				}
				parsed, err := mysql.ParseDSN(got.FormatDSN())
				if err != nil {
					t.Fatal(err)
				}
				// The previous TCP DSN defines the options both modes must preserve.
				want, err := mysql.ParseDSN("app:secret@tcp(127.0.0.1:3306)/dwh?collation=utf8mb4_unicode_ci&parseTime=true&maxAllowedPacket=0&time_zone=%27%2B00%3A00%27")
				if err != nil {
					t.Fatal(err)
				}
				want.Net, want.Addr = test.network, test.address
				want.User, want.Passwd, want.DBName = test.config.User, test.config.Password, test.config.Name
				want.MultiStatements = migrations
				if !reflect.DeepEqual(parsed, want) {
					t.Fatal("formatted DSN changed connection settings or existing MySQL options")
				}
				if test.config.Password == "" && !strings.HasPrefix(got.FormatDSN(), test.config.User+"@unix("+test.address+")/") {
					t.Fatal("passwordless Unix DSN has unexpected credentials or address")
				}
			})
		}
	}
}

func TestMySQLConfigRejectsInvalidNetwork(t *testing.T) {
	for _, test := range []struct{ network, want string }{{"udp", "DB_NETWORK"}, {"unix", "DB_SOCKET"}} {
		if _, err := mysqlConfig(config.DatabaseConfig{Network: test.network}, false); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("network=%q error=%v want %q", test.network, err, test.want)
		}
	}
}

func TestOpenUnixMissingSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "missing.sock")
	for name, openDatabase := range map[string]func(context.Context, config.DatabaseConfig) (*sqlx.DB, error){
		"runtime": Open, "migrations": OpenMigrations,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			db, err := openDatabase(ctx, config.DatabaseConfig{Network: "unix", Socket: socket, Host: "127.0.0.1", Port: 3306, Name: "dwh", User: "dwhadmin"})
			if db != nil {
				_ = db.Close()
				t.Fatal("missing socket unexpectedly returned a database")
			}
			var networkError *net.OpError
			if !errors.As(err, &networkError) || networkError.Net != "unix" || !strings.Contains(err.Error(), "ping mysql at "+socket) {
				t.Fatalf("error=%v, want Unix socket failure without TCP fallback", err)
			}
		})
	}
}
