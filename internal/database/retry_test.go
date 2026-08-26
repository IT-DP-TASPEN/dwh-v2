package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

type retryState struct {
	sync.Mutex
	begun, committed, rolledBack, execs int
	execErrors                          []error
}

type retryConnector struct{ state *retryState }
type retryDriver struct{}
type retryConn struct{ state *retryState }
type retryTx struct{ state *retryState }

func (connector retryConnector) Connect(context.Context) (driver.Conn, error) {
	return &retryConn{state: connector.state}, nil
}
func (retryConnector) Driver() driver.Driver { return retryDriver{} }
func (retryDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector required")
}
func (connection *retryConn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (connection *retryConn) Close() error                        { return nil }
func (connection *retryConn) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}
func (connection *retryConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	connection.state.Lock()
	connection.state.begun++
	connection.state.Unlock()
	return &retryTx{state: connection.state}, nil
}
func (connection *retryConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.state.Lock()
	defer connection.state.Unlock()
	index := connection.state.execs
	connection.state.execs++
	if index < len(connection.state.execErrors) {
		return nil, connection.state.execErrors[index]
	}
	return driver.RowsAffected(1), nil
}
func (transaction *retryTx) Commit() error {
	transaction.state.Lock()
	transaction.state.committed++
	transaction.state.Unlock()
	return nil
}
func (transaction *retryTx) Rollback() error {
	transaction.state.Lock()
	transaction.state.rolledBack++
	transaction.state.Unlock()
	return nil
}

func retryDB(t *testing.T, state *retryState) *sqlx.DB {
	t.Helper()
	db := sqlx.NewDb(sql.OpenDB(retryConnector{state: state}), "retry-test")
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRetryReplaySafeTxUsesFreshWholeTransactions(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			state := &retryState{}
			db := retryDB(t, state)
			attempts, preparations := 0, 0
			prepared := func() string {
				preparations++
				return "already-fetched-candidate"
			}()
			seen := map[*sqlx.Tx]bool{}
			used, err := RetryReplaySafeTx(context.Background(), db, func(tx *sqlx.Tx) error {
				attempts++
				if prepared != "already-fetched-candidate" {
					t.Fatal("prepared candidate changed")
				}
				if seen[tx] {
					t.Fatal("transaction reused")
				}
				seen[tx] = true
				if attempts < 3 {
					return &mysql.MySQLError{Number: number, Message: "retryable concurrency failure"}
				}
				return nil
			})
			if err != nil || used != 3 || attempts != 3 || preparations != 1 || state.begun != 3 || state.rolledBack != 2 || state.committed != 1 {
				t.Fatalf("used=%d attempts=%d prepared=%d state=%+v error=%v", used, attempts, preparations, state, err)
			}
		})
	}
}

func TestRetryReplaySafeTxRejectsUntypedAndStopsOnCancellation(t *testing.T) {
	state := &retryState{}
	db := retryDB(t, state)
	attempts, err := RetryReplaySafeTx(context.Background(), db, func(*sqlx.Tx) error { return errors.New("deadlock text") })
	if err == nil || attempts != 1 || state.begun != 1 || state.rolledBack != 1 {
		t.Fatalf("attempts=%d state=%+v error=%v", attempts, state, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	state = &retryState{}
	db = retryDB(t, state)
	attempts, err = RetryReplaySafeTx(ctx, db, func(*sqlx.Tx) error {
		cancel()
		return &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 || state.begun != 1 || state.rolledBack != 1 {
		t.Fatalf("attempts=%d state=%+v error=%v", attempts, state, err)
	}
}

func TestRetryReplaySafeExecRetriesTypedConcurrencyOnly(t *testing.T) {
	state := &retryState{execErrors: []error{
		&mysql.MySQLError{Number: 1205, Message: "timeout"},
		&mysql.MySQLError{Number: 1213, Message: "deadlock"},
	}}
	result, attempts, err := RetryReplaySafeExec(context.Background(), retryDB(t, state), "UPDATE replay_safe SET value=1")
	if err != nil || result == nil || attempts != 3 || state.execs != 3 {
		t.Fatalf("attempts=%d execs=%d result=%v error=%v", attempts, state.execs, result, err)
	}
}
