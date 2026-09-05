//go:build integration

package customdataset

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestCustomDatasetScale(t *testing.T) {
	if os.Getenv("CUSTOM_DATASET_SCALE") != "1" {
		t.Skip("set CUSTOM_DATASET_SCALE=1 for the destructive 1M-row scale test")
	}
	db := integrationdb.Open(t)
	cleanupDynamicTables(t, db)
	integrationdb.Reset(t, db, nil)
	t.Cleanup(func() { cleanupDynamicTables(t, db) })
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	user := integrationdb.User(t, db, "custom-dataset-scale", role.ID, true)
	requester := integrationdb.Requester(user, role)
	storage, _ := NewStorage(t.TempDir())
	repository, _ := NewRepository(db)
	ddl, _ := NewDDL(db)
	worker, _ := NewWorker(repository, ddl, storage, WorkerConfig{Concurrency: 1, HeartbeatInterval: time.Second, StaleAfter: 10 * time.Second, CleanupInterval: time.Hour, CleanupGrace: 0}, nil)

	sourcePath := writeScaleCSV(t)
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := storage.Save(context.Background(), "scale.csv", source)
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Size < 145_000_000 || stored.Size > MaxUploadBytes {
		t.Fatalf("scale source size=%d", stored.Size)
	}
	upload, err := repository.CreateUpload(context.Background(), requester, stored, integrationdb.Now().Add(time.Hour), integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	columns := make([]Column, 50)
	for index := range columns {
		kind := TypeInteger
		if index == 0 {
			kind = TypeText
		}
		columns[index] = Column{Ordinal: uint16(index + 1), DisplayName: fmt.Sprintf("Column %d", index+1), QueryName: fmt.Sprintf("column_%d", index+1), PhysicalName: fmt.Sprintf("c%03d", index+1), LogicalType: kind}
	}
	dataset, job, err := repository.Submit(context.Background(), requester, Submission{Name: "Scale", UploadID: upload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := repository.Claim(context.Background(), worker.owner)
	if err != nil || claimed == nil {
		t.Fatalf("initial claim=%+v err=%v", claimed, err)
	}
	if err := ddl.Ensure(context.Background(), dataset, columns, claimed.ID); err != nil {
		t.Fatal(err)
	}
	packet, err := repository.MaxAllowedPacket(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := storage.Open(upload.StorageKey)
	batch := newInserter(repository, dataset, columns, *claimed, worker.owner, packet)
	seen := 0
	_, _, _, _, err = Stream(context.Background(), retained, DelimiterComma, 1, columns, func(record uint64, values []any) error {
		if err := batch.Add(context.Background(), record, values); err != nil {
			return err
		}
		seen++
		if seen == 10_000 {
			return context.Canceled
		}
		return nil
	})
	_ = retained.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restart injection error=%v", err)
	}
	if _, err := db.Exec(`UPDATE custom_dataset_imports SET heartbeat_at=TIMESTAMPADD(MINUTE,-1,UTC_TIMESTAMP(6)) WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.RequeueStale(context.Background(), 10*time.Second); err != nil || count != 1 {
		t.Fatalf("requeue=%d err=%v", count, err)
	}

	beforeBinlog := binaryLogStatus(db)
	started := time.Now()
	executeNext(t, repository, worker)
	duration := time.Since(started)
	finished, _ := repository.Find(context.Background(), dataset.ID)
	if finished.RowCount != MaxDataRows {
		t.Fatalf("published rows=%d", finished.RowCount)
	}
	var visible uint64
	if err := db.Get(&visible, `SELECT COUNT(*) FROM `+quoteIdentifier(dataset.ViewName())); err != nil || visible != MaxDataRows {
		t.Fatalf("visible=%d err=%v", visible, err)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	var size struct {
		Data  uint64 `db:"data"`
		Index uint64 `db:"index_bytes"`
	}
	_ = db.Get(&size, `SELECT COALESCE(DATA_LENGTH,0) data,COALESCE(INDEX_LENGTH,0) index_bytes FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, dataset.TableName())
	t.Logf("scale file=%d rows=%d columns=50 huge_record=%d max_allowed_packet=%d duration=%s heap_alloc=%d heap_sys=%d maxrss=%d table_data=%d table_index=%d binlog_before=%s binlog_after=%s", stored.Size, visible, 8<<20, packet, duration, memory.HeapAlloc, memory.HeapSys, usage.Maxrss, size.Data, size.Index, beforeBinlog, binaryLogStatus(db))
}

func writeScaleCSV(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/scale.csv"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 1<<20)
	header := make([]string, 50)
	for index := range header {
		header[index] = fmt.Sprintf("Column %d", index+1)
	}
	_, _ = writer.WriteString(strings.Join(header, ",") + "\n")
	normal, huge := strings.Repeat("x", 42), strings.Repeat("x", 8<<20)
	for row := 0; row < int(MaxDataRows); row++ {
		first := normal
		if row == 0 {
			first = huge
		}
		_, _ = writer.WriteString(first)
		_, _ = writer.WriteString(strings.Repeat(",1", 49))
		_ = writer.WriteByte('\n')
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func binaryLogStatus(db *sqlx.DB) string {
	rows, err := db.Queryx(`SHOW BINARY LOG STATUS`)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	defer rows.Close()
	if !rows.Next() {
		return "disabled"
	}
	values := map[string]any{}
	if err := rows.MapScan(values); err != nil {
		return "unavailable: " + err.Error()
	}
	return fmt.Sprint(values)
}
