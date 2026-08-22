package ingestionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

type DetailRepository struct{ db *sqlx.DB }

func NewDetailRepository(db *sqlx.DB) *DetailRepository { return &DetailRepository{db: db} }

type detailColumn struct {
	name       string
	identifier bool
	scale      int
}

type detailSpec struct {
	domain       ingestion.DetailDomain
	jobKey       string
	table, stage string
	fields       []detailColumn
	children     []detailChildSpec
}

type detailChildSpec struct {
	key, table, stage string
	fields            []detailColumn
}

var detailSpecifications = []detailSpec{
	{domain: ingestion.DetailCIF, jobKey: "cif_detail", table: "fincloud_cifs", stage: "stg_fincloud_cif_details", fields: []detailColumn{
		{"cif_no", true, 0}, {"customer_name", false, 0}, {"customer_type", false, 0}, {"identity_type", false, 0},
		{"ktp_no", false, 0}, {"birth_date", false, 0}, {"cif_open_date", false, 0}, {"record_created_at", false, 0},
	}},
	{domain: ingestion.DetailSaving, jobKey: "saving_detail", table: "fincloud_saving_details", stage: "stg_fincloud_saving_details", fields: []detailColumn{
		{"account_no", true, 0}, {"cif_no", false, 0}, {"account_name", false, 0}, {"location_id", false, 0},
		{"beginning_balance", false, 6}, {"balance", false, 6}, {"blocked_balance", false, 6},
		{"debit_mutation", false, 6}, {"credit_mutation", false, 6}, {"open_date", false, 0}, {"closed_date", false, 0},
	}},
	{domain: ingestion.DetailTimeDeposit, jobKey: "time_deposit_detail", table: "fincloud_time_deposit_details", stage: "stg_fincloud_time_deposit_details", fields: []detailColumn{
		{"account_no", true, 0}, {"cif_no", false, 0}, {"nominal", false, 6}, {"accrued_interest", false, 6},
		{"product_interest_rate", false, 2}, {"open_date", false, 0}, {"maturity_date", false, 0}, {"location_id", false, 0},
	}, children: []detailChildSpec{{key: "mutasideposito", table: "fincloud_time_deposit_mutations", stage: "stg_fincloud_time_deposit_mutations", fields: []detailColumn{
		{"transaction_date", false, 0}, {"transaction_type", false, 0}, {"currency", false, 0}, {"nominal", false, 6},
		{"interest_rate", false, 2}, {"reference", false, 0}, {"branch", false, 0}, {"journal_no", false, 0},
	}}}},
	{domain: ingestion.DetailLoan, jobKey: "loan_detail", table: "fincloud_loan_details", stage: "stg_fincloud_loan_details", fields: []detailColumn{
		{"account_no", true, 0}, {"cif_no", false, 0}, {"location_id", false, 0}, {"disbursement_date", false, 0},
		{"outstanding_principal", false, 6}, {"principal_arrears", false, 6}, {"interest_arrears", false, 6},
		{"penalty_arrears", false, 6}, {"dpd", false, 0}, {"collectability_bi", false, 0},
		{"product_interest_rate", false, 2}, {"write_off_date", false, 0},
	}, children: []detailChildSpec{
		{key: "biayapencairan", table: "fincloud_loan_disbursement_fees", stage: "stg_fincloud_loan_disbursement_fees", fields: []detailColumn{{"fee_name", false, 0}, {"fee_amount", false, 6}, {"calculate_dwp", false, 0}}},
		{key: "jadwalangsuran", table: "fincloud_loan_repayment_schedule", stage: "stg_fincloud_loan_repayment_schedule", fields: []detailColumn{
			{"schedule_date", false, 0}, {"installment_amount", false, 6}, {"interest_amount", false, 6}, {"principal_amount", false, 6},
			{"penalty_amount", false, 6}, {"paid_principal", false, 6}, {"paid_interest", false, 6}, {"paid_penalty", false, 6},
			{"remaining_loan", false, 6}, {"installment_no", false, 0},
		}},
		{key: "historybayar", table: "fincloud_loan_payment_history", stage: "stg_fincloud_loan_payment_history", fields: []detailColumn{
			{"transaction_date", false, 0}, {"installment_no", false, 0}, {"payment_date", false, 0}, {"currency", false, 0},
			{"due_date", false, 0}, {"total_paid", false, 6}, {"paid_principal", false, 6}, {"paid_interest", false, 6},
			{"paid_penalty", false, 6}, {"journal_no", false, 0}, {"branch", false, 0},
		}},
	}},
}

