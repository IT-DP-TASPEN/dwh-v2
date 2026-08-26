package reporttemplates

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/reporting"
)

type Handler struct {
	admin      *adminshell.Shell
	repository *reporting.Repository
	service    *reporting.Service
}

type ListData struct {
	Rows      []reporting.Template
	CanCreate bool
}
type DetailData struct {
	Value                          reporting.Template
	CanUpdate, CanState, CanAccess bool
	Access                         AccessData
}
type AccessData struct {
	ReportID   uint64
	Query      string
	Rows       []reporting.AccessUser
	Pagination pagination.Page
}
type FormData struct {
	ID, Revision                                                             uint64
	Name, Description, DatasourceID, SQLText, ParametersJSON, TestValuesJSON string
	Datasources                                                              []reporting.Datasource
	Errors                                                                   map[string]string
	TestResult                                                               *reporting.InteractiveResult
	TestResultJSON                                                           template.JS
}

func NewHandler(admin *adminshell.Shell, repository *reporting.Repository, service *reporting.Service) *Handler {
	return &Handler{admin: admin, repository: repository, service: service}
}

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/report-templates", handler.Index)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Get("/report-templates/new", handler.New)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Post("/report-templates", handler.Create)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/report-templates/{id}", handler.Show)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Get("/report-templates/{id}/edit", handler.Edit)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Post("/report-templates/{id}", handler.Update)
	router.With(handler.admin.RequirePermission(PermissionUpdate), handler.admin.RequirePermission("reports.execute")).Post("/report-templates/{id}/test", handler.Test)
	router.With(handler.admin.RequirePermission(PermissionChangeState)).Post("/report-templates/{id}/state", handler.State)
	router.With(handler.admin.RequirePermission(PermissionManageAccess)).Get("/report-templates/{id}/access", handler.Access)
	router.With(handler.admin.RequirePermission(PermissionManageAccess)).Post("/report-templates/{id}/access/{userID}", handler.ChangeAccess)
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.repository.ListTemplates(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list report templates", err)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	handler.admin.RenderPage(writer, request, 200, "features/reporttemplates/index", "Report templates", ListData{Rows: rows, CanCreate: principal.Can(PermissionCreate)})
}

func (handler *Handler) New(writer http.ResponseWriter, request *http.Request) {
	datasources, err := handler.repository.ListDatasources(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list report datasources", err)
		return
	}
	form := FormData{Datasources: datasources, ParametersJSON: "[]", TestValuesJSON: "{}", Errors: map[string]string{}}
	handler.admin.RenderPage(writer, request, 200, "features/reporttemplates/form", "New report template", form)
}

