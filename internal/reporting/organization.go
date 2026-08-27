package reporting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"
)

const MaxFolderNameLength = 100

var ErrFolderNameTaken = errors.New("report folder name already exists")

type RuntimeReportFilter struct {
	Query    string
	Starred  bool
	FolderID *uint64
}

type RuntimeReport struct {
	ID             uint64
	Name           string
	Description    string
	DatasourceName string
	FolderID       *uint64
	Starred        bool
}

type UserReportFolder struct {
	ID                 uint64 `db:"id"`
	Name               string `db:"name"`
	VisibleReportCount int    `db:"visible_report_count"`
}

type RuntimeReportOrganization struct {
	Reports             []RuntimeReport
	Folders             []UserReportFolder
	StarredVisibleCount int
}

type runtimeReportRow struct {
	ID             uint64        `db:"id"`
	Name           string        `db:"name"`
	Description    string        `db:"description"`
	DatasourceName string        `db:"datasource_name"`
	FolderID       sql.NullInt64 `db:"folder_id"`
	Starred        bool          `db:"starred"`
}

func NormalizeFolderName(name string) string { return strings.TrimSpace(name) }

func ValidateFolderName(name string) error {
	name = NormalizeFolderName(name)
	if name == "" {
		return fmt.Errorf("folder name must not be empty")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("folder name must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > MaxFolderNameLength {
		return fmt.Errorf("folder name must be at most %d characters", MaxFolderNameLength)
	}
	return nil
}

func (repository *Repository) ListRuntimeReportOrganization(ctx context.Context, userID uint64, filter RuntimeReportFilter) (RuntimeReportOrganization, error) {
	if userID == 0 || filter.Starred && filter.FolderID != nil {
		return RuntimeReportOrganization{}, ErrInvalid
	}
	tx, err := repository.database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RuntimeReportOrganization{}, err
	}
	defer tx.Rollback()

	result := RuntimeReportOrganization{Folders: make([]UserReportFolder, 0), Reports: make([]RuntimeReport, 0)}
	if err := tx.SelectContext(ctx, &result.Folders, `SELECT f.id,f.name,COALESCE(COUNT(d.id), 0) AS visible_report_count
		FROM report_user_folders f
		JOIN users u ON u.id=f.user_id AND u.is_active=TRUE
		LEFT JOIN report_user_preferences p ON p.user_id=f.user_id AND p.folder_id=f.id
		LEFT JOIN report_template_user_access a ON a.user_id=f.user_id AND a.report_id=p.report_id
		LEFT JOIN report_templates r ON r.id=a.report_id AND r.status='active'
		LEFT JOIN report_datasources d ON d.id=r.datasource_id AND d.status='active'
		WHERE f.user_id=?
		GROUP BY f.id,f.name
		ORDER BY f.name,f.id`, userID); err != nil {
		return RuntimeReportOrganization{}, fmt.Errorf("list report folders: %w", err)
	}
	if filter.FolderID != nil {
		owned := false
		for _, folder := range result.Folders {
			if folder.ID == *filter.FolderID {
				owned = true
				break
			}
		}
		if !owned {
			return RuntimeReportOrganization{}, ErrNotFound
		}
	}
	if err := tx.GetContext(ctx, &result.StarredVisibleCount, `SELECT COUNT(*)
		FROM report_user_preferences p
		JOIN users u ON u.id=p.user_id AND u.is_active=TRUE
		JOIN report_template_user_access a ON a.user_id=p.user_id AND a.report_id=p.report_id
		JOIN report_templates r ON r.id=a.report_id AND r.status='active'
		JOIN report_datasources d ON d.id=r.datasource_id AND d.status='active'
		WHERE p.user_id=? AND p.starred=TRUE`, userID); err != nil {
		return RuntimeReportOrganization{}, fmt.Errorf("count starred reports: %w", err)
	}

	query := `SELECT r.id,r.name,r.description,d.name AS datasource_name,p.folder_id,COALESCE(p.starred,FALSE) AS starred
		FROM users u
		JOIN report_template_user_access a ON a.user_id=u.id
		JOIN report_templates r ON r.id=a.report_id AND r.status='active'
		JOIN report_datasources d ON d.id=r.datasource_id AND d.status='active'
		LEFT JOIN report_user_preferences p ON p.user_id=u.id AND p.report_id=r.id
		WHERE u.id=? AND u.is_active=TRUE`
	arguments := []any{userID}
	if filter.Starred {
		query += ` AND p.starred=TRUE`
	} else if filter.FolderID != nil {
		query += ` AND p.folder_id=?`
		arguments = append(arguments, *filter.FolderID)
	}
	if search := NormalizeFolderName(filter.Query); search != "" {
		pattern := "%" + escapeReportLike(search) + "%"
		query += ` AND (r.name LIKE ? ESCAPE '!' OR r.description LIKE ? ESCAPE '!')`
		arguments = append(arguments, pattern, pattern)
	}
	query += ` ORDER BY r.name,r.id`
	rows := make([]runtimeReportRow, 0)
	if err := tx.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return RuntimeReportOrganization{}, fmt.Errorf("list runtime reports: %w", err)
	}
	for _, row := range rows {
		value := RuntimeReport{ID: row.ID, Name: row.Name, Description: row.Description, DatasourceName: row.DatasourceName, Starred: row.Starred}
		if row.FolderID.Valid {
			folderID := uint64(row.FolderID.Int64)
			value.FolderID = &folderID
		}
		result.Reports = append(result.Reports, value)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeReportOrganization{}, err
	}
	return result, nil
}

