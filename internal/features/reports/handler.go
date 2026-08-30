package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Handler struct {
	admin           *adminshell.Shell
	reports         *reporting.Repository
	service         *reporting.Service
	exports         exportStore
	storage         *reportexport.Storage
	downloadTimeout time.Duration
}

const ExportPageSize = 100

type exportStore interface {
	Submit(context.Context, securityctx.Requester, reporting.Template, map[string]reporting.NormalizedValue, time.Time) (reportexport.Job, error)
	CountVisible(context.Context, reportexport.Scope, uint64) (int64, error)
	ListVisible(context.Context, reportexport.Scope, uint64, int, int) ([]reportexport.VisibleJob, error)
	FindVisible(context.Context, uint64, reportexport.Scope, uint64) (reportexport.VisibleJob, error)
	AuthorizeDownload(context.Context, uint64, uint64, bool) (reportexport.Job, reportexport.DownloadAccess, error)
	RecordDownload(context.Context, securityctx.Requester, reportexport.Job, reportexport.DownloadAccess, time.Time) error
}

type ParameterView struct {
	Value reporting.Parameter
	Input reporting.InputValue
}

func (view ParameterView) Scalar() string {
	if len(view.Input.Values) == 0 {
		return ""
	}
	return view.Input.Values[0]
}
func (view ParameterView) Selected(value string) bool {
	for _, selected := range view.Input.Values {
		if selected == value {
			return true
		}
	}
	return false
}

type ShowData struct {
	Report                                reporting.Template
	Parameters                            []ParameterView
	ParametersJSON                        template.JS
	Errors                                map[string]string
	Result                                *reporting.InteractiveResult
	ResultJSON                            template.JS
	CanExecute, CanExport, CanLoadOptions bool
}

type runtimeParameter struct {
	Key          string          `json:"key"`
	Type         string          `json:"type"`
	OptionSource string          `json:"option_source,omitempty"`
	Required     bool            `json:"required"`
	Default      json.RawMessage `json:"default"`
	Current      []string        `json:"current"`
	Present      bool            `json:"present"`
}
type ExportsData struct {
	Rows                 []reportexport.VisibleJob
	Scope                reportexport.Scope
	CanViewAll           bool
	Pagination           pagination.Page
	AllURL, MineURL      string
	PreviousURL, NextURL string
}

type ExportDetailData struct {
	Job         reportexport.VisibleJob
	CanDownload bool
	BackURL     string
}

func NewHandler(admin *adminshell.Shell, reports *reporting.Repository, service *reporting.Service, exports exportStore, storage *reportexport.Storage, downloadTimeout time.Duration) *Handler {
	return &Handler{admin: admin, reports: reports, service: service, exports: exports, storage: storage, downloadTimeout: downloadTimeout}
}

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/reports", handler.Index)
	router.With(handler.admin.RequirePermission(PermissionView)).Post("/reports/folders", handler.CreateFolder)
	router.With(handler.admin.RequirePermission(PermissionView)).Post("/reports/folders/{folderID}/rename", handler.RenameFolder)
	router.With(handler.admin.RequirePermission(PermissionView)).Post("/reports/folders/{folderID}/delete", handler.DeleteFolder)
	router.With(handler.admin.RequirePermission(PermissionView)).Post("/reports/{id}/star", handler.Star)
	router.With(handler.admin.RequirePermission(PermissionView)).Post("/reports/{id}/folder", handler.MoveToFolder)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/reports/{id}", handler.Show)
	router.With(handler.admin.RequirePermission(PermissionView)).Post("/reports/{id}/parameters/{key}/options", handler.Options)
	router.With(handler.admin.RequirePermission(PermissionExecute)).Post("/reports/{id}/run", handler.Run)
	router.With(handler.admin.RequirePermission(PermissionExport)).Post("/reports/{id}/export", handler.Export)
	readExports := handler.admin.RequireAnyPermission(PermissionExport, PermissionViewAllExports)
	router.With(readExports).Get("/exports", handler.Exports)
	router.With(readExports).Get("/exports/{id}", handler.ExportDetail)
	router.With(readExports).Get("/exports/{id}/download", handler.Download)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	report, principal, ok := handler.authorized(writer, request)
	if !ok {
		return
	}
	handler.render(writer, request, 200, report, nil, nil, principal.Can(PermissionExecute), principal.Can(PermissionExport))
}