func detailSpecification(domain ingestion.DetailDomain) (detailSpec, error) {
	for _, specification := range detailSpecifications {
		if specification.domain == domain {
			return specification, nil
		}
	}
	return detailSpec{}, fmt.Errorf("unsupported detail domain %q", domain)
}

func (repository *DetailRepository) Stage(ctx context.Context, runID uint64, record ingestion.DetailRecord) error {
	specification, err := detailSpecification(record.Domain)
	if err != nil {
		return err
	}
	if repository == nil || repository.db == nil || runID == 0 || record.Identifier == "" || record.LastFetchedAt.IsZero() || len(record.RawPayload) == 0 || record.RawChecksum == "" {
		return fmt.Errorf("complete staged detail is required")
	}
	err = retryTransaction(ctx, "stage_detail", func() error {
		return repository.stageTransaction(ctx, runID, record, specification)
	})
	return wrapDatabaseError(err, "stage_detail", "replace_staged_detail", specification.stage, 0, 0)
}

func (repository *DetailRepository) stageTransaction(ctx context.Context, runID uint64, record ingestion.DetailRecord, specification detailSpec) error {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin staged detail: %w", err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	columns := []string{"ingestion_run_id"}
	values := []any{runID}
	for _, field := range specification.fields {
		columns = append(columns, field.name)
		value := record.Fields[field.name]
		if field.identifier {
			value = record.Identifier
		}
		value, err = detailSQLValue(value, field)
		if err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
		values = append(values, value)
	}
	columns = append(columns, "raw_payload", "raw_checksum", "last_fetched_at")
	values = append(values, string(record.RawPayload), record.RawChecksum, record.LastFetchedAt.UTC())
	if err := upsertStagedDetail(ctx, tx, specification.stage, columns, values); err != nil {
		return err
	}
	for _, childSpecification := range specification.children {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+childSpecification.stage+"` WHERE ingestion_run_id=? AND account_no=?", runID, record.Identifier); err != nil {
			return wrapDatabaseError(fmt.Errorf("delete staged %s children: %w", childSpecification.stage, err), "stage_detail", "delete_staged_child_rows", childSpecification.stage, 0, 0)
		}
		childColumns := []string{"ingestion_run_id", "account_no", "item_index"}
		for _, field := range childSpecification.fields {
			childColumns = append(childColumns, field.name)
		}
		childColumns = append(childColumns, "raw_item_payload", "raw_item_checksum")
		children := record.Children[childSpecification.key]
		rows := make([][]any, len(children))
		for index, child := range children {
			if child.Identifier != record.Identifier || child.ItemIndex != index || len(child.RawItemPayload) == 0 || child.RawItemChecksum == "" {
				return fmt.Errorf("%s child %d does not belong to detail", childSpecification.key, index)
			}
			row := []any{runID, record.Identifier, child.ItemIndex}
			for _, field := range childSpecification.fields {
				value, valueErr := detailSQLValue(child.Fields[field.name], field)
				if valueErr != nil {
					return fmt.Errorf("%s[%d].%s: %w", childSpecification.key, index, field.name, valueErr)
				}
				row = append(row, value)
			}
			rows[index] = append(row, string(child.RawItemPayload), child.RawItemChecksum)
		}
		if err := insertRows(ctx, tx, childSpecification.stage, childColumns, rows); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit staged detail: %w", err)
	}
	committed = true
	return nil
}

func detailSQLValue(value any, field detailColumn) (any, error) {
	if field.scale > 0 {
		precision := 24
		if field.scale == 2 {
			precision = 20
		}
		return decimalString(value, precision, field.scale)
	}
	if date, ok := value.(ingestion.CalendarDate); ok {
		return date.String(), nil
	}
	return value, nil
}

