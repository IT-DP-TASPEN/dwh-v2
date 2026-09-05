package customdataset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ibldzn/go-admin/internal/audit"
)

func (repository *Repository) Claim(ctx context.Context, owner string) (*Import, error) {
	if len(owner) != 64 {
		return nil, fmt.Errorf("opaque custom dataset owner is required")
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id uint64
	if err := tx.GetContext(ctx, &id, `SELECT id FROM custom_dataset_imports WHERE status='queued' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE custom_dataset_imports SET status='running',phase='validating',attempt=attempt+1,owner_id=?,claimed_at=UTC_TIMESTAMP(6),heartbeat_at=UTC_TIMESTAMP(6),started_at=COALESCE(started_at,UTC_TIMESTAMP(6)),source_records=0,staged_rows=0,failure_class=NULL,failure_message=NULL,diagnostics=NULL,diagnostics_truncated=FALSE WHERE id=? AND status='queued'`, owner, id)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, ErrClaimLost
	}
	var job Import
	if err := tx.GetContext(ctx, &job, `SELECT id,dataset_id,upload_id,delimiter,header_record_number,mode,schema_revision,generation_id,status,phase,attempt,published_attempt,owner_id,claimed_at,heartbeat_at,source_records,staged_rows,failure_class,failure_message,diagnostics,diagnostics_truncated,submitted_by_user_id,started_at,finished_at,created_at,updated_at FROM custom_dataset_imports WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (repository *Repository) Heartbeat(ctx context.Context, id uint64, owner string, attempt uint32) (bool, error) {
	result, err := repository.db.ExecContext(ctx, `UPDATE custom_dataset_imports SET heartbeat_at=UTC_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, id, owner, attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) Progress(ctx context.Context, id uint64, owner string, attempt uint32, phase string, sourceRecords, stagedRows uint64) (bool, error) {
	if phase != "validating" && phase != "staging" && phase != "publishing" {
		return false, fmt.Errorf("invalid custom dataset phase")
	}
	result, err := repository.db.ExecContext(ctx, `UPDATE custom_dataset_imports SET phase=?,source_records=?,staged_rows=? WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, phase, sourceRecords, stagedRows, id, owner, attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) RequeueStale(ctx context.Context, staleAfter time.Duration) (int64, error) {
	result, err := repository.db.ExecContext(ctx, `UPDATE custom_dataset_imports SET status='queued',phase='queued',owner_id=NULL,claimed_at=NULL,heartbeat_at=NULL WHERE status='running' AND heartbeat_at<TIMESTAMPADD(MICROSECOND,-?,UTC_TIMESTAMP(6))`, staleAfter.Microseconds())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (repository *Repository) Fail(ctx context.Context, job Import, owner, class, message string, diagnostics []Diagnostic, truncated bool) (bool, error) {
	class, message = truncate(class, 64), truncate(message, 500)
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE custom_dataset_imports SET status='failed',phase='complete',failure_class=?,failure_message=?,diagnostics=?,diagnostics_truncated=?,finished_at=UTC_TIMESTAMP(6),heartbeat_at=UTC_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, class, message, nullableJSON(encodeDiagnostics(diagnostics)), truncated, job.ID, owner, job.Attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return affected == 1, err
	}
	if err := audit.Append(ctx, tx, audit.Event{Action: audit.ActionCustomDatasetImportFailed, Resource: audit.ResourceCustomDatasetImport, ResourceID: job.ID, Metadata: audit.CustomDatasetMetadata{DatasetID: job.DatasetID, UploadID: job.UploadID, ImportID: job.ID, Mode: string(job.Mode), Outcome: class}, CreatedAt: time.Now().UTC()}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var outcome struct {
			Status  ImportStatus `db:"status"`
			Attempt uint32       `db:"attempt"`
		}
		inspectErr := repository.db.GetContext(inspect, &outcome, `SELECT status,attempt FROM custom_dataset_imports WHERE id=?`, job.ID)
		if inspectErr == nil && outcome.Status == ImportFailed && outcome.Attempt == job.Attempt {
			return true, nil
		}
		if inspectErr != nil {
			return false, InfrastructureError{Reason: "custom dataset failure outcome could not be determined after a commit error"}
		}
		return false, err
	}
	return true, nil
}

func (repository *Repository) Publish(ctx context.Context, job Import, owner string, rows uint64) (bool, error) {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current Import
	if err := tx.GetContext(ctx, &current, `SELECT id,dataset_id,upload_id,delimiter,header_record_number,mode,schema_revision,generation_id,status,phase,attempt,published_attempt,owner_id,claimed_at,heartbeat_at,source_records,staged_rows,failure_class,failure_message,diagnostics,diagnostics_truncated,submitted_by_user_id,started_at,finished_at,created_at,updated_at FROM custom_dataset_imports WHERE id=? FOR UPDATE`, job.ID); err != nil {
		return false, err
	}
	var dataset Dataset
	if err := tx.GetContext(ctx, &dataset, `SELECT id,name,description,status,revision,schema_revision,current_generation_id,row_count,created_by_user_id,updated_by_user_id,activated_at,archived_at,created_at,updated_at FROM custom_datasets WHERE id=? FOR UPDATE`, job.DatasetID); err != nil {
		return false, err
	}
	if current.Status != ImportRunning || current.OwnerID == nil || *current.OwnerID != owner || current.Attempt != job.Attempt || current.SchemaRevision != dataset.SchemaRevision || dataset.Status == DatasetArchived || current.GenerationID == nil {
		return false, ErrClaimLost
	}
	if current.Mode == ModeAppend && (dataset.CurrentGenerationID == nil || *dataset.CurrentGenerationID != *current.GenerationID || dataset.Status != DatasetActive) {
		return false, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE custom_dataset_imports SET status='succeeded',phase='complete',published_attempt=attempt,staged_rows=?,finished_at=UTC_TIMESTAMP(6),heartbeat_at=UTC_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, rows, job.ID, owner, job.Attempt)
	if err != nil {
		return false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return false, ErrClaimLost
	}
	activated := dataset.Status == DatasetProvisioning
	if current.Mode == ModeReplace {
		_, err = tx.ExecContext(ctx, `UPDATE custom_datasets SET status='active',current_generation_id=?,row_count=?,revision=revision+1,activated_at=COALESCE(activated_at,UTC_TIMESTAMP(6)),updated_by_user_id=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?`, *current.GenerationID, rows, current.SubmittedByUserID, dataset.ID)
	} else {
		if dataset.RowCount > MaxDataRows-rows {
			return false, fmt.Errorf("%w: append would exceed %d published rows", ErrInvalid, MaxDataRows)
		}
		_, err = tx.ExecContext(ctx, `UPDATE custom_datasets SET row_count=row_count+?,revision=revision+1,updated_by_user_id=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?`, rows, current.SubmittedByUserID, dataset.ID)
	}
	if err != nil {
		return false, err
	}
	metadata := audit.CustomDatasetMetadata{DatasetID: job.DatasetID, UploadID: job.UploadID, ImportID: job.ID, SchemaRevision: job.SchemaRevision, Rows: rows, Mode: string(job.Mode), Outcome: "succeeded"}
	if err := audit.Append(ctx, tx, audit.Event{Action: audit.ActionCustomDatasetImportSucceeded, Resource: audit.ResourceCustomDatasetImport, ResourceID: job.ID, Metadata: metadata, CreatedAt: time.Now().UTC()}); err != nil {
		return false, err
	}
	if activated {
		if err := audit.Append(ctx, tx, audit.Event{Action: audit.ActionCustomDatasetActivated, Resource: audit.ResourceCustomDataset, ResourceID: job.DatasetID, Metadata: metadata, CreatedAt: time.Now().UTC()}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var outcome struct {
			Status    ImportStatus `db:"status"`
			Attempt   uint32       `db:"attempt"`
			Published *uint32      `db:"published_attempt"`
		}
		inspectErr := repository.db.GetContext(inspect, &outcome, `SELECT status,attempt,published_attempt FROM custom_dataset_imports WHERE id=?`, job.ID)
		if inspectErr == nil && outcome.Status == ImportSucceeded && outcome.Attempt == job.Attempt && outcome.Published != nil && *outcome.Published == job.Attempt {
			return true, nil
		}
		if inspectErr != nil {
			return false, InfrastructureError{Reason: "custom dataset publication outcome could not be determined after a commit error"}
		}
		return false, err
	}
	return true, nil
}

type CleanupImport struct {
	ID               uint64  `db:"id"`
	DatasetID        uint64  `db:"dataset_id"`
	PublishedAttempt *uint32 `db:"published_attempt"`
	Retired          bool    `db:"retired"`
}

func (repository *Repository) CleanupCandidates(ctx context.Context, grace time.Duration) ([]CleanupImport, error) {
	rows := make([]CleanupImport, 0)
	err := repository.db.SelectContext(ctx, &rows, `SELECT i.id,i.dataset_id,i.published_attempt,(i.status='succeeded' AND (d.current_generation_id IS NULL OR i.generation_id<>d.current_generation_id)) retired FROM custom_dataset_imports i JOIN custom_datasets d ON d.id=i.dataset_id WHERE i.finished_at<TIMESTAMPADD(MICROSECOND,-?,UTC_TIMESTAMP(6)) AND i.status IN ('failed','succeeded') ORDER BY i.finished_at,i.id`, grace.Microseconds())
	return rows, err
}

func (repository *Repository) CleanupRows(ctx context.Context, item CleanupImport) (int64, error) {
	dataset, err := repository.Find(ctx, item.DatasetID)
	if err != nil {
		return 0, err
	}
	statement := `DELETE FROM ` + quoteIdentifier(dataset.TableName()) + ` WHERE _import_id=?`
	arguments := []any{item.ID}
	if !item.Retired && item.PublishedAttempt != nil {
		statement += ` AND _import_attempt<>?`
		arguments = append(arguments, *item.PublishedAttempt)
	}
	statement += ` LIMIT 5000`
	result, err := repository.db.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (repository *Repository) MaxAllowedPacket(ctx context.Context) (uint64, error) {
	var value uint64
	if err := repository.db.GetContext(ctx, &value, `SELECT @@max_allowed_packet`); err != nil {
		return 0, fmt.Errorf("discover max_allowed_packet: %w", err)
	}
	return value, nil
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		value = value[:maximum]
	}
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func nullableJSON(value []byte) any {
	if string(value) == "[]" {
		return nil
	}
	return value
}
