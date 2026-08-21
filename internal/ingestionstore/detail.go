package ingestionstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/ingestion"
)

type DetailRepository struct{ db *sqlx.DB }

func NewDetailRepository(db *sqlx.DB) *DetailRepository { return &DetailRepository{db: db} }

func (repository *DetailRepository) SaveCIFSnapshot(ctx context.Context, record ingestion.DetailRecord) error {
	if record.Domain != ingestion.DetailCIF {
		return fmt.Errorf("CIF snapshot has domain %q", record.Domain)
	}
	return repository.save(ctx, record, detailSpec{
		table: "fincloud_cifs", fields: []detailColumn{
			{"cif_no", true, 0}, {"customer_name", false, 0}, {"customer_type", false, 0}, {"identity_type", false, 0},
			{"ktp_no", false, 0}, {"birth_date", false, 0}, {"cif_open_date", false, 0}, {"record_created_at", false, 0},
		},
	})
}

func (repository *DetailRepository) SaveSavingSnapshot(ctx context.Context, record ingestion.DetailRecord) error {
	if record.Domain != ingestion.DetailSaving {
		return fmt.Errorf("saving snapshot has domain %q", record.Domain)
	}
	return repository.save(ctx, record, detailSpec{
		table: "fincloud_saving_details", fields: []detailColumn{
			{"account_no", true, 0}, {"cif_no", false, 0}, {"account_name", false, 0}, {"location_id", false, 0},
			{"beginning_balance", false, 6}, {"balance", false, 6}, {"blocked_balance", false, 6},
			{"debit_mutation", false, 6}, {"credit_mutation", false, 6}, {"open_date", false, 0}, {"closed_date", false, 0},
		},
	})
}

func (repository *DetailRepository) SaveTimeDepositSnapshot(ctx context.Context, record ingestion.DetailRecord) error {
	if record.Domain != ingestion.DetailTimeDeposit {
		return fmt.Errorf("time-deposit snapshot has domain %q", record.Domain)
	}
	return repository.save(ctx, record, detailSpec{
		table: "fincloud_time_deposit_details", fields: []detailColumn{
			{"account_no", true, 0}, {"cif_no", false, 0}, {"nominal", false, 6}, {"accrued_interest", false, 6},
			{"product_interest_rate", false, 2}, {"open_date", false, 0}, {"maturity_date", false, 0}, {"location_id", false, 0},
		}, children: []detailChildSpec{{
			key: "mutasideposito", table: "fincloud_time_deposit_mutations", fields: []detailColumn{
				{"transaction_date", false, 0}, {"transaction_type", false, 0}, {"currency", false, 0}, {"nominal", false, 6},
				{"interest_rate", false, 2}, {"reference", false, 0}, {"branch", false, 0}, {"journal_no", false, 0},
			},
		}},
	})
}

func (repository *DetailRepository) SaveLoanSnapshot(ctx context.Context, record ingestion.DetailRecord) error {
	if record.Domain != ingestion.DetailLoan {
		return fmt.Errorf("loan snapshot has domain %q", record.Domain)
	}
	return repository.save(ctx, record, detailSpec{
		table: "fincloud_loan_details", fields: []detailColumn{
			{"account_no", true, 0}, {"cif_no", false, 0}, {"location_id", false, 0}, {"disbursement_date", false, 0},
			{"outstanding_principal", false, 6}, {"principal_arrears", false, 6}, {"interest_arrears", false, 6},
			{"penalty_arrears", false, 6}, {"dpd", false, 0}, {"collectability_bi", false, 0},
			{"product_interest_rate", false, 2}, {"write_off_date", false, 0},
		}, children: []detailChildSpec{
			{key: "biayapencairan", table: "fincloud_loan_disbursement_fees", fields: []detailColumn{{"fee_name", false, 0}, {"fee_amount", false, 6}, {"calculate_dwp", false, 0}}},
			{key: "jadwalangsuran", table: "fincloud_loan_repayment_schedule", fields: []detailColumn{
				{"schedule_date", false, 0}, {"installment_amount", false, 6}, {"interest_amount", false, 6}, {"principal_amount", false, 6},
				{"penalty_amount", false, 6}, {"paid_principal", false, 6}, {"paid_interest", false, 6}, {"paid_penalty", false, 6},
				{"remaining_loan", false, 6}, {"installment_no", false, 0},
			}},
			{key: "historybayar", table: "fincloud_loan_payment_history", fields: []detailColumn{
				{"transaction_date", false, 0}, {"installment_no", false, 0}, {"payment_date", false, 0}, {"currency", false, 0},
				{"due_date", false, 0}, {"total_paid", false, 6}, {"paid_principal", false, 6}, {"paid_interest", false, 6},
				{"paid_penalty", false, 6}, {"journal_no", false, 0}, {"branch", false, 0},
			}},
		},
	})
}

