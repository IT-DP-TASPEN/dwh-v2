package datasources

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/reporting"
)

type Handler struct {
	admin      *adminshell.Shell
	repository *reporting.Repository
	service    *reporting.Service
	pools      *reporting.PoolManager
}

type ListData struct {
	Rows      []reporting.Datasource
	CanCreate bool
}
type DetailData struct {
	Value                        reporting.Datasource
	CanUpdate, CanState, CanTest bool
}
type FormData struct {
	ID, Revision                                                     uint64
	Name, Description, Host, Port, DatabaseName, Username, TLSPolicy string
	Errors                                                           map[string]string
}

func NewHandler(admin *adminshell.Shell, repository *reporting.Repository, service *reporting.Service, pools *reporting.PoolManager) *Handler {
	return &Handler{admin: admin, repository: repository, service: service, pools: pools}
}

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/datasources", handler.Index)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Get("/datasources/new", handler.New)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Post("/datasources", handler.Create)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/datasources/{id}", handler.Show)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Get("/datasources/{id}/edit", handler.Edit)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Post("/datasources/{id}", handler.Update)
	router.With(handler.admin.RequirePermission(PermissionTest)).Post("/datasources/{id}/test", handler.Test)
	router.With(handler.admin.RequirePermission(PermissionChangeState)).Post("/datasources/{id}/state", handler.State)
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.repository.ListDatasources(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list report datasources", err)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	handler.admin.RenderPage(writer, request, 200, "features/datasources/index", "Report datasources", ListData{Rows: rows, CanCreate: principal.Can(PermissionCreate)})
}

func (handler *Handler) New(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, 200, "features/datasources/form", "New report datasource", FormData{Port: "3306", TLSPolicy: string(reporting.TLSRequired), Errors: map[string]string{}})
}

func (handler *Handler) Create(writer http.ResponseWriter, request *http.Request) {
	form, input, ok := handler.form(writer, request, true)
	if !ok {
		return
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, 422, "features/datasources/form", "New report datasource", form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	created, err := handler.repository.CreateDatasource(request.Context(), principal.SecurityContext(), input, time.Now().UTC())
	if err != nil {
		form.Errors["form"] = publicError(err)
		handler.admin.RenderPage(writer, request, 422, "features/datasources/form", "New report datasource", form)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/datasources/%d?notice=report-datasource-created", created.ID), http.StatusSeeOther)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.find(writer, request)
	if !ok {
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	handler.admin.RenderPage(writer, request, 200, "features/datasources/show", "Report datasource", DetailData{Value: value, CanUpdate: principal.Can(PermissionUpdate), CanState: principal.Can(PermissionChangeState), CanTest: principal.Can(PermissionTest)})
}

func (handler *Handler) Edit(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.find(writer, request)
	if !ok {
		return
	}
	form := FormData{ID: value.ID, Revision: value.Revision, Name: value.Name, Description: value.Description, Host: value.Host, Port: strconv.Itoa(int(value.Port)), DatabaseName: value.DatabaseName, Username: value.Username, TLSPolicy: string(value.TLSPolicy), Errors: map[string]string{}}
	handler.admin.RenderPage(writer, request, 200, "features/datasources/form", "Edit report datasource", form)
}

func (handler *Handler) Update(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(writer, request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	form, input, ok := handler.form(writer, request, false)
	if !ok {
		return
	}
	form.ID = id
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, 422, "features/datasources/form", "Edit report datasource", form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	_, err := handler.repository.UpdateDatasource(request.Context(), principal.SecurityContext(), id, form.Revision, input, time.Now().UTC())
	if err != nil {
		form.Errors["form"] = publicError(err)
		handler.admin.RenderPage(writer, request, 422, "features/datasources/form", "Edit report datasource", form)
		return
	}
	handler.pools.Invalidate(id)
	http.Redirect(writer, request, fmt.Sprintf("/datasources/%d?notice=report-datasource-updated", id), http.StatusSeeOther)
}

func (handler *Handler) Test(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(writer, request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.service.TestDatasource(request.Context(), principal.SecurityContext(), id); err != nil {
		http.Redirect(writer, request, fmt.Sprintf("/datasources/%d?notice=report-datasource-test-failed", id), http.StatusSeeOther)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/datasources/%d?notice=report-datasource-test-ok", id), http.StatusSeeOther)
}

func (handler *Handler) State(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(writer, request)
	if !ok || !webutil.ParseForm(writer, request, 8<<10) {
		return
	}
	revision, err := strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	status := reporting.Status(request.PostFormValue("status"))
	if err != nil {
		http.Error(writer, "Invalid revision.", 422)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.service.SetDatasourceStatus(request.Context(), principal.SecurityContext(), id, revision, status); err != nil {
		http.Error(writer, publicError(err), 422)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/datasources/%d?notice=report-datasource-state", id), http.StatusSeeOther)
}

func (handler *Handler) form(writer http.ResponseWriter, request *http.Request, passwordRequired bool) (FormData, reporting.DatasourceInput, bool) {
	if !webutil.ParseForm(writer, request, 64<<10) {
		return FormData{}, reporting.DatasourceInput{}, false
	}
	form := FormData{Name: strings.TrimSpace(request.PostFormValue("name")), Description: strings.TrimSpace(request.PostFormValue("description")), Host: strings.TrimSpace(request.PostFormValue("host")), Port: request.PostFormValue("port"), DatabaseName: strings.TrimSpace(request.PostFormValue("database_name")), Username: strings.TrimSpace(request.PostFormValue("username")), TLSPolicy: request.PostFormValue("tls_policy"), Errors: map[string]string{}}
	form.Revision, _ = strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	port, err := strconv.ParseUint(form.Port, 10, 16)
	if err != nil || port == 0 {
		form.Errors["port"] = "Port must be between 1 and 65535."
	}
	password := request.PostFormValue("password")
	if passwordRequired && password == "" {
		form.Errors["password"] = "Password is required."
	}
	input := reporting.DatasourceInput{Name: form.Name, Description: form.Description, Host: form.Host, Port: uint16(port), DatabaseName: form.DatabaseName, Username: form.Username, Password: password, TLSPolicy: reporting.TLSPolicy(form.TLSPolicy)}
	return form, input, true
}

func (handler *Handler) find(writer http.ResponseWriter, request *http.Request) (reporting.Datasource, bool) {
	id, ok := idParam(writer, request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return reporting.Datasource{}, false
	}
	value, err := handler.repository.FindDatasource(request.Context(), id)
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return reporting.Datasource{}, false
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find report datasource", err)
		return reporting.Datasource{}, false
	}
	return value, true
}

func idParam(_ http.ResponseWriter, request *http.Request) (uint64, bool) {
	value, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	return value, err == nil && value != 0
}

func publicError(err error) string {
	switch {
	case errors.Is(err, reporting.ErrConflict):
		return "This record changed. Reload and try again."
	case errors.Is(err, reporting.ErrInvalid), errors.Is(err, reporting.ErrInactive):
		return err.Error()
	default:
		return "The operation could not be completed." + err.Error()
	}
}
