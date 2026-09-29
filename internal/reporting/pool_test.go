package reporting

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func testPoolManager(t *testing.T) *PoolManager {
	t.Helper()
	manager, err := NewPoolManager(NewCipher([32]byte{}), PoolConfig{ConnectTimeout: 5 * time.Second, MySQLMaxPacketBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestPoolMySQLConfigNetworks(t *testing.T) {
	manager := testPoolManager(t)
	ciphertext, err := manager.cipher.Encrypt(7, "p@ss:/?#&")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ network, host, socket, tls, address, password string }{
		{"", "127.0.0.1", "", "required", "127.0.0.1:3307", "p@ss:/?#&"},
		{"tcp", "::1", "", "disabled", "[::1]:3307", "p@ss:/?#&"},
		{"unix", "ignored.example.test", "/var/run/mysqld/mysqld.sock", "disabled", "/var/run/mysqld/mysqld.sock", ""},
	} {
		t.Run(test.network+"/"+test.tls, func(t *testing.T) {
			credential := ciphertext
			if test.network == "unix" {
				credential = []byte("invalid ciphertext must never be decrypted")
			}
			got, err := manager.mysqlConfig(Datasource{ID: 7, Network: test.network, Host: test.host, Port: 3307, SocketPath: test.socket, DatabaseName: "dwh", Username: "dwhadmin", PasswordCiphertext: credential, TLSPolicy: TLSPolicy(test.tls)})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := mysql.ParseDSN(got.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			network := test.network
			if network == "" {
				network = "tcp"
			}
			if parsed.Net != network || parsed.Addr != test.address || parsed.User != "dwhadmin" || parsed.Passwd != test.password || parsed.DBName != "dwh" {
				t.Fatal("incorrect transport or credentials in formatted datasource DSN")
			}
			if !parsed.ParseTime || parsed.Loc != time.UTC || parsed.Timeout != 5*time.Second || parsed.MaxAllowedPacket != 64<<20 || parsed.MultiStatements || parsed.ReadTimeout != 0 || parsed.WriteTimeout != 0 || len(parsed.Params) != 0 {
				t.Fatal("existing datasource MySQL options changed")
			}
			wantTLS := ""
			if test.tls == "required" {
				wantTLS = "true"
			}
			if parsed.TLSConfig != wantTLS {
				t.Fatalf("TLS=%q want %q", parsed.TLSConfig, wantTLS)
			}
		})
	}
	manager.cipher = nil
	if got, err := manager.mysqlConfig(Datasource{Network: "unix", SocketPath: "/tmp/mysql.sock", TLSPolicy: TLSDisabled}); err != nil || got.Passwd != "" || got.TLSConfig != "" {
		t.Fatalf("Unix required credentials or TLS: %v", err)
	}
	if _, err := manager.mysqlConfig(Datasource{Network: "unix", SocketPath: "/tmp/mysql.sock", TLSPolicy: TLSRequired}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Unix TLS contradiction accepted: %v", err)
	}
	if _, err := manager.mysqlConfig(Datasource{Network: "udp"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown network accepted: %v", err)
	}
}

func TestPoolRevisionAndInvalidation(t *testing.T) {
	manager := testPoolManager(t)
	old, connection := fakeDatabase(t, &fakeRawState{})
	_ = connection.Close()
	entry := manager.entry(7)
	entry.database, entry.revision = old, 1
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	current := Datasource{ID: 7, Network: "tcp", Status: StatusActive, Revision: 1}
	if got, err := manager.Database(ctx, current, false); err != nil || got != old {
		t.Fatalf("unchanged revision did not reuse pool: %v", err)
	}
	current.Network, current.TLSPolicy, current.PasswordCiphertext = "unix", TLSDisabled, []byte("old encrypted TCP credential")
	for _, socket := range []string{"first.sock", "second.sock"} {
		current.Revision++
		current.SocketPath = filepath.Join(t.TempDir(), socket)
		got, err := manager.Database(ctx, current, false)
		var networkError *net.OpError
		if got != nil || !errors.As(err, &networkError) || networkError.Net != "unix" {
			t.Fatalf("changed connection reused TCP or decrypted credentials: %v", err)
		}
	}
	manager.Invalidate(7)
	if entry.database != nil || entry.revision != 0 {
		t.Fatal("explicit invalidation kept cached pool")
	}
	current.Status = StatusArchived
	if _, err := manager.Database(ctx, current, true); !errors.Is(err, ErrInactive) {
		t.Fatalf("archived datasource accepted: %v", err)
	}
}
