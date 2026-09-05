package customdataset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const (
	batchRows       = 500
	batchParameters = 60_000
	batchSoftBytes  = 4 << 20
	packetReserve   = 64 << 10
)

type WorkerConfig struct {
	Concurrency       int
	HeartbeatInterval time.Duration
	StaleAfter        time.Duration
	CleanupInterval   time.Duration
	CleanupGrace      time.Duration
}

type Worker struct {
	repository *Repository
	ddl        *DDL
	storage    *Storage
	config     WorkerConfig
	owner      string
	logger     *slog.Logger
	heartbeat  func(context.Context, uint64, string, uint32) (bool, error)
}

func NewWorker(repository *Repository, ddl *DDL, storage *Storage, config WorkerConfig, logger *slog.Logger) (*Worker, error) {
	if repository == nil || ddl == nil || storage == nil || config.Concurrency < 1 || config.HeartbeatInterval <= 0 || config.StaleAfter <= config.HeartbeatInterval || config.CleanupInterval <= 0 || config.CleanupGrace < 0 {
		return nil, fmt.Errorf("custom dataset worker dependencies and bounds are required")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	worker := &Worker{repository: repository, ddl: ddl, storage: storage, config: config, owner: hex.EncodeToString(random), logger: logger}
	worker.heartbeat = repository.Heartbeat
	return worker, nil
}

func (worker *Worker) OwnerID() string { return worker.owner }

func (worker *Worker) Run(ctx context.Context) {
	var group sync.WaitGroup
	for range worker.config.Concurrency {
		group.Add(1)
		go func() { defer group.Done(); worker.claimLoop(ctx) }()
	}
	group.Add(2)
	go func() { defer group.Done(); worker.staleLoop(ctx) }()
	go func() { defer group.Done(); worker.cleanupLoop(ctx) }()
	group.Wait()
}

func (worker *Worker) claimLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		job, err := worker.repository.Claim(ctx, worker.owner)
		if err != nil && !errors.Is(err, context.Canceled) {
			worker.logger.ErrorContext(ctx, "claim custom dataset import", "error", err)
		}
		if job != nil {
			worker.execute(ctx, *job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (worker *Worker) execute(parent context.Context, job Import) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go worker.heartbeatLoop(ctx, cancel, job, done)
	class := "infrastructure"
	diagnostics := []Diagnostic(nil)
	truncated := false
	err := func() error {
		dataset, err := worker.repository.Find(ctx, job.DatasetID)
		if err != nil {
			return err
		}
		if dataset.Status == DatasetArchived || dataset.SchemaRevision != job.SchemaRevision {
			return ErrConflict
		}
		columns, err := worker.repository.Columns(ctx, dataset.ID)
		if err != nil {
			return err
		}
		if len(columns) == 0 {
			return fmt.Errorf("custom dataset has no columns")
		}
		upload, err := worker.repository.FindUpload(ctx, job.UploadID)
		if err != nil {
			return err
		}
		if upload.Status != "retained" {
			return fmt.Errorf("custom dataset upload is unavailable")
		}
		if err := worker.ddl.Ensure(ctx, dataset, columns, job.ID); err != nil {
			return err
		}
		packet, err := worker.repository.MaxAllowedPacket(ctx)
		if err != nil {
			return err
		}
		file, err := worker.storage.Open(upload.StorageKey)
		if err != nil {
			return fmt.Errorf("open retained CSV: %w", err)
		}
		defer file.Close()
		batch := newInserter(worker.repository, dataset, columns, job, worker.owner, packet)
		class = "validation"
		sourceRecords, rows, found, foundTruncated, err := Stream(ctx, file, job.Delimiter, job.HeaderRecordNumber, columns, func(record uint64, values []any) error {
			if dataset.RowCount+batch.rows+uint64(len(batch.records))+1 > MaxDataRows && job.Mode == ModeAppend {
				return fmt.Errorf("%w: append would exceed %d published rows", ErrInvalid, MaxDataRows)
			}
			return batch.Add(ctx, record, values)
		})
		diagnostics, truncated = found, foundTruncated
		if err != nil {
			return err
		}
		if len(diagnostics) != 0 {
			return fmt.Errorf("%w: CSV validation failed", ErrInvalid)
		}
		class = "infrastructure"
		if err := batch.Flush(ctx); err != nil {
			return err
		}
		if owned, err := worker.repository.Progress(ctx, job.ID, worker.owner, job.Attempt, "publishing", sourceRecords, rows); err != nil {
			return err
		} else if !owned {
			return ErrClaimLost
		}
		class = "publication"
		owned, err := worker.repository.Publish(ctx, job, worker.owner, rows)
		if err != nil {
			return err
		}
		if !owned {
			return ErrClaimLost
		}
		return nil
	}()
	cancel(err)
	<-done
	if err == nil {
		return
	}
	cause := context.Cause(ctx)
	if errors.Is(err, ErrClaimLost) || errors.Is(cause, ErrClaimLost) {
		worker.logger.Info("custom dataset import claim lost", "import_id", job.ID)
		return
	}
	if errors.Is(cause, ErrLeaseUnproven) {
		class, err = "lease_unproven", cause
	}
	if errors.Is(err, ErrInvalid) {
		class = "validation"
	}
	var infrastructure InfrastructureError
	if errors.As(err, &infrastructure) {
		class = "configuration"
	}
	finish, finishCancel := context.WithTimeout(context.WithoutCancel(parent), worker.heartbeatTimeout())
	defer finishCancel()
	if owned, failErr := worker.repository.Fail(finish, job, worker.owner, class, publicWorkerError(class, err), diagnostics, truncated); failErr != nil || !owned {
		worker.logger.Error("finish failed custom dataset import", "import_id", job.ID, "error", failErr)
	}
	worker.logger.Error("custom dataset import failed", "import_id", job.ID, "class", class, "error", err)
}

func (worker *Worker) heartbeatLoop(ctx context.Context, cancel context.CancelCauseFunc, job Import, done chan<- struct{}) {
	defer close(done)
	lastProof := time.Now()
	ticker := time.NewTicker(worker.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, heartbeatCancel := context.WithTimeout(ctx, worker.heartbeatTimeout())
			owned, err := worker.heartbeat(heartbeatCtx, job.ID, worker.owner, job.Attempt)
			heartbeatCancel()
			if err == nil && !owned {
				cancel(ErrClaimLost)
				return
			}
			if err == nil {
				lastProof = time.Now()
				continue
			}
			worker.logger.WarnContext(ctx, "heartbeat custom dataset import", "import_id", job.ID, "error", err)
			if time.Since(lastProof) >= worker.config.StaleAfter {
				cancel(ErrLeaseUnproven)
				return
			}
		}
	}
}

func (worker *Worker) heartbeatTimeout() time.Duration {
	value := worker.config.StaleAfter / 3
	if value > 5*time.Second {
		return 5 * time.Second
	}
	return value
}

func (worker *Worker) staleLoop(ctx context.Context) {
	ticker := time.NewTicker(worker.config.StaleAfter / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := worker.repository.RequeueStale(ctx, worker.config.StaleAfter); err != nil {
				worker.logger.ErrorContext(ctx, "requeue stale custom dataset imports", "error", err)
			}
		}
	}
}

