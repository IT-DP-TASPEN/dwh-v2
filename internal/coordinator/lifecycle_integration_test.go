//go:build integration

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestCancelMapsTerminalNoOpToTransition(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO ingestion_runs
		(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,finished_at)
		VALUES ('job','cif_detail','succeeded','detail_live_snapshot_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',UTC_TIMESTAMP(6))`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM ingestion_runs WHERE id=?`, id) })

	coordinator := &Coordinator{runs: runs}
	if err := coordinator.Cancel(context.Background(), uint64(id), "late", securityctx.Requester{}); !errors.Is(err, ingestionrun.ErrTransition) {
		t.Fatalf("terminal cancellation error=%v", err)
	}
}

func TestPeriodicRecoverySweepDrainsManyStaleRunsAndReleasesJobs(t *testing.T) {
	db := integrationdb.Open(t)
	catalog, _ := ingestion.NewCatalog()
	runs, err := ingestionrun.NewRepository(db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	jobs := catalog.Jobs()[:12]
	ids := make([]uint64, 0, len(jobs))
	for index, job := range jobs {
		owner := fmt.Sprintf("%064x", index+1)
		result, err := db.Exec(`INSERT INTO ingestion_runs
			(kind,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,
			 owner_id,claimed_at,heartbeat_at,started_at)
			VALUES ('job',?,'running','fixed_range_v1',1,JSON_OBJECT(),UNHEX(REPEAT('00',32)),'direct',?,
			 UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE,UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE,UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE)`, job.Key, owner)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		ids = append(ids, uint64(id))
	}
	t.Cleanup(func() {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		arguments := make([]any, len(ids))
		for index := range ids {
			arguments[index] = ids[index]
		}
		_, _ = db.Exec("DELETE FROM ingestion_runs WHERE id IN ("+placeholders+")", arguments...)
	})
	coordinator := &Coordinator{runs: runs, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		local: map[uint64]context.CancelCauseFunc{}, parents: map[uint64]string{}}
	if recovered := coordinator.recoverStaleSweep(context.Background()); recovered != len(ids) {
		t.Fatalf("recovered=%d want=%d", recovered, len(ids))
	}
	var abandoned int
	if err := db.Get(&abandoned, `SELECT COUNT(*) FROM ingestion_runs WHERE status='abandoned' AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, uint64sToAny(ids)...); err != nil || abandoned != len(ids) {
		t.Fatalf("abandoned=%d want=%d error=%v", abandoned, len(ids), err)
	}
	job := jobs[0]
	var wasEnabled bool
	if err := db.Get(&wasEnabled, `SELECT enabled FROM source_settings WHERE source_id=?`, job.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id=?`, job.Key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`UPDATE source_settings SET enabled=? WHERE source_id=?`, wasEnabled, job.Key) })
	parameters, err := parametersForIntegrationJob(job)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := runs.Submit(context.Background(), job.Key, parameters, ingestionrun.TriggerDirect, "after-stale-recovery", nil)
	if err != nil {
		t.Fatalf("logical job remained busy: %v", err)
	}
	ids = append(ids, newID)
}

func uint64sToAny(values []uint64) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func parametersForIntegrationJob(job ingestion.JobDefinition) (ingestionrun.Parameters, error) {
	date, _ := ingestion.ParseCalendarDate("2026-08-26")
	switch job.DateStrategy {
	case ingestion.RangeCapable:
		return ingestionrun.NewRangeExecution(job.Key, date, date)
	case ingestion.SingleDate:
		if job.Category == ingestion.CategoryFixed {
			return ingestionrun.NewDateSeriesExecution(job.Key, date, date)
		}
		return ingestionrun.NewMaintenanceSeriesExecution(job.Key, date, date)
	default:
		return ingestionrun.NewLiveSnapshotExecution(job.Key)
	}
}
