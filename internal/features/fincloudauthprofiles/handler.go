package fincloudauthprofiles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/fincloudauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
)

type Handler struct {
	admin      *adminshell.Shell
	repository *fincloudauth.Repository
	service    *fincloudauth.Service
	listValues listValuesReader
}

type listValuesReader interface {
	FetchAuthListValues(context.Context) (fincloud.AuthListValues, error)
}

type ListData struct {
	Rows      []fincloudauth.Profile
	CanManage bool
}

type DetailData struct {
	Value     fincloudauth.Profile
	CanManage bool
}

type FormData struct {
	ID, Revision                         uint64
	Name, Username, RoleID, LocationID   string
	Roles, Locations                     []fincloud.ListValue
	OptionsError                         string
	RoleUnavailable, LocationUnavailable bool
	Errors                               map[string]string
}

func NewHandler(admin *adminshell.Shell, repository *fincloudauth.Repository, service *fincloudauth.Service, listValues listValuesReader) *Handler {
	return &Handler{admin: admin, repository: repository, service: service, listValues: listValues}
}

func (handler *Handler) RegisterRoutes(router chi.Router) {
	view := handler.admin.RequirePermission(PermissionView)
	manage := handler.admin.RequirePermission(PermissionManage)
	router.With(view).Get("/fincloud-auth-profiles", handler.Index)
	router.With(manage).Get("/fincloud-auth-profiles/new", handler.New)
	router.With(manage).Post("/fincloud-auth-profiles", handler.Create)
	router.With(view).Get("/fincloud-auth-profiles/{id}", handler.Show)
	router.With(manage).Get("/fincloud-auth-profiles/{id}/edit", handler.Edit)
	router.With(manage).Post("/fincloud-auth-profiles/{id}", handler.Update)
	router.With(manage).Post("/fincloud-auth-profiles/{id}/test", handler.Test)
	router.With(manage).Post("/fincloud-auth-profiles/{id}/state", handler.State)
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.repository.List(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list Fincloud Auth Profiles", err)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/fincloudauthprofiles/index", "Fincloud Auth Profiles", ListData{Rows: rows, CanManage: principal.Can(PermissionManage)})
}

func (handler *Handler) New(writer http.ResponseWriter, request *http.Request) {
	form := handler.withListValues(request.Context(), FormData{Errors: map[string]string{}}, nil)
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/fincloudauthprofiles/form", "New Fincloud Auth Profile", form)
}

func (handler *Handler) Create(writer http.ResponseWriter, request *http.Request) {
	form, input, ok := handler.form(writer, request, true)
	if !ok {
		return
	}
	form = handler.withListValues(request.Context(), form, nil)
	if form.OptionsError != "" {
		handler.admin.RenderPage(writer, request, http.StatusServiceUnavailable, "features/fincloudauthprofiles/form", "New Fincloud Auth Profile", form)
		return
	}
	handler.validateSelections(&form, nil)
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/fincloudauthprofiles/form", "New Fincloud Auth Profile", form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	created, err := handler.repository.Create(request.Context(), principal.SecurityContext(), input, time.Now().UTC())
	if err != nil {
		form.Errors["form"] = publicError(err)
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/fincloudauthprofiles/form", "New Fincloud Auth Profile", form)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/fincloud-auth-profiles/%d?notice=fincloud-auth-profile-created", created.ID), http.StatusSeeOther)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.find(writer, request)
	if !ok {
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/fincloudauthprofiles/show", "Fincloud Auth Profile", DetailData{Value: value, CanManage: principal.Can(PermissionManage)})
}

func (handler *Handler) Edit(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.find(writer, request)
	if !ok {
		return
	}
	form := FormData{ID: value.ID, Revision: value.Revision, Name: value.Name, Username: value.Username, RoleID: value.RoleID, LocationID: value.LocationID, Errors: map[string]string{}}
	form = handler.withListValues(request.Context(), form, &value)
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/fincloudauthprofiles/form", "Edit Fincloud Auth Profile", form)
}

