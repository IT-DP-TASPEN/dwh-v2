//go:build integration

package reporting_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

type collectingSink struct{ rows [][]driver.Value }

func (*collectingSink) Columns([]reporting.Column) error { return nil }
func (sink *collectingSink) Row(row []driver.Value) error {
	sink.rows = append(sink.rows, row)
	return nil
}

func TestBoundedRawMySQLExecutionDiscardsOnlyAbortedConnections(t *testing.T) {
	database := reportDatabase(t)
	engine := reporting.QueryEngine{}
	connectionID := func() int64 {
		sink := &collectingSink{}
		if err := engine.Stream(context.Background(), database, `SELECT CONNECTION_ID()`, nil, map[string]reporting.InputValue{}, sink); err != nil {
			t.Fatal(err)
		}
		return sink.rows[0][0].(int64)
	}

	first := connectionID()
	if second := connectionID(); second != first {
		t.Fatalf("normal EOF replaced reusable connection: %d -> %d", first, second)
	}

	result, err := reporting.RunInteractive(context.Background(), engine, database, `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<100) SELECT n FROM seq`, nil, map[string]reporting.InputValue{}, 10, 1<<20, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.TruncationReason != "row_limit" || len(result.Rows) != 10 {
		t.Fatalf("result=%+v", result)
	}
	afterRows := connectionID()
	if afterRows == first {
		t.Fatal("row-bounded abort did not discard physical connection")
	}

	result, err = reporting.RunInteractive(context.Background(), engine, database, `SELECT REPEAT('x', 10000)`, nil, map[string]reporting.InputValue{}, 10, 4096, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.TruncationReason != "payload_limit" {
		t.Fatalf("payload result=%+v", result)
	}
	afterPayload := connectionID()
	if afterPayload == afterRows {
		t.Fatal("payload-bounded abort did not discard physical connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = engine.Stream(ctx, database, `SELECT SLEEP(5)`, nil, map[string]reporting.InputValue{}, &collectingSink{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow query error=%v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("slow query cancellation took %s", time.Since(started))
	}
	if afterTimeout := connectionID(); afterTimeout == afterPayload {
		t.Fatal("timed-out query did not discard physical connection")
	}

	if err := engine.Stream(context.Background(), database, `SELECT 1; SELECT 2`, nil, map[string]reporting.InputValue{}, &collectingSink{}); err == nil {
		t.Fatal("multiple statements accepted")
	}
}

func reportDatabase(t *testing.T) *sql.DB {
	t.Helper()
	config := integrationdb.Config(t)
	var key [32]byte
	cipher := reporting.NewCipher(key)
	credential, err := cipher.Encrypt(1, config.Password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := reporting.NewPoolManager(cipher, reporting.PoolConfig{ConnectTimeout: 5 * time.Second, MySQLMaxPacketBytes: 64 << 20, MaxOpen: 1, MaxIdle: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	database, err := manager.Database(context.Background(), reporting.Datasource{ID: 1, Host: config.Host, Port: uint16(config.Port), DatabaseName: config.Name, Username: config.User, PasswordCiphertext: credential, TLSPolicy: reporting.TLSDisabled, Status: reporting.StatusActive, Revision: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	return database
}
