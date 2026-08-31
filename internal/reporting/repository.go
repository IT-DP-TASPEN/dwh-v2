package reporting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Repository struct {
	database *sqlx.DB
	cipher   *Cipher
}

func NewRepository(database *sqlx.DB, cipher *Cipher) (*Repository, error) {
	if database == nil || cipher == nil {
		return nil, fmt.Errorf("reporting database and cipher are required")
	}
	return &Repository{database: database, cipher: cipher}, nil
}

type DatasourceInput struct {
	Name, Description, Host, DatabaseName, Username, Password string
	Port                                                      uint16
	TLSPolicy                                                 TLSPolicy
}

func (repository *Repository) ListDatasources(ctx context.Context) ([]Datasource, error) {
	rows := make([]Datasource, 0)
	if err := repository.database.SelectContext(ctx, &rows, `SELECT * FROM report_datasources ORDER BY name,id`); err != nil {
		return nil, fmt.Errorf("list report datasources: %w", err)
	}
	return rows, nil
}

func (repository *Repository) FindDatasource(ctx context.Context, id uint64) (Datasource, error) {
	var value Datasource
	if err := repository.database.GetContext(ctx, &value, `SELECT * FROM report_datasources WHERE id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Datasource{}, ErrNotFound
		}
		return Datasource{}, fmt.Errorf("find report datasource: %w", err)
	}
	return value, nil
}

func (repository *Repository) CreateDatasource(ctx context.Context, requester securityctx.Requester, input DatasourceInput, now time.Time) (Datasource, error) {
	if err := validateDatasourceInput(input, true); err != nil {
		return Datasource{}, err
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return Datasource{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO report_datasources
		(name,description,host,port,database_name,username,password_ciphertext,tls_policy,status,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES (?,?,?,?,?,?,NULL,?,'disabled',?,?,?,?)`, input.Name, input.Description, input.Host, input.Port,
		input.DatabaseName, input.Username, input.TLSPolicy, requester.Effective.UserID, requester.Effective.UserID, now.UTC(), now.UTC())
	if err != nil {
		return Datasource{}, fmt.Errorf("insert report datasource: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Datasource{}, err
	}
	ciphertext, err := repository.cipher.Encrypt(uint64(id), input.Password)
	if err != nil {
		return Datasource{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE report_datasources SET password_ciphertext=? WHERE id=?`, ciphertext, id); err != nil {
		return Datasource{}, err
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportDatasourceCreated, audit.ResourceReportDatasource, uint64(id), nil, now); err != nil {
		return Datasource{}, err
	}
	if err := tx.Commit(); err != nil {
		return Datasource{}, err
	}
	return repository.FindDatasource(ctx, uint64(id))
}

func (repository *Repository) UpdateDatasource(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, input DatasourceInput, now time.Time) (Datasource, error) {
	if err := validateDatasourceInput(input, false); err != nil {
		return Datasource{}, err
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return Datasource{}, err
	}
	defer tx.Rollback()
	var existing Datasource
	if err := tx.GetContext(ctx, &existing, `SELECT * FROM report_datasources WHERE id=? FOR UPDATE`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Datasource{}, ErrNotFound
		}
		return Datasource{}, err
	}
	if existing.Revision != expectedRevision {
		return Datasource{}, ErrConflict
	}
	if existing.Status == StatusArchived {
		return Datasource{}, ErrInactive
	}
	ciphertext := existing.PasswordCiphertext
	if input.Password != "" {
		ciphertext, err = repository.cipher.Encrypt(id, input.Password)
		if err != nil {
			return Datasource{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE report_datasources SET name=?,description=?,host=?,port=?,database_name=?,username=?,password_ciphertext=?,tls_policy=?,revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=?`,
		input.Name, input.Description, input.Host, input.Port, input.DatabaseName, input.Username, ciphertext, input.TLSPolicy, requester.Effective.UserID, now.UTC(), id, expectedRevision)
	if err != nil {
		return Datasource{}, fmt.Errorf("update report datasource: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Datasource{}, ErrConflict
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportDatasourceUpdated, audit.ResourceReportDatasource, id, audit.DatasourceUpdatedMetadata{CredentialsChanged: input.Password != ""}, now); err != nil {
		return Datasource{}, err
	}
	if err := tx.Commit(); err != nil {
		return Datasource{}, err
	}
	return repository.FindDatasource(ctx, id)
}

func (repository *Repository) SetDatasourceStatus(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, status Status, now time.Time) error {
	if status != StatusActive && status != StatusDisabled && status != StatusArchived {
		return fmt.Errorf("%w: invalid datasource status", ErrInvalid)
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from Status
	if err := tx.GetContext(ctx, &from, `SELECT status FROM report_datasources WHERE id=? FOR UPDATE`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if from == StatusArchived {
		return ErrInactive
	}
	result, err := tx.ExecContext(ctx, `UPDATE report_datasources SET status=?,revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=?`, status, requester.Effective.UserID, now.UTC(), id, expectedRevision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	metadata := audit.StatusChangeMetadata{From: string(from), To: string(status)}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportDatasourceStateChanged, audit.ResourceReportDatasource, id, metadata, now); err != nil {
		return err
	}
	return tx.Commit()
}

func validateDatasourceInput(input DatasourceInput, passwordRequired bool) error {
	input.Name, input.Host, input.DatabaseName, input.Username = strings.TrimSpace(input.Name), strings.TrimSpace(input.Host), strings.TrimSpace(input.DatabaseName), strings.TrimSpace(input.Username)
	if input.Name == "" || input.Host == "" || input.DatabaseName == "" || input.Username == "" || input.Port == 0 {
		return fmt.Errorf("%w: datasource connection fields are required", ErrInvalid)
	}
	if passwordRequired && input.Password == "" {
		return fmt.Errorf("%w: datasource password is required", ErrInvalid)
	}
	if input.TLSPolicy != TLSRequired && input.TLSPolicy != TLSDisabled {
		return fmt.Errorf("%w: TLS policy must be required or disabled", ErrInvalid)
	}
	return nil
}

type TemplateInput struct {
	Name, Description, SQLText string
	DatasourceID               uint64
	Parameters                 []Parameter
}

const templateColumns = `r.id,r.name,r.description,r.datasource_id,d.name AS datasource_name,d.status AS datasource_status,r.sql_text,r.status,r.revision,r.created_by_user_id,r.updated_by_user_id,r.created_at,r.updated_at`

func (repository *Repository) ListTemplates(ctx context.Context) ([]Template, error) {
	rows := make([]Template, 0)
	if err := repository.database.SelectContext(ctx, &rows, `SELECT `+templateColumns+` FROM report_templates r JOIN report_datasources d ON d.id=r.datasource_id ORDER BY r.name,r.id`); err != nil {
		return nil, fmt.Errorf("list report templates: %w", err)
	}
	return rows, nil
}

func (repository *Repository) FindTemplate(ctx context.Context, id uint64) (Template, error) {
	tx, err := repository.database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Template{}, err
	}
	defer tx.Rollback()
	var value Template
	if err := tx.GetContext(ctx, &value, `SELECT `+templateColumns+` FROM report_templates r JOIN report_datasources d ON d.id=r.datasource_id WHERE r.id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Template{}, ErrNotFound
		}
		return Template{}, fmt.Errorf("find report template: %w", err)
	}
	parameters, err := parameters(ctx, tx, id)
	if err != nil {
		return Template{}, err
	}
	value.Parameters = parameters
	if err := tx.Commit(); err != nil {
		return Template{}, err
	}
	return value, nil
}

func (repository *Repository) CreateTemplate(ctx context.Context, requester securityctx.Requester, input TemplateInput, now time.Time) (Template, error) {
	if err := validateTemplateInput(input); err != nil {
		return Template{}, err
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return Template{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO report_templates (name,description,datasource_id,sql_text,status,created_by_user_id,updated_by_user_id,created_at,updated_at) VALUES (?,?,?,?,'disabled',?,?,?,?)`,
		input.Name, input.Description, input.DatasourceID, input.SQLText, requester.Effective.UserID, requester.Effective.UserID, now.UTC(), now.UTC())
	if err != nil {
		return Template{}, fmt.Errorf("insert report template: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Template{}, err
	}
	if err := replaceParameters(ctx, tx, uint64(id), input.Parameters); err != nil {
		return Template{}, err
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportTemplateCreated, audit.ResourceReportTemplate, uint64(id), nil, now); err != nil {
		return Template{}, err
	}
	if err := tx.Commit(); err != nil {
		return Template{}, err
	}
	return repository.FindTemplate(ctx, uint64(id))
}

func (repository *Repository) UpdateTemplate(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, input TemplateInput, now time.Time) (Template, error) {
	if err := validateTemplateInput(input); err != nil {
		return Template{}, err
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return Template{}, err
	}
	defer tx.Rollback()
	var existing struct {
		Revision uint64 `db:"revision"`
		Status   Status `db:"status"`
	}
	if err := tx.GetContext(ctx, &existing, `SELECT revision,status FROM report_templates WHERE id=? FOR UPDATE`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Template{}, ErrNotFound
		}
		return Template{}, err
	}
	if existing.Revision != expectedRevision {
		return Template{}, ErrConflict
	}
	if existing.Status == StatusArchived {
		return Template{}, ErrInactive
	}
	result, err := tx.ExecContext(ctx, `UPDATE report_templates SET name=?,description=?,datasource_id=?,sql_text=?,revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=?`,
		input.Name, input.Description, input.DatasourceID, input.SQLText, requester.Effective.UserID, now.UTC(), id, expectedRevision)
	if err != nil {
		return Template{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Template{}, ErrConflict
	}
	if err := replaceParameters(ctx, tx, id, input.Parameters); err != nil {
		return Template{}, err
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportTemplateUpdated, audit.ResourceReportTemplate, id, nil, now); err != nil {
		return Template{}, err
	}
	if err := tx.Commit(); err != nil {
		return Template{}, err
	}
	return repository.FindTemplate(ctx, id)
}

func (repository *Repository) SetTemplateStatus(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, status Status, now time.Time) error {
	if status != StatusActive && status != StatusDisabled && status != StatusArchived {
		return fmt.Errorf("%w: invalid report status", ErrInvalid)
	}
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from Status
	if err := tx.GetContext(ctx, &from, `SELECT status FROM report_templates WHERE id=? FOR UPDATE`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if from == StatusArchived {
		return ErrInactive
	}
	result, err := tx.ExecContext(ctx, `UPDATE report_templates SET status=?,revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=?`, status, requester.Effective.UserID, now.UTC(), id, expectedRevision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportTemplateStateChanged, audit.ResourceReportTemplate, id, audit.StatusChangeMetadata{From: string(from), To: string(status)}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func validateTemplateInput(input TemplateInput) error {
	if strings.TrimSpace(input.Name) == "" || input.DatasourceID == 0 {
		return fmt.Errorf("%w: report name and datasource are required", ErrInvalid)
	}
	if len(input.SQLText) > 1<<20 {
		return fmt.Errorf("%w: SQL exceeds 1 MiB", ErrInvalid)
	}
	return ValidateParameters(input.Parameters)
}

func replaceParameters(ctx context.Context, tx *sqlx.Tx, reportID uint64, parameters []Parameter) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM report_parameters WHERE report_id=?`, reportID); err != nil {
		return err
	}
	for _, parameter := range parameters {
		var defaultValue any
		if len(parameter.DefaultValue) != 0 {
			defaultValue = string(parameter.DefaultValue)
		}
		var optionSource, dynamicOptionSQL any
		if isOptionType(parameter.Type) {
			optionSource = effectiveOptionSource(parameter)
			if effectiveOptionSource(parameter) == OptionSourceDynamic {
				dynamicOptionSQL = parameter.DynamicOptionSQL
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO report_parameters (report_id,parameter_key,label,parameter_type,option_source,dynamic_option_sql,required,default_value,display_order) VALUES (?,?,?,?,?,?,?,?,?)`,
			reportID, parameter.Key, parameter.Label, parameter.Type, optionSource, dynamicOptionSQL, parameter.Required, defaultValue, parameter.DisplayOrder)
		if err != nil {
			return err
		}
		parameterID, _ := result.LastInsertId()
		for _, option := range parameter.Options {
			if _, err := tx.ExecContext(ctx, `INSERT INTO report_parameter_options (parameter_id,option_value,label,display_order) VALUES (?,?,?,?)`, parameterID, option.Value, option.Label, option.DisplayOrder); err != nil {
				return err
			}
		}
	}
	return nil
}

func parameters(ctx context.Context, executor *sqlx.Tx, reportID uint64) ([]Parameter, error) {
	parameters := make([]Parameter, 0)
	if err := executor.SelectContext(ctx, &parameters, `SELECT id,report_id,parameter_key,label,parameter_type,COALESCE(option_source,'') AS option_source,COALESCE(dynamic_option_sql,'') AS dynamic_option_sql,required,default_value,display_order FROM report_parameters WHERE report_id=? ORDER BY display_order,id`, reportID); err != nil {
		return nil, err
	}
	for index := range parameters {
		options := make([]ParameterOption, 0)
		if err := executor.SelectContext(ctx, &options, `SELECT id,parameter_id,option_value,label,display_order FROM report_parameter_options WHERE parameter_id=? ORDER BY display_order,id`, parameters[index].ID); err != nil {
			return nil, err
		}
		parameters[index].Options = options
	}
	return parameters, nil
}

func (repository *Repository) HasAccess(ctx context.Context, reportID, userID uint64) (bool, error) {
	var allowed bool
	if err := repository.database.GetContext(ctx, &allowed, `SELECT EXISTS(SELECT 1 FROM report_template_user_access a JOIN users u ON u.id=a.user_id WHERE a.report_id=? AND a.user_id=? AND u.is_active=TRUE)`, reportID, userID); err != nil {
		return false, err
	}
	return allowed, nil
}

func (repository *Repository) AppendEvent(ctx context.Context, requester securityctx.Requester, action audit.Action, resource audit.ResourceType, id uint64, metadata audit.Metadata, now time.Time) error {
	return appendAudit(ctx, repository.database, requester, action, resource, id, metadata, now)
}

func (repository *Repository) SetAccess(ctx context.Context, requester securityctx.Requester, reportID, userID uint64, grant bool, now time.Time) error {
	tx, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.GetContext(ctx, &active, `SELECT is_active FROM users WHERE id=?`, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if grant && !active {
		return fmt.Errorf("%w: user is inactive", ErrInvalid)
	}
	if grant {
		_, err = tx.ExecContext(ctx, `INSERT INTO report_template_user_access (report_id,user_id,created_by_user_id,created_at) VALUES (?,?,?,?) ON DUPLICATE KEY UPDATE report_id=VALUES(report_id)`, reportID, userID, requester.Effective.UserID, now.UTC())
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM report_template_user_access WHERE report_id=? AND user_id=?`, reportID, userID)
	}
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionReportTemplateAccessChanged, audit.ResourceReportTemplate, reportID, audit.AccessChangeMetadata{UserID: userID, Granted: grant}, now); err != nil {
		return err
	}
	return tx.Commit()
}

type AccessUser struct {
	ID       uint64 `db:"id"`
	Username string `db:"username"`
	Name     string `db:"name"`
	Granted  bool   `db:"granted"`
}

func (repository *Repository) ListAccessUsers(ctx context.Context, reportID uint64, query string, limit, offset int) ([]AccessUser, int, error) {
	pattern := "%" + strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(query), "!", "!!"), "%", "!%") + "%"
	where, arguments := "", []any{reportID}
	if strings.TrimSpace(query) != "" {
		where = ` AND (u.username LIKE ? ESCAPE '!' OR u.name LIKE ? ESCAPE '!')`
		arguments = append(arguments, pattern, pattern)
	}
	var total int
	if err := repository.database.GetContext(ctx, &total, `SELECT COUNT(*) FROM users u WHERE u.is_active=TRUE`+where, arguments[1:]...); err != nil {
		return nil, 0, err
	}
	arguments = append(arguments, limit, offset)
	rows := make([]AccessUser, 0)
	if err := repository.database.SelectContext(ctx, &rows, `SELECT u.id,u.username,u.name,(a.user_id IS NOT NULL) AS granted FROM users u LEFT JOIN report_template_user_access a ON a.user_id=u.id AND a.report_id=? WHERE u.is_active=TRUE`+where+` ORDER BY (a.user_id IS NOT NULL) DESC,u.name,u.username,u.id LIMIT ? OFFSET ?`, arguments...); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func appendAudit(ctx context.Context, executor sqlx.ExtContext, requester securityctx.Requester, action audit.Action, resource audit.ResourceType, id uint64, metadata audit.Metadata, now time.Time) error {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Append(ctx, executor, audit.Event{Attribution: audit.Attribution{Actor: &actor, Effective: &effective}, Action: action, Resource: resource, ResourceID: id, Metadata: metadata, CreatedAt: now.UTC()})
}
