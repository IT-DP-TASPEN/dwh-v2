package ingestionrun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/audit"
	databasepkg "github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Repository struct {
	db      *sqlx.DB
	catalog ingestion.Catalog
}

type RuntimeSettings struct {
	MaxRunningJobs, FixedMemberConcurrency, DetailConcurrency int
}

func NewRepository(db *sqlx.DB, catalog ingestion.Catalog) (*Repository, error) {
	if db == nil || len(catalog.Jobs()) != 36 {
		return nil, fmt.Errorf("database and canonical catalog are required")
	}
	return &Repository{db: db, catalog: catalog}, nil
}

func (repository *Repository) RuntimeSettings(ctx context.Context) (RuntimeSettings, error) {
	var settings struct {
		MaxRunningJobs         int `db:"max_running_jobs"`
		FixedMemberConcurrency int `db:"fixed_member_concurrency"`
		DetailConcurrency      int `db:"detail_concurrency"`
	}
	if err := repository.db.GetContext(ctx, &settings, `SELECT max_running_jobs,fixed_member_concurrency,detail_concurrency FROM ingestion_runtime_settings WHERE id=1`); err != nil {
		return RuntimeSettings{}, err
	}
	if settings.MaxRunningJobs < 1 || settings.FixedMemberConcurrency < 1 || settings.DetailConcurrency < 1 {
		return RuntimeSettings{}, fmt.Errorf("invalid ingestion runtime settings")
	}
	return RuntimeSettings(settings), nil
}

func (repository *Repository) Submit(ctx context.Context, jobKey string, parameters Parameters, trigger Trigger, reference string, requester *uint64) (uint64, error) {
	return repository.submit(ctx, jobKey, parameters, trigger, reference, requester, nil)
}

func (repository *Repository) SubmitManual(ctx context.Context, jobKey string, parameters Parameters, trigger Trigger, reference string, requester securityctx.Requester) (uint64, error) {
	actor := requester.Actor.UserID
	return repository.submit(ctx, jobKey, parameters, trigger, reference, &actor, &requester)
}