func (repository *Repository) CreateUserReportFolder(ctx context.Context, userID uint64, name string, now time.Time) (UserReportFolder, error) {
	name = NormalizeFolderName(name)
	if userID == 0 || ValidateFolderName(name) != nil {
		return UserReportFolder{}, ErrInvalid
	}
	result, err := repository.database.ExecContext(ctx, `INSERT INTO report_user_folders (user_id,name,created_at,updated_at) VALUES (?,?,?,?)`, userID, name, now.UTC(), now.UTC())
	if isDuplicateEntry(err) {
		return UserReportFolder{}, ErrFolderNameTaken
	}
	if err != nil {
		return UserReportFolder{}, fmt.Errorf("create report folder: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return UserReportFolder{}, err
	}
	return UserReportFolder{ID: uint64(id), Name: name}, nil
}

func (repository *Repository) RenameUserReportFolder(ctx context.Context, userID, folderID uint64, name string, now time.Time) error {
	name = NormalizeFolderName(name)
	if userID == 0 || folderID == 0 || ValidateFolderName(name) != nil {
		return ErrInvalid
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockUserFolder(ctx, tx, userID, folderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE report_user_folders SET name=?,updated_at=? WHERE user_id=? AND id=?`, name, now.UTC(), userID, folderID); err != nil {
		if isDuplicateEntry(err) {
			return ErrFolderNameTaken
		}
		return fmt.Errorf("rename report folder: %w", err)
	}
	return tx.Commit()
}

func (repository *Repository) DeleteUserReportFolder(ctx context.Context, userID, folderID uint64) error {
	if userID == 0 || folderID == 0 {
		return ErrInvalid
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockUserFolder(ctx, tx, userID, folderID); err != nil {
		return err
	}
	// Intentionally unfile every membership, including currently inaccessible reports.
	if _, err := tx.ExecContext(ctx, `UPDATE report_user_preferences SET folder_id=NULL WHERE user_id=? AND folder_id=?`, userID, folderID); err != nil {
		return fmt.Errorf("unfile reports before folder deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM report_user_folders WHERE user_id=? AND id=?`, userID, folderID); err != nil {
		return fmt.Errorf("delete report folder: %w", err)
	}
	return tx.Commit()
}

func (repository *Repository) SetReportStarred(ctx context.Context, userID, reportID uint64, starred bool, now time.Time) error {
	if userID == 0 || reportID == 0 {
		return ErrInvalid
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockVisibleReport(ctx, tx, userID, reportID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO report_user_preferences (user_id,report_id,folder_id,starred,created_at,updated_at)
		VALUES (?,?,NULL,?,?,?) ON DUPLICATE KEY UPDATE starred=VALUES(starred),updated_at=VALUES(updated_at)`, userID, reportID, starred, now.UTC(), now.UTC()); err != nil {
		return fmt.Errorf("set report star: %w", err)
	}
	return tx.Commit()
}

func (repository *Repository) MoveReportToFolder(ctx context.Context, userID, reportID uint64, folderID *uint64, now time.Time) error {
	if userID == 0 || reportID == 0 || folderID != nil && *folderID == 0 {
		return ErrInvalid
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockVisibleReport(ctx, tx, userID, reportID); err != nil {
		return err
	}
	if folderID != nil {
		if err := lockUserFolder(ctx, tx, userID, *folderID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO report_user_preferences (user_id,report_id,folder_id,starred,created_at,updated_at)
		VALUES (?,?,?,FALSE,?,?) ON DUPLICATE KEY UPDATE folder_id=VALUES(folder_id),updated_at=VALUES(updated_at)`, userID, reportID, folderID, now.UTC(), now.UTC()); err != nil {
		return fmt.Errorf("move report to folder: %w", err)
	}
	return tx.Commit()
}

func lockVisibleReport(ctx context.Context, tx interface {
	GetContext(context.Context, any, string, ...any) error
}, userID, reportID uint64) error {
	var id uint64
	err := tx.GetContext(ctx, &id, `SELECT r.id
		FROM users u
		JOIN report_template_user_access a ON a.user_id=u.id AND a.report_id=?
		JOIN report_templates r ON r.id=a.report_id AND r.status='active'
		JOIN report_datasources d ON d.id=r.datasource_id AND d.status='active'
		WHERE u.id=? AND u.is_active=TRUE FOR SHARE`, reportID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}

func lockUserFolder(ctx context.Context, tx interface {
	GetContext(context.Context, any, string, ...any) error
}, userID, folderID uint64) error {
	var id uint64
	err := tx.GetContext(ctx, &id, `SELECT id FROM report_user_folders WHERE user_id=? AND id=? FOR UPDATE`, userID, folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func escapeReportLike(value string) string {
	value = strings.ReplaceAll(value, "!", "!!")
	value = strings.ReplaceAll(value, "%", "!%")
	return strings.ReplaceAll(value, "_", "!_")
}

func isDuplicateEntry(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}