type detailColumn struct {
	name       string
	identifier bool
	scale      int
}

type detailSpec struct {
	table    string
	fields   []detailColumn
	children []detailChildSpec
}

type detailChildSpec struct {
	key, table string
	fields     []detailColumn
}

func (repository *DetailRepository) save(ctx context.Context, record ingestion.DetailRecord, specification detailSpec) error {
	if repository == nil || repository.db == nil || record.AsOfDate.IsZero() || record.Identifier == "" || record.LastFetchedAt.IsZero() || len(record.RawPayload) == 0 || record.RawChecksum == "" {
		return fmt.Errorf("complete detail snapshot is required")
	}
	err := retryTransaction(ctx, "persist_detail", func() error {
		return wrapDatabaseError(repository.saveTransaction(ctx, record, specification),
			"persist_detail", "replace_detail_snapshot", specification.table, 0, 0)
	})
	return wrapDatabaseError(err, "persist_detail", "replace_detail_snapshot", specification.table, 0, 0)
}

func (repository *DetailRepository) saveTransaction(ctx context.Context, record ingestion.DetailRecord, specification detailSpec) error {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin detail snapshot: %w", err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	columns := []string{"as_of_date"}
	values := []any{record.AsOfDate.String()}
	for _, field := range specification.fields {
		columns = append(columns, field.name)
		value := record.Fields[field.name]
		if field.identifier {
			value = record.Identifier
		}
		if field.scale > 0 {
			precision := 24
			if field.scale == 2 {
				precision = 20
			}
			value, err = decimalString(value, precision, field.scale)
			if err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
		if date, ok := value.(ingestion.CalendarDate); ok {
			value = date.String()
		}
		values = append(values, value)
	}
	columns = append(columns, "raw_payload", "raw_checksum", "last_fetched_at")
	values = append(values, string(record.RawPayload), record.RawChecksum, record.LastFetchedAt.UTC())
	if err := upsertDetailParent(ctx, tx, specification.table, columns, values); err != nil {
		return err
	}
	for _, childSpecification := range specification.children {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+childSpecification.table+"` WHERE as_of_date = ? AND account_no = ?", record.AsOfDate.String(), record.Identifier); err != nil {
			return wrapDatabaseError(fmt.Errorf("delete %s children: %w", childSpecification.table, err), "persist_detail", "delete_child_rows", childSpecification.table, 0, 0)
		}
		childColumns := []string{"as_of_date", "account_no", "item_index"}
		for _, field := range childSpecification.fields {
			childColumns = append(childColumns, field.name)
		}
		childColumns = append(childColumns, "raw_item_payload", "raw_item_checksum")
		children := record.Children[childSpecification.key]
		rows := make([][]any, len(children))
		for index, child := range children {
			if child.Identifier != record.Identifier || child.AsOfDate != record.AsOfDate || child.ItemIndex != index || len(child.RawItemPayload) == 0 || child.RawItemChecksum == "" {
				return fmt.Errorf("%s child %d does not belong to snapshot", childSpecification.key, index)
			}
			row := []any{record.AsOfDate.String(), record.Identifier, child.ItemIndex}
			for _, field := range childSpecification.fields {
				value := child.Fields[field.name]
				if field.scale > 0 {
					precision := 24
					if field.scale == 2 {
						precision = 20
					}
					value, err = decimalString(value, precision, field.scale)
					if err != nil {
						return fmt.Errorf("%s[%d].%s: %w", childSpecification.key, index, field.name, err)
					}
				}
				if date, ok := value.(ingestion.CalendarDate); ok {
					value = date.String()
				}
				row = append(row, value)
			}
			rows[index] = append(row, string(child.RawItemPayload), child.RawItemChecksum)
		}
		if err := insertRows(ctx, tx, childSpecification.table, childColumns, rows); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit detail snapshot: %w", err)
	}
	committed = true
	return nil
}

func upsertDetailParent(ctx context.Context, tx *sqlx.Tx, table string, columns []string, values []any) error {
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
		if column != "as_of_date" && column != "cif_no" && column != "account_no" {
			updates = append(updates, quoted[index]+"=VALUES("+quoted[index]+")")
		}
	}
	query := "INSERT INTO " + quotedTable + " (" + strings.Join(quoted, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") ON DUPLICATE KEY UPDATE " + strings.Join(updates, ",")
	if _, err := tx.ExecContext(ctx, query, values...); err != nil {
		return wrapDatabaseError(fmt.Errorf("upsert %s: %w", table, err), "persist_detail", "upsert_detail_parent", table, 1, 1)
	}
	return nil
}
