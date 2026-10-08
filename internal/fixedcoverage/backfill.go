// Package fixedcoverage owns the bounded historical preparation of Fixed dates.
// Fixed table DDL remains migration-owned; this package never alters schemas.
package fixedcoverage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/jmoiron/sqlx"
)

var ErrNotReady = errors.New("Fixed coverage is not ready; run fixed-coverage-backfill before ingestion")

func RequireReady(ctx context.Context, db sqlx.QueryerContext) error {
	var ready bool
	if err := sqlx.GetContext(ctx, db, &ready, `SELECT ready FROM fixed_report_coverage_state WHERE id=1`); err != nil {
		return fmt.Errorf("read Fixed coverage readiness: %w", err)
	}
	if !ready {
		return ErrNotReady
	}
	return nil
}

// Backfill validates every historical final row, including dates already filled
// by an earlier batch. Data and its PK watermark commit together. Stop all Fixed
// writers first; readiness blocks new execution throughout preparation.
func Backfill(ctx context.Context, db *sqlx.DB, batchSize int) error {
	if batchSize < 1 || batchSize > 5000 {
		return fmt.Errorf("backfill batch size must be between 1 and 5000")
	}
	var name string
	if err := db.GetContext(ctx, &name, `SELECT DATABASE()`); err != nil {
		return err
	}
	if name == "" || strings.EqualFold(name, "dwh2") {
		return fmt.Errorf("refusing Fixed backfill database %q", name)
	}
	if err := RequireReady(ctx, db); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotReady) {
		return err
	}
	for _, def := range ingestion.FixedDefinitions() {
		if def.PublicationMode != ingestion.DateAddressable {
			continue
		}
		for {
			complete, err := BackfillBatch(ctx, db, def, batchSize)
			if err != nil {
				return err
			}
			if complete {
				break
			}
		}
	}
	_, err := database.RetryReplaySafeTx(ctx, db, func(tx *sqlx.Tx) error {
		var ready bool
		if err := tx.GetContext(ctx, &ready, `SELECT ready FROM fixed_report_coverage_state WHERE id=1 FOR UPDATE`); err != nil {
			return err
		}
		if ready {
			return nil
		}
		if err := requireQuiescent(ctx, tx); err != nil {
			return err
		}
		for _, def := range ingestion.FixedDefinitions() {
			if def.PublicationMode != ingestion.DateAddressable {
				continue
			}
			table, _ := ingestion.FixedTableName(def.Key)
			var complete bool
			if err := tx.GetContext(ctx, &complete, `SELECT complete FROM fixed_report_coverage_backfill WHERE job_key=? FOR UPDATE`, def.Key); err != nil {
				return err
			}
			if !complete {
				return ErrNotReady
			}
			var missing bool
			if err := tx.GetContext(ctx, &missing, "SELECT EXISTS(SELECT 1 FROM `"+table+"` FORCE INDEX (idx_fixed_coverage_date) WHERE coverage_date IS NULL LIMIT 1)"); err != nil {
				return err
			}
			if missing {
				return fmt.Errorf("%s still contains unprepared coverage dates", table)
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE fixed_report_coverage_state SET ready=TRUE,ready_at=CURRENT_TIMESTAMP(6) WHERE id=1`)
		return err
	})
	return err
}

func requireQuiescent(ctx context.Context, tx *sqlx.Tx) error {
	var active bool
	if err := tx.GetContext(ctx, &active, `SELECT EXISTS(SELECT 1 FROM ingestion_runs r JOIN fixed_report_publication_locks j ON j.job_key=r.job_key WHERE r.status IN ('queued','running') LIMIT 1)`); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("stop or cancel queued/running Fixed ingestion before coverage backfill")
	}
	return nil
}

// BackfillBatch is one resumable transaction, bounded by the final table PK.
func BackfillBatch(ctx context.Context, db *sqlx.DB, def ingestion.FixedDefinition, batchSize int) (bool, error) {
	if batchSize < 1 || batchSize > 5000 || def.PublicationMode != ingestion.DateAddressable {
		return false, fmt.Errorf("valid date-report backfill batch required")
	}
	table, err := ingestion.FixedTableName(def.Key)
	if err != nil {
		return false, err
	}
	column := ingestion.FixedColumnName(def.CoverageDateHeader)
	if def.SnapshotDate {
		column = "as_of_date"
	}
	var databaseName string
	if err := db.GetContext(ctx, &databaseName, `SELECT DATABASE()`); err != nil {
		return false, err
	}
	if databaseName == "" || strings.EqualFold(databaseName, "dwh2") {
		return false, fmt.Errorf("refusing Fixed backfill database %q", databaseName)
	}
	complete := false
	_, err = database.RetryReplaySafeTx(ctx, db, func(tx *sqlx.Tx) error {
		complete = false
		var ready bool
		if err := tx.GetContext(ctx, &ready, `SELECT ready FROM fixed_report_coverage_state WHERE id=1 FOR UPDATE`); err != nil {
			return err
		}
		if ready {
			complete = true
			return nil
		}
		if err := requireQuiescent(ctx, tx); err != nil {
			return err
		}
		var last uint64
		if err := tx.GetContext(ctx, &last, `SELECT last_id FROM fixed_report_coverage_backfill WHERE job_key=? FOR UPDATE`, def.Key); err != nil {
			return err
		}
		type row struct {
			ID       uint64         `db:"id"`
			Raw      sql.NullString `db:"raw_date"`
			Coverage sql.NullString `db:"coverage"`
		}
		rows := []row{}
		raw := "`" + column + "`"
		if def.SnapshotDate {
			raw = "DATE_FORMAT(as_of_date,'%Y-%m-%d')"
		}
		if err := tx.SelectContext(ctx, &rows, "SELECT id,"+raw+" raw_date,DATE_FORMAT(coverage_date,'%Y-%m-%d') coverage FROM `"+table+"` WHERE id>? ORDER BY id LIMIT ? FOR UPDATE", last, batchSize); err != nil {
			return err
		}
		if len(rows) == 0 {
			complete = true
			_, err := tx.ExecContext(ctx, `UPDATE fixed_report_coverage_backfill SET complete=TRUE WHERE job_key=?`, def.Key)
			return err
		}
		var cases, ids []string
		var args, idArgs []any
		for _, r := range rows {
			snapshot := ingestion.CalendarDate{}
			if def.SnapshotDate {
				snapshot, _ = ingestion.ParseCalendarDate(r.Raw.String)
			}
			date, err := ingestion.ParseFixedCoverageDate(def, r.Raw.String, snapshot)
			if !r.Raw.Valid || err != nil || date.IsZero() {
				return fmt.Errorf("invalid historical coverage date: job=%s row_id=%d; batch rolled back", def.Key, r.ID)
			}
			if r.Coverage.Valid && r.Coverage.String != date.String() {
				return fmt.Errorf("historical coverage mismatch: job=%s row_id=%d; batch rolled back", def.Key, r.ID)
			}
			if !r.Coverage.Valid {
				cases = append(cases, "WHEN ? THEN ?")
				args = append(args, r.ID, date.String())
				ids = append(ids, "?")
				idArgs = append(idArgs, r.ID)
			}
		}
		if len(ids) > 0 {
			args = append(args, idArgs...)
			if _, err := tx.ExecContext(ctx, "UPDATE `"+table+"` SET coverage_date=CASE id "+strings.Join(cases, " ")+" END WHERE id IN ("+strings.Join(ids, ",")+")", args...); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE fixed_report_coverage_backfill SET last_id=?,complete=FALSE WHERE job_key=?`, rows[len(rows)-1].ID, def.Key)
		return err
	})
	return complete, err
}