func upsertStagedDetail(ctx context.Context, tx *sqlx.Tx, table string, columns []string, values []any) error {
	quotedTable, err := quoteIdentifier(table)
	if err != nil {
		return err
	}
	quoted := make([]string, len(columns))
	updates := make([]string, 0, len(columns)-2)
	for index, column := range columns {
		quoted[index], err = quoteIdentifier(column)
		if err != nil {
			return err
		}
		if column != "ingestion_run_id" && column != "cif_no" && column != "account_no" {
			updates = append(updates, quoted[index]+"=VALUES("+quoted[index]+")")
		}
	}
	query := "INSERT INTO " + quotedTable + " (" + strings.Join(quoted, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") ON DUPLICATE KEY UPDATE " + strings.Join(updates, ",")
	if _, err := tx.ExecContext(ctx, query, values...); err != nil {
		return wrapDatabaseError(fmt.Errorf("upsert %s: %w", table, err), "stage_detail", "upsert_staged_parent", table, 1, 1)
	}
	return nil
}

func (repository *DetailRepository) Publish(ctx context.Context, runID uint64, ownerID string, domain ingestion.DetailDomain, expected uint64) error {
	specification, err := detailSpecification(domain)
	if err != nil {
		return err
	}
	if repository == nil || repository.db == nil || runID == 0 || ownerID == "" {
		return fmt.Errorf("complete detail publication identity is required")
	}
	err = retryTransaction(ctx, "publish_detail", func() error {
		return repository.publishTransaction(ctx, runID, ownerID, expected, specification)
	})
	if err == nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	committed, checkErr := repository.publicationCommitted(checkCtx, runID)
	if checkErr == nil && committed {
		return nil
	}
	if checkErr != nil {
		err = errors.Join(err, fmt.Errorf("check Detail publication outcome: %w", checkErr))
	}
	return wrapDatabaseError(err, "publish_detail", "publish_current_detail", specification.table, 0, 0)
}

func (repository *DetailRepository) publishTransaction(ctx context.Context, runID uint64, ownerID string, expected uint64, specification detailSpec) error {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Detail publication: %w", err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	var run struct {
		JobKey            string `db:"job_key"`
		Status            string `db:"status"`
		OwnerID           string `db:"owner_id"`
		CancelRequested   bool   `db:"cancel_requested"`
		ProgressTotal     uint64 `db:"progress_total"`
		ProgressStarted   uint64 `db:"progress_started"`
		ProgressSucceeded uint64 `db:"progress_succeeded"`
		ProgressFailed    uint64 `db:"progress_failed"`
	}
	if err := tx.GetContext(ctx, &run, `SELECT COALESCE(job_key,'') job_key,status,COALESCE(owner_id,'') owner_id,
		cancel_requested_at IS NOT NULL cancel_requested,progress_total,progress_started,progress_succeeded,progress_failed
		FROM ingestion_runs WHERE id=? FOR UPDATE`, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "lock_ingestion_run", "ingestion_runs", 0, 0)
	}
	if run.JobKey != specification.jobKey || run.Status != string(ingestionrun.StatusRunning) || run.OwnerID != ownerID || run.CancelRequested ||
		run.ProgressTotal != expected || run.ProgressStarted != expected || run.ProgressSucceeded != expected || run.ProgressFailed != 0 {
		return fmt.Errorf("Detail candidate is not complete and publishable")
	}
	quotedStage, _ := quoteIdentifier(specification.stage)
	var staged uint64
	if err := tx.GetContext(ctx, &staged, "SELECT COUNT(*) FROM "+quotedStage+" WHERE ingestion_run_id=?", runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "count_staged_parents", specification.stage, 0, 0)
	}
	if staged != expected {
		return fmt.Errorf("staged Detail parent count %d does not match expected %d", staged, expected)
	}
	if err := reconcileDetail(ctx, tx, runID, specification); err != nil {
		return err
	}
	if err := ingestionrun.FinishSucceededInTx(ctx, tx, runID, ownerID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "finish_published_run", "ingestion_runs", 0, 0)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Detail publication: %w", err)
	}
	committed = true
	return nil
}

func (repository *DetailRepository) publicationCommitted(ctx context.Context, runID uint64) (bool, error) {
	var status string
	if err := repository.db.GetContext(ctx, &status, `SELECT status FROM ingestion_runs WHERE id=?`, runID); err != nil {
		return false, err
	}
	return status == string(ingestionrun.StatusSucceeded), nil
}

