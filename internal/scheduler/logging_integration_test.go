//go:build integration

package scheduler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func TestDeliveryLogsOneCorrelatedSubmissionAndEmptySweepsStaySilent(t *testing.T) {
	db, service := integrationService(t)
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := service.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("empty sweep logged: %s", logs.String())
	}

	// The shared integration database can hold active runs left by other
	// suites; pick a job that is free so delivery is not blocked as job_busy.
	var job string
	if err := db.Get(&job, `SELECT s.source_id FROM source_settings s WHERE s.source_id IN
		('eod_detail_outstanding_rekening_pinjaman','cif_opening_report','fund_distribution_report','vault_mutation_report')
		AND NOT EXISTS (SELECT 1 FROM ingestion_runs r WHERE r.job_key=s.source_id AND r.status IN ('queued','running'))
		ORDER BY s.source_id LIMIT 1`); err != nil {
		t.Fatalf("no idle scheduler test job: %v", err)
	}
	configureSource(t, db, job)
	due := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	schedule := createDueSchedule(t, db, service, "logged", job, due)
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || !changed {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
	attempt := latestAttempt(t, db, schedule.ID)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("records=%q", lines)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "scheduler.run.submitted" || record["level"] != "INFO" || record["schedule_id"] != float64(schedule.ID) ||
		record["occurrence_id"] != float64(attempt.OccurrenceID) || record["run_id"] != float64(attempt.RunID) ||
		record["job_key"] != job || record["trigger"] != "scheduler" ||
		record["scheduled_for"] != due.Format(time.RFC3339) || record["delivery_delay_ms"] == nil {
		t.Fatalf("record=%v", record)
	}

	logs.Reset()
	if changed, err := service.process(context.Background(), schedule.ID); err != nil || changed {
		t.Fatalf("in-flight changed=%v error=%v", changed, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("in-flight check logged: %s", logs.String())
	}
}

// configureSource attaches a minimal active Auth Profile so submission is not
// blocked by source_configuration_required, independent of suite order.
func configureSource(t *testing.T, db *sqlx.DB, key string) {
	t.Helper()
	var previous sql.NullInt64
	if err := db.Get(&previous, `SELECT fincloud_auth_profile_id FROM source_settings WHERE source_id=?`, key); err != nil {
		t.Fatal(err)
	}
	role, err := db.Exec(`INSERT INTO roles (name,slug,is_system,created_at,updated_at) VALUES ('scheduler-logging',CONCAT('scheduler-logging-',UUID()),FALSE,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`)
	if err != nil {
		t.Fatal(err)
	}
	roleID, _ := role.LastInsertId()
	user, err := db.Exec(`INSERT INTO users (username,name,password_hash,role_id,is_active,created_at,updated_at)
		VALUES (CONCAT('scheduler-logging-',UUID()),'scheduler logging','test',?,TRUE,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, roleID)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	profile, err := db.Exec(`INSERT INTO fincloud_auth_profiles (name,username,role_id,location_id,password_ciphertext,status,created_by_user_id,updated_by_user_id)
		VALUES (CONCAT('scheduler-logging-',UUID()),'user','role','001',NULL,'active',?,?)`, userID, userID)
	if err != nil {
		t.Fatal(err)
	}
	profileID, _ := profile.LastInsertId()
	if _, err := db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=? WHERE source_id=?`, profileID, key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=? WHERE source_id=?`, previous, key)
		_, _ = db.Exec(`DELETE FROM fincloud_auth_profiles WHERE id=?`, profileID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, userID)
		_, _ = db.Exec(`DELETE FROM roles WHERE id=?`, roleID)
	})
}
