package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
)

type Handler struct {
	admin           *adminshell.Shell
	reports         *reporting.Repository
	service         *reporting.Service
	exports         *reportexport.Repository
	storage         *reportexport.Storage
	downloadTimeout time.Duration
}

type ListData struct{ Rows []reporting.Template }
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
	Report                reporting.Template
	Parameters            []ParameterView
	Errors                map[string]string
	Result                *reporting.InteractiveResult
	ResultJSON            template.JS
	CanExecute, CanExport bool
}
type ExportsData struct{ Rows []reportexport.Job }

func NewHandler(admin *adminshell.Shell, reports *reporting.Repository, service *reporting.Service, exports *reportexport.Repository, storage *reportexport.Storage, downloadTimeout time.Duration) *Handler {
	return &Handler{admin: admin, reports: reports, service: service, exports: exports, storage: storage, downloadTimeout: downloadTimeout}
}

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/reports", handler.Index)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/reports/{id}", handler.Show)
	router.With(handler.admin.RequirePermission(PermissionExecute)).Post("/reports/{id}/run", handler.Run)
	router.With(handler.admin.RequirePermission(PermissionExport)).Post("/reports/{id}/export", handler.Export)
	router.With(handler.admin.RequirePermission(PermissionExport)).Get("/exports", handler.Exports)
	router.With(handler.admin.RequirePermission(PermissionExport)).Get("/exports/{id}/download", handler.Download)
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	rows, err := handler.reports.ListAvailableTemplates(request.Context(), principal.UserID)
	if err != nil {
		handler.admin.Internal(writer, request, "list reports", err)
		return
	}
	handler.admin.RenderPage(writer, request, 200, "features/reports/index", "Reports", ListData{Rows: rows})
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	report, principal, ok := handler.authorized(writer, request)
	if !ok {
		return
	}
	handler.render(writer, request, 200, report, nil, nil, principal.Can(PermissionExecute), principal.Can(PermissionExport))
}

func (handler *Handler) Run(writer http.ResponseWriter, request *http.Request) {
	report, principal, ok := handler.authorized(writer, request)
	if !ok || !webutil.ParseForm(writer, request, 2<<20) {
		return
	}
	input := formInput(request, report.Parameters)
	result, err := handler.service.Run(request.Context(), principal.SecurityContext(), report.ID, input)
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
	job, err := handler.exports.Submit(request.Context(), principal.SecurityContext(), report, formInput(request, report.Parameters), time.Now().UTC())
	if err != nil {
		handler.render(writer, request, 422, report, formInput(request, report.Parameters), map[string]string{"form": publicError(err)}, principal.Can(PermissionExecute), true)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/exports?notice=report-export-submitted&submitted=%d", job.ID), http.StatusSeeOther)
}

func (handler *Handler) Exports(writer http.ResponseWriter, request *http.Request) {
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	rows, err := handler.exports.ListForUser(request.Context(), principal.UserID, 100)
	if err != nil {
		handler.admin.Internal(writer, request, "list report exports", err)
		return
	}
	handler.admin.RenderPage(writer, request, 200, "features/reports/exports", "My report exports", ExportsData{Rows: rows})
}

func (handler *Handler) Download(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	job, allowed, err := handler.exports.Downloadable(request.Context(), id, principal.UserID)
	if err != nil {
		handler.admin.Internal(writer, request, "authorize report download", err)
		return
	}
	if !allowed || job.ArtifactPath == nil || job.ArtifactName == nil {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	file, err := handler.storage.Open(*job.ArtifactPath)
	if err != nil {
		handler.admin.NotFound(writer, request)
		return
	}
	defer file.Close()
	if err := handler.exports.RecordDownload(request.Context(), principal.SecurityContext(), id, time.Now().UTC()); err != nil {
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
	for index, parameter := range report.Parameters {
		views[index] = ParameterView{Value: parameter, Input: input[parameter.Key]}
	}
	data := ShowData{Report: report, Parameters: views, Errors: formErrors, CanExecute: execute, CanExport: export}
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
	return "The report could not be completed." + err.Error()
}
