-- +goose Up
ALTER TABLE fincloud_coa_movement_reports
    ADD KEY idx_coa_movement_coa_no (co_a_no(64)),
    ADD KEY idx_coa_movement_date_coa_no (`date`(10), co_a_no(64));

ALTER TABLE fincloud_journal_transaction_reports
    ADD KEY idx_journal_coa_no (co_a_no(64)),
    ADD KEY idx_journal_coa_name (co_a_name(128)),
    ADD KEY idx_journal_transaction_date_coa_no (transaction_date(10), co_a_no(64));

-- +goose Down
ALTER TABLE fincloud_coa_movement_reports
    DROP KEY idx_coa_movement_coa_no,
    DROP KEY idx_coa_movement_date_coa_no;

ALTER TABLE fincloud_journal_transaction_reports
    DROP KEY idx_journal_coa_no,
    DROP KEY idx_journal_coa_name,
    DROP KEY idx_journal_transaction_date_coa_no;
