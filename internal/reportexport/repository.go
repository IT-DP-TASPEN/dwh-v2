package reportexport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Repository struct{ database *sqlx.DB }

const visibleJobColumns = `
	j.id,j.report_id,j.report_name,j.submitted_by_user_id,
	u.name AS requester_name,u.username AS requester_username,
	j.status,j.attempt,j.progress_rows,j.current_part,j.final_parts,j.total_rows,
	j.artifact_name,j.artifact_type,j.artifact_size,j.artifact_expires_at,j.artifact_deleted_at,
	j.failure_message,j.created_at,j.started_at,j.finished_at,j.updated_at`

func NewRepository(database *sqlx.DB) (*Repository, error) {
	if database == nil {
		return nil, fmt.Errorf("export database is required")
	}
	return &Repository{database: database}, nil
}

func (repository *Repository) Submit(ctx context.Context, requester securityctx.Requester, report reporting.Template, normalized map[string]reporting.NormalizedValue, now time.Time) (Job, error) {
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var currentRevision uint64
	err = tx.GetContext(ctx, &currentRevision, `SELECT r.revision FROM users u JOIN roles role ON role.id=u.role_id JOIN report_templates r ON r.id=? JOIN report_datasources d ON d.id=r.datasource_id JOIN report_template_user_access a ON a.report_id=r.id AND a.user_id=u.id WHERE u.id=? AND u.is_active=TRUE AND r.status='active' AND d.status='active' AND (role.slug=? OR EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=role.id AND p.key='reports.export')) FOR SHARE`, report.ID, requester.Effective.UserID, access.AdminRoleSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, reporting.ErrForbidden
	}
	if err != nil {
		return Job{}, err
	}
	if currentRevision != report.Revision {
		return Job{}, reporting.ErrConflict
	}
	canonical := reporting.CanonicalInput(normalized)
	if _, err := reporting.NormalizeSnapshotParameters(report.Parameters, canonical); err != nil {
		return Job{}, err
	}
	snapshot := Snapshot{Version: 1, Parameters: report.Parameters, Input: canonical}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return Job{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO report_export_jobs (report_id,report_name,sql_text,datasource_id,parameter_version,parameters_json,submitted_by_user_id,status,created_at,updated_at) VALUES (?,?,?,?,1,?,?,'queued',?,?)`,
		report.ID, report.Name, report.SQLText, report.DatasourceID, encoded, requester.Effective.UserID, now.UTC(), now.UTC())
	if err != nil {
		return Job{}, fmt.Errorf("submit report export: %w", err)
	}
	id, _ := result.LastInsertId()
	metadata := audit.ReportExportSubmittedMetadata{
		ReportIdentityMetadata: reporting.AuditIdentity(report),
		ExportJobID:            uint64(id),
		Parameters:             reporting.AuditParameters(report.Parameters, normalized),
		Outcome:                "submitted",
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportExportSubmitted, uint64(id), metadata, now); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return repository.Find(ctx, uint64(id))
}

func (repository *Repository) Find(ctx context.Context, id uint64) (Job, error) {
	var job Job
	if err := repository.database.GetContext(ctx, &job, `SELECT * FROM report_export_jobs WHERE id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, reporting.ErrNotFound
		}
		return Job{}, err
	}
	return job, nil
}

func (repository *Repository) CountVisible(ctx context.Context, scope Scope, userID uint64) (int64, error) {
	where, arguments, err := visibleScope(scope, userID)
	if err != nil {
		return 0, err
	}
	var total int64
	if err := repository.database.GetContext(ctx, &total, `SELECT COUNT(*) FROM report_export_jobs j`+where, arguments...); err != nil {
		return 0, fmt.Errorf("count visible report exports: %w", err)
	}
	return total, nil
}

func (repository *Repository) ListVisible(ctx context.Context, scope Scope, userID uint64, limit, offset int) ([]VisibleJob, error) {
	where, arguments, err := visibleScope(scope, userID)
	if err != nil {
		return nil, err
	}
	arguments = append(arguments, limit, offset)
	jobs := make([]VisibleJob, 0)
	statement := `SELECT ` + visibleJobColumns + ` FROM report_export_jobs j LEFT JOIN users u ON u.id=j.submitted_by_user_id` + where + ` ORDER BY j.created_at DESC,j.id DESC LIMIT ? OFFSET ?`
	if err := repository.database.SelectContext(ctx, &jobs, statement, arguments...); err != nil {
		return nil, fmt.Errorf("list visible report exports: %w", err)
	}
	return jobs, nil
}

