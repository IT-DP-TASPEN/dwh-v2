package ingestionstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/ibldzn/go-admin/internal/ingestiondiag"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/shopspring/decimal"
)

func TestDecimalStringRejectsRounding(t *testing.T) {
	for _, test := range []struct {
		value            string
		precision, scale int
		valid            bool
	}{
		{"1234567890.123456", 24, 6, true},
		{"123456789012345678.123456", 24, 6, true},
		{"1234567890123456789.1", 24, 6, false},
		{"1.1234567", 24, 6, false},
		{"12.34", 20, 2, true},
		{"12.345", 20, 2, false},
	} {
		_, err := decimalString(decimal.RequireFromString(test.value), test.precision, test.scale)
		if (err == nil) != test.valid {
			t.Fatalf("decimal %s valid=%v error=%v", test.value, test.valid, err)
		}
	}
}

func TestQuoteIdentifier(t *testing.T) {
	if _, err := quoteIdentifier("safe_name"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "Bad", "x` DROP TABLE users", "1bad"} {
		if _, err := quoteIdentifier(value); err == nil {
			t.Fatalf("unsafe identifier %q accepted", value)
		}
	}
}

func TestRetryTransactionUsesTyped1213Only(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		attempts int32
	}{
		{"deadlock", &mysql.MySQLError{Number: 1213, Message: "deadlock"}, 3},
		{"wrapped deadlock", errors.Join(errors.New("transaction failed"), &mysql.MySQLError{Number: 1213, Message: "deadlock"}), 3},
		{"lock timeout", &mysql.MySQLError{Number: 1205, Message: "timeout"}, 1},
		{"text", errors.New("deadlock found when trying to get lock"), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			err := retryTransaction(context.Background(), "test", func() error {
				attempts.Add(1)
				return test.err
			})
			if err == nil || attempts.Load() != test.attempts {
				t.Fatalf("attempts=%d error=%v", attempts.Load(), err)
			}
		})
	}

	var attempts atomic.Int32
	err := retryTransaction(context.Background(), "test", func() error {
		if attempts.Add(1) == 1 {
			return &mysql.MySQLError{Number: 1213, Message: "deadlock"}
		}
		return nil
	})
	if err != nil || attempts.Load() != 2 {
		t.Fatalf("recovered attempts=%d error=%v", attempts.Load(), err)
	}
}

func TestRetryTransactionDoesNotRepeatPreparation(t *testing.T) {
	var preparations atomic.Int32
	prepared := func() string {
		preparations.Add(1)
		return "fetched-and-mapped"
	}()
	var attempts atomic.Int32
	err := retryTransaction(context.Background(), "test", func() error {
		if prepared != "fetched-and-mapped" {
			t.Fatal("prepared input changed")
		}
		if attempts.Add(1) == 1 {
			return &mysql.MySQLError{Number: 1213, Message: "deadlock"}
		}
		return nil
	})
	if err != nil || preparations.Load() != 1 || attempts.Load() != 2 {
		t.Fatalf("preparations=%d attempts=%d error=%v", preparations.Load(), attempts.Load(), err)
	}
}

func TestRetryTransactionStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var attempts atomic.Int32
	err := retryTransaction(ctx, "test", func() error {
		attempts.Add(1)
		cancel()
		return &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	})
	if !errors.Is(err, context.Canceled) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d error=%v", attempts.Load(), err)
	}
}

func TestRetryDiagnosticsPreserveMySQLMetadataAndRecovery(t *testing.T) {
	var events []ingestionrun.TechnicalEvent
	ctx := ingestiondiag.WithRecorder(context.Background(), func(_ context.Context, event ingestionrun.TechnicalEvent, aggregate bool) {
		if !aggregate {
			t.Error("retry event was not aggregated")
		}
		events = append(events, event)
	}, 9, "saving_detail")
	ctx = ingestiondiag.WithScope(ctx, ingestiondiag.Scope{Class: "persistence", Step: "persist_detail", Operation: "replace_saving_snapshot", ItemIdentifier: "safe-item"})
	var attempts int
	err := retryTransaction(ctx, "replace_saving_snapshot", func() error {
		attempts++
		if attempts == 1 {
			mysqlError := &mysql.MySQLError{Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}, Message: "Deadlock found when trying to get lock; try restarting transaction"}
			return &DatabaseError{Operation: "insert_child_rows", QueryName: "replace_saving_mutations", Table: "fincloud_saving_mutations", BatchStart: 1, BatchEnd: 61, Cause: mysqlError}
		}
		return nil
	})
	if err != nil || attempts != 2 || len(events) != 2 || events[0].EventKind != "retry" || events[1].EventKind != "recovery" || events[1].Recovered == nil || !*events[1].Recovered {
		t.Fatalf("attempts=%d events=%+v error=%v", attempts, events, err)
	}
	var details struct {
		Database DatabaseDiagnostic `json:"database"`
	}
	if err := json.Unmarshal(events[0].Details, &details); err != nil || details.Database.MySQLNumber != 1213 || details.Database.SQLState != "40001" || details.Database.Table != "fincloud_saving_mutations" || details.Database.TxAttempt != 1 {
		t.Fatalf("details=%+v error=%v", details, err)
	}
}