func (handler *Handler) Update(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	existing, err := handler.repository.Find(request.Context(), id)
	if errors.Is(err, fincloudauth.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find Fincloud Auth Profile", err)
		return
	}
	form, input, ok := handler.form(writer, request, false)
	if !ok {
		return
	}
	form.ID = id
	form = handler.withListValues(request.Context(), form, &existing)
	if form.OptionsError != "" {
		form.RoleID, form.LocationID = existing.RoleID, existing.LocationID
		handler.admin.RenderPage(writer, request, http.StatusServiceUnavailable, "features/fincloudauthprofiles/form", "Edit Fincloud Auth Profile", form)
		return
	}
	handler.validateSelections(&form, &existing)
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/fincloudauthprofiles/form", "Edit Fincloud Auth Profile", form)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if _, err := handler.repository.Update(request.Context(), principal.SecurityContext(), id, form.Revision, input, time.Now().UTC()); err != nil {
		form.Errors["form"] = publicError(err)
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/fincloudauthprofiles/form", "Edit Fincloud Auth Profile", form)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/fincloud-auth-profiles/%d?notice=fincloud-auth-profile-updated", id), http.StatusSeeOther)
}

func (handler *Handler) Test(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	notice := "fincloud-auth-profile-test-ok"
	if err := handler.service.Test(request.Context(), principal.SecurityContext(), id); err != nil {
		notice = "fincloud-auth-profile-test-failed"
	}
	http.Redirect(writer, request, fmt.Sprintf("/fincloud-auth-profiles/%d?notice=%s", id, notice), http.StatusSeeOther)
}

func (handler *Handler) State(writer http.ResponseWriter, request *http.Request) {
	id, ok := idParam(request)
	if !ok || !webutil.ParseForm(writer, request, 8<<10) {
		return
	}
	revision, err := strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	if err != nil {
		http.Error(writer, "Invalid revision.", http.StatusUnprocessableEntity)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.service.SetStatus(request.Context(), principal.SecurityContext(), id, revision, fincloudauth.Status(request.PostFormValue("status"))); err != nil {
		http.Error(writer, publicError(err), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/fincloud-auth-profiles/%d?notice=fincloud-auth-profile-state", id), http.StatusSeeOther)
}

func (handler *Handler) form(writer http.ResponseWriter, request *http.Request, passwordRequired bool) (FormData, fincloudauth.Input, bool) {
	if !webutil.ParseForm(writer, request, 32<<10) {
		return FormData{}, fincloudauth.Input{}, false
	}
	form := FormData{Name: strings.TrimSpace(request.PostFormValue("name")), Username: request.PostFormValue("username"),
		RoleID: request.PostFormValue("role_id"), LocationID: request.PostFormValue("location_id"), Errors: map[string]string{}}
	form.Revision, _ = strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	password := request.PostFormValue("password")
	if passwordRequired && password == "" {
		form.Errors["password"] = "Password is required."
	}
	return form, fincloudauth.Input{Name: form.Name, Username: form.Username, Password: password, RoleID: form.RoleID, LocationID: form.LocationID}, true
}

func (handler *Handler) withListValues(ctx context.Context, form FormData, existing *fincloudauth.Profile) FormData {
	values, err := handler.listValues.FetchAuthListValues(ctx)
	if err != nil {
		form.OptionsError = "Role and location options could not be loaded from Fincloud. Retry to continue."
		return form
	}
	form.Roles, form.Locations = values.Roles, values.Locations
	if existing != nil {
		form.RoleUnavailable = form.RoleID == existing.RoleID && !containsListValue(values.Roles, existing.RoleID)
		form.LocationUnavailable = form.LocationID == existing.LocationID && !containsListValue(values.Locations, existing.LocationID)
	}
	return form
}

func (handler *Handler) validateSelections(form *FormData, existing *fincloudauth.Profile) {
	if !containsListValue(form.Roles, form.RoleID) && (existing == nil || form.RoleID != existing.RoleID) {
		form.Errors["role_id"] = "Choose an available Fincloud role."
	}
	if !containsListValue(form.Locations, form.LocationID) && (existing == nil || form.LocationID != existing.LocationID) {
		form.Errors["location_id"] = "Choose an available Fincloud location."
	}
}

func containsListValue(values []fincloud.ListValue, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func (handler *Handler) find(writer http.ResponseWriter, request *http.Request) (fincloudauth.Profile, bool) {
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return fincloudauth.Profile{}, false
	}
	value, err := handler.repository.Find(request.Context(), id)
	if errors.Is(err, fincloudauth.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return fincloudauth.Profile{}, false
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find Fincloud Auth Profile", err)
		return fincloudauth.Profile{}, false
	}
	return value, true
}

func idParam(request *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	return id, err == nil && id != 0
}

func publicError(err error) string {
	switch {
	case errors.Is(err, fincloudauth.ErrConflict):
		return "This profile changed. Reload and try again."
	case errors.Is(err, fincloudauth.ErrInvalid), errors.Is(err, fincloudauth.ErrInactive):
		return err.Error()
	default:
		return "The operation could not be completed."
	}
}
