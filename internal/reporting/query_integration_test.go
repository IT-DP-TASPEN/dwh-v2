//go:build integration

package reporting_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestMySQLReadOnlyPolicyAndTransaction(t *testing.T) {
	config := integrationdb.Config(t)
	setup, err := database.OpenMigrations(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = setup.Close() })
	for _, statement := range []string{
		`DROP FUNCTION IF EXISTS report_security_write_probe`,
		`DROP TABLE IF EXISTS report_security_probe`,
		`CREATE TABLE report_security_probe (id INT PRIMARY KEY, value INT NOT NULL) ENGINE=InnoDB`,
		`INSERT INTO report_security_probe VALUES (1,10)`,
		`CREATE FUNCTION report_security_write_probe() RETURNS INT MODIFIES SQL DATA BEGIN UPDATE report_security_probe SET value=99 WHERE id=1; RETURN 1; END`,
	} {
		if _, err := setup.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = setup.Exec(`DROP FUNCTION IF EXISTS report_security_write_probe`)
		_, _ = setup.Exec(`DROP TABLE IF EXISTS report_security_probe`)
	})
	db := reportDatabase(t)
	engine := reporting.QueryEngine{}
	sink := &collectingSink{}
	if err := engine.Stream(context.Background(), db, `SELECT value FROM report_security_probe`, nil, nil, sink); err != nil || len(sink.rows) != 1 || sink.rows[0][0] != int64(10) {
		t.Fatalf("valid SELECT: rows=%v err=%v", sink.rows, err)
	}
	for _, unsafe := range []string{`INSERT INTO report_security_probe VALUES(2,20)`, `UPDATE report_security_probe SET value=20`, `DELETE FROM report_security_probe`, "SELECT `GET_LOCK`('report_security_probe_lock',0)"} {
		if err := engine.Stream(context.Background(), db, unsafe, nil, nil, &collectingSink{}); !errors.Is(err, reporting.ErrInvalid) {
			t.Fatalf("unsafe SQL %q: %v", unsafe, err)
		}
	}
	// Observe the current physical connection's transaction, not its default
	// session variable (which does not describe START TRANSACTION READ ONLY).
	sink = &collectingSink{}
	if err := engine.Stream(context.Background(), db, `SELECT tx.ACCESS_MODE FROM performance_schema.events_transactions_current tx JOIN performance_schema.threads th ON th.THREAD_ID=tx.THREAD_ID WHERE th.PROCESSLIST_ID=CONNECTION_ID() AND tx.STATE='ACTIVE'`, nil, nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows) != 1 || string(sink.rows[0][0].([]byte)) != "READ ONLY" {
		t.Fatalf("transaction=%v", sink.rows)
	}
	// A routine hides writes behind a lexically valid SELECT. MySQL must block
	// it even though this disposable account intentionally has write privileges.
	if err := engine.Stream(context.Background(), db, `SELECT report_security_write_probe()`, nil, nil, &collectingSink{}); err == nil {
		t.Fatal("routine wrote through READ ONLY transaction")
	}
	var count, value int
	if err := setup.QueryRow(`SELECT COUNT(*),MAX(value) FROM report_security_probe`).Scan(&count, &value); err != nil || count != 1 || value != 10 {
		t.Fatalf("data modified: count=%d value=%d err=%v", count, value, err)
	}
	// Normal EOF must leave no active transaction on the reused connection.
	sink = &collectingSink{}
	if err := engine.Stream(context.Background(), db, `SELECT CONNECTION_ID()`, nil, nil, sink); err != nil {
		t.Fatal(err)
	}
	id := sink.rows[0][0].(int64)
	var reused int64
	if err := db.QueryRow(`SELECT CONNECTION_ID()`).Scan(&reused); err != nil || reused != id {
		t.Fatalf("connection not reusable: %d %d %v", id, reused, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM performance_schema.events_transactions_current tx JOIN performance_schema.threads th ON th.THREAD_ID=tx.THREAD_ID WHERE th.PROCESSLIST_ID=CONNECTION_ID() AND tx.STATE='ACTIVE'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dirty transaction count=%d err=%v", count, err)
	}
}

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

