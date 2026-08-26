package reporting

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

var registerBoundedDriver sync.Once
var boundedState atomic.Pointer[fakeRawState]

type fakeRawState struct {
	rowsClosed, connectionsClosed atomic.Int32
	rows                          int
	nextResult                    bool
}
type fakeDriver struct{}
type fakeConn struct{ state *fakeRawState }
type fakeStmt struct{ state *fakeRawState }
type fakeRows struct {
	state *fakeRawState
	read  int
}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return &fakeConn{state: boundedState.Load()}, nil
}
func (connection *fakeConn) Prepare(string) (driver.Stmt, error) {
	return &fakeStmt{state: connection.state}, nil
}
func (connection *fakeConn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	return &fakeStmt{state: connection.state}, nil
}
func (connection *fakeConn) Close() error                    { connection.state.connectionsClosed.Add(1); return nil }
func (*fakeConn) Begin() (driver.Tx, error)                  { return nil, errors.New("not supported") }
func (*fakeStmt) Close() error                               { return nil }
func (*fakeStmt) NumInput() int                              { return -1 }
func (*fakeStmt) Exec([]driver.Value) (driver.Result, error) { return nil, errors.New("not supported") }
func (statement *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return &fakeRows{state: statement.state}, nil
}
func (statement *fakeStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{state: statement.state}, nil
}
func (*fakeRows) Columns() []string { return []string{"value"} }
func (rows *fakeRows) Close() error { rows.state.rowsClosed.Add(1); return nil }
func (rows *fakeRows) Next(destination []driver.Value) error {
	if rows.read >= rows.state.rows {
		return io.EOF
	}
	rows.read++
	destination[0] = int64(rows.read)
	return nil
}
func (rows *fakeRows) HasNextResultSet() bool { return rows.state.nextResult }
func (rows *fakeRows) NextResultSet() error {
	rows.state.nextResult = false
	return nil
}

type testSink struct {
	stop error
	rows int
}

func (*testSink) Columns([]Column) error        { return nil }
func (sink *testSink) Row([]driver.Value) error { sink.rows++; return sink.stop }

func TestRawBoundedAbortDoesNotCloseAndDrainRows(t *testing.T) {
	state := &fakeRawState{rows: 100}
	database, connection := fakeDatabase(t, state)
	stop := errors.New("bounded stop")
	err := streamRaw(context.Background(), connection, "SELECT value", nil, &testSink{stop: stop})
	if !errors.Is(err, stop) {
		t.Fatalf("error=%v", err)
	}
	if state.rowsClosed.Load() != 0 {
		t.Fatalf("driver rows Close called %d times", state.rowsClosed.Load())
	}
	if state.connectionsClosed.Load() != 1 {
		t.Fatalf("physical connections closed=%d", state.connectionsClosed.Load())
	}
	_ = database.Close()
}

func TestRawEOFClosesRowsAndKeepsConnectionReusable(t *testing.T) {
	state := &fakeRawState{rows: 2}
	database, connection := fakeDatabase(t, state)
	sink := &testSink{}
	if err := streamRaw(context.Background(), connection, "SELECT value", nil, sink); err != nil {
		t.Fatal(err)
	}
	if sink.rows != 2 || state.rowsClosed.Load() != 1 || state.connectionsClosed.Load() != 0 {
		t.Fatalf("rows=%d rows_close=%d conn_close=%d", sink.rows, state.rowsClosed.Load(), state.connectionsClosed.Load())
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if state.connectionsClosed.Load() != 1 {
		t.Fatalf("connection was not returned and closed normally")
	}
}

func TestRawRejectsSecondResultSet(t *testing.T) {
	state := &fakeRawState{rows: 1, nextResult: true}
	database, connection := fakeDatabase(t, state)
	err := streamRaw(context.Background(), connection, "CALL report()", nil, &testSink{})
	if !errors.Is(err, ErrMultipleResultSets) {
		t.Fatalf("error=%v", err)
	}
	_ = connection.Close()
	_ = database.Close()
}

func fakeDatabase(t *testing.T, state *fakeRawState) (*sql.DB, *sql.Conn) {
	t.Helper()
	registerBoundedDriver.Do(func() { sql.Register("reporting-bounded-test", fakeDriver{}) })
	boundedState.Store(state)
	database, err := sql.Open("reporting-bounded-test", "")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return database, connection
}

func TestInteractiveSinkBoundsPayloadRowsAndCellPreviews(t *testing.T) {
	sink := &interactiveSink{maxRows: 1, payloadCap: 4096, cellPreview: 4, approximateSize: 1024}
	if err := sink.Columns([]Column{{Name: "text", DatabaseType: "TEXT"}}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Row([]driver.Value{[]byte("ééé")}); err != nil {
		t.Fatal(err)
	}
	if got := sink.result.Rows[0][0]; got.Text != "éé" || !got.Preview || got.OriginalBytes != 6 {
		t.Fatalf("cell=%+v", got)
	}
	if err := sink.Row([]driver.Value{[]byte("next")}); !errors.Is(err, errInteractiveBound) || sink.result.TruncationReason != "row_limit" {
		t.Fatalf("error=%v result=%+v", err, sink.result)
	}
	if err := sink.fit(); err != nil || int64(sink.result.EncodedBytes) > sink.payloadCap {
		t.Fatalf("fit error=%v bytes=%d", err, sink.result.EncodedBytes)
	}
}