func reconcileDetail(ctx context.Context, tx *sqlx.Tx, runID uint64, specification detailSpec) error {
	key := detailIdentifier(specification.fields)
	finalTable, _ := quoteIdentifier(specification.table)
	stageTable, _ := quoteIdentifier(specification.stage)
	quotedKey, _ := quoteIdentifier(key)

	deleteMissing := "DELETE current_row FROM " + finalTable + " current_row LEFT JOIN " + stageTable +
		" candidate ON candidate.ingestion_run_id=? AND candidate." + quotedKey + "=current_row." + quotedKey +
		" WHERE candidate." + quotedKey + " IS NULL"
	if _, err := tx.ExecContext(ctx, deleteMissing, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "delete_missing_parents", specification.table, 0, 0)
	}

	difference := detailDifference("current_row", "candidate", specification.fields, "raw_checksum")
	updateUnchanged := "UPDATE " + finalTable + " current_row JOIN " + stageTable + " candidate ON candidate.ingestion_run_id=? AND candidate." + quotedKey + "=current_row." + quotedKey +
		" SET current_row.last_fetched_at=candidate.last_fetched_at,current_row.updated_at=current_row.updated_at" +
		" WHERE NOT (" + difference + ") AND NOT (current_row.last_fetched_at <=> candidate.last_fetched_at)"
	if _, err := tx.ExecContext(ctx, updateUnchanged, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "refresh_unchanged_parent", specification.table, 0, 0)
	}

	assignments := detailAssignments("current_row", "candidate", specification.fields, "raw_payload", "raw_checksum", "last_fetched_at")
	updateChanged := "UPDATE " + finalTable + " current_row JOIN " + stageTable + " candidate ON candidate.ingestion_run_id=? AND candidate." + quotedKey + "=current_row." + quotedKey +
		" SET " + strings.Join(assignments, ",") + " WHERE " + difference
	if _, err := tx.ExecContext(ctx, updateChanged, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "update_changed_parent", specification.table, 0, 0)
	}

	columns := detailColumnNames(specification.fields, "raw_payload", "raw_checksum", "last_fetched_at")
	insertNew := "INSERT INTO " + finalTable + " (" + quotedColumns(columns) + ") SELECT " + selectedColumns("candidate", columns) +
		" FROM " + stageTable + " candidate LEFT JOIN " + finalTable + " current_row ON current_row." + quotedKey + "=candidate." + quotedKey +
		" WHERE candidate.ingestion_run_id=? AND current_row." + quotedKey + " IS NULL"
	if _, err := tx.ExecContext(ctx, insertNew, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "insert_new_parent", specification.table, 0, 0)
	}

	for _, child := range specification.children {
		if err := reconcileDetailChildren(ctx, tx, runID, specification, child); err != nil {
			return err
		}
	}
	return nil
}

func reconcileDetailChildren(ctx context.Context, tx *sqlx.Tx, runID uint64, parent detailSpec, child detailChildSpec) error {
	parentKey := detailIdentifier(parent.fields)
	finalTable, _ := quoteIdentifier(child.table)
	stageTable, _ := quoteIdentifier(child.stage)
	stageParent, _ := quoteIdentifier(parent.stage)
	quotedParentKey, _ := quoteIdentifier(parentKey)

	deleteMissing := "DELETE current_child FROM " + finalTable + " current_child JOIN " + stageParent +
		" candidate_parent ON candidate_parent.ingestion_run_id=? AND candidate_parent." + quotedParentKey + "=current_child.account_no" +
		" LEFT JOIN " + stageTable + " candidate_child ON candidate_child.ingestion_run_id=? AND candidate_child.account_no=current_child.account_no AND candidate_child.item_index=current_child.item_index" +
		" WHERE candidate_child.account_no IS NULL"
	if _, err := tx.ExecContext(ctx, deleteMissing, runID, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "delete_missing_children", child.table, 0, 0)
	}

	difference := detailDifference("current_child", "candidate_child", child.fields, "raw_item_checksum")
	assignments := detailAssignments("current_child", "candidate_child", child.fields, "raw_item_payload", "raw_item_checksum")
	updateChanged := "UPDATE " + finalTable + " current_child JOIN " + stageTable +
		" candidate_child ON candidate_child.ingestion_run_id=? AND candidate_child.account_no=current_child.account_no AND candidate_child.item_index=current_child.item_index" +
		" SET " + strings.Join(assignments, ",") + " WHERE " + difference
	if _, err := tx.ExecContext(ctx, updateChanged, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "update_changed_children", child.table, 0, 0)
	}

	columns := append([]string{"account_no", "item_index"}, detailColumnNames(child.fields, "raw_item_payload", "raw_item_checksum")...)
	insertNew := "INSERT INTO " + finalTable + " (" + quotedColumns(columns) + ") SELECT " + selectedColumns("candidate_child", columns) +
		" FROM " + stageTable + " candidate_child LEFT JOIN " + finalTable +
		" current_child ON current_child.account_no=candidate_child.account_no AND current_child.item_index=candidate_child.item_index" +
		" WHERE candidate_child.ingestion_run_id=? AND current_child.account_no IS NULL"
	if _, err := tx.ExecContext(ctx, insertNew, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "insert_new_children", child.table, 0, 0)
	}
	return nil
}