func TestDynamicOptionMySQLContractAndBoundedAbort(t *testing.T) {
	database := reportDatabase(t)
	engine := reporting.QueryEngine{}
	options, err := reporting.RunDynamicOptions(context.Background(), engine, database, `SELECT '001' AS value,'First' AS label UNION ALL SELECT '000','Zero'`, nil, map[string]reporting.NormalizedValue{}, 1000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].Value != "001" || options[1].Value != "000" {
		t.Fatalf("options=%+v", options)
	}
	empty, err := reporting.RunDynamicOptions(context.Background(), engine, database, `SELECT 'x' AS value,'X' AS label WHERE FALSE`, nil, map[string]reporting.NormalizedValue{}, 1000, 1<<20)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%+v error=%v", empty, err)
	}
	connectionID := func() int64 {
		sink := &collectingSink{}
		if err := engine.Stream(context.Background(), database, `SELECT CONNECTION_ID()`, nil, map[string]reporting.InputValue{}, sink); err != nil {
			t.Fatal(err)
		}
		return sink.rows[0][0].(int64)
	}
	before := connectionID()
	_, err = reporting.RunDynamicOptions(context.Background(), engine, database, `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<100) SELECT CAST(n AS CHAR) AS value,CAST(n AS CHAR) AS label FROM seq`, nil, map[string]reporting.NormalizedValue{}, 10, 1<<20)
	if !errors.Is(err, reporting.ErrInvalid) {
		t.Fatalf("row bound error=%v", err)
	}
	if after := connectionID(); after == before {
		t.Fatal("dynamic option row bound did not discard physical connection")
	}
	before = connectionID()
	_, err = reporting.RunDynamicOptions(context.Background(), engine, database, `SELECT 'x' AS value,REPEAT('x', 10000) AS label`, nil, map[string]reporting.NormalizedValue{}, 1000, 4096)
	if !errors.Is(err, reporting.ErrInvalid) {
		t.Fatalf("payload bound error=%v", err)
	}
	if after := connectionID(); after == before {
		t.Fatal("dynamic option payload bound did not discard physical connection")
	}
}

func TestOptionalBlockValidationUsesSessionSQLModeAndPreparesShapes(t *testing.T) {
	database := reportDatabase(t)
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`SET SESSION sql_mode='ANSI_QUOTES,NO_BACKSLASH_ESCAPES'`); err != nil {
		t.Fatal(err)
	}
	engine := reporting.QueryEngine{}
	parameters := []reporting.Parameter{{Key: "optional", Label: "Optional", Type: reporting.ParameterInteger}}
	statement := "SELECT 'backslash\\' AS \"value[[ :fake_double ]]\", 1 AS `tick[[ :fake_tick ]]`\n" +
		"-- [[ :fake_dash ]]\n# [[ :fake_hash ]]\n/* [[ :fake_block ]] */\n" +
		"WHERE 1=1 [[ AND :optional=0 ]]"
	if err := engine.ValidateTemplate(context.Background(), database, statement, parameters); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]map[string]reporting.InputValue{
		"omitted":  {"optional": {Present: true}},
		"zero":     {"optional": {Present: true, Values: []string{"0"}}},
		"non-zero": {"optional": {Present: true, Values: []string{"1"}}},
	} {
		sink := &collectingSink{}
		if err := engine.Stream(context.Background(), database, statement, parameters, input, sink); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantRows := 1
		if name == "non-zero" {
			wantRows = 0
		}
		if len(sink.rows) != wantRows {
			t.Fatalf("%s rows=%d", name, len(sink.rows))
		}
	}

	for _, invalid := range []string{
		`SELECT 1 WHERE [[ :optional=1 ]]`,
		`SELECT 1 [[ BROKEN :optional ]]`,
	} {
		if err := engine.ValidateTemplate(context.Background(), database, invalid, parameters); !errors.Is(err, reporting.ErrInvalid) {
			t.Fatalf("invalid shape accepted: %q error=%v", invalid, err)
		}
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
