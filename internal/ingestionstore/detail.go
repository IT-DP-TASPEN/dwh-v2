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
	extensions   []detailExtensionSpec
	children     []detailChildSpec
}

type detailExtensionSpec struct {
	key, table, stage string
	fields            []detailColumn
}

type detailChildSpec struct {
	key, table, stage string
	fields            []detailColumn
	required, fetched bool
}

func detailID(name string) detailColumn  { return detailColumn{name: name, identifier: true} }
func detailCol(name string) detailColumn { return detailColumn{name: name} }
func detailDecimal(name string, scale int) detailColumn {
	return detailColumn{name: name, scale: scale}
}

var detailSpecifications = []detailSpec{
	{domain: ingestion.DetailCIF, jobKey: "cif_detail", table: "fincloud_cifs", stage: "stg_fincloud_cif_details", fields: []detailColumn{
		detailID("cif_no"), detailCol("customer_name"), detailCol("customer_type"), detailCol("identity_type"), detailCol("ktp_no"),
		detailCol("birth_date"), detailCol("cif_open_date"), detailCol("record_created_at"), detailCol("alt_no"), detailCol("document_status"),
		detailCol("location_name"), detailCol("record_created_by"), detailCol("record_created_location"), detailCol("record_updated_by"),
		detailCol("record_updated_location"), detailCol("record_updated_at"), detailCol("record_timestamp"),
	}, extensions: []detailExtensionSpec{
		{key: "personal_profile", table: "fincloud_cif_personal_profiles", stage: "stg_fincloud_cif_personal_profiles", fields: []detailColumn{
			detailCol("birth_place"), detailCol("gender"), detailCol("religion"), detailCol("marital_status"), detailCol("formal_education"),
			detailCol("mother_name"), detailCol("nationality"), detailCol("country_of_origin"), detailCol("email"), detailCol("title"),
			detailCol("member_type"), detailCol("dependent_count"), detailCol("residence_years"), detailCol("residence_months"),
			detailCol("residence_status"), detailCol("ethnicity"), detailCol("marriage_date"), detailCol("npwp_no"),
		}},
		{key: "ktp", table: "fincloud_cif_ktp", stage: "stg_fincloud_cif_ktp", fields: []detailColumn{
			detailCol("ktp_name"), detailCol("ktp_birth_date"), detailCol("ktp_religion"), detailCol("ktp_snapshot_address"),
			detailCol("ktp_valid_for_life"), detailCol("ktp_blood_type"), detailCol("ktp_gender"), detailCol("ktp_nationality"),
			detailCol("ktp_occupation"), detailCol("ktp_marital_status"), detailCol("ktp_birth_place"), detailCol("ktp_issue_place"),
			detailCol("ktp_issue_date"), detailCol("ktp_valid_until"),
		}},
		{key: "addresses", table: "fincloud_cif_addresses", stage: "stg_fincloud_cif_addresses", fields: []detailColumn{
			detailCol("ktp_address_line_1"), detailCol("ktp_address_line_2"), detailCol("ktp_subdistrict"), detailCol("ktp_district"),
			detailCol("ktp_city"), detailCol("ktp_province"), detailCol("ktp_postal_code"), detailCol("ktp_rt"), detailCol("ktp_rw"),
			detailCol("home_address_line_1"), detailCol("home_address_line_2"), detailCol("home_subdistrict"), detailCol("home_district"),
			detailCol("home_city"), detailCol("home_province"), detailCol("home_postal_code"), detailCol("home_rt"), detailCol("home_rw"),
			detailCol("home_area_code"), detailCol("home_phone"), detailCol("home_mobile"), detailCol("home_fax"),
			detailCol("office_address_line_1"), detailCol("office_address_line_2"), detailCol("office_subdistrict"), detailCol("office_district"),
			detailCol("office_city"), detailCol("office_province"), detailCol("office_postal_code"), detailCol("office_rt"), detailCol("office_rw"),
			detailCol("office_area_code"), detailCol("office_phone"), detailCol("office_mobile"), detailCol("office_fax"),
			detailCol("home_same_as_ktp"), detailCol("office_same_as_deed"), detailCol("mailing_address_type"),
		}},
		{key: "employment", table: "fincloud_cif_employment", stage: "stg_fincloud_cif_employment", fields: []detailColumn{
			detailCol("work_type"), detailCol("job_title"), detailCol("business_field"), detailCol("company_name"), detailCol("previous_company_name"),
			detailCol("economic_sector"), detailCol("work_years"), detailCol("work_months"), detailCol("previous_work_years"),
			detailCol("previous_work_months"), detailDecimal("monthly_net_income", 6), detailDecimal("monthly_side_income", 6),
			detailDecimal("monthly_expense", 6), detailCol("side_business"),
		}},
		{key: "company", table: "fincloud_cif_company", stage: "stg_fincloud_cif_company", fields: []detailColumn{
			detailCol("company_npwp_no"), detailCol("company_business_entity_type"), detailCol("company_initial_deed_no"),
			detailCol("company_latest_deed_no"), detailCol("company_initial_deed_place"), detailCol("company_latest_deed_place"),
			detailCol("company_initial_deed_date"), detailCol("company_latest_deed_date"),
		}},
		{key: "kyc", table: "fincloud_cif_kyc", stage: "stg_fincloud_cif_kyc", fields: []detailColumn{
			detailCol("kyc_source_of_funds"), detailCol("kyc_income_source"), detailCol("kyc_fund_use_purpose"),
			detailDecimal("kyc_cash_deposit_limit", 6), detailDecimal("kyc_noncash_deposit_limit", 6),
			detailDecimal("kyc_cash_withdrawal_limit", 6), detailDecimal("kyc_noncash_withdrawal_limit", 6),
			detailCol("kyc_transaction_frequency_limit"), detailDecimal("kyc_company_income", 6), detailCol("kyc_company_business_form"),
			detailCol("kyc_company_business_field"), detailCol("kyc_company_fund_use"),
		}},
		{key: "regulatory", table: "fincloud_cif_regulatory", stage: "stg_fincloud_cif_regulatory", fields: []detailColumn{
			detailCol("sid_alias_name"), detailCol("sid_debtor_group"), detailCol("sid_debtor_city"), detailCol("sid_status"), detailCol("sid_din"),
			detailCol("sid_related_party"), detailCol("sid_related_party_notes"), detailCol("sid_exceeds_bmpk"), detailCol("sid_violates_bmpk"),
			detailCol("labul_debtor_group"), detailCol("risk_identity"), detailCol("risk_business_location"), detailCol("risk_transaction_count"),
			detailCol("risk_business_activity"), detailCol("risk_ownership_structure"), detailCol("risk_product_service_network"),
			detailCol("risk_other_information"), detailCol("risk_final_summary"), detailCol("risk_profile"),
		}},
	}},
	{domain: ingestion.DetailSaving, jobKey: "saving_detail", table: "fincloud_saving_details", stage: "stg_fincloud_saving_details", fields: []detailColumn{
		detailID("account_no"), detailCol("cif_no"), detailCol("account_name"), detailCol("location_id"), detailDecimal("beginning_balance", 6),
		detailDecimal("balance", 6), detailDecimal("blocked_balance", 6), detailDecimal("debit_mutation", 6), detailDecimal("credit_mutation", 6),
		detailCol("open_date"), detailCol("closed_date"), detailCol("alt_no"), detailCol("product_id"), detailCol("product_savings_type"),
		detailCol("savings_type"), detailCol("currency"), detailCol("document_status"), detailCol("created_date"),
		detailDecimal("product_credit_interest_rate", 2), detailCol("credit_interest_type"), detailCol("overdraft"), detailCol("joint_account"),
		detailCol("opening_purpose"), detailCol("source_of_fund"), detailCol("product_bnpl"), detailCol("auto_debit"), detailCol("print_bilyet"),
		detailCol("print_savings_book"), detailCol("block_reason"), detailCol("block_notes"), detailCol("unblock_reason"), detailCol("block_status"),
		detailCol("block_date"), detailCol("block_end_date"), detailCol("unblock_date"), detailDecimal("unblock_amount", 6),
		detailDecimal("accrued_balance", 6), detailDecimal("accrued_debit_balance", 6), detailDecimal("accrued_credit_interest_balance", 6),
		detailCol("active_standing_order_count"), detailCol("fixed_debit_interest_payment_day"), detailCol("fixed_credit_interest_payment_day"),
		detailCol("marketing_code"), detailCol("marketing_notes"),
	}, children: []detailChildSpec{{key: ingestion.SavingAccountStatementChildKey, table: "fincloud_saving_account_statements", stage: "stg_fincloud_saving_account_statements", required: true, fetched: true, fields: []detailColumn{
		detailCol("transaction_date"), detailCol("transaction_time"), detailDecimal("opening_balance", 6), detailDecimal("debit", 6),
		detailDecimal("credit", 6), detailDecimal("closing_balance", 6), detailDecimal("closing_balance_equivalent", 6),
		detailCol("transaction_type"), detailCol("description"), detailCol("reference"), detailCol("location"), detailCol("journal_no"),
		detailCol("created_by"), detailDecimal("trx_rate", 6), detailDecimal("mid_rate_dc", 6),
	}}}},
	{domain: ingestion.DetailTimeDeposit, jobKey: "time_deposit_detail", table: "fincloud_time_deposit_details", stage: "stg_fincloud_time_deposit_details", fields: []detailColumn{
		detailID("account_no"), detailCol("cif_no"), detailDecimal("nominal", 6), detailDecimal("accrued_interest", 6),
		detailDecimal("product_interest_rate", 2), detailCol("open_date"), detailCol("maturity_date"), detailCol("location_id"),
		detailCol("account_name"), detailCol("certificate_no"), detailCol("product_id"), detailCol("product_deposit_type"), detailCol("currency"),
		detailCol("term"), detailCol("period"), detailCol("automatic_rollover"), detailCol("compound_interest"), detailCol("interest_rate_change"),
		detailCol("interest_payment_method"), detailCol("print_certificate"), detailCol("document_status"), detailCol("joint_account"),
		detailCol("joint_account_type"), detailCol("source_of_fund"), detailCol("opening_purpose"), detailCol("last_interest_payment_date"),
		detailCol("next_interest_payment_date"), detailCol("source_account_no"), detailCol("interest_destination_account"),
		detailCol("disbursement_account_no"), detailCol("created_date"), detailCol("description"),
	}, children: []detailChildSpec{{key: "mutasideposito", table: "fincloud_time_deposit_mutations", stage: "stg_fincloud_time_deposit_mutations", fields: []detailColumn{
		detailCol("transaction_date"), detailCol("transaction_type"), detailCol("currency"), detailDecimal("nominal", 6),
		detailDecimal("interest_rate", 2), detailCol("reference"), detailCol("branch"), detailCol("journal_no"), detailCol("period"),
		detailCol("term"), detailCol("officer"), detailCol("description"),
	}}}},
	{domain: ingestion.DetailLoan, jobKey: "loan_detail", table: "fincloud_loan_details", stage: "stg_fincloud_loan_details", fields: []detailColumn{
		detailID("account_no"), detailCol("cif_no"), detailCol("location_id"), detailCol("disbursement_date"),
		detailDecimal("outstanding_principal", 6), detailDecimal("principal_arrears", 6), detailDecimal("interest_arrears", 6),
		detailDecimal("penalty_arrears", 6), detailCol("dpd"), detailCol("collectability_bi"), detailDecimal("product_interest_rate", 2),
		detailCol("write_off_date"), detailCol("application_number"), detailDecimal("insurance_premium", 6), detailCol("insurance_company"),
		detailCol("collateral_policy_number"), detailCol("collateral_type"), detailDecimal("collateral_value", 6), detailCol("alt_no"), detailCol("pk_no"),
		detailCol("loan_agreement_no"), detailCol("product_id"), detailCol("product_code"), detailCol("product_loan_type"),
		detailCol("product_installment_type"), detailCol("account_status"), detailCol("document_status"), detailCol("currency"), detailCol("term"),
		detailCol("period"), detailCol("installment_day"), detailDecimal("principal_amount", 6), detailDecimal("credit_limit", 6),
		detailDecimal("accrued_interest", 6), detailDecimal("flat_interest_rate", 2), detailCol("collectability_bpr"), detailCol("collectability_update"),
		detailCol("arrears_start_date"), detailCol("last_due_date"), detailCol("next_due_date"), detailCol("last_principal_interest_payment_date"),
		detailCol("next_principal_interest_payment_date"), detailCol("close_date"), detailCol("restructure_final_agreement_no"),
		detailCol("restructure_final_agreement_date"), detailCol("restructure_method"), detailCol("restructure_frequency"), detailCol("sid_credit_nature"),
		detailCol("sid_credit_nature_2"), detailCol("sid_usage_type"), detailCol("sid_repayment_source"), detailCol("sid_credit_group"),
		detailCol("sid_usage_orientation"), detailCol("sid_economic_sector"), detailCol("sid_economic_sector_2"), detailCol("sid_business_type"),
		detailCol("disbursement_saving_account"), detailCol("disbursement_saving_account_2"), detailCol("installment_payment_account"),
		detailCol("installment_payment_account_2"), detailCol("loan_purpose"), detailDecimal("last_month_ppap", 6), detailCol("last_ppap_date"),
		detailDecimal("write_off_principal_balance", 6), detailDecimal("write_off_accrued_interest", 6), detailDecimal("write_off_interest_arrears", 6),
		detailDecimal("write_off_penalty_arrears", 6), detailDecimal("total_write_off", 6), detailDecimal("charge_off_principal", 6),
		detailDecimal("charge_off_interest", 6), detailCol("marketing"), detailCol("record_created_by"), detailCol("record_created_location"),
	}, children: []detailChildSpec{
		{key: "biayapencairan", table: "fincloud_loan_disbursement_fees", stage: "stg_fincloud_loan_disbursement_fees", fields: []detailColumn{
			detailCol("fee_name"), detailDecimal("fee_amount", 6), detailCol("calculate_dwp"),
		}},
		{key: "jadwalangsuran", table: "fincloud_loan_repayment_schedule", stage: "stg_fincloud_loan_repayment_schedule", fields: []detailColumn{
			detailCol("schedule_date"), detailDecimal("installment_amount", 6), detailDecimal("interest_amount", 6), detailDecimal("principal_amount", 6),
			detailDecimal("penalty_amount", 6), detailDecimal("paid_principal", 6), detailDecimal("paid_interest", 6), detailDecimal("paid_penalty", 6),
			detailDecimal("remaining_loan", 6), detailCol("installment_no"), detailDecimal("flat_interest", 6), detailDecimal("flat_principal", 6),
			detailDecimal("flat_loan", 6), detailCol("payment_status"),
		}},
		{key: "historybayar", table: "fincloud_loan_payment_history", stage: "stg_fincloud_loan_payment_history", fields: []detailColumn{
			detailCol("transaction_date"), detailCol("installment_no"), detailCol("payment_date"), detailCol("currency"), detailCol("due_date"),
			detailDecimal("total_paid", 6), detailDecimal("paid_principal", 6), detailDecimal("paid_interest", 6), detailDecimal("paid_penalty", 6),
			detailCol("journal_no"), detailCol("branch"), detailDecimal("paid_closing_penalty", 6), detailDecimal("dwp_nominal", 6),
			detailCol("description"), detailCol("officer"), detailCol("authorizer"),
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
	err = retryReplaySafeTx(ctx, repository.db, "stage_detail", func(tx *sqlx.Tx) error {
		return repository.stageTransaction(ctx, tx, runID, record, specification)
	})
	return wrapDatabaseError(err, "stage_detail", "replace_staged_detail", specification.stage, 0, 0)
}

func (repository *DetailRepository) stageTransaction(ctx context.Context, tx *sqlx.Tx, runID uint64, record ingestion.DetailRecord, specification detailSpec) error {
	columns := []string{"ingestion_run_id"}
	values := []any{runID}
	for _, field := range specification.fields {
		columns = append(columns, field.name)
		value := record.Fields[field.name]
		if field.identifier {
			value = record.Identifier
		}
		value, valueErr := detailSQLValue(value, field)
		if valueErr != nil {
			return fmt.Errorf("%s: %w", field.name, valueErr)
		}
		values = append(values, value)
	}
	columns = append(columns, "raw_payload", "raw_checksum", "last_fetched_at")
	values = append(values, string(record.RawPayload), record.RawChecksum, record.LastFetchedAt.UTC())
	if err := upsertStagedDetail(ctx, tx, specification.stage, columns, values); err != nil {
		return err
	}
	for _, extension := range specification.extensions {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+extension.stage+"` WHERE ingestion_run_id=? AND cif_no=?", runID, record.Identifier); err != nil {
			return wrapDatabaseError(fmt.Errorf("delete staged %s section: %w", extension.stage, err), "stage_detail", "delete_staged_extension", extension.stage, 0, 0)
		}
		section, present := record.Sections[extension.key]
		if !present {
			continue
		}
		extensionColumns := []string{"ingestion_run_id", "cif_no"}
		extensionValues := []any{runID, record.Identifier}
		for _, field := range extension.fields {
			value, valueErr := detailSQLValue(section[field.name], field)
			if valueErr != nil {
				return fmt.Errorf("%s.%s: %w", extension.key, field.name, valueErr)
			}
			extensionColumns = append(extensionColumns, field.name)
			extensionValues = append(extensionValues, value)
		}
		if err := upsertStagedDetail(ctx, tx, extension.stage, extensionColumns, extensionValues); err != nil {
			return err
		}
	}
	for _, childSpecification := range specification.children {
		children, present := record.Children[childSpecification.key]
		if childSpecification.required && !present {
			return fmt.Errorf("required %s child is missing", childSpecification.key)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+childSpecification.stage+"` WHERE ingestion_run_id=? AND account_no=?", runID, record.Identifier); err != nil {
			return wrapDatabaseError(fmt.Errorf("delete staged %s children: %w", childSpecification.stage, err), "stage_detail", "delete_staged_child_rows", childSpecification.stage, 0, 0)
		}
		childColumns := []string{"ingestion_run_id", "account_no", "item_index"}
		for _, field := range childSpecification.fields {
			childColumns = append(childColumns, field.name)
		}
		childColumns = append(childColumns, "raw_item_payload", "raw_item_checksum")
		if childSpecification.fetched {
			childColumns = append(childColumns, "last_fetched_at")
		}
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
			row = append(row, string(child.RawItemPayload), child.RawItemChecksum)
			if childSpecification.fetched {
				row = append(row, record.LastFetchedAt.UTC())
			}
			rows[index] = row
		}
		if err := insertRows(ctx, tx, childSpecification.stage, childColumns, rows); err != nil {
			return err
		}
	}
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
	err = retryReplaySafeTx(ctx, repository.db, "publish_detail", func(tx *sqlx.Tx) error {
		return repository.publishTransaction(ctx, tx, runID, ownerID, expected, specification)
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

func (repository *DetailRepository) publishTransaction(ctx context.Context, tx *sqlx.Tx, runID uint64, ownerID string, expected uint64, specification detailSpec) error {
	var run struct {
		JobKey          string `db:"job_key"`
		Status          string `db:"status"`
		OwnerID         string `db:"owner_id"`
		CancelRequested bool   `db:"cancel_requested"`
	}
	if err := tx.GetContext(ctx, &run, `SELECT COALESCE(job_key,'') job_key,status,COALESCE(owner_id,'') owner_id,
		cancel_requested_at IS NOT NULL cancel_requested
		FROM ingestion_runs WHERE id=?`, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "check_ingestion_run", "ingestion_runs", 0, 0)
	}
	if run.JobKey != specification.jobKey || run.Status != string(ingestionrun.StatusRunning) || run.OwnerID != ownerID || run.CancelRequested {
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

	for _, extension := range specification.extensions {
		if err := reconcileDetailExtension(ctx, tx, runID, specification, extension); err != nil {
			return err
		}
	}
	for _, child := range specification.children {
		if err := reconcileDetailChildren(ctx, tx, runID, specification, child); err != nil {
			return err
		}
	}
	return nil
}

func reconcileDetailExtension(ctx context.Context, tx *sqlx.Tx, runID uint64, parent detailSpec, extension detailExtensionSpec) error {
	finalTable, _ := quoteIdentifier(extension.table)
	stageTable, _ := quoteIdentifier(extension.stage)
	stageParent, _ := quoteIdentifier(parent.stage)

	deleteMissing := "DELETE current_extension FROM " + finalTable + " current_extension JOIN " + stageParent +
		" candidate_parent ON candidate_parent.ingestion_run_id=? AND candidate_parent.cif_no=current_extension.cif_no" +
		" LEFT JOIN " + stageTable + " candidate_extension ON candidate_extension.ingestion_run_id=? AND candidate_extension.cif_no=current_extension.cif_no" +
		" WHERE candidate_extension.cif_no IS NULL"
	if _, err := tx.ExecContext(ctx, deleteMissing, runID, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "delete_missing_extensions", extension.table, 0, 0)
	}

	difference := detailDifference("current_extension", "candidate_extension", extension.fields, "")
	assignments := detailAssignments("current_extension", "candidate_extension", extension.fields)
	updateChanged := "UPDATE " + finalTable + " current_extension JOIN " + stageTable +
		" candidate_extension ON candidate_extension.ingestion_run_id=? AND candidate_extension.cif_no=current_extension.cif_no" +
		" SET " + strings.Join(assignments, ",") + " WHERE " + difference
	if _, err := tx.ExecContext(ctx, updateChanged, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "update_changed_extensions", extension.table, 0, 0)
	}

	columns := append([]string{"cif_no"}, detailColumnNames(extension.fields)...)
	insertNew := "INSERT INTO " + finalTable + " (" + quotedColumns(columns) + ") SELECT " + selectedColumns("candidate_extension", columns) +
		" FROM " + stageTable + " candidate_extension LEFT JOIN " + finalTable +
		" current_extension ON current_extension.cif_no=candidate_extension.cif_no" +
		" WHERE candidate_extension.ingestion_run_id=? AND current_extension.cif_no IS NULL"
	if _, err := tx.ExecContext(ctx, insertNew, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "insert_new_extensions", extension.table, 0, 0)
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
	if child.fetched {
		updateUnchanged := "UPDATE " + finalTable + " current_child JOIN " + stageTable +
			" candidate_child ON candidate_child.ingestion_run_id=? AND candidate_child.account_no=current_child.account_no AND candidate_child.item_index=current_child.item_index" +
			" SET current_child.last_fetched_at=candidate_child.last_fetched_at,current_child.updated_at=current_child.updated_at" +
			" WHERE NOT (" + difference + ") AND NOT (current_child.last_fetched_at <=> candidate_child.last_fetched_at)"
		if _, err := tx.ExecContext(ctx, updateUnchanged, runID); err != nil {
			return wrapDatabaseError(err, "publish_detail", "refresh_unchanged_children", child.table, 0, 0)
		}
	}
	extra := []string{"raw_item_payload", "raw_item_checksum"}
	if child.fetched {
		extra = append(extra, "last_fetched_at")
	}
	assignments := detailAssignments("current_child", "candidate_child", child.fields, extra...)
	updateChanged := "UPDATE " + finalTable + " current_child JOIN " + stageTable +
		" candidate_child ON candidate_child.ingestion_run_id=? AND candidate_child.account_no=current_child.account_no AND candidate_child.item_index=current_child.item_index" +
		" SET " + strings.Join(assignments, ",") + " WHERE " + difference
	if _, err := tx.ExecContext(ctx, updateChanged, runID); err != nil {
		return wrapDatabaseError(err, "publish_detail", "update_changed_children", child.table, 0, 0)
	}

	columns := append([]string{"account_no", "item_index"}, detailColumnNames(child.fields, extra...)...)
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
	comparisons := make([]string, 0, len(fields)+1)
	if checksum != "" {
		comparisons = append(comparisons, "NOT ("+current+".`"+checksum+"` <=> "+candidate+".`"+checksum+"`)")
	}
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
	return repository.clearRun(ctx, runID, "cleanup_detail_staging")
}

func (repository *DetailRepository) PrepareRun(ctx context.Context, runID uint64) error {
	return repository.clearRun(ctx, runID, "prepare_detail_staging")
}

func (repository *DetailRepository) clearRun(ctx context.Context, runID uint64, operation string) error {
	if repository == nil || repository.db == nil || runID == 0 {
		return fmt.Errorf("Detail staging cleanup requires a run")
	}
	return retryReplaySafeTx(ctx, repository.db, operation, func(tx *sqlx.Tx) error {
		for _, specification := range detailSpecifications {
			if _, err := tx.ExecContext(ctx, "DELETE FROM `"+specification.stage+"` WHERE ingestion_run_id=?", runID); err != nil {
				return wrapDatabaseError(err, operation, "delete_run_staging", specification.stage, 0, 0)
			}
		}
		return nil
	})
}

func (repository *DetailRepository) CleanupTerminal(ctx context.Context, limit int) (int64, error) {
	if repository == nil || repository.db == nil || limit < 1 {
		return 0, fmt.Errorf("positive Detail staging cleanup limit is required")
	}
	var deleted int64
	err := retryReplaySafeTx(ctx, repository.db, "cleanup_detail_staging", func(tx *sqlx.Tx) error {
		var attemptDeleted int64
		for _, specification := range detailSpecifications {
			query := "DELETE FROM `" + specification.stage + "` WHERE ingestion_run_id IN (SELECT ingestion_run_id FROM (" +
				"SELECT DISTINCT candidate.ingestion_run_id FROM `" + specification.stage + "` candidate LEFT JOIN ingestion_runs run ON run.id=candidate.ingestion_run_id " +
				"WHERE run.id IS NULL OR run.status IN ('succeeded','failed','skipped','cancelled','abandoned','completed','completed_with_skips') ORDER BY candidate.ingestion_run_id LIMIT ?" +
				") stale_runs)"
			result, err := tx.ExecContext(ctx, query, limit)
			if err != nil {
				return wrapDatabaseError(err, "cleanup_detail_staging", "delete_terminal_staging", specification.stage, 0, 0)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			attemptDeleted += affected
		}
		deleted = attemptDeleted
		return nil
	})
	return deleted, err
}
