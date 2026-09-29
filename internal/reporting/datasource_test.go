package reporting

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDatasourceInputNetworks(t *testing.T) {
	tcp := DatasourceInput{Name: "TCP", Host: "127.0.0.1", Port: 3306, DatabaseName: "dwh", Username: "app", Password: "secret", TLSPolicy: TLSRequired}
	unix := DatasourceInput{Name: "Socket", Network: "unix", SocketPath: "/var/run/mysqld/mysqld.sock", DatabaseName: "dwh", Username: "dwhadmin"}
	for _, test := range []struct {
		name   string
		input  DatasourceInput
		create bool
		change func(*DatasourceInput)
		valid  bool
	}{
		{"legacy TCP", tcp, true, nil, true},
		{"explicit TCP", tcp, true, func(v *DatasourceInput) { v.Network = "tcp" }, true},
		{"TCP create password", tcp, true, func(v *DatasourceInput) { v.Password = "" }, false},
		{"TCP edit preserves password", tcp, false, func(v *DatasourceInput) { v.Password = "" }, true},
		{"TCP host", tcp, true, func(v *DatasourceInput) { v.Host = "" }, false},
		{"TCP port", tcp, true, func(v *DatasourceInput) { v.Port = 0 }, false},
		{"TCP TLS", tcp, true, func(v *DatasourceInput) { v.TLSPolicy = "insecure" }, false},
		{"passwordless Unix", unix, true, nil, true},
		{"Unix ignores TCP", unix, true, func(v *DatasourceInput) { v.Host, v.Port = "stale.example", 3306 }, true},
		{"Unix missing socket", unix, true, func(v *DatasourceInput) { v.SocketPath = " " }, false},
		{"Unix relative socket", unix, true, func(v *DatasourceInput) { v.SocketPath = "mysql.sock" }, false},
		{"Unix NUL socket", unix, true, func(v *DatasourceInput) { v.SocketPath += "\x00" }, false},
		{"Unix database", unix, true, func(v *DatasourceInput) { v.DatabaseName = "" }, false},
		{"Unix username", unix, true, func(v *DatasourceInput) { v.Username = "" }, false},
		{"Unix TLS contradiction", unix, true, func(v *DatasourceInput) { v.TLSPolicy = TLSRequired }, false},
		{"Unix password contradiction", unix, true, func(v *DatasourceInput) { v.Password = "secret" }, false},
		{"unknown network", unix, true, func(v *DatasourceInput) { v.Network = "udp" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			if test.change != nil {
				test.change(&input)
			}
			err := validateDatasourceInput(input, test.create)
			if (err == nil) != test.valid || (err != nil && !errors.Is(err, ErrInvalid)) {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
	unix.Host, unix.Port = "ignored", 3306
	normalized := normalizeDatasourceInput(unix)
	if normalized.Host != "" || normalized.Port != 0 || normalized.TLSPolicy != TLSDisabled {
		t.Fatalf("inactive TCP fields retained: %+v", normalized)
	}
}

func TestDatasourcePasswordModeSwitch(t *testing.T) {
	repository := &Repository{cipher: NewCipher([32]byte{})}
	old, err := repository.cipher.Encrypt(7, "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	existing := Datasource{Network: "tcp", PasswordCiphertext: old}
	preserved, err := repository.datasourcePassword(7, DatasourceInput{Network: "tcp"}, existing)
	if err != nil || !bytes.Equal(preserved, old) {
		t.Fatalf("TCP password changed: %v", err)
	}
	cleared, err := repository.datasourcePassword(7, DatasourceInput{Network: "unix"}, existing)
	if err != nil || cleared != nil {
		t.Fatalf("Unix retained a credential: %v", err)
	}
	existing.Network = "unix"
	if _, err := repository.datasourcePassword(7, DatasourceInput{Network: "tcp"}, existing); !errors.Is(err, ErrInvalid) {
		t.Fatalf("switch reused old TCP password: %v", err)
	}
	updated, err := repository.datasourcePassword(7, DatasourceInput{Network: "tcp", Password: "new-secret"}, existing)
	if err != nil {
		t.Fatal(err)
	}
	password, err := repository.cipher.Decrypt(7, updated)
	if err != nil || password != "new-secret" {
		t.Fatalf("new TCP credential invalid: %v", err)
	}
	// Unix creation needs neither an encryption key nor an encrypted empty string.
	repository.cipher = nil
	if credential, err := repository.datasourcePassword(8, DatasourceInput{Network: "unix"}, Datasource{}); err != nil || credential != nil {
		t.Fatalf("Unix creation stored a credential: %v", err)
	}
}

func TestDatasourceConnectionAudit(t *testing.T) {
	existing := Datasource{Network: "tcp", Host: "127.0.0.1", Port: 3306, DatabaseName: "dwh", Username: "app", TLSPolicy: TLSRequired}
	input := DatasourceInput{Network: "tcp", Host: existing.Host, Port: existing.Port, DatabaseName: existing.DatabaseName, Username: existing.Username, TLSPolicy: existing.TLSPolicy}
	for _, field := range []string{"network", "host", "port", "socket_path", "database_name", "username", "tls_policy", "password", "description"} {
		t.Run(field, func(t *testing.T) {
			changed := input
			switch field {
			case "network":
				changed.Network = "unix"
			case "host":
				changed.Host = "mysql.example.test"
			case "port":
				changed.Port++
			case "socket_path":
				changed.SocketPath = "/tmp/mysql.sock"
			case "database_name":
				changed.DatabaseName = "another"
			case "username":
				changed.Username = "dwhadmin"
			case "tls_policy":
				changed.TLSPolicy = TLSDisabled
			case "password":
				changed.Password = "never-audit-this-secret"
			case "description":
				changed.Description = "new description"
			}
			metadata := datasourceUpdateMetadata(existing, changed)
			wantConnection := field != "password" && field != "description"
			wantCredentials := field == "network" || field == "username" || field == "password"
			if metadata.ConnectionChanged != wantConnection || metadata.CredentialsChanged != wantCredentials {
				t.Fatalf("unexpected audit flags: %+v", metadata)
			}
			encoded, err := json.Marshal(metadata)
			if err != nil || strings.Contains(string(encoded), "never-audit-this-secret") {
				t.Fatalf("unsafe audit metadata: %v", err)
			}
			if wantConnection && (len(metadata.ChangedFields) != 1 || metadata.ChangedFields[0] != field) {
				t.Fatalf("changed field missing: %+v", metadata)
			}
		})
	}
}
