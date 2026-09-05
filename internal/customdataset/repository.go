package customdataset

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Repository struct{ db *sqlx.DB }

type Submission struct {
	Name, Description   string
	DatasetID, UploadID uint64
	DatasetRevision     uint64
	Delimiter           Delimiter
	HeaderRecordNumber  uint64
	Mode                ImportMode
	Columns             []Column
}

func NewRepository(db *sqlx.DB) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("custom dataset database is required")
	}
	return &Repository{db: db}, nil
}

func (repository *Repository) CreateUpload(ctx context.Context, requester securityctx.Requester, file StoredFile, expiresAt, now time.Time) (Upload, error) {
	if file.Key == "" || file.Size <= 0 || requester.Effective.UserID == 0 {
		return Upload{}, fmt.Errorf("%w: invalid upload", ErrInvalid)
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return Upload{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO custom_dataset_uploads
		(storage_key,original_filename,byte_size,sha256,status,revision,created_by_user_id,expires_at,created_at,updated_at)
		VALUES (?,?,?,?,'uploaded',1,?,?,?,?)`, file.Key, file.OriginalName, file.Size, file.SHA256[:], requester.Effective.UserID, expiresAt.UTC(), now.UTC(), now.UTC())
	if err != nil {
		return Upload{}, fmt.Errorf("record custom dataset upload: %w", err)
	}
	id, _ := result.LastInsertId()
	metadata := audit.CustomDatasetMetadata{UploadID: uint64(id), SHA256: hex.EncodeToString(file.SHA256[:]), Outcome: "stored"}
	if err := appendAudit(ctx, tx, requester, audit.ActionCustomDatasetUploadStored, audit.ResourceCustomDatasetUpload, uint64(id), metadata, now); err != nil {
		return Upload{}, err
	}
	if err := tx.Commit(); err != nil {
		return Upload{}, err
	}
	return repository.FindUpload(ctx, uint64(id))
}

func (repository *Repository) FindUpload(ctx context.Context, id uint64) (Upload, error) {
	var upload Upload
	if err := repository.db.GetContext(ctx, &upload, `SELECT id,storage_key,original_filename,byte_size,sha256,status,revision,created_by_user_id,retained_at,expires_at,created_at FROM custom_dataset_uploads WHERE id=?`, id); err != nil {
		return Upload{}, notFound(err)
	}
	return upload, nil
}

func (repository *Repository) ExpiredUploads(ctx context.Context) ([]Upload, error) {
	rows := make([]Upload, 0)
	err := repository.db.SelectContext(ctx, &rows, `SELECT id,storage_key,original_filename,byte_size,sha256,status,revision,created_by_user_id,retained_at,expires_at,created_at FROM custom_dataset_uploads WHERE status='uploaded' AND expires_at<=UTC_TIMESTAMP(6) ORDER BY id`)
	return rows, err
}

func (repository *Repository) ExpireUpload(ctx context.Context, id, revision uint64) (bool, error) {
	result, err := repository.db.ExecContext(ctx, `UPDATE custom_dataset_uploads SET status='expired',revision=revision+1 WHERE id=? AND revision=? AND status='uploaded' AND expires_at<=UTC_TIMESTAMP(6)`, id, revision)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) ReferencedUploadKeys(ctx context.Context) (map[string]struct{}, error) {
	var keys []string
	if err := repository.db.SelectContext(ctx, &keys, `SELECT storage_key FROM custom_dataset_uploads WHERE status IN ('uploaded','retained')`); err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result, nil
}

func (repository *Repository) List(ctx context.Context) ([]Dataset, error) {
	rows := make([]Dataset, 0)
	if err := repository.db.SelectContext(ctx, &rows, `SELECT id,name,description,status,revision,schema_revision,current_generation_id,row_count,created_by_user_id,updated_by_user_id,activated_at,archived_at,created_at,updated_at FROM custom_datasets ORDER BY created_at DESC,id DESC`); err != nil {
		return nil, fmt.Errorf("list custom datasets: %w", err)
	}
	return rows, nil
}

func (repository *Repository) Find(ctx context.Context, id uint64) (Dataset, error) {
	var dataset Dataset
	if err := repository.db.GetContext(ctx, &dataset, `SELECT id,name,description,status,revision,schema_revision,current_generation_id,row_count,created_by_user_id,updated_by_user_id,activated_at,archived_at,created_at,updated_at FROM custom_datasets WHERE id=?`, id); err != nil {
		return Dataset{}, notFound(err)
	}
	return dataset, nil
}

func (repository *Repository) Columns(ctx context.Context, datasetID uint64) ([]Column, error) {
	columns := make([]Column, 0, MaxColumns)
	if err := repository.db.SelectContext(ctx, &columns, `SELECT dataset_id,ordinal,display_name,query_name,physical_name,logical_type,date_format FROM custom_dataset_columns WHERE dataset_id=? ORDER BY ordinal`, datasetID); err != nil {
		return nil, fmt.Errorf("list custom dataset columns: %w", err)
	}
	return columns, nil
}

func (repository *Repository) Imports(ctx context.Context, datasetID uint64) ([]Import, error) {
	imports := make([]Import, 0)
	if err := repository.db.SelectContext(ctx, &imports, `SELECT id,dataset_id,upload_id,delimiter,header_record_number,mode,schema_revision,generation_id,status,phase,attempt,published_attempt,owner_id,claimed_at,heartbeat_at,source_records,staged_rows,failure_class,failure_message,diagnostics,diagnostics_truncated,submitted_by_user_id,started_at,finished_at,created_at,updated_at FROM custom_dataset_imports WHERE dataset_id=? ORDER BY created_at DESC,id DESC`, datasetID); err != nil {
		return nil, fmt.Errorf("list custom dataset imports: %w", err)
	}
	return imports, nil
}

func (repository *Repository) Sample(ctx context.Context, dataset Dataset, limit int) ([][]SampleCell, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid custom dataset sample limit")
	}
	rows, err := repository.db.QueryxContext(ctx, `SELECT * FROM `+quoteIdentifier(dataset.ViewName())+` LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sample custom dataset: %w", err)
	}
	defer rows.Close()
	result := make([][]SampleCell, 0, limit)
	for rows.Next() {
		values, err := rows.SliceScan()
		if err != nil {
			return nil, err
		}
		row := make([]SampleCell, len(values))
		for index, value := range values {
			if value == nil {
				row[index].Null = true
				continue
			}
			switch typed := value.(type) {
			case []byte:
				row[index].Value = excerpt(string(typed), 256)
			case time.Time:
				row[index].Value = typed.UTC().Format("2006-01-02 15:04:05")
			default:
				row[index].Value = excerpt(fmt.Sprint(typed), 256)
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (repository *Repository) Submit(ctx context.Context, requester securityctx.Requester, input Submission, now time.Time) (Dataset, Import, error) {
	if err := validateSubmission(input); err != nil {
		return Dataset{}, Import{}, err
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return Dataset{}, Import{}, err
	}
	defer tx.Rollback()

	var dataset Dataset
	created := input.DatasetID == 0
	if created {
		result, err := tx.ExecContext(ctx, `INSERT INTO custom_datasets (name,description,status,revision,schema_revision,row_count,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES (?,?,'provisioning',1,1,0,?,?,?,?)`, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), requester.Effective.UserID, requester.Effective.UserID, now.UTC(), now.UTC())
		if err != nil {
			return Dataset{}, Import{}, fmt.Errorf("create custom dataset: %w", err)
		}
		id, _ := result.LastInsertId()
		dataset.ID, dataset.Status, dataset.Revision, dataset.SchemaRevision = uint64(id), DatasetProvisioning, 1, 1
		dataset.Name, dataset.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
		if err := replaceColumns(ctx, tx, dataset.ID, input.Columns); err != nil {
			return Dataset{}, Import{}, err
		}
	} else {
		if err := tx.GetContext(ctx, &dataset, `SELECT id,name,description,status,revision,schema_revision,current_generation_id,row_count,created_by_user_id,updated_by_user_id,activated_at,archived_at,created_at,updated_at FROM custom_datasets WHERE id=? FOR UPDATE`, input.DatasetID); err != nil {
			return Dataset{}, Import{}, notFound(err)
		}
		if dataset.Status == DatasetArchived {
			return Dataset{}, Import{}, fmt.Errorf("%w: archived dataset is terminal", ErrInactive)
		}
		if input.DatasetRevision != dataset.Revision {
			return Dataset{}, Import{}, ErrConflict
		}
		if dataset.Status == DatasetProvisioning {
			var successful, active int
			if err := tx.GetContext(ctx, &successful, `SELECT COUNT(*) FROM custom_dataset_imports WHERE dataset_id=? AND status='succeeded'`, dataset.ID); err != nil {
				return Dataset{}, Import{}, err
			}
			if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM custom_dataset_imports WHERE dataset_id=? AND status IN ('queued','running')`, dataset.ID); err != nil {
				return Dataset{}, Import{}, err
			}
			if successful != 0 || dataset.CurrentGenerationID != nil {
				return Dataset{}, Import{}, fmt.Errorf("%w: published schema is frozen", ErrConflict)
			}
			if active != 0 {
				return Dataset{}, Import{}, fmt.Errorf("%w: an import is already active", ErrConflict)
			}
			dataset.SchemaRevision++
			dataset.Revision++
			name, description := strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
			if name == "" {
				name, description = dataset.Name, dataset.Description
			}
			if _, err := tx.ExecContext(ctx, `UPDATE custom_datasets SET name=?,description=?,schema_revision=?,revision=?,updated_by_user_id=?,updated_at=? WHERE id=?`, name, description, dataset.SchemaRevision, dataset.Revision, requester.Effective.UserID, now.UTC(), dataset.ID); err != nil {
				return Dataset{}, Import{}, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM custom_dataset_columns WHERE dataset_id=?`, dataset.ID); err != nil {
				return Dataset{}, Import{}, err
			}
			if err := replaceColumns(ctx, tx, dataset.ID, input.Columns); err != nil {
				return Dataset{}, Import{}, err
			}
		} else {
			if input.Name != "" && (strings.TrimSpace(input.Name) != dataset.Name || strings.TrimSpace(input.Description) != dataset.Description) {
				return Dataset{}, Import{}, fmt.Errorf("%w: import cannot change dataset metadata", ErrInvalid)
			}
			current, err := columnsInTx(ctx, tx, dataset.ID)
			if err != nil {
				return Dataset{}, Import{}, err
			}
			if !sameColumns(current, input.Columns) {
				return Dataset{}, Import{}, fmt.Errorf("%w: active dataset schema is frozen", ErrInvalid)
			}
		}
	}

	var uploadStatus string
	if err := tx.GetContext(ctx, &uploadStatus, `SELECT status FROM custom_dataset_uploads WHERE id=? FOR UPDATE`, input.UploadID); err != nil {
		return Dataset{}, Import{}, fmt.Errorf("%w: upload is unavailable", ErrInvalid)
	}
	if uploadStatus == "expired" || (created || dataset.Status == DatasetActive) && uploadStatus != "uploaded" {
		return Dataset{}, Import{}, fmt.Errorf("%w: active and new datasets require a new upload", ErrInvalid)
	}
	if !created && dataset.Status == DatasetProvisioning && uploadStatus == "retained" {
		var associated int
		if err := tx.GetContext(ctx, &associated, `SELECT COUNT(*) FROM custom_dataset_imports WHERE upload_id=? AND dataset_id=?`, input.UploadID, dataset.ID); err != nil {
			return Dataset{}, Import{}, err
		}
		if associated == 0 {
			return Dataset{}, Import{}, fmt.Errorf("%w: retained upload belongs to another dataset", ErrInvalid)
		}
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO custom_dataset_imports (dataset_id,upload_id,delimiter,header_record_number,mode,schema_revision,status,phase,attempt,submitted_by_user_id,created_at,updated_at) VALUES (?,?,?,?,?,?,'queued','queued',0,?,?,?)`, dataset.ID, input.UploadID, input.Delimiter, input.HeaderRecordNumber, input.Mode, dataset.SchemaRevision, requester.Effective.UserID, now.UTC(), now.UTC())
	if err != nil {
		if duplicate(err) {
			return Dataset{}, Import{}, fmt.Errorf("%w: an import is already active", ErrConflict)
		}
		return Dataset{}, Import{}, fmt.Errorf("submit custom dataset import: %w", err)
	}
	importID, _ := result.LastInsertId()
	generationID := uint64(importID)
	if input.Mode == ModeAppend {
		if dataset.Status != DatasetActive || dataset.CurrentGenerationID == nil {
			return Dataset{}, Import{}, fmt.Errorf("%w: append requires an active dataset", ErrInactive)
		}
		generationID = *dataset.CurrentGenerationID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE custom_dataset_imports SET generation_id=? WHERE id=?`, generationID, importID); err != nil {
		return Dataset{}, Import{}, err
	}
	if result, err := tx.ExecContext(ctx, `UPDATE custom_dataset_uploads SET status='retained',retained_at=COALESCE(retained_at,?),expires_at=NULL,revision=revision+1 WHERE id=? AND status='uploaded'`, now.UTC(), input.UploadID); err != nil {
		return Dataset{}, Import{}, err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		var status string
		if err := tx.GetContext(ctx, &status, `SELECT status FROM custom_dataset_uploads WHERE id=?`, input.UploadID); err != nil || status != "retained" {
			return Dataset{}, Import{}, fmt.Errorf("%w: upload is unavailable", ErrInvalid)
		}
	}
	if created {
		metadata := audit.CustomDatasetMetadata{DatasetID: dataset.ID, UploadID: input.UploadID, SchemaRevision: dataset.SchemaRevision, Outcome: "provisioning"}
		if err := appendAudit(ctx, tx, requester, audit.ActionCustomDatasetProvisioned, audit.ResourceCustomDataset, dataset.ID, metadata, now); err != nil {
			return Dataset{}, Import{}, err
		}
	} else if dataset.Status == DatasetProvisioning {
		metadata := audit.CustomDatasetMetadata{DatasetID: dataset.ID, UploadID: input.UploadID, SchemaRevision: dataset.SchemaRevision, Outcome: "revised"}
		if err := appendAudit(ctx, tx, requester, audit.ActionCustomDatasetSchemaRevised, audit.ResourceCustomDataset, dataset.ID, metadata, now); err != nil {
			return Dataset{}, Import{}, err
		}
	}
	metadata := audit.CustomDatasetMetadata{DatasetID: dataset.ID, UploadID: input.UploadID, ImportID: uint64(importID), SchemaRevision: dataset.SchemaRevision, Mode: string(input.Mode), Outcome: "submitted"}
	if err := appendAudit(ctx, tx, requester, audit.ActionCustomDatasetImportSubmitted, audit.ResourceCustomDatasetImport, uint64(importID), metadata, now); err != nil {
		return Dataset{}, Import{}, err
	}
	if err := tx.Commit(); err != nil {
		return Dataset{}, Import{}, err
	}
	dataset, err = repository.Find(ctx, dataset.ID)
	if err != nil {
		return Dataset{}, Import{}, err
	}
	job, err := repository.FindImport(ctx, uint64(importID))
	return dataset, job, err
}

func (repository *Repository) FindImport(ctx context.Context, id uint64) (Import, error) {
	var value Import
	if err := repository.db.GetContext(ctx, &value, `SELECT id,dataset_id,upload_id,delimiter,header_record_number,mode,schema_revision,generation_id,status,phase,attempt,published_attempt,owner_id,claimed_at,heartbeat_at,source_records,staged_rows,failure_class,failure_message,diagnostics,diagnostics_truncated,submitted_by_user_id,started_at,finished_at,created_at,updated_at FROM custom_dataset_imports WHERE id=?`, id); err != nil {
		return Import{}, notFound(err)
	}
	return value, nil
}

func (repository *Repository) UpdateMetadata(ctx context.Context, requester securityctx.Requester, id, revision uint64, name, description string, now time.Time) error {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" || len(name) > 128 || len(description) > 1000 {
		return fmt.Errorf("%w: invalid dataset metadata", ErrInvalid)
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE custom_datasets SET name=?,description=?,revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=? AND status<>'archived'`, name, description, requester.Effective.UserID, now.UTC(), id, revision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionCustomDatasetUpdated, audit.ResourceCustomDataset, id, audit.CustomDatasetMetadata{DatasetID: id, Outcome: "updated"}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (repository *Repository) Archive(ctx context.Context, requester securityctx.Requester, id, revision uint64, now time.Time) error {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE custom_datasets SET status='archived',archived_at=?,revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=? AND status<>'archived' AND NOT EXISTS (SELECT 1 FROM custom_dataset_imports i WHERE i.dataset_id=custom_datasets.id AND i.status IN ('queued','running'))`, now.UTC(), requester.Effective.UserID, now.UTC(), id, revision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionCustomDatasetArchived, audit.ResourceCustomDataset, id, audit.CustomDatasetMetadata{DatasetID: id, Outcome: "archived"}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (repository *Repository) ReservedWords(ctx context.Context) (map[string]struct{}, error) {
	words := make(map[string]struct{})
	var rows []string
	if err := repository.db.SelectContext(ctx, &rows, `SELECT WORD FROM information_schema.KEYWORDS WHERE RESERVED=1`); err != nil {
		return nil, fmt.Errorf("load MySQL reserved words: %w", err)
	}
	for _, word := range rows {
		words[strings.ToLower(word)] = struct{}{}
	}
	return words, nil
}

func validateSubmission(input Submission) error {
	if input.UploadID == 0 || input.HeaderRecordNumber == 0 || len(input.Columns) == 0 || len(input.Columns) > MaxColumns {
		return fmt.Errorf("%w: incomplete import configuration", ErrInvalid)
	}
	if _, err := input.Delimiter.Rune(); err != nil {
		return err
	}
	if input.Mode != ModeReplace && input.Mode != ModeAppend {
		return fmt.Errorf("%w: invalid import mode", ErrInvalid)
	}
	if input.DatasetID == 0 && (strings.TrimSpace(input.Name) == "" || len(strings.TrimSpace(input.Name)) > 128 || len(strings.TrimSpace(input.Description)) > 1000) {
		return fmt.Errorf("%w: invalid dataset metadata", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(input.Columns))
	for index, column := range input.Columns {
		if int(column.Ordinal) != index+1 || column.PhysicalName != fmt.Sprintf("c%03d", index+1) || strings.TrimSpace(column.DisplayName) == "" || column.QueryName == "" || len(column.QueryName) > 64 {
			return fmt.Errorf("%w: invalid column %d", ErrInvalid, index+1)
		}
		if _, exists := seen[column.QueryName]; exists {
			return fmt.Errorf("%w: duplicate query name", ErrInvalid)
		}
		seen[column.QueryName] = struct{}{}
		if _, err := sqlType(column); err != nil {
			return err
		}
	}
	return nil
}

func replaceColumns(ctx context.Context, tx *sqlx.Tx, datasetID uint64, columns []Column) error {
	for _, column := range columns {
		if _, err := tx.ExecContext(ctx, `INSERT INTO custom_dataset_columns (dataset_id,ordinal,display_name,query_name,physical_name,logical_type,date_format) VALUES (?,?,?,?,?,?,?)`, datasetID, column.Ordinal, column.DisplayName, column.QueryName, column.PhysicalName, column.LogicalType, column.DateFormat); err != nil {
			return fmt.Errorf("store custom dataset column %d: %w", column.Ordinal, err)
		}
	}
	return nil
}

func columnsInTx(ctx context.Context, tx *sqlx.Tx, datasetID uint64) ([]Column, error) {
	columns := make([]Column, 0, MaxColumns)
	if err := tx.SelectContext(ctx, &columns, `SELECT dataset_id,ordinal,display_name,query_name,physical_name,logical_type,date_format FROM custom_dataset_columns WHERE dataset_id=? ORDER BY ordinal`, datasetID); err != nil {
		return nil, err
	}
	return columns, nil
}

func sameColumns(left, right []Column) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Ordinal != right[index].Ordinal || left[index].DisplayName != right[index].DisplayName || left[index].QueryName != right[index].QueryName || left[index].PhysicalName != right[index].PhysicalName || left[index].LogicalType != right[index].LogicalType || !sameStringPointer(left[index].DateFormat, right[index].DateFormat) {
			return false
		}
	}
	return true
}

func sameStringPointer(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func duplicate(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func appendAudit(ctx context.Context, executor sqlx.ExtContext, requester securityctx.Requester, action audit.Action, resource audit.ResourceType, id uint64, metadata audit.Metadata, now time.Time) error {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Append(ctx, executor, audit.Event{Attribution: audit.Attribution{Actor: &actor, Effective: &effective}, Action: action, Resource: resource, ResourceID: id, Metadata: metadata, CreatedAt: now.UTC()})
}

func encodeDiagnostics(diagnostics []Diagnostic) []byte {
	encoded, _ := json.Marshal(diagnostics)
	return encoded
}
