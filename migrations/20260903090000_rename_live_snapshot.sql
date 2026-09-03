-- +goose Up
DROP PROCEDURE IF EXISTS validate_live_snapshot_rename;
-- +goose StatementBegin
CREATE PROCEDURE validate_live_snapshot_rename()
BEGIN
    IF EXISTS (
        SELECT 1 FROM schedules
        WHERE archived_at IS NULL AND policy_kind='detail_live_snapshot'
          AND (policy_version <> 1 OR JSON_TYPE(policy_json) <> 'OBJECT' OR JSON_LENGTH(policy_json) <> 0
               OR policy_checksum <> UNHEX('39344a5541ef427ed51b38da8f8c6ba67a719cd4517ed44cdc95af0c5c8334e4'))
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='invalid executable legacy live-snapshot schedule';
    END IF;
    IF EXISTS (
        SELECT 1 FROM schedule_occurrences o
        WHERE o.status='unresolved' AND o.policy_kind='detail_live_snapshot'
          AND (o.policy_version <> 1 OR JSON_TYPE(o.policy_json) <> 'OBJECT' OR JSON_LENGTH(o.policy_json) <> 0
               OR o.policy_checksum <> UNHEX('39344a5541ef427ed51b38da8f8c6ba67a719cd4517ed44cdc95af0c5c8334e4'))
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='invalid recoverable legacy live-snapshot occurrence';
    END IF;
    IF EXISTS (
        SELECT 1 FROM schedule_occurrences o JOIN schedules s ON s.id=o.schedule_id
        WHERE o.status='unresolved' AND (
            (o.policy_kind='detail_live_snapshot') <> (s.policy_kind='detail_live_snapshot') OR
            (s.archived_at IS NOT NULL AND o.policy_kind='detail_live_snapshot')
        )
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='live-snapshot schedule and unresolved occurrence mismatch';
    END IF;
    IF EXISTS (
        SELECT 1 FROM ingestion_runs
        WHERE status IN ('planned','queued','running') AND parameter_kind='detail_live_snapshot_v1'
          AND (parameter_version <> 1 OR JSON_TYPE(parameters_json) <> 'OBJECT' OR JSON_LENGTH(parameters_json) <> 0
               OR parameter_checksum <> UNHEX(SHA2('{}',256)))
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='invalid executable legacy live-snapshot run';
    END IF;
END;
-- +goose StatementEnd
CALL validate_live_snapshot_rename();
DROP PROCEDURE validate_live_snapshot_rename;

START TRANSACTION;
UPDATE schedules
SET policy_kind='live_snapshot',
    policy_checksum=UNHEX('6aabf8e855d1dd0a653754257a4e4bce0380ccf0bb1734b4dc50de2b1f5ec60a'),
    updated_at=updated_at
WHERE archived_at IS NULL AND policy_kind='detail_live_snapshot';

UPDATE schedule_occurrences
SET policy_kind='live_snapshot',
    policy_checksum=UNHEX('6aabf8e855d1dd0a653754257a4e4bce0380ccf0bb1734b4dc50de2b1f5ec60a'),
    updated_at=updated_at
WHERE status='unresolved' AND policy_kind='detail_live_snapshot';

UPDATE ingestion_runs
SET parameter_kind='live_snapshot_v1', updated_at=updated_at
WHERE status IN ('planned','queued','running') AND parameter_kind='detail_live_snapshot_v1';
COMMIT;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='irreversible: executable live-snapshot naming was canonicalized';