func (worker *Worker) cleanupLoop(ctx context.Context) {
	worker.cleanup(ctx)
	ticker := time.NewTicker(worker.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.cleanup(ctx)
		}
	}
}

func (worker *Worker) cleanup(ctx context.Context) {
	items, err := worker.repository.CleanupCandidates(ctx, worker.config.CleanupGrace)
	if err != nil {
		worker.logger.ErrorContext(ctx, "list custom dataset cleanup candidates", "error", err)
		return
	}
	for _, item := range items {
		for {
			deleted, err := worker.repository.CleanupRows(ctx, item)
			if err != nil {
				worker.logger.ErrorContext(ctx, "clean custom dataset rows", "import_id", item.ID, "error", err)
				break
			}
			if deleted < 5000 {
				break
			}
		}
	}
	uploads, err := worker.repository.ExpiredUploads(ctx)
	if err != nil {
		worker.logger.ErrorContext(ctx, "list expired custom dataset uploads", "error", err)
		return
	}
	for _, upload := range uploads {
		if upload.Status == "uploaded" {
			expired, err := worker.repository.ExpireUpload(ctx, upload.ID, upload.Revision)
			if err != nil {
				worker.logger.ErrorContext(ctx, "expire custom dataset upload", "upload_id", upload.ID, "error", err)
				continue
			}
			if !expired {
				continue
			}
		}
		if err := worker.storage.Remove(upload.StorageKey); err != nil && !errors.Is(err, os.ErrNotExist) {
			worker.logger.ErrorContext(ctx, "remove expired custom dataset upload", "upload_id", upload.ID, "error", err)
		}
	}
	referenced, err := worker.repository.ReferencedUploadKeys(ctx)
	if err != nil {
		worker.logger.ErrorContext(ctx, "list referenced custom dataset uploads", "error", err)
		return
	}
	if err := worker.storage.Reconcile(referenced, time.Now().Add(-worker.config.CleanupGrace)); err != nil {
		worker.logger.ErrorContext(ctx, "reconcile custom dataset upload storage", "error", err)
	}
}

type inserter struct {
	repository *Repository
	dataset    Dataset
	columns    []Column
	job        Import
	owner      string
	packet     uint64
	records    []stagedRecord
	bytes      uint64
	rows       uint64
	lastSource uint64
}

type stagedRecord struct {
	source uint64
	values []any
	bytes  uint64
}

func newInserter(repository *Repository, dataset Dataset, columns []Column, job Import, owner string, packet uint64) *inserter {
	return &inserter{repository: repository, dataset: dataset, columns: columns, job: job, owner: owner, packet: packet, records: make([]stagedRecord, 0, batchRows)}
}