func (repository *Repository) submit(ctx context.Context, jobKey string, parameters Parameters, trigger Trigger, reference string, requestedBy *uint64, auditRequester *securityctx.Requester) (uint64, error) {
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := repository.SubmitInTx(ctx, tx, jobKey, parameters, trigger, reference, requestedBy)
	if err != nil {
		return 0, err
	}
	if auditRequester != nil {
		if err := appendRunAudit(ctx, tx, *auditRequester, audit.ActionIngestionRunSubmitted, id, audit.IngestionSubmissionMetadata{JobKey: jobKey}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// SubmitInTx is the canonical job-submission path for callers that must link a
// run to their own durable state in the same transaction.
func (repository *Repository) SubmitInTx(ctx context.Context, tx *sqlx.Tx, jobKey string, parameters Parameters, trigger Trigger, reference string, requester *uint64) (uint64, error) {
	if tx == nil {
		return 0, fmt.Errorf("transaction is required")
	}
	if trigger == TriggerScheduler && strings.TrimSpace(reference) == "" {
		return 0, fmt.Errorf("scheduler trigger reference is required")
	}
	job, found := repository.catalog.Find(jobKey)
	if !found {
		return 0, fmt.Errorf("unknown job %q", jobKey)
	}
	if err := parameters.Validate(job); err != nil {
		return 0, err
	}
	enabled, err := sourceEnabled(ctx, tx, jobKey)
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, ErrSourceDisabled
	}
	result, err := insertRun(ctx, tx, KindJob, nil, nil, jobKey, StatusQueued, parameters, trigger, reference, requester)
	if duplicate(err) {
		return 0, ErrJobBusy
	}
	if err != nil {
		return 0, err
	}
	id, _ := result.LastInsertId()
	return uint64(id), nil
}

func (repository *Repository) CreateRunAll(ctx context.Context, from, to ingestion.CalendarDate, trigger Trigger, reference string, requester *uint64) (uint64, error) {
	return repository.createRunAll(ctx, from, to, trigger, reference, requester, nil)
}

func (repository *Repository) CreateRunAllManual(ctx context.Context, from, to ingestion.CalendarDate, trigger Trigger, reference string, requester securityctx.Requester) (uint64, error) {
	actor := requester.Actor.UserID
	return repository.createRunAll(ctx, from, to, trigger, reference, &actor, &requester)
}

func (repository *Repository) createRunAll(ctx context.Context, from, to ingestion.CalendarDate, trigger Trigger, reference string, requestedBy *uint64, auditRequester *securityctx.Requester) (uint64, error) {
	parentParameters, err := NewRunAllRange(from, to)
	if err != nil {
		return 0, err
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := insertRun(ctx, tx, KindRunAllParent, nil, nil, "", StatusRunning, parentParameters, trigger, reference, requestedBy)
	if err != nil {
		return 0, err
	}
	parentID, _ := result.LastInsertId()
	parentOwner, err := NewOwnerID()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET owner_id=?,claimed_at=CURRENT_TIMESTAMP(6),heartbeat_at=CURRENT_TIMESTAMP(6),started_at=CURRENT_TIMESTAMP(6) WHERE id=? AND status='running'`, parentOwner, parentID); err != nil {
		return 0, err
	}
	for index, job := range repository.catalog.Jobs() {
		parameters, err := parametersForJob(job, from, to)
		if err != nil {
			return 0, err
		}
		parent, position := uint64(parentID), uint16(index+1)
		if _, err := insertRun(ctx, tx, KindRunAllChild, &parent, &position, job.Key, StatusPlanned, parameters, TriggerRunAll, fmt.Sprint(parentID), requestedBy); err != nil {
			return 0, err
		}
	}
	if auditRequester != nil {
		metadata := audit.IngestionSubmissionMetadata{From: from.String(), To: to.String()}
		if err := appendRunAudit(ctx, tx, *auditRequester, audit.ActionIngestionRunAllSubmitted, uint64(parentID), metadata); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uint64(parentID), nil
}

func parametersForJob(job ingestion.JobDefinition, from, to ingestion.CalendarDate) (Parameters, error) {
	switch job.DateStrategy {
	case ingestion.RangeCapable:
		return NewRangeExecution(job.Key, from, to)
	case ingestion.SingleDate:
		if job.Category == ingestion.CategoryFixed {
			return NewDateSeriesExecution(job.Key, from, to)
		}
		return NewMaintenanceSeriesExecution(job.Key, from, to)
	case ingestion.NoDate:
		return NewLiveSnapshotExecution(job.Key)
	default:
		return Parameters{}, fmt.Errorf("job %s has no date strategy", job.Key)
	}
}

func (repository *Repository) Claim(ctx context.Context, ownerID string) (*Run, error) {
	if len(ownerID) != 64 {
		return nil, fmt.Errorf("opaque owner identity is required")
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var limit int
	if err := tx.GetContext(ctx, &limit, `SELECT max_running_jobs FROM ingestion_runtime_settings WHERE id=1 FOR UPDATE`); err != nil {
		return nil, err
	}
	var running int
	if err := tx.GetContext(ctx, &running, `SELECT COUNT(*) FROM ingestion_runs WHERE kind IN ('job','run_all_child') AND status='running'`); err != nil {
		return nil, err
	}
	if running >= limit {
		return nil, nil
	}
	var id uint64
	err = tx.GetContext(ctx, &id, `SELECT id FROM ingestion_runs
		WHERE kind IN ('job','run_all_child') AND status='queued'
		ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='running',owner_id=?,claimed_at=CURRENT_TIMESTAMP(6),
		heartbeat_at=CURRENT_TIMESTAMP(6),started_at=COALESCE(started_at,CURRENT_TIMESTAMP(6)) WHERE id=? AND status='queued'`, ownerID, id)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, ErrTransition
	}
	run, err := getRun(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &run, nil
}

func (repository *Repository) Heartbeat(ctx context.Context, runID uint64, ownerID string) (HeartbeatResult, error) {
	result, _, err := databasepkg.RetryReplaySafeExec(ctx, repository.db, `UPDATE ingestion_runs SET heartbeat_at=CURRENT_TIMESTAMP(6)
		WHERE id=? AND kind IN ('job','run_all_child') AND status='running' AND owner_id=?`, runID, ownerID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	if _, err := result.RowsAffected(); err != nil {
		return HeartbeatResult{}, err
	}
	var cancelled bool
	err = repository.db.GetContext(ctx, &cancelled, `SELECT cancel_requested_at IS NOT NULL FROM ingestion_runs
		WHERE id=? AND status='running' AND owner_id=?`, runID, ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return HeartbeatResult{}, nil
	}
	return HeartbeatResult{Owned: err == nil, CancelRequested: cancelled}, err
}

func (repository *Repository) UpdateProgress(ctx context.Context, runID uint64, ownerID string, progress Progress, diagnostics *MapperDiagnostics) error {
	var encoded any
	if diagnostics != nil {
		data, err := diagnostics.Marshal()
		if err != nil {
			return err
		}
		encoded = string(data)
	}
	result, _, err := databasepkg.RetryReplaySafeExec(ctx, repository.db, `UPDATE ingestion_runs SET progress_total=?,progress_started=?,progress_succeeded=?,
		progress_failed=?,rows_processed=?,current_step=?,mapper_diagnostics=COALESCE(?,mapper_diagnostics)
		WHERE id=? AND status='running' AND owner_id=?`,
		progress.Total, progress.Started, progress.Succeeded, progress.Failed, progress.Rows, nullable(progress.Step), encoded, runID, ownerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var owned bool
		if checkErr := repository.db.GetContext(ctx, &owned, `SELECT EXISTS(SELECT 1 FROM ingestion_runs WHERE id=? AND status='running' AND owner_id=?)`, runID, ownerID); checkErr != nil {
			return checkErr
		}
		if !owned {
			return ErrOwnershipLost
		}
	}
	return nil
}

func (repository *Repository) FreezeSnapshotDate(ctx context.Context, runID uint64, ownerID string, date ingestion.CalendarDate) error {
	if date.IsZero() {
		return fmt.Errorf("snapshot date is required")
	}
	result, _, err := databasepkg.RetryReplaySafeExec(ctx, repository.db, `UPDATE ingestion_runs SET snapshot_date=?
		WHERE id=? AND status='running' AND owner_id=? AND snapshot_date IS NULL`, date.String(), runID, ownerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrOwnershipLost
	}
	return nil
}

func (repository *Repository) Finish(ctx context.Context, runID uint64, ownerID string, status Status, safeError SafeError) error {
	if status != StatusSucceeded && status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("invalid executable terminal status %q", status)
	}
	condition := ""
	if status == StatusSucceeded {
		condition = " AND cancel_requested_at IS NULL"
	}
	result, _, err := databasepkg.RetryReplaySafeExec(ctx, repository.db, `UPDATE ingestion_runs SET status=?,error_class=?,error_message=?,error_step=?,
		finished_at=CURRENT_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=?`+condition, status,
		nullable(safeError.Class), nullable(safeError.Message), nullable(safeError.Step), runID, ownerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrTransition
	}
	return nil
}

// FinishSucceededInTx applies the canonical successful Finish transition inside
// a caller-owned transaction so business publication and run state commit together.
func FinishSucceededInTx(ctx context.Context, tx *sqlx.Tx, runID uint64, ownerID string) error {
	if tx == nil {
		return fmt.Errorf("transaction is required")
	}
	return finish(ctx, tx, runID, ownerID, StatusSucceeded, SafeError{})
}

type finishExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func finish(ctx context.Context, executor finishExecutor, runID uint64, ownerID string, status Status, safeError SafeError) error {
	condition := ""
	if status == StatusSucceeded {
		condition = " AND cancel_requested_at IS NULL"
	}
	result, err := executor.ExecContext(ctx, `UPDATE ingestion_runs SET status=?,error_class=?,error_message=?,error_step=?,
		finished_at=CURRENT_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=?`+condition, status,
		nullable(safeError.Class), nullable(safeError.Message), nullable(safeError.Step), runID, ownerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrTransition
	}
	return nil
}

func (repository *Repository) RecoverOneStale(ctx context.Context, lease time.Duration, replacementOwner string) (*Recovery, error) {
	if lease <= 0 || len(replacementOwner) != 64 {
		return nil, fmt.Errorf("lease and replacement owner are required")
	}
	for {
		var recovered *Recovery
		candidateSeen := false
		_, err := databasepkg.RetryReplaySafeTx(ctx, repository.db, func(tx *sqlx.Tx) error {
			recovered, candidateSeen = nil, false
			var candidate struct {
				ID          uint64         `db:"id"`
				ParentRunID sql.NullInt64  `db:"parent_run_id"`
				Kind        Kind           `db:"kind"`
				JobKey      sql.NullString `db:"job_key"`
				Owner       string         `db:"owner_id"`
				Heartbeat   time.Time      `db:"heartbeat_at"`
			}
			err := tx.GetContext(ctx, &candidate, `SELECT id,parent_run_id,kind,job_key,owner_id,heartbeat_at FROM ingestion_runs
			WHERE status='running' AND owner_id IS NOT NULL AND heartbeat_at IS NOT NULL
			AND heartbeat_at<TIMESTAMPADD(MICROSECOND,-?,UTC_TIMESTAMP(6))
			ORDER BY heartbeat_at,id LIMIT 1`, lease.Microseconds())
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			candidateSeen = true
			result := Recovery{RunID: candidate.ID, Kind: candidate.Kind, JobKey: candidate.JobKey.String,
				PreviousOwner: candidate.Owner, PreviousHeartbeat: candidate.Heartbeat}
			if candidate.ParentRunID.Valid {
				result.ParentRunID = uint64(candidate.ParentRunID.Int64)
			}
			if candidate.Kind == KindRunAllParent {
				updated, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET owner_id=?,claimed_at=CURRENT_TIMESTAMP(6),heartbeat_at=CURRENT_TIMESTAMP(6)
				WHERE id=? AND kind='run_all_parent' AND status='running' AND owner_id=? AND heartbeat_at=?
				AND heartbeat_at<TIMESTAMPADD(MICROSECOND,-?,UTC_TIMESTAMP(6))`, replacementOwner, candidate.ID, candidate.Owner, candidate.Heartbeat, lease.Microseconds())
				if err != nil {
					return err
				}
				affected, err := updated.RowsAffected()
				if err != nil {
					return err
				}
				if affected != 1 {
					return nil
				}
				if err := repository.ensureRunAllChildren(ctx, tx, candidate.ID); err != nil {
					return err
				}
				result.NewOwner = replacementOwner
			} else {
				updated, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='abandoned',error_class='abandoned',
				error_message='Worker ownership expired; stale execution recovered automatically.',error_step='ownership_lease',
				abandoned_previous_owner=owner_id,abandoned_previous_heartbeat=heartbeat_at,finished_at=CURRENT_TIMESTAMP(6)
				WHERE id=? AND status='running' AND owner_id=? AND heartbeat_at=?
				AND heartbeat_at<TIMESTAMPADD(MICROSECOND,-?,UTC_TIMESTAMP(6))`, candidate.ID, candidate.Owner, candidate.Heartbeat, lease.Microseconds())
				if err != nil {
					return err
				}
				affected, err := updated.RowsAffected()
				if err != nil {
					return err
				}
				if affected != 1 {
					return nil
				}
			}
			recovered = &result
			return nil
		})
		if err != nil || recovered != nil || !candidateSeen {
			return recovered, err
		}
		// The observed heartbeat renewed or another actor won the exact CAS.
		// Scan again so one race cannot hide other stale rows from this sweep.
	}
}

func (repository *Repository) ensureRunAllChildren(ctx context.Context, tx *sqlx.Tx, parentID uint64) error {
	parent, err := getRun(ctx, tx, parentID)
	if err != nil {
		return err
	}
	rangeValue, err := DecodeRange(parent.Parameters)
	if err != nil {
		return err
	}
	requester := parent.RequestedByUserID
	type identity struct {
		Position uint16 `db:"child_position"`
		JobKey   string `db:"job_key"`
	}
	var existing []identity
	if err := tx.SelectContext(ctx, &existing, `SELECT child_position,job_key FROM ingestion_runs WHERE parent_run_id=?`, parentID); err != nil {
		return err
	}
	byPosition := make(map[uint16]string, len(existing))
	byJob := make(map[string]uint16, len(existing))
	jobs := repository.catalog.Jobs()
	for _, child := range existing {
		if child.Position == 0 || int(child.Position) > len(jobs) || jobs[child.Position-1].Key != child.JobKey {
			return fmt.Errorf("Run All child identity position=%d job=%q is not canonical", child.Position, child.JobKey)
		}
		byPosition[child.Position], byJob[child.JobKey] = child.JobKey, child.Position
	}
	for index, job := range jobs {
		position := uint16(index + 1)
		if existingJob, found := byPosition[position]; found && existingJob != job.Key {
			return fmt.Errorf("Run All child position %d is %q, want %q", position, existingJob, job.Key)
		}
		if existingPosition, found := byJob[job.Key]; found && existingPosition != position {
			return fmt.Errorf("Run All child %q is position %d, want %d", job.Key, existingPosition, position)
		}
		if byPosition[position] == job.Key {
			continue
		}
		parameters, err := parametersForJob(job, rangeValue.From, rangeValue.To)
		if err != nil {
			return err
		}
		parentRef := parentID
		if _, err := insertRun(ctx, tx, KindRunAllChild, &parentRef, &position, job.Key, StatusPlanned, parameters, TriggerRunAll, fmt.Sprint(parentID), requester); err != nil {
			return err
		}
	}
	return nil
}

func (repository *Repository) RequestCancellation(ctx context.Context, runID uint64, reason string, requester securityctx.Requester) error {
	reason = safeText(reason, 255)
	_, err := databasepkg.RetryReplaySafeTx(ctx, repository.db, func(tx *sqlx.Tx) error {
		var row struct {
			Kind   Kind   `db:"kind"`
			Status Status `db:"status"`
		}
		if err := tx.GetContext(ctx, &row, `SELECT kind,status FROM ingestion_runs WHERE id=? FOR UPDATE`, runID); err != nil {
			return err
		}
		if IsTerminal(row.Status) {
			return nil
		}
		if row.Kind == KindRunAllParent {
			if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET cancel_requested_at=CURRENT_TIMESTAMP(6),cancel_reason=? WHERE id=?`, nullable(reason), runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='cancelled',finished_at=CURRENT_TIMESTAMP(6),cancel_requested_at=CURRENT_TIMESTAMP(6),cancel_reason=?
			WHERE parent_run_id=? AND status IN ('planned','queued')`, nullable(reason), runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET cancel_requested_at=CURRENT_TIMESTAMP(6),cancel_reason=?
			WHERE parent_run_id=? AND status='running'`, nullable(reason), runID); err != nil {
				return err
			}
		} else if row.Status == StatusPlanned || row.Status == StatusQueued {
			if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='cancelled',cancel_requested_at=CURRENT_TIMESTAMP(6),cancel_reason=?,finished_at=CURRENT_TIMESTAMP(6) WHERE id=?`, nullable(reason), runID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET cancel_requested_at=CURRENT_TIMESTAMP(6),cancel_reason=? WHERE id=?`, nullable(reason), runID); err != nil {
				return err
			}
		}
		if err := appendRunAudit(ctx, tx, requester, audit.ActionIngestionCancellationRequested, runID, nil); err != nil {
			return err
		}
		return nil
	})
	return err
}