func (repository *Repository) FindVisible(ctx context.Context, id uint64, scope Scope, userID uint64) (VisibleJob, error) {
	where, arguments, err := visibleScope(scope, userID)
	if err != nil {
		return VisibleJob{}, err
	}
	arguments = append([]any{id}, arguments...)
	var job VisibleJob
	statement := `SELECT ` + visibleJobColumns + ` FROM report_export_jobs j LEFT JOIN users u ON u.id=j.submitted_by_user_id WHERE j.id=?`
	if where != "" {
		statement += ` AND ` + strings.TrimPrefix(where, ` WHERE `)
	}
	if err := repository.database.GetContext(ctx, &job, statement, arguments...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return VisibleJob{}, reporting.ErrNotFound
		}
		return VisibleJob{}, fmt.Errorf("find visible report export: %w", err)
	}
	return job, nil
}

func visibleScope(scope Scope, userID uint64) (string, []any, error) {
	switch scope {
	case ScopeAll:
		return "", nil, nil
	case ScopeMine:
		if userID == 0 {
			return "", nil, fmt.Errorf("effective export viewer is required")
		}
		return ` WHERE j.submitted_by_user_id=?`, []any{userID}, nil
	default:
		return "", nil, fmt.Errorf("invalid report export scope %q", scope)
	}
}

func (repository *Repository) HealthForUser(ctx context.Context, userID uint64) (Health, error) {
	var rows []struct {
		Status Status `db:"status"`
		Count  uint64 `db:"count"`
	}
	if err := repository.database.SelectContext(ctx, &rows, `SELECT status,COUNT(*) count FROM report_export_jobs
		WHERE submitted_by_user_id=? AND status IN ('queued','running','failed') GROUP BY status`, userID); err != nil {
		return Health{}, err
	}
	var health Health
	for _, row := range rows {
		switch row.Status {
		case StatusQueued:
			health.Queued = row.Count
		case StatusRunning:
			health.Running = row.Count
		case StatusFailed:
			health.Failed = row.Count
		}
	}
	return health, nil
}

