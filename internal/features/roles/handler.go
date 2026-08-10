package roles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

const maxFormBody = 32 << 10

type roleService interface {
	List(context.Context) ([]Record, error)
	Find(context.Context, uint64) (Detail, error)
	Create(context.Context, securityctx.Requester, CreateInput, time.Time) (Record, error)
	Update(context.Context, securityctx.Requester, uint64, string, time.Time) (Record, error)
	Delete(context.Context, securityctx.Requester, uint64, time.Time) error
	ReplacePermissions(context.Context, securityctx.Requester, uint64, []string, time.Time) error
}

type Handler struct {
	admin   *adminshell.Shell
	service roleService
}

type RowView struct {
	Role                 Record
	CanEdit              bool
	CanDelete            bool
	CanManagePermissions bool
}

type ListData struct {
	Rows      []RowView
	CanCreate bool
}

type FormData struct {
	Action string
	Name   string
	Slug   string
	Errors map[string]string
}

type DetailData struct {
	Detail               Detail
	CanEdit              bool
	CanDelete            bool
	CanManagePermissions bool
	Errors               map[string]string
}

type ConflictData struct {
	Message string
	BackURL string
}

func NewHandler(admin *adminshell.Shell, service roleService) *Handler {
	return &Handler{admin: admin, service: service}
}