func (handler *Handler) Run(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	if !webutil.ParseForm(writer, request, 2<<20) {
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	input := formInput(request, nil)
	report, result, err := handler.service.Run(request.Context(), principal.SecurityContext(), id, input)
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if report.ID == 0 {
		handler.admin.Internal(writer, request, "run report", err)
		return
	}
	if errors.Is(err, reporting.ErrForbidden) || errors.Is(err, reporting.ErrInactive) {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	if err != nil {
		handler.render(writer, request, 422, report, input, map[string]string{"form": publicError(err)}, true, principal.Can(PermissionExport))
		return
	}
	handler.render(writer, request, 200, report, input, nil, true, principal.Can(PermissionExport), result)
}

func (handler *Handler) Export(writer http.ResponseWriter, request *http.Request) {
	report, principal, ok := handler.authorized(writer, request)
	if !ok || !webutil.ParseForm(writer, request, 2<<20) {
		return
	}
	input := formInput(request, report.Parameters)
	validatedReport, normalized, err := handler.service.PrepareExport(request.Context(), principal.SecurityContext(), report.ID, input)
	var job reportexport.Job
	if err == nil {
		job, err = handler.exports.Submit(request.Context(), principal.SecurityContext(), validatedReport, normalized, time.Now().UTC())
	}
	if err != nil {
		handler.render(writer, request, 422, report, input, map[string]string{"form": publicError(err)}, principal.Can(PermissionExecute), true)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/exports?notice=report-export-submitted&submitted=%d", job.ID), http.StatusSeeOther)
}

func (handler *Handler) Options(writer http.ResponseWriter, request *http.Request) {
	report, principal, ok := handler.authorized(writer, request)
	if !ok {
		return
	}
	if !principal.Can(PermissionExecute) && !principal.Can(PermissionExport) {
		writeOptionJSON(writer, http.StatusForbidden, reporting.OptionLoad{State: "error"})
		return
	}
	if !webutil.ParseForm(writer, request, 2<<20) {
		return
	}
	result, err := handler.service.LoadOptions(request.Context(), principal.SecurityContext(), report.ID, chi.URLParam(request, "key"), formInput(request, report.Parameters))
	if err == nil {
		writeOptionJSON(writer, http.StatusOK, result)
		return
	}
	status := http.StatusUnprocessableEntity
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	} else if !errors.Is(err, reporting.ErrInvalid) && !errors.Is(err, reporting.ErrInactive) && !errors.Is(err, reporting.ErrForbidden) {
		status = http.StatusInternalServerError
		slog.ErrorContext(request.Context(), "load report options", "report_id", report.ID, "parameter", chi.URLParam(request, "key"), "error", err)
	}
	writeOptionJSON(writer, status, reporting.OptionLoad{State: "error"})
}

func writeOptionJSON(writer http.ResponseWriter, status int, result reporting.OptionLoad) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler *Handler) Exports(writer http.ResponseWriter, request *http.Request) {
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	canViewAll := principal.Can(PermissionViewAllExports)
	scope, status := requestedExportScope(request.URL.Query().Get("scope"), canViewAll)
	if status != 0 {
		if status == http.StatusForbidden {
			handler.admin.RenderPage(writer, request, status, "forbidden", "Forbidden", nil)
		} else {
			http.Error(writer, http.StatusText(status), status)
		}
		return
	}
	total, err := handler.exports.CountVisible(request.Context(), scope, principal.UserID)
	if err != nil {
		handler.admin.Internal(writer, request, "count report exports", err)
		return
	}
	page := pagination.New(webutil.Page(request), ExportPageSize, total)
	rows, err := handler.exports.ListVisible(request.Context(), scope, principal.UserID, page.PerPage, page.Offset())
	if err != nil {
		handler.admin.Internal(writer, request, "list report exports", err)
		return
	}
	data := ExportsData{
		Rows: rows, Scope: scope, CanViewAll: canViewAll, Pagination: page,
		AllURL: exportPageURL(reportexport.ScopeAll, 1), MineURL: exportPageURL(reportexport.ScopeMine, 1),
		PreviousURL: exportPageURL(scope, page.Previous), NextURL: exportPageURL(scope, page.Next),
	}
	title := "My report exports"
	if scope == reportexport.ScopeAll {
		title = "All report exports"
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/reports/exports", title, data)
}

func (handler *Handler) ExportDetail(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	scope := reportexport.ScopeMine
	if principal.Can(PermissionViewAllExports) {
		scope = reportexport.ScopeAll
	}
	backScope := scope
	if request.URL.Query().Get("scope") == string(reportexport.ScopeMine) {
		backScope = reportexport.ScopeMine
	}
	job, err := handler.exports.FindVisible(request.Context(), id, scope, principal.UserID)
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find report export", err)
		return
	}
	canDownload := job.Status == reportexport.StatusSucceeded && job.ArtifactDeletedAt == nil && job.ArtifactName != nil
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/reports/export", fmt.Sprintf("Export #%d", job.ID), ExportDetailData{Job: job, CanDownload: canDownload, BackURL: exportPageURL(backScope, 1)})
}

func (handler *Handler) Download(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	job, accessPath, err := handler.exports.AuthorizeDownload(request.Context(), id, principal.UserID, principal.Can(PermissionViewAllExports))
	if errors.Is(err, reporting.ErrNotFound) || accessPath == "" {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "authorize report download", err)
		return
	}
	if job.Status != reportexport.StatusSucceeded || job.ArtifactDeletedAt != nil || job.ArtifactPath == nil || job.ArtifactName == nil {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	file, err := handler.storage.Open(*job.ArtifactPath)
	if err != nil {
		handler.admin.NotFound(writer, request)
		return
	}
	defer file.Close()
	if err := handler.exports.RecordDownload(request.Context(), principal.SecurityContext(), job, accessPath, time.Now().UTC()); err != nil {
		handler.admin.Internal(writer, request, "audit report download", err)
		return
	}
	controller := http.NewResponseController(writer)
	_ = controller.SetWriteDeadline(time.Now().Add(handler.downloadTimeout))
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, *job.ArtifactName))
	if job.ArtifactType != nil && *job.ArtifactType == "zip" {
		writer.Header().Set("Content-Type", "application/zip")
	} else {
		writer.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	}
	if job.ArtifactSize != nil {
		writer.Header().Set("Content-Length", strconv.FormatUint(*job.ArtifactSize, 10))
	}
	http.ServeContent(writer, request, *job.ArtifactName, job.FinishedAtValue(), file)
}