func detailIdentifier(fields []detailColumn) string {
	for _, field := range fields {
		if field.identifier {
			return field.name
		}
	}
	panic("detail specification has no identifier")
}

func detailDifference(current, candidate string, fields []detailColumn, checksum string) string {
	comparisons := []string{"NOT (" + current + ".`" + checksum + "` <=> " + candidate + ".`" + checksum + "`)"}
	for _, field := range fields {
		if !field.identifier {
			comparisons = append(comparisons, "NOT ("+current+".`"+field.name+"` <=> "+candidate+".`"+field.name+"`)")
		}
	}
	return strings.Join(comparisons, " OR ")
}

func detailAssignments(current, candidate string, fields []detailColumn, extra ...string) []string {
	assignments := make([]string, 0, len(fields)+len(extra))
	for _, field := range fields {
		if !field.identifier {
			assignments = append(assignments, current+".`"+field.name+"`="+candidate+".`"+field.name+"`")
		}
	}
	for _, column := range extra {
		assignments = append(assignments, current+".`"+column+"`="+candidate+".`"+column+"`")
	}
	return assignments
}

func detailColumnNames(fields []detailColumn, extra ...string) []string {
	columns := make([]string, 0, len(fields)+len(extra))
	for _, field := range fields {
		columns = append(columns, field.name)
	}
	return append(columns, extra...)
}

func quotedColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = "`" + column + "`"
	}
	return strings.Join(quoted, ",")
}

func selectedColumns(alias string, columns []string) string {
	selected := make([]string, len(columns))
	for index, column := range columns {
		selected[index] = alias + ".`" + column + "`"
	}
	return strings.Join(selected, ",")
}

func (repository *DetailRepository) CleanupRun(ctx context.Context, runID uint64) error {
	if repository == nil || repository.db == nil || runID == 0 {
		return fmt.Errorf("Detail staging cleanup requires a run")
	}
	return retryTransaction(ctx, "cleanup_detail_staging", func() error {
		tx, err := repository.db.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer rollbackUnlessCommitted(tx, &committed)
		for _, specification := range detailSpecifications {
			if _, err := tx.ExecContext(ctx, "DELETE FROM `"+specification.stage+"` WHERE ingestion_run_id=?", runID); err != nil {
				return wrapDatabaseError(err, "cleanup_detail_staging", "delete_run_staging", specification.stage, 0, 0)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}

func (repository *DetailRepository) CleanupTerminal(ctx context.Context, limit int) error {
	if repository == nil || repository.db == nil || limit < 1 {
		return fmt.Errorf("positive Detail staging cleanup limit is required")
	}
	return retryTransaction(ctx, "cleanup_detail_staging", func() error {
		tx, err := repository.db.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer rollbackUnlessCommitted(tx, &committed)
		for _, specification := range detailSpecifications {
			query := "DELETE FROM `" + specification.stage + "` WHERE ingestion_run_id IN (SELECT ingestion_run_id FROM (" +
				"SELECT DISTINCT candidate.ingestion_run_id FROM `" + specification.stage + "` candidate JOIN ingestion_runs run ON run.id=candidate.ingestion_run_id " +
				"WHERE run.status IN ('succeeded','failed','skipped','cancelled','abandoned','completed','completed_with_skips') ORDER BY candidate.ingestion_run_id LIMIT ?" +
				") stale_runs)"
			if _, err := tx.ExecContext(ctx, query, limit); err != nil {
				return wrapDatabaseError(err, "cleanup_detail_staging", "delete_terminal_staging", specification.stage, 0, 0)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}
