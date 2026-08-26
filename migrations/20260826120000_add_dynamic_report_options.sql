-- +goose Up
ALTER TABLE report_parameters
    ADD COLUMN option_source VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER parameter_type,
    ADD COLUMN dynamic_option_sql LONGTEXT NULL AFTER option_source;

UPDATE report_parameters
SET option_source='static'
WHERE parameter_type IN ('single_option','multiple_option');

ALTER TABLE report_parameters
    ADD CONSTRAINT chk_report_parameters_option_source CHECK (
        (parameter_type IN ('single_option','multiple_option')
            AND option_source IS NOT NULL
            AND option_source IN ('static','dynamic')
            AND ((option_source='static' AND dynamic_option_sql IS NULL)
                OR (option_source='dynamic' AND dynamic_option_sql IS NOT NULL)))
        OR
        (parameter_type NOT IN ('single_option','multiple_option')
            AND option_source IS NULL
            AND dynamic_option_sql IS NULL)
    );

-- +goose Down
ALTER TABLE report_parameters
    DROP CHECK chk_report_parameters_option_source,
    DROP COLUMN dynamic_option_sql,
    DROP COLUMN option_source;