func requestedExportScope(value string, canViewAll bool) (reportexport.Scope, int) {
	switch value {
	case "":
		if canViewAll {
			return reportexport.ScopeAll, 0
		}
		return reportexport.ScopeMine, 0
	case string(reportexport.ScopeMine):
		return reportexport.ScopeMine, 0
	case string(reportexport.ScopeAll):
		if !canViewAll {
			return "", http.StatusForbidden
		}
		return reportexport.ScopeAll, 0
	default:
		return "", http.StatusBadRequest
	}
}

func exportPageURL(scope reportexport.Scope, page int) string {
	if page == 0 {
		return ""
	}
	query := url.Values{"scope": {string(scope)}}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	return "/exports?" + query.Encode()
}

func (handler *Handler) authorized(writer http.ResponseWriter, request *http.Request) (reporting.Template, browserauth.Principal, bool) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return reporting.Template{}, browserauth.Principal{}, false
	}
	report, err := handler.reports.FindTemplate(request.Context(), id)
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return reporting.Template{}, browserauth.Principal{}, false
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find report", err)
		return reporting.Template{}, browserauth.Principal{}, false
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	allowed, err := handler.reports.HasAccess(request.Context(), id, principal.UserID)
	if err != nil {
		handler.admin.Internal(writer, request, "authorize report", err)
		return reporting.Template{}, principal, false
	}
	if !allowed || report.Status != reporting.StatusActive || report.DatasourceStatus != reporting.StatusActive {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return reporting.Template{}, principal, false
	}
	return report, principal, true
}

func (handler *Handler) render(writer http.ResponseWriter, request *http.Request, status int, report reporting.Template, input map[string]reporting.InputValue, formErrors map[string]string, execute, export bool, results ...reporting.InteractiveResult) {
	if input == nil {
		input = make(map[string]reporting.InputValue)
		for _, parameter := range report.Parameters {
			input[parameter.Key] = reporting.DefaultInput(parameter)
		}
	}
	views := make([]ParameterView, len(report.Parameters))
	definitions := make([]runtimeParameter, len(report.Parameters))
	for index, parameter := range report.Parameters {
		views[index] = ParameterView{Value: parameter, Input: input[parameter.Key]}
		definitions[index] = runtimeParameter{Key: parameter.Key, Type: string(parameter.Type), OptionSource: string(parameter.OptionSource), Required: parameter.Required, Default: parameter.DefaultValue, Current: append([]string(nil), input[parameter.Key].Values...), Present: input[parameter.Key].Present}
	}
	data := ShowData{Report: report, Parameters: views, Errors: formErrors, CanExecute: execute, CanExport: export, CanLoadOptions: execute || export}
	encodedParameters, _ := json.Marshal(definitions)
	data.ParametersJSON = template.JS(encodedParameters)
	if len(results) != 0 {
		data.Result = &results[0]
		encoded, _ := json.Marshal(results[0])
		data.ResultJSON = template.JS(encoded)
	}
	handler.admin.RenderPage(writer, request, status, "features/reports/show", report.Name, data)
}

func formInput(request *http.Request, parameters []reporting.Parameter) map[string]reporting.InputValue {
	result := make(map[string]reporting.InputValue, len(parameters))
	for _, parameter := range parameters {
		result[parameter.Key] = reporting.InputValue{Present: request.PostFormValue("present_"+parameter.Key) == "1", Values: append([]string(nil), request.PostForm["param_"+parameter.Key]...)}
	}
	for name := range request.PostForm {
		key, found := strings.CutPrefix(name, "param_")
		if !found {
			key, found = strings.CutPrefix(name, "present_")
		}
		if found && key != "" {
			if _, known := result[key]; !known {
				result[key] = reporting.InputValue{Present: request.PostFormValue("present_"+key) == "1", Values: append([]string(nil), request.PostForm["param_"+key]...)}
			}
		}
	}
	return result
}

func idParam(request *http.Request) (uint64, bool) {
	value, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	return value, err == nil && value != 0
}
func publicError(err error) string {
	if errors.Is(err, reporting.ErrInvalid) || errors.Is(err, reporting.ErrInactive) || errors.Is(err, reporting.ErrForbidden) {
		return err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The interactive query timed out. Use background export for longer reports."
	}
	return "The report could not be completed."
}
