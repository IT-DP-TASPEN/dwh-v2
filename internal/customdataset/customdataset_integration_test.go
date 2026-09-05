//go:build integration

package customdataset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestCustomDatasetPublicationProvenanceAndRetiredGenerationCleanup(t *testing.T) {
	db := integrationdb.Open(t)
	cleanupDynamicTables(t, db)
	integrationdb.Reset(t, db, nil)
	t.Cleanup(func() { cleanupDynamicTables(t, db) })
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	user := integrationdb.User(t, db, "custom-dataset-owner", role.ID, true)
	requester := integrationdb.Requester(user, role)
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	ddl, _ := NewDDL(db)
	worker, err := NewWorker(repository, ddl, storage, WorkerConfig{Concurrency: 1, HeartbeatInterval: 50 * time.Millisecond, StaleAfter: time.Second, CleanupInterval: time.Hour, CleanupGrace: 0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	columns := []Column{{Ordinal: 1, DisplayName: "Name", QueryName: "name", PhysicalName: "c001", LogicalType: TypeText}, {Ordinal: 2, DisplayName: "Amount", QueryName: "amount", PhysicalName: "c002", LogicalType: TypeInteger}}

	badUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nbad,not-an-integer\n")
	dataset, badImport, err := repository.Submit(ctx, requester, Submission{Name: "Ledger", Description: "integration", UploadID: badUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	executeNext(t, repository, worker)
	badImport, _ = repository.FindImport(ctx, badImport.ID)
	if badImport.Status != ImportFailed {
		t.Fatalf("bad import status=%s", badImport.Status)
	}
	dataset, _ = repository.Find(ctx, dataset.ID)
	if dataset.Status != DatasetProvisioning || dataset.CurrentGenerationID != nil {
		t.Fatalf("failed provisioning became visible: %+v", dataset)
	}
	revisedColumns := append([]Column(nil), columns...)
	revisedColumns[1].LogicalType = TypeDecimal
	_, sameUploadRetry, err := repository.Submit(ctx, requester, Submission{Name: dataset.Name, Description: dataset.Description, DatasetID: dataset.ID, DatasetRevision: dataset.Revision, UploadID: badUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: revisedColumns}, integrationdb.Now().Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if sameUploadRetry.UploadID != badImport.UploadID || sameUploadRetry.ID == badImport.ID {
		t.Fatal("same retained upload retry did not preserve independent import provenance")
	}
	executeNext(t, repository, worker)
	sameUploadRetry, _ = repository.FindImport(ctx, sameUploadRetry.ID)
	if sameUploadRetry.Status != ImportFailed {
		t.Fatalf("same-upload retry status=%s", sameUploadRetry.Status)
	}
	dataset, _ = repository.Find(ctx, dataset.ID)

	correctedUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\ninitial-a,1\ninitial-b,2\n")
	generationGDataset, generationG, err := repository.Submit(ctx, requester, Submission{Name: dataset.Name, Description: dataset.Description, DatasetID: dataset.ID, DatasetRevision: dataset.Revision, UploadID: correctedUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if generationG.UploadID == badImport.UploadID {
		t.Fatal("corrected CSV did not create immutable new provenance")
	}
	executeNext(t, repository, worker)
	generationGDataset, _ = repository.Find(ctx, generationGDataset.ID)
	if generationGDataset.Status != DatasetActive || generationGDataset.RowCount != 2 {
		t.Fatalf("initial publication=%+v", generationGDataset)
	}
	assertVisibleValues(t, db, generationGDataset.ViewName(), []string{"initial-a", "initial-b"})

	columns, _ = repository.Columns(ctx, dataset.ID)
	appendAUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nappend-a,3\n")
	_, appendA, err := repository.Submit(ctx, requester, Submission{DatasetID: dataset.ID, DatasetRevision: generationGDataset.Revision, UploadID: appendAUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeAppend, Columns: columns}, integrationdb.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executeNext(t, repository, worker)
	afterA, _ := repository.Find(ctx, dataset.ID)
	appendBUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nappend-b,4\n")
	_, appendB, err := repository.Submit(ctx, requester, Submission{DatasetID: dataset.ID, DatasetRevision: afterA.Revision, UploadID: appendBUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeAppend, Columns: columns}, integrationdb.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executeNext(t, repository, worker)
	afterB, _ := repository.Find(ctx, dataset.ID)
	assertVisibleValues(t, db, afterB.ViewName(), []string{"append-a", "append-b", "initial-a", "initial-b"})

	replaceUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nreplacement,9\n")
	_, generationH, err := repository.Submit(ctx, requester, Submission{DatasetID: dataset.ID, DatasetRevision: afterB.Revision, UploadID: replaceUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now().Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executeNext(t, repository, worker)
	assertVisibleValues(t, db, afterB.ViewName(), []string{"replacement"})

	if _, err := db.Exec(`UPDATE custom_dataset_imports SET finished_at=TIMESTAMPADD(HOUR,-2,UTC_TIMESTAMP(6)) WHERE dataset_id=?`, dataset.ID); err != nil {
		t.Fatal(err)
	}
	items, err := repository.CleanupCandidates(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		for {
			deleted, err := repository.CleanupRows(ctx, item)
			if err != nil {
				t.Fatal(err)
			}
			if deleted < 5000 {
				break
			}
		}
	}
	var retiredRows int
	if err := db.Get(&retiredRows, `SELECT COUNT(*) FROM `+quoteIdentifier(afterB.TableName())+` WHERE _import_id IN (?,?,?)`, generationG.ID, appendA.ID, appendB.ID); err != nil || retiredRows != 0 {
		t.Fatalf("retired rows=%d err=%v", retiredRows, err)
	}
	var currentRows int
	if err := db.Get(&currentRows, `SELECT COUNT(*) FROM `+quoteIdentifier(afterB.TableName())+` WHERE _import_id=?`, generationH.ID); err != nil || currentRows != 1 {
		t.Fatalf("current rows=%d err=%v", currentRows, err)
	}
	var imports, uploads int
	_ = db.Get(&imports, `SELECT COUNT(*) FROM custom_dataset_imports WHERE dataset_id=?`, dataset.ID)
	_ = db.Get(&uploads, `SELECT COUNT(*) FROM custom_dataset_uploads WHERE id IN (?,?,?,?,?)`, badUpload.ID, correctedUpload.ID, appendAUpload.ID, appendBUpload.ID, replaceUpload.ID)
	if imports != 6 || uploads != 5 {
		t.Fatalf("history lost: imports=%d uploads=%d", imports, uploads)
	}

	assertPhysicalAndPlans(t, db, afterB, generationH)
	current, _ := repository.Find(ctx, dataset.ID)
	if err := repository.Archive(ctx, requester, dataset.ID, current.Revision, integrationdb.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertVisibleValues(t, db, afterB.ViewName(), []string{"replacement"})
}

func TestCustomDatasetDiscoversServerPacketAndRejectsOversizedSingleRow(t *testing.T) {
	db := integrationdb.Open(t)
	var original uint64
	if err := db.Get(&original, `SELECT @@GLOBAL.max_allowed_packet`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SET GLOBAL max_allowed_packet=1048576`); err != nil {
		t.Skipf("cannot change disposable server packet limit: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`SET GLOBAL max_allowed_packet=?`, original) })
	configured := integrationdb.Config(t)
	connection, err := database.Open(context.Background(), configured)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	repository, _ := NewRepository(connection)
	packet, err := repository.MaxAllowedPacket(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if packet > 1048576 {
		t.Fatalf("driver did not discover server max_allowed_packet: %d", packet)
	}
	batch := newInserter(repository, Dataset{ID: 1}, []Column{{PhysicalName: "c001", LogicalType: TypeText}}, Import{ID: 1, Attempt: 1}, strings.Repeat("o", 64), packet)
	err = batch.Add(context.Background(), 2, []any{strings.Repeat("x", 2<<20)})
	var infrastructure InfrastructureError
	if !errors.As(err, &infrastructure) || !strings.Contains(err.Error(), "max_allowed_packet") {
		t.Fatalf("packet failure=%v", err)
	}
}

func TestCustomDatasetAtomicVisibilityOwnershipAndRollback(t *testing.T) {
	db := integrationdb.Open(t)
	cleanupDynamicTables(t, db)
	integrationdb.Reset(t, db, nil)
	t.Cleanup(func() { cleanupDynamicTables(t, db) })
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	user := integrationdb.User(t, db, "custom-dataset-atomic", role.ID, true)
	requester := integrationdb.Requester(user, role)
	storage, _ := NewStorage(t.TempDir())
	repository, _ := NewRepository(db)
	ddl, _ := NewDDL(db)
	worker, _ := NewWorker(repository, ddl, storage, WorkerConfig{Concurrency: 1, HeartbeatInterval: 50 * time.Millisecond, StaleAfter: time.Second, CleanupInterval: time.Hour}, nil)
	columns := []Column{{Ordinal: 1, DisplayName: "Name", QueryName: "name", PhysicalName: "c001", LogicalType: TypeText}, {Ordinal: 2, DisplayName: "Amount", QueryName: "amount", PhysicalName: "c002", LogicalType: TypeInteger}}
	upload := integrationUpload(t, repository, storage, requester, "Name,Amount\nold,1\n")
	dataset, _, err := repository.Submit(context.Background(), requester, Submission{Name: "Atomic", UploadID: upload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	executeNext(t, repository, worker)
	dataset, _ = repository.Find(context.Background(), dataset.ID)
	columns, _ = repository.Columns(context.Background(), dataset.ID)

	replaceUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nnew,2\n")
	_, replace, err := repository.Submit(context.Background(), requester, Submission{DatasetID: dataset.ID, DatasetRevision: dataset.Revision, UploadID: replaceUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replaceClaim, _ := repository.Claim(context.Background(), worker.owner)
	if _, err := db.Exec(`INSERT INTO `+quoteIdentifier(dataset.TableName())+` (_import_id,_import_attempt,_source_record_number,c001,c002) VALUES (?,?,?,?,?)`, replace.ID, replaceClaim.Attempt, 2, "new", 2); err != nil {
		t.Fatal(err)
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"old"})
	if _, err := db.Exec(`CREATE TRIGGER custom_dataset_test_publish_rollback BEFORE UPDATE ON custom_datasets FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced publication rollback'`); err != nil {
		t.Fatal(err)
	}
	_, publishErr := repository.Publish(context.Background(), *replaceClaim, worker.owner, 1)
	if _, err := db.Exec(`DROP TRIGGER custom_dataset_test_publish_rollback`); err != nil {
		t.Fatal(err)
	}
	if publishErr == nil {
		t.Fatal("forced publication failure committed")
	}
	rolledBack, _ := repository.FindImport(context.Background(), replace.ID)
	if rolledBack.Status != ImportRunning || rolledBack.PublishedAttempt != nil {
		t.Fatalf("publication did not roll back: %+v", rolledBack)
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"old"})
	if owned, err := repository.Publish(context.Background(), *replaceClaim, worker.owner, 1); err != nil || !owned {
		t.Fatalf("publish after rollback owned=%v err=%v", owned, err)
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"new"})

	dataset, _ = repository.Find(context.Background(), dataset.ID)
	appendUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nappended,3\n")
	_, appendImport, err := repository.Submit(context.Background(), requester, Submission{DatasetID: dataset.ID, DatasetRevision: dataset.Revision, UploadID: appendUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeAppend, Columns: columns}, integrationdb.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	appendClaim, _ := repository.Claim(context.Background(), worker.owner)
	if _, err := db.Exec(`INSERT INTO `+quoteIdentifier(dataset.TableName())+` (_import_id,_import_attempt,_source_record_number,c001,c002) VALUES (?,?,?,?,?)`, appendImport.ID, appendClaim.Attempt, 2, "appended", 3); err != nil {
		t.Fatal(err)
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"new"})
	if owned, err := repository.Publish(context.Background(), *appendClaim, worker.owner, 1); err != nil || !owned {
		t.Fatalf("append publish owned=%v err=%v", owned, err)
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"appended", "new"})

	dataset, _ = repository.Find(context.Background(), dataset.ID)
	lostUpload := integrationUpload(t, repository, storage, requester, "Name,Amount\nlost,4\n")
	_, lost, err := repository.Submit(context.Background(), requester, Submission{DatasetID: dataset.ID, DatasetRevision: dataset.Revision, UploadID: lostUpload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	lostClaim, _ := repository.Claim(context.Background(), worker.owner)
	if _, err := db.Exec(`UPDATE custom_dataset_imports SET owner_id=? WHERE id=?`, strings.Repeat("x", 64), lost.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(context.Background(), *lostClaim, worker.owner, 1); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("ownership loss error=%v", err)
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"appended", "new"})
}

func TestCustomDatasetResolvesAmbiguousPublicationCommit(t *testing.T) {
	db := integrationdb.Open(t)
	cleanupDynamicTables(t, db)
	integrationdb.Reset(t, db, nil)
	t.Cleanup(func() { cleanupDynamicTables(t, db) })
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	user := integrationdb.User(t, db, "custom-dataset-ambiguous", role.ID, true)
	requester := integrationdb.Requester(user, role)
	storage, _ := NewStorage(t.TempDir())
	repository, _ := NewRepository(db)
	ddl, _ := NewDDL(db)
	columns := []Column{{Ordinal: 1, DisplayName: "Name", QueryName: "name", PhysicalName: "c001", LogicalType: TypeText}}
	upload := integrationUpload(t, repository, storage, requester, "Name\ncommitted\n")
	dataset, _, err := repository.Submit(context.Background(), requester, Submission{Name: "Ambiguous commit", UploadID: upload.ID, Delimiter: DelimiterComma, HeaderRecordNumber: 1, Mode: ModeReplace, Columns: columns}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 64)
	job, err := repository.Claim(context.Background(), owner)
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if err := ddl.Ensure(context.Background(), dataset, columns, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO `+quoteIdentifier(dataset.TableName())+` (_import_id,_import_attempt,_source_record_number,c001) VALUES (?,?,?,?)`, job.ID, job.Attempt, 2, "committed"); err != nil {
		t.Fatal(err)
	}

	configuration := integrationdb.Config(t)
	proxy := newCommitDropProxy(t, net.JoinHostPort(configuration.Host, strconv.Itoa(configuration.Port)))
	t.Cleanup(proxy.Close)
	_, proxyPort, _ := net.SplitHostPort(proxy.listener.Addr().String())
	configuration.Host = "127.0.0.1"
	configuration.Port, _ = strconv.Atoi(proxyPort)
	proxiedDB, err := database.Open(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer proxiedDB.Close()
	proxiedRepository, _ := NewRepository(proxiedDB)
	proxy.dropCommit.Store(true)
	if owned, err := proxiedRepository.Publish(context.Background(), *job, owner, 1); err != nil || !owned {
		t.Fatalf("ambiguous publish owned=%v err=%v", owned, err)
	}
	if proxy.dropCommit.Load() {
		t.Fatal("test proxy did not observe COMMIT")
	}
	assertVisibleValues(t, db, dataset.ViewName(), []string{"committed"})
}

func integrationUpload(t *testing.T, repository *Repository, storage *Storage, requester securityctx.Requester, contents string) Upload {
	t.Helper()
	file, err := storage.Save(context.Background(), "input.csv", strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	upload, err := repository.CreateUpload(context.Background(), requester, file, integrationdb.Now().Add(time.Hour), integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	return upload
}

func executeNext(t *testing.T, repository *Repository, worker *Worker) {
	t.Helper()
	job, err := repository.Claim(context.Background(), worker.owner)
	if err != nil || job == nil {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	worker.execute(context.Background(), *job)
	finished, err := repository.FindImport(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != ImportSucceeded && finished.Status != ImportFailed {
		t.Fatalf("unfinished import: %+v", finished)
	}
}

func assertVisibleValues(t *testing.T, db *sqlx.DB, view string, want []string) {
	t.Helper()
	var got []string
	if err := db.Select(&got, `SELECT name FROM `+quoteIdentifier(view)+` ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("visible=%v want=%v", got, want)
	}
}

func assertPhysicalAndPlans(t *testing.T, db *sqlx.DB, dataset Dataset, current Import) {
	t.Helper()
	var row struct {
		Type     string `db:"type"`
		Nullable string `db:"nullable"`
		Extra    string `db:"extra"`
		Key      string `db:"column_key"`
	}
	if err := db.Get(&row, `SELECT COLUMN_TYPE type,IS_NULLABLE nullable,EXTRA extra,COLUMN_KEY column_key FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='_row_id'`, dataset.TableName()); err != nil || row.Type != "bigint unsigned" || row.Nullable != "NO" || row.Extra != "auto_increment" || row.Key != "PRI" {
		t.Fatalf("row id=%+v err=%v", row, err)
	}
	var indexes []string
	if err := db.Select(&indexes, `SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY INDEX_NAME`, dataset.TableName()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(indexes) != "[PRIMARY uq_import_attempt_source]" {
		t.Fatalf("physical indexes=%v", indexes)
	}
	type planRow struct {
		Key *string `db:"key"`
	}
	var plan []planRow
	query := `EXPLAIN SELECT r.c001 FROM custom_dataset_imports i JOIN ` + quoteIdentifier(dataset.TableName()) + ` r ON r._import_id=i.id AND r._import_attempt=i.published_attempt WHERE i.dataset_id=? AND i.generation_id=? AND i.status='succeeded'`
	if err := db.Unsafe().Select(&plan, query, dataset.ID, *current.GenerationID); err != nil {
		t.Fatal(err)
	}
	used := ""
	for _, row := range plan {
		if row.Key != nil {
			used += " " + *row.Key
		}
	}
	if !strings.Contains(used, "idx_custom_dataset_imports_publish") || !strings.Contains(used, "uq_import_attempt_source") {
		t.Fatalf("unexpected EXPLAIN keys:%s", used)
	}
	viewRow := db.QueryRowx(`SHOW CREATE VIEW ` + quoteIdentifier(dataset.ViewName()))
	values := map[string]any{}
	if err := viewRow.MapScan(values); err != nil {
		t.Fatal(err)
	}
	definition := ""
	for key, value := range values {
		if strings.EqualFold(key, "Create View") {
			if bytes, ok := value.([]byte); ok {
				definition = string(bytes)
			} else {
				definition = fmt.Sprint(value)
			}
		}
	}
	if !strings.Contains(definition, "ALGORITHM=UNDEFINED") || !strings.Contains(definition, "SQL SECURITY INVOKER") {
		t.Fatalf("unexpected view definition: %s", definition)
	}
}

func cleanupDynamicTables(t *testing.T, db *sqlx.DB) {
	t.Helper()
	var objects []struct {
		Name string `db:"table_name"`
		Type string `db:"table_type"`
	}
	if err := db.Select(&objects, `SELECT TABLE_NAME table_name,TABLE_TYPE table_type FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND (TABLE_NAME REGEXP '^custom_dataset_[0-9]+$' OR TABLE_NAME REGEXP '^custom_dataset_view_[0-9]+$') ORDER BY TABLE_TYPE DESC`); err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		statement := `DROP TABLE IF EXISTS ` + quoteIdentifier(object.Name)
		if object.Type == "VIEW" {
			statement = `DROP VIEW IF EXISTS ` + quoteIdentifier(object.Name)
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

type commitDropProxy struct {
	listener   net.Listener
	target     string
	dropCommit atomic.Bool
	wait       sync.WaitGroup
}

func newCommitDropProxy(t *testing.T, target string) *commitDropProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &commitDropProxy{listener: listener, target: target}
	proxy.wait.Add(1)
	go func() {
		defer proxy.wait.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			proxy.wait.Add(1)
			go proxy.forward(connection)
		}
	}()
	return proxy
}

func (proxy *commitDropProxy) Close() {
	_ = proxy.listener.Close()
	proxy.wait.Wait()
}

func (proxy *commitDropProxy) forward(client net.Conn) {
	defer proxy.wait.Done()
	server, err := net.Dial("tcp", proxy.target)
	if err != nil {
		_ = client.Close()
		return
	}
	defer client.Close()
	defer server.Close()
	dropResponse := atomic.Bool{}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			packet, err := readMySQLPacket(server)
			if err != nil {
				return
			}
			if dropResponse.Swap(false) {
				_ = client.Close()
				_ = server.Close()
				return
			}
			if err := writeAll(client, packet); err != nil {
				return
			}
		}
	}()
	for {
		packet, err := readMySQLPacket(client)
		if err != nil {
			break
		}
		if len(packet) >= 5 && packet[4] == 0x03 && strings.EqualFold(string(packet[5:]), "COMMIT") && proxy.dropCommit.CompareAndSwap(true, false) {
			dropResponse.Store(true)
		}
		if err := writeAll(server, packet); err != nil {
			break
		}
	}
	_ = client.Close()
	_ = server.Close()
	<-serverDone
}

func readMySQLPacket(connection net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	packet := make([]byte, 4+length)
	copy(packet, header)
	_, err := io.ReadFull(connection, packet[4:])
	return packet, err
}

func writeAll(connection net.Conn, value []byte) error {
	for len(value) != 0 {
		written, err := connection.Write(value)
		if err != nil {
			return err
		}
		value = value[written:]
	}
	return nil
}