func (repository *Repository) RecentFailuresForUser(ctx context.Context, userID uint64, limit int) ([]Job, error) {
	jobs := make([]Job, 0, limit)
	if err := repository.database.SelectContext(ctx, &jobs, `SELECT * FROM report_export_jobs
		WHERE submitted_by_user_id=? AND status=? ORDER BY COALESCE(finished_at,updated_at) DESC,id DESC LIMIT ?`, userID, StatusFailed, limit); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (repository *Repository) Claim(ctx context.Context, owner string, now time.Time) (*Job, error) {
	if len(owner) != 64 {
		return nil, fmt.Errorf("opaque export owner is required")
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id uint64
	err = tx.GetContext(ctx, &id, `SELECT id FROM report_export_jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE report_export_jobs SET status='running',attempt=attempt+1,owner_id=?,claimed_at=?,heartbeat_at=?,started_at=COALESCE(started_at,?),progress_rows=0,current_part=0,failure_class=NULL,failure_message=NULL WHERE id=? AND status='queued'`, owner, now.UTC(), now.UTC(), now.UTC(), id)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, reporting.ErrClaimLost
	}
	var job Job
	if err := tx.GetContext(ctx, &job, `SELECT * FROM report_export_jobs WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (repository *Repository) Heartbeat(ctx context.Context, id uint64, owner string, attempt uint32, now time.Time) (bool, error) {
	result, err := repository.database.ExecContext(ctx, `UPDATE report_export_jobs SET heartbeat_at=? WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, now.UTC(), id, owner, attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) Progress(ctx context.Context, id uint64, owner string, attempt uint32, rows uint64, part uint32) (bool, error) {
	result, err := repository.database.ExecContext(ctx, `UPDATE report_export_jobs SET progress_rows=?,current_part=? WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, rows, part, id, owner, attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) Succeed(ctx context.Context, id uint64, owner string, attempt uint32, artifact Artifact, expires, now time.Time) (bool, error) {
	result, err := repository.database.ExecContext(ctx, `UPDATE report_export_jobs SET status='succeeded',final_parts=?,total_rows=?,artifact_path=?,artifact_name=?,artifact_type=?,artifact_size=?,artifact_expires_at=?,finished_at=?,heartbeat_at=? WHERE id=? AND status='running' AND owner_id=? AND attempt=?`,
		artifact.Parts, artifact.Rows, artifact.RelativePath, artifact.Name, artifact.Type, artifact.Size, expires.UTC(), now.UTC(), now.UTC(), id, owner, attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) Fail(ctx context.Context, id uint64, owner string, attempt uint32, class, message string, now time.Time) (bool, error) {
	if len(class) > 64 {
		class = class[:64]
	}
	if len(message) > 500 {
		message = message[:500]
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE report_export_jobs SET status='failed',failure_class=?,failure_message=?,finished_at=?,heartbeat_at=? WHERE id=? AND status='running' AND owner_id=? AND attempt=?`, class, message, now.UTC(), now.UTC(), id, owner, attempt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (repository *Repository) RequeueStale(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := repository.database.ExecContext(ctx, `UPDATE report_export_jobs SET status='queued',owner_id=NULL,claimed_at=NULL,heartbeat_at=NULL WHERE status='running' AND heartbeat_at<?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (repository *Repository) EligibleForExecution(ctx context.Context, userID, reportID, datasourceID uint64) (bool, error) {
	return repository.eligible(ctx, userID, reportID, datasourceID, true)
}

func (repository *Repository) eligible(ctx context.Context, userID, reportID, datasourceID uint64, requireDatasource bool) (bool, error) {
	datasourceJoin, datasourceClause := "JOIN report_datasources d ON d.id=r.datasource_id", ""
	arguments := []any{reportID, userID, access.AdminRoleSlug}
	if requireDatasource {
		datasourceJoin = "JOIN report_datasources d ON d.id=?"
		datasourceClause = " AND d.status='active'"
		arguments = []any{reportID, datasourceID, userID, access.AdminRoleSlug}
	}
	var eligible bool
	err := repository.database.GetContext(ctx, &eligible, `SELECT EXISTS(SELECT 1 FROM users u JOIN roles role ON role.id=u.role_id JOIN report_templates r ON r.id=? `+datasourceJoin+` JOIN report_template_user_access a ON a.report_id=r.id AND a.user_id=u.id WHERE u.id=? AND u.is_active=TRUE AND r.status='active'`+datasourceClause+` AND (role.slug=? OR EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=role.id AND p.key='reports.export')))`, arguments...)
	return eligible, err
}

func (repository *Repository) AuthorizeDownload(ctx context.Context, id, userID uint64, viewAll bool) (Job, DownloadAccess, error) {
	job, err := repository.Find(ctx, id)
	if err != nil {
		return Job{}, "", err
	}
	if job.SubmittedByUserID == userID {
		eligible, eligibleErr := repository.eligible(ctx, userID, job.ReportID, 0, false)
		if eligibleErr == nil {
			if access := authorizedDownloadAccess(eligible, viewAll); access != "" {
				return job, access, nil
			}
			return Job{}, "", nil
		}
		if eligibleErr != nil && !viewAll {
			return Job{}, "", eligibleErr
		}
	}
	if access := authorizedDownloadAccess(false, viewAll); access != "" {
		return job, access, nil
	}
	return Job{}, "", nil
}

func authorizedDownloadAccess(ownerAllowed, viewAll bool) DownloadAccess {
	if ownerAllowed {
		return DownloadAccessOwner
	}
	if viewAll {
		return DownloadAccessViewAll
	}
	return ""
}

func (repository *Repository) ReferencedArtifacts(ctx context.Context) (map[string]struct{}, error) {
	var paths []string
	if err := repository.database.SelectContext(ctx, &paths, `SELECT artifact_path FROM report_export_jobs WHERE status='succeeded' AND artifact_path IS NOT NULL AND artifact_deleted_at IS NULL`); err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		result[path] = struct{}{}
	}
	return result, nil
}

func (repository *Repository) ExpiredArtifacts(ctx context.Context, now time.Time) ([]Job, error) {
	jobs := make([]Job, 0)
	if err := repository.database.SelectContext(ctx, &jobs, `SELECT * FROM report_export_jobs WHERE status='succeeded' AND artifact_path IS NOT NULL AND artifact_deleted_at IS NULL AND artifact_expires_at<=?`, now.UTC()); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (repository *Repository) MarkArtifactDeleted(ctx context.Context, id uint64, now time.Time) error {
	_, err := repository.database.ExecContext(ctx, `UPDATE report_export_jobs SET artifact_deleted_at=? WHERE id=? AND status='succeeded' AND artifact_deleted_at IS NULL`, now.UTC(), id)
	return err
}

func (repository *Repository) RecordDownload(ctx context.Context, requester securityctx.Requester, job Job, accessPath DownloadAccess, now time.Time) error {
	metadata := audit.ReportExportDownloadedMetadata{
		ReportTemplateID:  job.ReportID,
		ReportName:        job.ReportName,
		DatasourceID:      job.DatasourceID,
		ExportJobID:       job.ID,
		SubmittedByUserID: job.SubmittedByUserID,
		AccessPath:        string(accessPath),
	}
	if job.ArtifactName != nil {
		metadata.ArtifactName = *job.ArtifactName
	}
	if job.ArtifactType != nil {
		metadata.ArtifactType = *job.ArtifactType
	}
	return appendAudit(ctx, repository.database, requester, audit.ActionReportExportDownloaded, job.ID, metadata, now)
}

func appendAudit(ctx context.Context, executor sqlx.ExtContext, requester securityctx.Requester, action audit.Action, id uint64, metadata audit.Metadata, now time.Time) error {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Append(ctx, executor, audit.Event{Attribution: audit.Attribution{Actor: &actor, Effective: &effective}, Action: action, Resource: audit.ResourceReportExport, ResourceID: id, Metadata: metadata, CreatedAt: now.UTC()})
}
