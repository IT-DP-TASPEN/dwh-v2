package dwhschema

import "github.com/ibldzn/go-admin/internal/ingestion"

var BootstrapVersions = []int64{
	202608090001, 202608090002, 202608090003, 202608090004,
	202608090005, 202608090006, 202608090007,
}

var LegacyVersions = []int64{
	0, 20260614075633, 20260614075705, 20260614075734,
	20260614080122, 20260614080123, 20260614080124, 20260614080125,
	20260614080126, 20260614080127, 20260614080128, 20260614080129,
	20260614090100, 20260614090200, 20260721090000, 20260805090000,
	20260808090000,
}

var EmptyBeforeAdoption = []string{
	"fincloud_cif_opening_reports", "stg_fincloud_cif_opening_reports",
	"fincloud_journal_transaction_reports", "stg_fincloud_journal_transaction_reports",
	"fincloud_balance_sheet_reports", "stg_fincloud_balance_sheet_reports",
	"fincloud_profit_loss_statements", "stg_fincloud_profit_loss_statements",
	"fincloud_coa_movement_reports", "stg_fincloud_coa_movement_reports",
	"fincloud_fund_distribution_reports", "stg_fincloud_fund_distribution_reports",
	"fincloud_vault_mutation_reports", "stg_fincloud_vault_mutation_reports",
	"fincloud_teller_mutation_reports", "stg_fincloud_teller_mutation_reports",
	"fincloud_cifs", "stg_fincloud_cif_details",
	"fincloud_saving_details", "stg_fincloud_saving_details",
	"fincloud_time_deposit_details", "stg_fincloud_time_deposit_details", "fincloud_time_deposit_mutations",
	"stg_fincloud_time_deposit_mutations",
	"fincloud_loan_details", "stg_fincloud_loan_details", "fincloud_loan_disbursement_fees",
	"stg_fincloud_loan_disbursement_fees", "fincloud_loan_repayment_schedule", "stg_fincloud_loan_repayment_schedule",
	"fincloud_loan_payment_history", "stg_fincloud_loan_payment_history",
	"dynamic_csv_sources", "dynamic_csv_source_columns",
	"fixed_report_publications", "fixed_report_load_members", "fixed_report_loads",
	"maintenance_csv_ingestions", "run_log_events", "ingestion_row_errors", "ingestion_run_steps",
	"schedule_attempts", "schedule_occurrences", "schedule_executions", "ingestion_run_items", "ingestion_runs", "schedules",
	"ingestion_run_errors",
}

type MigrationGroup struct {
	Suffix string
	Tables []string
}

var AdoptionMigrationGroups = []MigrationGroup{
	{"create_ingestion_run_errors.sql", []string{"ingestion_run_errors"}},
	{"create_ingestion_scheduler.sql", []string{"schedule_attempts", "schedule_occurrences", "schedule_executions", "schedules"}},
	{"create_ingestion_execution_runtime.sql", []string{
		"maintenance_csv_ingestions", "run_log_events", "ingestion_row_errors", "ingestion_run_steps",
		"ingestion_run_items", "ingestion_runs", "ingestion_runtime_settings",
	}},
	{"create_fixed_report_load_control.sql", []string{"fixed_report_publications", "fixed_report_load_members", "fixed_report_loads"}},
	{"create_fixed_report_storage.sql", []string{
		"stg_fincloud_cif_opening_reports", "stg_fincloud_journal_transaction_reports",
		"stg_fincloud_balance_sheet_reports", "stg_fincloud_profit_loss_statements",
		"stg_fincloud_coa_movement_reports", "stg_fincloud_fund_distribution_reports",
		"stg_fincloud_vault_mutation_reports", "stg_fincloud_teller_mutation_reports",
		"fincloud_cif_opening_reports", "fincloud_journal_transaction_reports",
		"fincloud_balance_sheet_reports", "fincloud_profit_loss_statements",
		"fincloud_coa_movement_reports", "fincloud_fund_distribution_reports",
		"fincloud_vault_mutation_reports", "fincloud_teller_mutation_reports",
	}},
	{"create_detail_snapshot_storage.sql", []string{
		"stg_fincloud_cif_details", "stg_fincloud_saving_details", "stg_fincloud_time_deposit_details", "stg_fincloud_loan_details",
		"fincloud_time_deposit_mutations", "stg_fincloud_time_deposit_mutations",
		"fincloud_loan_disbursement_fees", "stg_fincloud_loan_disbursement_fees",
		"fincloud_loan_repayment_schedule", "stg_fincloud_loan_repayment_schedule",
		"fincloud_loan_payment_history", "stg_fincloud_loan_payment_history",
		"fincloud_cifs", "fincloud_saving_details", "fincloud_time_deposit_details", "fincloud_loan_details",
	}},
	{"create_maintenance_dynamic_registry.sql", []string{"dynamic_csv_source_columns", "dynamic_csv_sources"}},
}

type UserReference struct{ Table, Column, Constraint string }

var UserReferences = []UserReference{
	{"application_settings", "updated_by_user_id", "fk_application_settings_updated_by"},
	{"source_settings", "updated_by_user_id", "fk_source_settings_updated_by_user"},
	{"ingestion_runs", "requested_by_user_id", "fk_ingestion_runs_requested_by_user"},
	{"schedules", "created_by_user_id", "fk_schedules_created_by_user"},
}

func CanonicalSourceKeys() ([]string, error) {
	catalog, err := ingestion.NewCatalog()
	if err != nil {
		return nil, err
	}
	jobs := catalog.Jobs()
	keys := make([]string, len(jobs))
	for index, job := range jobs {
		keys[index] = job.Key
	}
	return keys, nil
}
