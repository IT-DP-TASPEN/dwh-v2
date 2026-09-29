-- +goose Up
ALTER TABLE report_datasources
    ADD COLUMN network VARCHAR(4) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'tcp',
    ADD COLUMN socket_path VARCHAR(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT '',
    DROP CHECK chk_report_datasources_port,
    ADD CONSTRAINT chk_report_datasources_network CHECK (network IN ('tcp','unix')),
    ADD CONSTRAINT chk_report_datasources_connection CHECK (
        (network = 'tcp' AND port BETWEEN 1 AND 65535 AND socket_path = '') OR
        (network = 'unix' AND host = '' AND port = 0 AND socket_path <> ''
            AND tls_policy = 'disabled' AND password_ciphertext IS NULL)
    );

-- +goose Down
-- This atomic ALTER refuses rollback while Unix rows remain (their port is zero).
-- Convert them to valid TCP connections with real credentials before rollback.
ALTER TABLE report_datasources
    DROP CHECK chk_report_datasources_connection,
    DROP CHECK chk_report_datasources_network,
    ADD CONSTRAINT chk_report_datasources_port CHECK (port BETWEEN 1 AND 65535),
    DROP COLUMN socket_path,
    DROP COLUMN network;