func (repository *Repository) RecoverAbandoned(ctx context.Context, runID uint64, expectedOwner string, expectedHeartbeat time.Time, reason string, requester securityctx.Requester) error {
	reason = safeText(reason, 500)
	if expectedOwner == "" || expectedHeartbeat.IsZero() || reason == "" {
		return fmt.Errorf("exact owner, heartbeat, and recovery reason are required")
	}
	_, err := databasepkg.RetryReplaySafeTx(ctx, repository.db, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='abandoned',error_class='abandoned',error_message=?,
		abandoned_previous_owner=owner_id,abandoned_previous_heartbeat=heartbeat_at,finished_at=CURRENT_TIMESTAMP(6)
		WHERE id=? AND status='running' AND owner_id=? AND heartbeat_at=?`, reason, runID, expectedOwner, expectedHeartbeat)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return ErrTransition
		}
		if err := appendRunAudit(ctx, tx, requester, audit.ActionIngestionAbandonedRecovered, runID, nil); err != nil {
			return err
		}
		return nil
	})
	return err
}

func appendRunAudit(ctx context.Context, executor sqlx.ExtContext, requester securityctx.Requester, action audit.Action, runID uint64, metadata audit.Metadata) error {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Append(ctx, executor, audit.Event{
		Attribution: audit.Attribution{Actor: &actor, Effective: &effective},
		Action:      action, Resource: audit.ResourceIngestionRun, ResourceID: runID, Metadata: metadata, CreatedAt: time.Now().UTC(),
	})
}

func (repository *Repository) ReconcileOneParent(ctx context.Context) (bool, error) {
	return repository.reconcileParent(ctx, 0, "")
}

// ReconcileParent advances only the exact live Run All parent ownership
// instance. A caller holding an expired token cannot queue or finish children.
func (repository *Repository) ReconcileParent(ctx context.Context, parentID uint64, ownerID string) (bool, error) {
	if parentID == 0 || len(ownerID) != 64 {
		return false, fmt.Errorf("exact Run All parent ownership is required")
	}
	return repository.reconcileParent(ctx, parentID, ownerID)
}

func (repository *Repository) reconcileParent(ctx context.Context, exactID uint64, exactOwner string) (bool, error) {
	var changed bool
	_, err := databasepkg.RetryReplaySafeTx(ctx, repository.db, func(tx *sqlx.Tx) error {
		var transactionErr error
		changed, transactionErr = repository.reconcileParentTransaction(ctx, tx, exactID, exactOwner)
		return transactionErr
	})
	return changed, err
}

func (repository *Repository) reconcileParentTransaction(ctx context.Context, tx *sqlx.Tx, exactID uint64, exactOwner string) (bool, error) {
	var parent struct {
		ID    uint64 `db:"id"`
		Owner string `db:"owner_id"`
	}
	var err error
	if exactID != 0 {
		err = tx.GetContext(ctx, &parent, `SELECT id,COALESCE(owner_id,'') owner_id FROM ingestion_runs
			WHERE id=? AND kind='run_all_parent' AND status='running' AND owner_id=? FOR UPDATE`, exactID, exactOwner)
	} else {
		err = tx.GetContext(ctx, &parent, `SELECT parent.id,COALESCE(parent.owner_id,'') owner_id FROM ingestion_runs parent WHERE parent.kind='run_all_parent' AND parent.status='running'
			AND NOT EXISTS (SELECT 1 FROM ingestion_runs child WHERE child.parent_run_id=parent.id AND child.status IN ('queued','running'))
			ORDER BY parent.id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if exactID != 0 {
			return false, ErrOwnershipLost
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	parentID := parent.ID
	if parent.Owner == "" {
		return false, fmt.Errorf("Run All parent has no owner")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET heartbeat_at=CURRENT_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=?`, parentID, parent.Owner); err != nil {
		return false, err
	}
	var cancelled bool
	if err := tx.GetContext(ctx, &cancelled, `SELECT cancel_requested_at IS NOT NULL FROM ingestion_runs WHERE id=?`, parentID); err != nil {
		return false, err
	}
	var active int
	if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM ingestion_runs WHERE parent_run_id=? AND status IN ('queued','running')`, parentID); err != nil {
		return false, err
	}
	if active > 0 {
		return false, nil
	}
	if cancelled {
		_, err = tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='cancelled',finished_at=CURRENT_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=?`, parentID, parent.Owner)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	var child struct {
		ID     uint64 `db:"id"`
		JobKey string `db:"job_key"`
	}
	err = tx.GetContext(ctx, &child, `SELECT id,job_key FROM ingestion_runs WHERE parent_run_id=? AND status='planned' ORDER BY child_position LIMIT 1 FOR UPDATE`, parentID)
	if errors.Is(err, sql.ErrNoRows) {
		status, err := aggregateParentStatus(ctx, tx, parentID)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET status=?,finished_at=CURRENT_TIMESTAMP(6) WHERE id=? AND status='running' AND owner_id=?`, status, parentID, parent.Owner); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	enabled, err := sourceEnabled(ctx, tx, child.JobKey)
	if err != nil {
		return false, err
	}
	if !enabled {
		_, err = tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='skipped',skip_reason='source_disabled',finished_at=CURRENT_TIMESTAMP(6) WHERE id=?`, child.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='queued' WHERE id=? AND status='planned'`, child.ID)
		if duplicate(err) {
			_, err = tx.ExecContext(ctx, `UPDATE ingestion_runs SET status='skipped',skip_reason='job_busy',finished_at=CURRENT_TIMESTAMP(6) WHERE id=?`, child.ID)
		}
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func aggregateParentStatus(ctx context.Context, tx *sqlx.Tx, parentID uint64) (Status, error) {
	counts := map[string]int{}
	rows, err := tx.QueryxContext(ctx, `SELECT status,COUNT(*) FROM ingestion_runs WHERE parent_run_id=? GROUP BY status`, parentID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return "", err
		}
		counts[status] = count
	}
	if counts[string(StatusFailed)]+counts[string(StatusCancelled)]+counts[string(StatusAbandoned)] > 0 {
		return StatusFailed, nil
	}
	if counts[string(StatusSkipped)] > 0 {
		return StatusCompletedWithSkips, nil
	}
	return StatusCompleted, nil
}

type runRow struct {
	ID                sql.NullInt64  `db:"id"`
	ParentRunID       sql.NullInt64  `db:"parent_run_id"`
	ChildPosition     sql.NullInt64  `db:"child_position"`
	Kind              string         `db:"kind"`
	JobKey            string         `db:"job_key"`
	Status            string         `db:"status"`
	ParameterKind     string         `db:"parameter_kind"`
	ParameterVersion  uint16         `db:"parameter_version"`
	ParameterJSON     []byte         `db:"parameters_json"`
	ParameterChecksum []byte         `db:"parameter_checksum"`
	TriggerType       string         `db:"trigger_type"`
	TriggerReference  sql.NullString `db:"trigger_reference"`
	RequestedByUserID sql.NullInt64  `db:"requested_by_user_id"`
	OwnerID           sql.NullString `db:"owner_id"`
	HeartbeatAt       sql.NullTime   `db:"heartbeat_at"`
	SnapshotDate      sql.NullString `db:"snapshot_date"`
	CancelRequestedAt sql.NullTime   `db:"cancel_requested_at"`
	ProgressTotal     uint64         `db:"progress_total"`
	ProgressStarted   uint64         `db:"progress_started"`
	ProgressSucceeded uint64         `db:"progress_succeeded"`
	ProgressFailed    uint64         `db:"progress_failed"`
	RowsProcessed     uint64         `db:"rows_processed"`
	CurrentStep       sql.NullString `db:"current_step"`
	MapperDiagnostics []byte         `db:"mapper_diagnostics"`
}

func getRun(ctx context.Context, query sqlx.QueryerContext, id uint64) (Run, error) {
	var row runRow
	if err := sqlx.GetContext(ctx, query, &row, `SELECT id,parent_run_id,child_position,kind,COALESCE(job_key,'') job_key,status,
		parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,requested_by_user_id,
		owner_id,heartbeat_at,DATE_FORMAT(snapshot_date,'%Y-%m-%d') snapshot_date,cancel_requested_at,
		progress_total,progress_started,progress_succeeded,progress_failed,rows_processed,current_step,mapper_diagnostics FROM ingestion_runs WHERE id=?`, id); err != nil {
		return Run{}, err
	}
	run := Run{ID: uint64(row.ID.Int64), Kind: Kind(row.Kind), JobKey: row.JobKey, Status: Status(row.Status), Trigger: Trigger(row.TriggerType), TriggerReference: row.TriggerReference.String,
		OwnerID: row.OwnerID.String, CancelRequested: row.CancelRequestedAt.Valid,
		Progress: Progress{Total: row.ProgressTotal, Started: row.ProgressStarted, Succeeded: row.ProgressSucceeded, Failed: row.ProgressFailed, Rows: row.RowsProcessed, Step: row.CurrentStep.String}}
	if row.ParentRunID.Valid {
		value := uint64(row.ParentRunID.Int64)
		run.ParentRunID = &value
	}
	if row.ChildPosition.Valid {
		value := uint16(row.ChildPosition.Int64)
		run.ChildPosition = &value
	}
	if row.RequestedByUserID.Valid {
		value := uint64(row.RequestedByUserID.Int64)
		run.RequestedByUserID = &value
	}
	if row.HeartbeatAt.Valid {
		value := row.HeartbeatAt.Time
		run.HeartbeatAt = &value
	}
	if row.SnapshotDate.Valid {
		run.SnapshotDate, _ = ingestion.ParseCalendarDate(row.SnapshotDate.String)
	}
	run.Parameters = Parameters{Kind: ParameterKind(row.ParameterKind), Version: row.ParameterVersion, JSON: append([]byte(nil), row.ParameterJSON...)}
	if len(row.ParameterChecksum) == 32 {
		copy(run.Parameters.Checksum[:], row.ParameterChecksum)
	}
	diagnostics, err := decodeMapperDiagnostics(row.MapperDiagnostics)
	if err != nil {
		return Run{}, err
	}
	run.MapperDiagnostics = diagnostics
	return run, nil
}

func (repository *Repository) Get(ctx context.Context, id uint64) (Run, error) {
	return getRun(ctx, repository.db, id)
}

func sourceEnabled(ctx context.Context, tx *sqlx.Tx, key string) (bool, error) {
	var enabled bool
	if err := tx.GetContext(ctx, &enabled, `SELECT enabled FROM source_settings WHERE source_id=? FOR UPDATE`, key); err != nil {
		return false, err
	}
	return enabled, nil
}

func insertRun(ctx context.Context, tx *sqlx.Tx, kind Kind, parent *uint64, position *uint16, jobKey string, status Status, parameters Parameters, trigger Trigger, reference string, requester *uint64) (sql.Result, error) {
	return tx.ExecContext(ctx, `INSERT INTO ingestion_runs
		(kind,parent_run_id,child_position,job_key,status,parameter_kind,parameter_version,parameters_json,parameter_checksum,trigger_type,trigger_reference,requested_by_user_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, kind, parent, position, nullable(jobKey), status, parameters.Kind, parameters.Version, parameters.JSON, parameters.Checksum[:], trigger, nullable(reference), requester)
}

func duplicate(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func safeText(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}