func (handler *Handler) Roles(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.service.List(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list roles", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	data := ListData{Rows: make([]RowView, 0, len(rows)), CanCreate: principal.Can(PermissionCreate)}
	for _, role := range rows {
		data.Rows = append(data.Rows, roleRow(principal, role))
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/roles/index", "Roles", data)
}

func (handler *Handler) NewRole(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/roles/new", "New Role", FormData{Errors: map[string]string{}})
}

func (handler *Handler) CreateRole(writer http.ResponseWriter, request *http.Request) {
	if !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	form := FormData{Name: strings.TrimSpace(request.PostFormValue("name")), Slug: NormalizeSlug(request.PostFormValue("slug")), Errors: map[string]string{}}
	_, err := handler.service.Create(request.Context(), principal.SecurityContext(), CreateInput{Name: form.Name, Slug: form.Slug}, time.Now().UTC())
	if validation := validationErrors(err); validation != nil {
		form.Errors = validation
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/roles/new", "New Role", form)
		return
	}
	if errors.Is(err, ErrRoleSlugTaken) {
		form.Errors["slug"] = "Slug is already in use."
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/roles/new", "New Role", form)
		return
	}
	if err != nil {
		handler.handleMutationError(writer, request, "create role", err, "/roles/new")
		return
	}
	http.Redirect(writer, request, "/roles?notice=role-created", http.StatusSeeOther)
}

func (handler *Handler) Role(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if ok {
		handler.renderDetail(writer, request, id, http.StatusOK, map[string]string{})
	}
}

func (handler *Handler) EditRole(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	detail, err := handler.service.Find(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "find edited role", err)
		return
	}
	if detail.Role.IsSystem {
		handler.renderConflict(writer, request, "System roles cannot be edited.", fmt.Sprintf("/roles/%d", id))
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/roles/edit", "Edit Role", FormData{Action: fmt.Sprintf("/roles/%d", id), Name: detail.Role.Name, Slug: detail.Role.Slug, Errors: map[string]string{}})
}

func (handler *Handler) UpdateRole(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	form := FormData{Action: fmt.Sprintf("/roles/%d", id), Name: strings.TrimSpace(request.PostFormValue("name")), Errors: map[string]string{}}
	if _, exists := request.PostForm["slug"]; exists {
		form.Errors["form"] = "role slug is immutable"
	}
	if _, exists := request.PostForm["is_system"]; exists {
		form.Errors["form"] = "system-role state cannot be changed"
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/roles/edit", "Edit Role", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	_, err := handler.service.Update(request.Context(), principal.SecurityContext(), id, form.Name, time.Now().UTC())
	if validation := validationErrors(err); validation != nil {
		form.Errors = validation
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/roles/edit", "Edit Role", form)
		return
	}
	if err != nil {
		handler.handleMutationError(writer, request, "update role", err, fmt.Sprintf("/roles/%d/edit", id))
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/roles/%d?notice=role-updated", id), http.StatusSeeOther)
}

func (handler *Handler) ReplaceRolePermissions(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	err := handler.service.ReplacePermissions(request.Context(), principal.SecurityContext(), id, request.PostForm["permissions"], time.Now().UTC())
	if errors.Is(err, ErrUnknownPermission) {
		handler.renderDetail(writer, request, id, http.StatusUnprocessableEntity, map[string]string{"permissions": "Submitted permissions are invalid."})
		return
	}
	if err != nil {
		handler.handleMutationError(writer, request, "replace role permissions", err, fmt.Sprintf("/roles/%d", id))
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/roles/%d?notice=permissions-updated", id), http.StatusSeeOther)
}

func (handler *Handler) DeleteRole(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	if err := handler.service.Delete(request.Context(), principal.SecurityContext(), id, time.Now().UTC()); err != nil {
		handler.handleMutationError(writer, request, "delete role", err, fmt.Sprintf("/roles/%d", id))
		return
	}
	http.Redirect(writer, request, "/roles?notice=role-deleted", http.StatusSeeOther)
}

func (handler *Handler) renderDetail(writer http.ResponseWriter, request *http.Request, id uint64, status int, formErrors map[string]string) {
	detail, err := handler.service.Find(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "find role detail", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	row := roleRow(principal, detail.Role)
	handler.admin.RenderPage(writer, request, status, "features/roles/show", "Role Details", DetailData{
		Detail: detail, CanEdit: row.CanEdit, CanDelete: row.CanDelete, CanManagePermissions: row.CanManagePermissions, Errors: formErrors,
	})
}

func roleRow(principal browserauth.Principal, role Record) RowView {
	return RowView{
		Role:      role,
		CanEdit:   principal.Can(PermissionUpdate) && !role.IsSystem,
		CanDelete: principal.Can(PermissionDelete) && !role.IsSystem,
		CanManagePermissions: principal.Can(PermissionManagePermissions) &&
			!access.IsAdminRole(role.Slug) && (access.IsAdminRole(principal.RoleSlug) || principal.RoleID != role.ID),
	}
}

func (handler *Handler) principal(writer http.ResponseWriter, request *http.Request) (browserauth.Principal, bool) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.admin.Internal(writer, request, "role handler", errors.New("principal missing from request context"))
	}
	return principal, ok
}

func (handler *Handler) routeID(writer http.ResponseWriter, request *http.Request) (uint64, bool) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
	}
	return id, ok
}

func (handler *Handler) readError(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	if errors.Is(err, ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	handler.admin.Internal(writer, request, operation, err)
}

func (handler *Handler) handleMutationError(writer http.ResponseWriter, request *http.Request, operation string, err error, backURL string) {
	switch {
	case errors.Is(err, ErrNotFound):
		handler.admin.NotFound(writer, request)
	case errors.Is(err, ErrSelfRolePermissions):
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
	case errors.Is(err, ErrRoleAssigned):
		handler.renderConflict(writer, request, "Move all users to another role before deleting this role.", backURL)
	case errors.Is(err, ErrProtectedRole):
		handler.renderConflict(writer, request, "System roles cannot be changed or deleted.", backURL)
	case errors.Is(err, ErrAdminPermissions):
		handler.renderConflict(writer, request, "Administrator access is automatic and its granular permissions cannot be changed.", backURL)
	default:
		handler.admin.Internal(writer, request, operation, err)
	}
}

func (handler *Handler) renderConflict(writer http.ResponseWriter, request *http.Request, message, backURL string) {
	handler.admin.RenderPage(writer, request, http.StatusConflict, "conflict", "Conflict", ConflictData{Message: message, BackURL: backURL})
}

func validationErrors(err error) map[string]string {
	var validation ValidationErrors
	if errors.As(err, &validation) {
		return map[string]string(validation)
	}
	return nil
}