func (handler *Handler) Create(writer http.ResponseWriter, request *http.Request) {
	form, input, _, ok := handler.form(writer, request)
	if !ok {
		return
	}
	if len(form.Errors) != 0 {
		handler.renderForm(writer, request, 422, form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	created, err := handler.repository.CreateTemplate(request.Context(), principal.SecurityContext(), input, time.Now().UTC())
	if err != nil {
		form.Errors["form"] = publicError(err)
		handler.renderForm(writer, request, 422, form)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/report-templates/%d", created.ID), http.StatusSeeOther)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.find(writer, request)
	if !ok {
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	access := AccessData{ReportID: value.ID}
	if principal.Can(PermissionManageAccess) {
		access, _ = handler.accessData(request, value.ID)
	}
	handler.admin.RenderPage(writer, request, 200, "features/reporttemplates/show", "Report template", DetailData{Value: value, CanUpdate: principal.Can(PermissionUpdate), CanState: principal.Can(PermissionChangeState), CanAccess: principal.Can(PermissionManageAccess), Access: access})
}

func (handler *Handler) Edit(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.find(writer, request)
	if !ok {
		return
	}
	datasources, _ := handler.repository.ListDatasources(request.Context())
	handler.admin.RenderPage(writer, request, 200, "features/reporttemplates/form", "Edit report template", FormData{ID: value.ID, Revision: value.Revision, Name: value.Name, Description: value.Description, DatasourceID: strconv.FormatUint(value.DatasourceID, 10), SQLText: value.SQLText, ParametersJSON: encodeParameters(value.Parameters), TestValuesJSON: "{}", Datasources: datasources, Errors: map[string]string{}})
}

func (handler *Handler) Update(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	form, input, _, ok := handler.form(writer, request)
	if !ok {
		return
	}
	form.ID = id
	if len(form.Errors) != 0 {
		handler.renderForm(writer, request, 422, form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	_, err := handler.service.UpdateTemplate(request.Context(), principal.SecurityContext(), id, form.Revision, input)
	if err != nil {
		form.Errors["form"] = publicError(err)
		handler.renderForm(writer, request, 422, form)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/report-templates/%d", id), http.StatusSeeOther)
}

func (handler *Handler) Test(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	form, input, values, ok := handler.form(writer, request)
	if !ok {
		return
	}
	form.ID = id
	if len(form.Errors) != 0 {
		handler.renderForm(writer, request, 422, form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	result, err := handler.service.TestQuery(request.Context(), principal.SecurityContext(), id, input, values)
	if err != nil {
		form.Errors["test"] = publicError(err)
		handler.renderForm(writer, request, 422, form)
		return
	}
	form.TestResult = &result
	encoded, _ := json.Marshal(result)
	form.TestResultJSON = template.JS(encoded)
	handler.renderForm(writer, request, 200, form)
}

func (handler *Handler) State(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok || !webutil.ParseForm(writer, request, 8<<10) {
		return
	}
	revision, err := strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	if err != nil {
		http.Error(writer, "Invalid revision.", 422)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.service.SetTemplateStatus(request.Context(), principal.SecurityContext(), id, revision, reporting.Status(request.PostFormValue("status"))); err != nil {
		http.Error(writer, publicError(err), 422)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/report-templates/%d", id), http.StatusSeeOther)
}

func (handler *Handler) Access(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	data, err := handler.accessData(request, id)
	if err != nil {
		handler.admin.Internal(writer, request, "list report access", err)
		return
	}
	if request.Header.Get("HX-Request") == "true" {
		if err := handler.admin.RenderPartial(writer, 200, "features/reporttemplates/show", "access-list", data); err != nil {
			handler.admin.Internal(writer, request, "render report access", err)
		}
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/report-templates/%d", id), http.StatusSeeOther)
}

func (handler *Handler) ChangeAccess(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	userID, err := strconv.ParseUint(chi.URLParam(request, "userID"), 10, 64)
	if !ok || err != nil || !webutil.ParseForm(writer, request, 8<<10) {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.repository.SetAccess(request.Context(), principal.SecurityContext(), id, userID, request.PostFormValue("grant") == "true", time.Now().UTC()); err != nil {
		http.Error(writer, publicError(err), 422)
		return
	}
	data, err := handler.accessData(request, id)
	if err != nil {
		handler.admin.Internal(writer, request, "list report access", err)
		return
	}
	if err := handler.admin.RenderPartial(writer, 200, "features/reporttemplates/show", "access-list", data); err != nil {
		handler.admin.Internal(writer, request, "render report access", err)
	}
}

func (handler *Handler) form(writer http.ResponseWriter, request *http.Request) (FormData, reporting.TemplateInput, map[string]reporting.InputValue, bool) {
	if !webutil.ParseForm(writer, request, 2<<20) {
		return FormData{}, reporting.TemplateInput{}, nil, false
	}
	form := FormData{Name: strings.TrimSpace(request.PostFormValue("name")), Description: strings.TrimSpace(request.PostFormValue("description")), DatasourceID: request.PostFormValue("datasource_id"), SQLText: request.PostFormValue("sql_text"), ParametersJSON: request.PostFormValue("parameters_json"), TestValuesJSON: request.PostFormValue("test_values_json"), Errors: map[string]string{}}
	form.Revision, _ = strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	datasourceID, err := strconv.ParseUint(form.DatasourceID, 10, 64)
	if err != nil || datasourceID == 0 {
		form.Errors["datasource"] = "Choose a datasource."
	}
	parameters, err := decodeParameters(form.ParametersJSON)
	if err != nil {
		form.Errors["parameters"] = err.Error()
	}
	values, err := decodeTestValues(form.TestValuesJSON)
	if err != nil {
		form.Errors["test_values"] = err.Error()
	}
	form.Datasources, _ = handler.repository.ListDatasources(request.Context())
	return form, reporting.TemplateInput{Name: form.Name, Description: form.Description, DatasourceID: datasourceID, SQLText: form.SQLText, Parameters: parameters}, values, true
}

func (handler *Handler) renderForm(writer http.ResponseWriter, request *http.Request, status int, form FormData) {
	handler.admin.RenderPage(writer, request, status, "features/reporttemplates/form", "Report template", form)
}

func (handler *Handler) find(writer http.ResponseWriter, request *http.Request) (reporting.Template, bool) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return reporting.Template{}, false
	}
	value, err := handler.repository.FindTemplate(request.Context(), id)
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return reporting.Template{}, false
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find report template", err)
		return reporting.Template{}, false
	}
	return value, true
}

func (handler *Handler) accessData(request *http.Request, reportID uint64) (AccessData, error) {
	page, _ := strconv.Atoi(request.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	query := request.URL.Query().Get("q")
	rows, total, err := handler.repository.ListAccessUsers(request.Context(), reportID, query, 20, (page-1)*20)
	if err != nil {
		return AccessData{}, err
	}
	return AccessData{ReportID: reportID, Query: query, Rows: rows, Pagination: pagination.New(page, 20, int64(total))}, nil
}

type parameterForm struct {
	Key      string          `json:"key"`
	Label    string          `json:"label"`
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Default  json.RawMessage `json:"default"`
	Order    uint16          `json:"order,omitempty"`
	Options  []optionForm    `json:"options,omitempty"`
}
type optionForm struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Order uint16 `json:"order,omitempty"`
}

func decodeParameters(value string) ([]reporting.Parameter, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var forms []parameterForm
	if err := decoder.Decode(&forms); err != nil {
		return nil, errors.New("Parameter definitions could not be processed.")
	}
	if len(forms) > 1<<16 {
		return nil, errors.New("Too many parameters.")
	}
	result := make([]reporting.Parameter, len(forms))
	for index, form := range forms {
		if len(form.Options) > 1<<16 {
			return nil, fmt.Errorf("Parameter %q has too many options.", form.Key)
		}
		result[index] = reporting.Parameter{Key: form.Key, Label: form.Label, Type: reporting.ParameterType(form.Type), Required: form.Required, DefaultValue: form.Default, DisplayOrder: uint16(index)}
		for optionIndex, option := range form.Options {
			result[index].Options = append(result[index].Options, reporting.ParameterOption{Value: option.Value, Label: option.Label, DisplayOrder: uint16(optionIndex)})
		}
	}
	return result, reporting.ValidateParameters(result)
}

func encodeParameters(parameters []reporting.Parameter) string {
	forms := make([]parameterForm, len(parameters))
	for index, parameter := range parameters {
		forms[index] = parameterForm{Key: parameter.Key, Label: parameter.Label, Type: string(parameter.Type), Required: parameter.Required, Default: parameter.DefaultValue}
		for _, option := range parameter.Options {
			forms[index].Options = append(forms[index].Options, optionForm{Value: option.Value, Label: option.Label})
		}
	}
	encoded, _ := json.MarshalIndent(forms, "", "  ")
	return string(encoded)
}

func decodeTestValues(value string) (map[string]reporting.InputValue, error) {
	if strings.TrimSpace(value) == "" {
		value = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, errors.New("Test values could not be processed.")
	}
	result := make(map[string]reporting.InputValue, len(raw))
	for key, value := range raw {
		input := reporting.InputValue{Present: true}
		items, ok := value.([]any)
		if !ok {
			items = []any{value}
		}
		for _, item := range items {
			switch typed := item.(type) {
			case string:
				input.Values = append(input.Values, typed)
			case bool:
				input.Values = append(input.Values, strconv.FormatBool(typed))
			case json.Number:
				input.Values = append(input.Values, typed.String())
			case nil:
			default:
				return nil, fmt.Errorf("Test value %q has unsupported JSON type", key)
			}
		}
		result[key] = input
	}
	return result, nil
}

func idParam(request *http.Request) (uint64, bool) {
	value, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	return value, err == nil && value != 0
}
func publicError(err error) string {
	if errors.Is(err, reporting.ErrConflict) {
		return "This report changed. Reload and try again."
	}
	if errors.Is(err, reporting.ErrInvalid) || errors.Is(err, reporting.ErrInactive) || errors.Is(err, reporting.ErrForbidden) {
		return err.Error()
	}
	return "The operation could not be completed."
}