func (batch *inserter) Add(ctx context.Context, source uint64, values []any) error {
	record := stagedRecord{source: source, values: values, bytes: estimateValues(values)}
	if len(batch.records) != 0 && (len(batch.records) == batchRows || (len(batch.records)+1)*(len(batch.columns)+3) > batchParameters || batch.bytes+record.bytes > batchSoftBytes) {
		if err := batch.Flush(ctx); err != nil {
			return err
		}
	}
	batch.records = append(batch.records, record)
	batch.bytes += record.bytes
	if len(batch.records) == 1 && batch.estimatePacket() >= batch.packet {
		return InfrastructureError{Reason: fmt.Sprintf("one legal CSV row requires an estimated %d-byte MySQL packet, but the server max_allowed_packet is %d; raise max_allowed_packet (production baseline: at least 256M)", batch.estimatePacket(), batch.packet)}
	}
	return nil
}

func (batch *inserter) Flush(ctx context.Context) error {
	if len(batch.records) == 0 {
		return nil
	}
	if batch.packet <= packetReserve || batch.estimatePacket() > batch.packet-packetReserve {
		if len(batch.records) > 1 {
			mid := len(batch.records) / 2
			right := append([]stagedRecord(nil), batch.records[mid:]...)
			batch.records = batch.records[:mid]
			batch.recalculate()
			if err := batch.Flush(ctx); err != nil {
				return err
			}
			batch.records = right
			batch.recalculate()
			return batch.Flush(ctx)
		}
		return InfrastructureError{Reason: fmt.Sprintf("one legal CSV row cannot be proven below the server max_allowed_packet of %d bytes; raise max_allowed_packet (production baseline: at least 256M)", batch.packet)}
	}
	statement, arguments := batch.statement()
	first, last, count := batch.records[0].source, batch.records[len(batch.records)-1].source, len(batch.records)
	if _, err := batch.repository.db.ExecContext(ctx, statement, arguments...); err != nil {
		var present int
		checkErr := batch.repository.db.GetContext(ctx, &present, `SELECT COUNT(*) FROM `+quoteIdentifier(batch.dataset.TableName())+` WHERE _import_id=? AND _import_attempt=? AND _source_record_number BETWEEN ? AND ?`, batch.job.ID, batch.job.Attempt, first, last)
		if checkErr != nil || present == 0 {
			return err
		}
		if present != count {
			return InfrastructureError{Reason: "custom dataset batch outcome was partial and could not be reconciled safely"}
		}
	}
	batch.rows += uint64(count)
	batch.lastSource = last
	if owned, err := batch.repository.Progress(ctx, batch.job.ID, batch.owner, batch.job.Attempt, "staging", last, batch.rows); err != nil {
		return err
	} else if !owned {
		return ErrClaimLost
	}
	batch.records, batch.bytes = batch.records[:0], 0
	return nil
}

func (batch *inserter) statement() (string, []any) {
	names := []string{"_import_id", "_import_attempt", "_source_record_number"}
	for _, column := range batch.columns {
		names = append(names, column.PhysicalName)
	}
	quoted := make([]string, len(names))
	for index := range names {
		quoted[index] = quoteIdentifier(names[index])
	}
	rowPlaceholder := "(" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
	rows := make([]string, len(batch.records))
	arguments := make([]any, 0, len(batch.records)*len(names))
	for index, record := range batch.records {
		rows[index] = rowPlaceholder
		arguments = append(arguments, batch.job.ID, batch.job.Attempt, record.source)
		arguments = append(arguments, record.values...)
	}
	return "INSERT INTO " + quoteIdentifier(batch.dataset.TableName()) + " (" + strings.Join(quoted, ",") + ") VALUES " + strings.Join(rows, ","), arguments
}

func (batch *inserter) estimatePacket() uint64 {
	statement, _ := batch.statement()
	return uint64(len(statement)) + batch.bytes + uint64(len(batch.records)*(len(batch.columns)+3)*10) + packetReserve
}

func (batch *inserter) recalculate() {
	batch.bytes = 0
	for _, record := range batch.records {
		batch.bytes += record.bytes
	}
}

func estimateValues(values []any) uint64 {
	var result uint64
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			result++
		case string:
			result += uint64(len(typed)) + 9
		case decimal.Decimal:
			result += uint64(len(typed.String())) + 9
		case time.Time:
			result += 16
		default:
			result += 16
		}
	}
	return result
}

func publicWorkerError(class string, err error) string {
	if class == "validation" {
		return truncate(err.Error(), 500)
	}
	if class == "configuration" {
		return truncate(err.Error(), 500)
	}
	if errors.Is(err, ErrLeaseUnproven) {
		return "Import stopped because worker ownership could not be verified."
	}
	return "The import could not be completed safely. Review infrastructure logs and retry."
}
