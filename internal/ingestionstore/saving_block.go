package ingestionstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

// savingBlockTransactions is parentless: its staged candidate for one run is
// the entire intended final table, so publication is a global exact set.
var savingBlockTransactions = detailChildSpec{
	key: ingestion.SavingBlockChildKey, table: "fincloud_saving_block_transactions", stage: "stg_fincloud_saving_block_transactions", fetched: true,
	fields: []detailColumn{
		detailCol("transaction_id"), detailCol("transaction_date"), detailCol("customer_name"), detailCol("block_date"), detailCol("block_end_date"),
		detailCol("currency"), detailCol("description"), detailCol("block_reason"), detailCol("transaction_type"), detailDecimal("blocked_amount", 6),
		detailCol("officer"), detailCol("not_batch"), detailCol("is_open"),
	},
}

const savingBlockJobKey = "saving_block_detail"

// detailRootStages are the staging tables whose run-scoped deletion clears a
// Detail candidate; child stages cascade from their parent stage.
func detailRootStages() []string {
	stages := make([]string, 0, len(detailSpecifications)+1)
	for _, specification := range detailSpecifications {
		stages = append(stages, specification.stage)
	}
	return append(stages, savingBlockTransactions.stage)
}

// StageSavingBlockTransactions replaces one account's staged block items for
// this run. Zero items is a valid outcome that leaves no staged rows.
func (repository *DetailRepository) StageSavingBlockTransactions(ctx context.Context, runID uint64, accountNo string, fetchedAt time.Time, items []ingestion.DetailChildRecord) error {
	if repository == nil || repository.db == nil || runID == 0 || accountNo == "" || fetchedAt.IsZero() {
		return fmt.Errorf("complete staged saving block detail is required")
	}
	err := retryReplaySafeTx(ctx, repository.db, "stage_detail", func(tx *sqlx.Tx) error {
		return stageDetailChildren(ctx, tx, runID, accountNo, fetchedAt, savingBlockTransactions, items)
	})
	return wrapDatabaseError(err, "stage_detail", "replace_staged_detail", savingBlockTransactions.stage, 0, 0)
}

// PublishSavingBlockTransactions atomically replaces the whole final table
// with this run's candidate and finishes the run in the same transaction.
func (repository *DetailRepository) PublishSavingBlockTransactions(ctx context.Context, runID uint64, ownerID string, expected uint64) error {
	if repository == nil || repository.db == nil || runID == 0 || ownerID == "" {
		return fmt.Errorf("complete detail publication identity is required")
	}
	child := savingBlockTransactions
	return repository.publishCommitted(ctx, runID, child.table, func(tx *sqlx.Tx) error {
		if err := requirePublishableDetailRun(ctx, tx, runID, ownerID, savingBlockJobKey); err != nil {
			return err
		}
		var staged uint64
		if err := tx.GetContext(ctx, &staged, "SELECT COUNT(*) FROM `"+child.stage+"` WHERE ingestion_run_id=?", runID); err != nil {
			return wrapDatabaseError(err, "publish_detail", "count_staged_children", child.stage, 0, 0)
		}
		if staged != expected {
			return fmt.Errorf("staged Detail row count %d does not match expected %d", staged, expected)
		}
		deleteMissing := "DELETE current_child FROM `" + child.table + "` current_child LEFT JOIN `" + child.stage +
			"` candidate_child ON candidate_child.ingestion_run_id=? AND candidate_child.account_no=current_child.account_no AND candidate_child.item_index=current_child.item_index" +
			" WHERE candidate_child.account_no IS NULL"
		if _, err := tx.ExecContext(ctx, deleteMissing, runID); err != nil {
			return wrapDatabaseError(err, "publish_detail", "delete_missing_children", child.table, 0, 0)
		}
		if err := upsertDetailChildren(ctx, tx, runID, child); err != nil {
			return err
		}
		if err := ingestionrun.FinishSucceededInTx(ctx, tx, runID, ownerID); err != nil {
			return wrapDatabaseError(err, "publish_detail", "finish_published_run", "ingestion_runs", 0, 0)
		}
		return nil
	})
}
