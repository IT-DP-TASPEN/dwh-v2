-- +goose Up
CREATE TABLE report_datasources (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    host VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    port SMALLINT UNSIGNED NOT NULL,
    database_name VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    username VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    password_ciphertext BLOB NULL,
    tls_policy VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'disabled',
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_by_user_id BIGINT UNSIGNED NOT NULL,
    updated_by_user_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_report_datasources_name (name),
    KEY idx_report_datasources_status (status, name, id),
    CONSTRAINT fk_report_datasources_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_datasources_updated_by FOREIGN KEY (updated_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_report_datasources_port CHECK (port BETWEEN 1 AND 65535),
    CONSTRAINT chk_report_datasources_tls CHECK (tls_policy IN ('required','disabled')),
    CONSTRAINT chk_report_datasources_status CHECK (status IN ('active','disabled','archived')),
    CONSTRAINT chk_report_datasources_revision CHECK (revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE report_templates (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(1000) NOT NULL DEFAULT '',
    datasource_id BIGINT UNSIGNED NOT NULL,
    sql_text LONGTEXT NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'disabled',
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_by_user_id BIGINT UNSIGNED NOT NULL,
    updated_by_user_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_report_templates_name (name),
    KEY idx_report_templates_status (status, name, id),
    KEY idx_report_templates_datasource (datasource_id),
    CONSTRAINT fk_report_templates_datasource FOREIGN KEY (datasource_id) REFERENCES report_datasources (id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_templates_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_templates_updated_by FOREIGN KEY (updated_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_report_templates_status CHECK (status IN ('active','disabled','archived')),
    CONSTRAINT chk_report_templates_revision CHECK (revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE report_parameters (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    report_id BIGINT UNSIGNED NOT NULL,
    parameter_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    label VARCHAR(128) NOT NULL,
    parameter_type VARCHAR(24) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    required BOOLEAN NOT NULL DEFAULT FALSE,
    default_value JSON NULL,
    display_order SMALLINT UNSIGNED NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_report_parameters_key (report_id, parameter_key),
    UNIQUE KEY uq_report_parameters_order (report_id, display_order),
    CONSTRAINT fk_report_parameters_report FOREIGN KEY (report_id) REFERENCES report_templates (id) ON DELETE CASCADE,
    CONSTRAINT chk_report_parameters_type CHECK (parameter_type IN ('text','integer','decimal','date','datetime','boolean','single_option','multiple_option'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE report_parameter_options (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    parameter_id BIGINT UNSIGNED NOT NULL,
    option_value VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    label VARCHAR(128) NOT NULL,
    display_order SMALLINT UNSIGNED NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_report_parameter_options_value (parameter_id, option_value),
    UNIQUE KEY uq_report_parameter_options_order (parameter_id, display_order),
    CONSTRAINT fk_report_parameter_options_parameter FOREIGN KEY (parameter_id) REFERENCES report_parameters (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE report_template_user_access (
    report_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    created_by_user_id BIGINT UNSIGNED NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (report_id, user_id),
    KEY idx_report_template_access_user (user_id, report_id),
    CONSTRAINT fk_report_template_access_report FOREIGN KEY (report_id) REFERENCES report_templates (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_template_access_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_template_access_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE report_export_jobs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    report_id BIGINT UNSIGNED NOT NULL,
    report_name VARCHAR(128) NOT NULL,
    sql_text LONGTEXT NOT NULL,
    datasource_id BIGINT UNSIGNED NOT NULL,
    parameter_version SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    parameters_json JSON NOT NULL,
    submitted_by_user_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued',
    attempt INT UNSIGNED NOT NULL DEFAULT 0,
    owner_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    claimed_at DATETIME(6) NULL,
    heartbeat_at DATETIME(6) NULL,
    progress_rows BIGINT UNSIGNED NOT NULL DEFAULT 0,
    current_part INT UNSIGNED NOT NULL DEFAULT 0,
    final_parts INT UNSIGNED NULL,
    total_rows BIGINT UNSIGNED NULL,
    artifact_path VARCHAR(1000) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
    artifact_name VARCHAR(255) NULL,
    artifact_type VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NULL,
    artifact_size BIGINT UNSIGNED NULL,
    artifact_expires_at DATETIME(6) NULL,
    artifact_deleted_at DATETIME(6) NULL,
    failure_class VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    failure_message VARCHAR(500) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    started_at DATETIME(6) NULL,
    finished_at DATETIME(6) NULL,
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_report_export_jobs_claim (status, created_at, id),
    KEY idx_report_export_jobs_stale (status, heartbeat_at, id),
    KEY idx_report_export_jobs_submitter (submitted_by_user_id, created_at, id),
    KEY idx_report_export_jobs_report (report_id, created_at, id),
    KEY idx_report_export_jobs_expiry (status, artifact_expires_at, artifact_deleted_at),
    CONSTRAINT fk_report_export_jobs_report FOREIGN KEY (report_id) REFERENCES report_templates (id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_export_jobs_datasource FOREIGN KEY (datasource_id) REFERENCES report_datasources (id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_export_jobs_submitter FOREIGN KEY (submitted_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_report_export_jobs_status CHECK (status IN ('queued','running','succeeded','failed')),
    CONSTRAINT chk_report_export_jobs_artifact_type CHECK (artifact_type IS NULL OR artifact_type IN ('xlsx','zip')),
    CONSTRAINT chk_report_export_jobs_attempt CHECK (attempt >= 0),
    CONSTRAINT chk_report_export_jobs_parameter_version CHECK (parameter_version = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE report_export_jobs;
DROP TABLE report_template_user_access;
DROP TABLE report_parameter_options;
DROP TABLE report_parameters;
DROP TABLE report_templates;
DROP TABLE report_datasources;
