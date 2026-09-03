package ingestionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

type MasterRepository struct{ db *sqlx.DB }

func NewMasterRepository(db *sqlx.DB) *MasterRepository { return &MasterRepository{db: db} }

func (repository *MasterRepository) PrepareRun(ctx context.Context, runID uint64) error {
	return repository.clearRun(ctx, runID, "prepare_master_staging")
}

func (repository *MasterRepository) CleanupRun(ctx context.Context, runID uint64) error {
	return repository.clearRun(ctx, runID, "cleanup_master_staging")
}

func (repository *MasterRepository) clearRun(ctx context.Context, runID uint64, operation string) error {
	if repository == nil || repository.db == nil || runID == 0 {
		return fmt.Errorf("Master staging cleanup requires a run")
	}
	return retryReplaySafeTx(ctx, repository.db, operation, func(tx *sqlx.Tx) error {
		for _, table := range []string{"stg_fincloud_reference_items", "stg_fincloud_reference_categories", "stg_fincloud_marketing_master"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM `"+table+"` WHERE ingestion_run_id=?", runID); err != nil {
				return wrapDatabaseError(err, operation, "delete_run_staging", table, 0, 0)
			}
		}
		return nil
	})
}

func (repository *MasterRepository) StageReference(ctx context.Context, runID uint64, candidate ingestion.ReferenceCandidate, fetchedAt time.Time) error {
	if repository == nil || repository.db == nil || runID == 0 || fetchedAt.IsZero() {
		return fmt.Errorf("complete reference staging identity is required")
	}
	return retryReplaySafeTx(ctx, repository.db, "stage_reference_master", func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM stg_fincloud_reference_items WHERE ingestion_run_id=?`, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM stg_fincloud_reference_categories WHERE ingestion_run_id=?`, runID); err != nil {
			return err
		}
		for _, category := range candidate.Categories {
			if _, err := tx.ExecContext(ctx, `INSERT INTO stg_fincloud_reference_categories
				(ingestion_run_id,domain,category_key,source_shape,source_item_count,item_count,discarded_blank_count,category_checksum,last_fetched_at)
				VALUES (?,?,?,?,?,?,?,?,?)`, runID, candidate.Domain, category.Key, category.Shape, category.SourceItemCount, category.ItemCount,
				category.DiscardedBlankCount, ingestion.ChecksumHex(category.Checksum), fetchedAt.UTC()); err != nil {
				return err
			}
			for _, item := range category.Items {
				if _, err := tx.ExecContext(ctx, `INSERT INTO stg_fincloud_reference_items
					(ingestion_run_id,domain,category_key,source_ordinal,code,description,raw_item_payload,item_checksum,last_fetched_at)
					VALUES (?,?,?,?,?,?,?,?,?)`, runID, candidate.Domain, category.Key, item.SourceOrdinal, item.Code, item.Description,
					string(item.RawItemPayload), ingestion.ChecksumHex(item.Checksum), fetchedAt.UTC()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (repository *MasterRepository) StageMarketing(ctx context.Context, runID uint64, candidate ingestion.MarketingCandidate, fetchedAt time.Time) error {
	if repository == nil || repository.db == nil || runID == 0 || fetchedAt.IsZero() {
		return fmt.Errorf("complete Marketing staging identity is required")
	}
	return retryReplaySafeTx(ctx, repository.db, "stage_marketing_master", func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM stg_fincloud_marketing_master WHERE ingestion_run_id=?`, runID); err != nil {
			return err
		}
		for _, entity := range candidate.Entities {
			if _, err := tx.ExecContext(ctx, `INSERT INTO stg_fincloud_marketing_master
				(ingestion_run_id,marketing_id,marketing_name,location_name,active_status,document_status,source_transaction_at,raw_payload,raw_checksum,last_fetched_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, runID, entity.ID, entity.Name, entity.LocationName, entity.ActiveStatus, entity.DocumentStatus,
				entity.SourceTransactionAt, string(entity.RawPayload), ingestion.ChecksumHex(entity.Checksum), fetchedAt.UTC()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (repository *MasterRepository) PublishReference(ctx context.Context, runID uint64, ownerID, jobKey string, expected ingestion.ReferenceCandidate) error {
	if repository == nil || repository.db == nil || runID == 0 || ownerID == "" {
		return fmt.Errorf("complete reference publication identity is required")
	}
	wantJob := map[ingestion.ReferenceDomain]string{ingestion.ReferenceCIF: "cif_reference_master", ingestion.ReferenceSaving: "saving_reference_master", ingestion.ReferenceTimeDeposit: "time_deposit_reference_master", ingestion.ReferenceLoan: "loan_reference_master"}[expected.Domain]
	if wantJob == "" || jobKey != wantJob {
		return fmt.Errorf("reference Master job/domain mismatch")
	}
	err := retryReplaySafeTx(ctx, repository.db, "publish_reference_master", func(tx *sqlx.Tx) error {
		if err := fenceMasterRun(ctx, tx, runID, ownerID, jobKey); err != nil {
			return err
		}
		if err := verifyStagedReference(ctx, tx, runID, expected); err != nil {
			return err
		}
		if err := reconcileReference(ctx, tx, runID, expected.Domain); err != nil {
			return err
		}
		return ingestionrun.FinishSucceededInTx(ctx, tx, runID, ownerID)
	})
	return repository.resolvePublication(ctx, runID, err, "publish reference master")
}

func (repository *MasterRepository) PublishMarketing(ctx context.Context, runID uint64, ownerID, jobKey string, expected ingestion.MarketingCandidate) error {
	if repository == nil || repository.db == nil || runID == 0 || ownerID == "" {
		return fmt.Errorf("complete Marketing publication identity is required")
	}
	if jobKey != "marketing_master" {
		return fmt.Errorf("unexpected Marketing Master job")
	}
	err := retryReplaySafeTx(ctx, repository.db, "publish_marketing_master", func(tx *sqlx.Tx) error {
		if err := fenceMasterRun(ctx, tx, runID, ownerID, jobKey); err != nil {
			return err
		}
		if err := verifyStagedMarketing(ctx, tx, runID, expected); err != nil {
			return err
		}
		if err := reconcileMarketing(ctx, tx, runID); err != nil {
			return err
		}
		return ingestionrun.FinishSucceededInTx(ctx, tx, runID, ownerID)
	})
	return repository.resolvePublication(ctx, runID, err, "publish Marketing master")
}

func (repository *MasterRepository) resolvePublication(ctx context.Context, runID uint64, publicationErr error, operation string) error {
	if publicationErr == nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var status string
	if err := repository.db.GetContext(checkCtx, &status, `SELECT status FROM ingestion_runs WHERE id=?`, runID); err == nil && status == string(ingestionrun.StatusSucceeded) {
		return nil
	} else if err != nil {
		publicationErr = errors.Join(publicationErr, err)
	}
	return fmt.Errorf("%s: %w", operation, publicationErr)
}

func fenceMasterRun(ctx context.Context, tx *sqlx.Tx, runID uint64, ownerID, jobKey string) error {
	var run struct {
		JobKey          string `db:"job_key"`
		Status          string `db:"status"`
		OwnerID         string `db:"owner_id"`
		CancelRequested bool   `db:"cancel_requested"`
	}
	if err := tx.GetContext(ctx, &run, `SELECT COALESCE(job_key,'') job_key,status,COALESCE(owner_id,'') owner_id,
		cancel_requested_at IS NOT NULL cancel_requested FROM ingestion_runs WHERE id=?`, runID); err != nil {
		return err
	}
	if run.JobKey != jobKey || run.Status != string(ingestionrun.StatusRunning) || run.OwnerID != ownerID || run.CancelRequested {
		return fmt.Errorf("Master candidate is not complete and publishable")
	}
	return nil
}

type stagedReferenceCategory struct {
	Domain              ingestion.ReferenceDomain `db:"domain"`
	Key                 string                    `db:"category_key"`
	Shape               ingestion.ReferenceShape  `db:"source_shape"`
	SourceItemCount     uint64                    `db:"source_item_count"`
	ItemCount           uint64                    `db:"item_count"`
	DiscardedBlankCount uint64                    `db:"discarded_blank_count"`
	Checksum            string                    `db:"category_checksum"`
}
type stagedReferenceItem struct {
	Domain      ingestion.ReferenceDomain `db:"domain"`
	CategoryKey string                    `db:"category_key"`
	Ordinal     uint64                    `db:"source_ordinal"`
	Code        string                    `db:"code"`
	Description string                    `db:"description"`
	Raw         []byte                    `db:"raw_item_payload"`
	Checksum    string                    `db:"item_checksum"`
}

func verifyStagedReference(ctx context.Context, tx *sqlx.Tx, runID uint64, expected ingestion.ReferenceCandidate) error {
	var rows []stagedReferenceCategory
	if err := tx.SelectContext(ctx, &rows, `SELECT domain,category_key,source_shape,source_item_count,item_count,discarded_blank_count,category_checksum
		FROM stg_fincloud_reference_categories WHERE ingestion_run_id=? ORDER BY BINARY domain,BINARY category_key`, runID); err != nil {
		return err
	}
	var unexpected int
	if err := tx.GetContext(ctx, &unexpected, `SELECT COUNT(*) FROM stg_fincloud_reference_items i LEFT JOIN stg_fincloud_reference_categories c
		ON c.ingestion_run_id=i.ingestion_run_id AND c.domain=i.domain AND c.category_key=i.category_key
		WHERE i.ingestion_run_id=? AND c.category_key IS NULL`, runID); err != nil || unexpected != 0 {
		if err != nil {
			return err
		}
		return fmt.Errorf("staged reference candidate has %d orphan items", unexpected)
	}
	if len(rows) != len(expected.Categories) {
		return fmt.Errorf("staged category count %d does not match expected %d", len(rows), len(expected.Categories))
	}
	verified := make([]ingestion.ReferenceCategory, 0, len(rows))
	var totalItems, totalBlanks uint64
	for _, row := range rows {
		if row.Domain != expected.Domain {
			return fmt.Errorf("unexpected staged reference domain %q", row.Domain)
		}
		var itemRows []stagedReferenceItem
		if err := tx.SelectContext(ctx, &itemRows, `SELECT domain,category_key,source_ordinal,code,description,raw_item_payload,item_checksum
			FROM stg_fincloud_reference_items WHERE ingestion_run_id=? AND domain=? AND category_key=? ORDER BY source_ordinal`, runID, row.Domain, row.Key); err != nil {
			return err
		}
		category := ingestion.ReferenceCategory{Key: row.Key, Shape: row.Shape, SourceItemCount: row.SourceItemCount, ItemCount: row.ItemCount, DiscardedBlankCount: row.DiscardedBlankCount}
		if uint64(len(itemRows)) != category.ItemCount || category.SourceItemCount != category.ItemCount+category.DiscardedBlankCount {
			return fmt.Errorf("staged category %s counts are inconsistent", row.Key)
		}
		for _, stored := range itemRows {
			if stored.Domain != expected.Domain || stored.CategoryKey != row.Key || stored.Ordinal >= row.SourceItemCount || strings.TrimSpace(stored.Code) == "" {
				return fmt.Errorf("staged category %s item identity is inconsistent", row.Key)
			}
			canonical, err := ingestion.CanonicalJSON(stored.Raw)
			if err != nil {
				return err
			}
			if err := verifyReferenceTypedRaw(row.Shape, stored.Code, stored.Description, canonical); err != nil {
				return fmt.Errorf("staged category %s ordinal %d: %w", row.Key, stored.Ordinal, err)
			}
			checksum := ingestion.ReferenceItemChecksum(row.Shape, stored.Code, stored.Description, canonical)
			if stored.Checksum != ingestion.ChecksumHex(checksum) {
				return fmt.Errorf("staged category %s item checksum mismatch", row.Key)
			}
			category.Items = append(category.Items, ingestion.ReferenceItem{SourceOrdinal: stored.Ordinal, Code: stored.Code, Description: stored.Description, RawItemPayload: canonical, Checksum: checksum})
		}
		category.Checksum = ingestion.ReferenceCategoryChecksum(category)
		if row.Checksum != ingestion.ChecksumHex(category.Checksum) {
			return fmt.Errorf("staged category %s checksum mismatch", row.Key)
		}
		totalItems += category.ItemCount
		totalBlanks += category.DiscardedBlankCount
		verified = append(verified, category)
	}
	checksum := ingestion.ReferenceDatasetChecksum(verified)
	if totalItems != expected.ItemCount || totalBlanks != expected.DiscardedBlankCount || checksum != expected.Checksum {
		return fmt.Errorf("staged reference dataset does not match executor candidate")
	}
	return nil
}

func verifyReferenceTypedRaw(shape ingestion.ReferenceShape, code, description string, raw []byte) error {
	switch shape {
	case ingestion.ShapeIDDescription:
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return fmt.Errorf("raw item is not an object")
		}
		var rawCode, rawDescription string
		if json.Unmarshal(value["id"], &rawCode) != nil || json.Unmarshal(value["descr"], &rawDescription) != nil || rawCode != code || rawDescription != description {
			return fmt.Errorf("typed fields differ from raw item")
		}
	case ingestion.ShapeStringArray:
		var value string
		if json.Unmarshal(raw, &value) != nil || value != code || value != description {
			return fmt.Errorf("typed fields differ from raw string")
		}
	default:
		return fmt.Errorf("items cannot use source shape %q", shape)
	}
	return nil
}

type stagedMarketing struct {
	ID          string `db:"marketing_id"`
	Name        string `db:"marketing_name"`
	Location    string `db:"location_name"`
	Active      string `db:"active_status"`
	Document    string `db:"document_status"`
	Transaction string `db:"source_transaction_at"`
	Raw         []byte `db:"raw_payload"`
	Checksum    string `db:"raw_checksum"`
}

func verifyStagedMarketing(ctx context.Context, tx *sqlx.Tx, runID uint64, expected ingestion.MarketingCandidate) error {
	var rows []stagedMarketing
	if err := tx.SelectContext(ctx, &rows, `SELECT marketing_id,marketing_name,location_name,active_status,document_status,source_transaction_at,raw_payload,raw_checksum
		FROM stg_fincloud_marketing_master WHERE ingestion_run_id=? ORDER BY BINARY marketing_id`, runID); err != nil {
		return err
	}
	if len(rows) != len(expected.Entities) {
		return fmt.Errorf("staged Marketing count %d does not match expected %d", len(rows), len(expected.Entities))
	}
	entities := make([]ingestion.MarketingEntity, 0, len(rows))
	for _, row := range rows {
		canonical, err := ingestion.CanonicalJSON(row.Raw)
		if err != nil {
			return err
		}
		entity := ingestion.MarketingEntity{ID: row.ID, Name: row.Name, LocationName: row.Location, ActiveStatus: row.Active, DocumentStatus: row.Document, SourceTransactionAt: row.Transaction, RawPayload: canonical}
		if err := verifyMarketingTypedRaw(entity); err != nil {
			return err
		}
		entity.Checksum = ingestion.MarketingEntityChecksum(entity)
		if row.Checksum != ingestion.ChecksumHex(entity.Checksum) {
			return fmt.Errorf("staged Marketing entity %s checksum mismatch", boundedIdentity(row.ID))
		}
		entities = append(entities, entity)
	}
	if ingestion.MarketingDatasetChecksum(entities) != expected.Checksum {
		return fmt.Errorf("staged Marketing dataset does not match executor candidate")
	}
	return nil
}

func verifyMarketingTypedRaw(entity ingestion.MarketingEntity) error {
	var value map[string]json.RawMessage
	if json.Unmarshal(entity.RawPayload, &value) != nil || value == nil {
		return fmt.Errorf("Marketing raw payload is not an object")
	}
	for _, field := range []struct{ name, value string }{{"id", entity.ID}, {"nama_marketing", entity.Name}, {"locationname", entity.LocationName}, {"aktif", entity.ActiveStatus}, {"status_dokumen", entity.DocumentStatus}, {"tgltransaksi", entity.SourceTransactionAt}} {
		var got string
		if json.Unmarshal(value[field.name], &got) != nil || got != field.value {
			return fmt.Errorf("Marketing typed field %s differs from raw payload", field.name)
		}
	}
	return nil
}

func boundedIdentity(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:4] + "…" + value[len(value)-4:]
}

func reconcileReference(ctx context.Context, tx *sqlx.Tx, runID uint64, domain ingestion.ReferenceDomain) error {
	queries := []struct {
		query string
		args  []any
	}{
		{`DELETE c FROM fincloud_reference_categories c LEFT JOIN stg_fincloud_reference_categories s ON s.ingestion_run_id=? AND s.domain=c.domain AND s.category_key=c.category_key WHERE c.domain=? AND s.category_key IS NULL`, []any{runID, domain}},
		{`UPDATE fincloud_reference_categories c JOIN stg_fincloud_reference_categories s ON s.ingestion_run_id=? AND s.domain=c.domain AND s.category_key=c.category_key SET c.last_fetched_at=s.last_fetched_at,c.updated_at=c.updated_at WHERE c.domain=? AND c.category_checksum=s.category_checksum`, []any{runID, domain}},
		{`UPDATE fincloud_reference_categories c JOIN stg_fincloud_reference_categories s ON s.ingestion_run_id=? AND s.domain=c.domain AND s.category_key=c.category_key SET c.source_shape=s.source_shape,c.source_item_count=s.source_item_count,c.item_count=s.item_count,c.discarded_blank_count=s.discarded_blank_count,c.category_checksum=s.category_checksum,c.last_fetched_at=s.last_fetched_at WHERE c.domain=? AND c.category_checksum<>s.category_checksum`, []any{runID, domain}},
		{`INSERT INTO fincloud_reference_categories (domain,category_key,source_shape,source_item_count,item_count,discarded_blank_count,category_checksum,last_fetched_at) SELECT s.domain,s.category_key,s.source_shape,s.source_item_count,s.item_count,s.discarded_blank_count,s.category_checksum,s.last_fetched_at FROM stg_fincloud_reference_categories s LEFT JOIN fincloud_reference_categories c ON c.domain=s.domain AND c.category_key=s.category_key WHERE s.ingestion_run_id=? AND s.domain=? AND c.category_key IS NULL`, []any{runID, domain}},
		{`DELETE i FROM fincloud_reference_items i LEFT JOIN stg_fincloud_reference_items s ON s.ingestion_run_id=? AND s.domain=i.domain AND s.category_key=i.category_key AND s.source_ordinal=i.source_ordinal WHERE i.domain=? AND s.category_key IS NULL`, []any{runID, domain}},
		{`UPDATE fincloud_reference_items i JOIN stg_fincloud_reference_items s ON s.ingestion_run_id=? AND s.domain=i.domain AND s.category_key=i.category_key AND s.source_ordinal=i.source_ordinal SET i.last_fetched_at=s.last_fetched_at,i.updated_at=i.updated_at WHERE i.domain=? AND i.item_checksum=s.item_checksum`, []any{runID, domain}},
		{`UPDATE fincloud_reference_items i JOIN stg_fincloud_reference_items s ON s.ingestion_run_id=? AND s.domain=i.domain AND s.category_key=i.category_key AND s.source_ordinal=i.source_ordinal SET i.code=s.code,i.description=s.description,i.raw_item_payload=s.raw_item_payload,i.item_checksum=s.item_checksum,i.last_fetched_at=s.last_fetched_at WHERE i.domain=? AND i.item_checksum<>s.item_checksum`, []any{runID, domain}},
		{`INSERT INTO fincloud_reference_items (domain,category_key,source_ordinal,code,description,raw_item_payload,item_checksum,last_fetched_at) SELECT s.domain,s.category_key,s.source_ordinal,s.code,s.description,s.raw_item_payload,s.item_checksum,s.last_fetched_at FROM stg_fincloud_reference_items s LEFT JOIN fincloud_reference_items i ON i.domain=s.domain AND i.category_key=s.category_key AND i.source_ordinal=s.source_ordinal WHERE s.ingestion_run_id=? AND s.domain=? AND i.category_key IS NULL`, []any{runID, domain}},
	}
	for _, statement := range queries {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func reconcileMarketing(ctx context.Context, tx *sqlx.Tx, runID uint64) error {
	queries := []string{
		`DELETE m FROM fincloud_marketing_master m LEFT JOIN stg_fincloud_marketing_master s ON s.ingestion_run_id=? AND s.marketing_id=m.marketing_id WHERE s.marketing_id IS NULL`,
		`UPDATE fincloud_marketing_master m JOIN stg_fincloud_marketing_master s ON s.ingestion_run_id=? AND s.marketing_id=m.marketing_id SET m.last_fetched_at=s.last_fetched_at,m.updated_at=m.updated_at WHERE m.raw_checksum=s.raw_checksum`,
		`UPDATE fincloud_marketing_master m JOIN stg_fincloud_marketing_master s ON s.ingestion_run_id=? AND s.marketing_id=m.marketing_id SET m.marketing_name=s.marketing_name,m.location_name=s.location_name,m.active_status=s.active_status,m.document_status=s.document_status,m.source_transaction_at=s.source_transaction_at,m.raw_payload=s.raw_payload,m.raw_checksum=s.raw_checksum,m.last_fetched_at=s.last_fetched_at WHERE m.raw_checksum<>s.raw_checksum`,
		`INSERT INTO fincloud_marketing_master (marketing_id,marketing_name,location_name,active_status,document_status,source_transaction_at,raw_payload,raw_checksum,last_fetched_at) SELECT s.marketing_id,s.marketing_name,s.location_name,s.active_status,s.document_status,s.source_transaction_at,s.raw_payload,s.raw_checksum,s.last_fetched_at FROM stg_fincloud_marketing_master s LEFT JOIN fincloud_marketing_master m ON m.marketing_id=s.marketing_id WHERE s.ingestion_run_id=? AND m.marketing_id IS NULL`,
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, query, runID); err != nil {
			return err
		}
	}
	return nil
}

func (repository *MasterRepository) CleanupTerminal(ctx context.Context, limit int) (int64, error) {
	if repository == nil || repository.db == nil || limit < 1 {
		return 0, fmt.Errorf("positive Master staging cleanup limit is required")
	}
	var deleted int64
	err := retryReplaySafeTx(ctx, repository.db, "cleanup_master_staging", func(tx *sqlx.Tx) error {
		var attempt int64
		for _, table := range []string{"stg_fincloud_reference_items", "stg_fincloud_reference_categories", "stg_fincloud_marketing_master"} {
			query := "DELETE FROM `" + table + "` WHERE ingestion_run_id IN (SELECT ingestion_run_id FROM (SELECT DISTINCT s.ingestion_run_id FROM `" + table + "` s LEFT JOIN ingestion_runs r ON r.id=s.ingestion_run_id WHERE r.id IS NULL OR r.status IN ('succeeded','failed','skipped','cancelled','abandoned','completed','completed_with_skips') ORDER BY s.ingestion_run_id LIMIT ?) stale)"
			result, err := tx.ExecContext(ctx, query, limit)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			attempt += count
		}
		deleted = attempt
		return nil
	})
	return deleted, err
}
