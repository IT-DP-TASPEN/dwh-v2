//go:build integration

package dwhschema

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestDatasourceSocketMigrationUpgradeAndRollback(t *testing.T) {
	db := integrationdb.Open(t)
	const table = "report_datasources_socket_migration_test"
	if _, err := db.Exec(`CREATE TABLE ` + table + ` (id INT PRIMARY KEY,host VARCHAR(255) NOT NULL,port SMALLINT UNSIGNED NOT NULL,password_ciphertext BLOB NULL,tls_policy VARCHAR(16) NOT NULL,CONSTRAINT chk_report_datasources_socket_migration_test_port CHECK (port BETWEEN 1 AND 65535)) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE ` + table) })
	cipher := reporting.NewCipher([32]byte{})
	credential, err := cipher.Encrypt(1, "legacy-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO `+table+` VALUES (1,'127.0.0.1',3306,?,'required')`, credential); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(integrationdb.Root(t), "migrations", "20260929120000_add_report_datasource_unix_socket.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down, found := strings.Cut(string(data), "-- +goose Down")
	if !found {
		t.Fatal("missing rollback")
	}
	up, down = strings.ReplaceAll(up, "report_datasources", table), strings.ReplaceAll(down, "report_datasources", table)
	if _, err := db.Exec(up); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var network, socket string
	var stored []byte
	if err := db.QueryRow(`SELECT network,socket_path,password_ciphertext FROM `+table+` WHERE id=1`).Scan(&network, &socket, &stored); err != nil {
		t.Fatal(err)
	}
	if network != "tcp" || socket != "" || !bytes.Equal(stored, credential) {
		t.Fatal("upgrade altered legacy TCP record or ciphertext")
	}
	if password, err := cipher.Decrypt(1, stored); err != nil || password != "legacy-password" {
		t.Fatalf("legacy ciphertext invalid after upgrade: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ` + table + ` (id,host,port,password_ciphertext,tls_policy,network,socket_path) VALUES (2,'',0,NULL,'disabled','unix','/tmp/mysql.sock')`); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"tls_policy='required'", "password_ciphertext=X'01'", "socket_path=''", "port=3306", "network='udp'"} {
		if _, err := db.Exec(`UPDATE ` + table + ` SET ` + change + ` WHERE id=2`); err == nil {
			t.Fatalf("contradictory Unix row accepted: %s", change)
		}
	}
	if _, err := db.Exec(down); err == nil {
		t.Fatal("rollback discarded Unix configuration")
	}
	if err := db.QueryRow(`SELECT network,socket_path FROM `+table+` WHERE id=2`).Scan(&network, &socket); err != nil || network != "unix" || socket != "/tmp/mysql.sock" {
		t.Fatalf("failed rollback altered Unix row: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM ` + table + ` WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(down); err != nil {
		t.Fatalf("TCP-only rollback: %v", err)
	}
	if err := db.QueryRow(`SELECT password_ciphertext FROM ` + table + ` WHERE id=1`).Scan(&stored); err != nil || !bytes.Equal(stored, credential) {
		t.Fatalf("rollback changed TCP credential: %v", err)
	}
}
