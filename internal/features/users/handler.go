package users

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
)

const maxManagementFormBody = 32 << 10

type service interface {
	ListUsers(context.Context, string, int) (UserPage, error)
	FindUser(context.Context, uint64) (UserRecord, error)
	AvailableRoles(context.Context, securityctx.Requester) ([]access.Role, error)
	CreateUser(context.Context, securityctx.Requester, CreateUserInput, time.Time) (UserRecord, error)
	UpdateUser(context.Context, securityctx.Requester, uint64, UpdateUserInput, time.Time) (UserRecord, error)
	AssignRole(context.Context, securityctx.Requester, uint64, uint64, time.Time) error
	SetActive(context.Context, securityctx.Requester, uint64, bool, time.Time) error
	ResetPassword(context.Context, securityctx.Requester, uint64, ResetPasswordInput, time.Time) error
}

type Handler struct {
	admin                *adminshell.Shell
	service              service
	cookies              browserauth.CookieManager
	assignRolePermission string
	canImpersonate       func(browserauth.Principal, uint64, string, bool) bool
}

type UserRowView struct {
	User             UserRecord
	CanEdit          bool
	CanAssignRole    bool
	CanChangeStatus  bool
	CanResetPassword bool
}

type UserListData struct {
	Rows        []UserRowView
	Query       string
	Pagination  pagination.Page
	PreviousURL string
	NextURL     string
	CanCreate   bool
}

type UserFormData struct {
	Action       string
	Name         string
	Username     string
	SelectedRole uint64
	ShowRoles    bool
	Roles        []access.Role
	Errors       map[string]string
}

type UserDetailData struct {
	User             UserRecord
	Roles            []access.Role
	CanEdit          bool
	CanAssignRole    bool
	CanChangeStatus  bool
	CanResetPassword bool
	CanImpersonate   bool
}

type PasswordFormData struct {
	User   UserRecord
	Errors map[string]string
}

type ConflictData struct {
	Message string
	BackURL string
}

func NewHandler(admin *adminshell.Shell, service service, cookies browserauth.CookieManager, assignRolePermission string, canImpersonate func(browserauth.Principal, uint64, string, bool) bool) *Handler {
	return &Handler{admin: admin, service: service, cookies: cookies, assignRolePermission: assignRolePermission, canImpersonate: canImpersonate}
}

func (handler *Handler) Users(writer http.ResponseWriter, request *http.Request) {
	page, err := handler.service.ListUsers(request.Context(), request.URL.Query().Get("q"), queryPage(request))
	if err != nil {
		handler.internalError(writer, request, "list users", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	data := UserListData{
		Rows:       make([]UserRowView, 0, len(page.Users)),
		Query:      page.Query,
		Pagination: page.Pagination,
		CanCreate:  principal.Can(PermissionCreate),
	}
	for _, found := range page.Users {
		data.Rows = append(data.Rows, handler.userRow(principal, found))
	}
	data.PreviousURL = usersPageURL(page.Query, page.Pagination.Previous)
	data.NextURL = usersPageURL(page.Query, page.Pagination.Next)
	pageData, ok := handler.admin.PageData(request, "Users", data)
	if !ok {
		handler.internalError(writer, request, "prepare users page", errors.New("principal missing from request context"))
		return
	}
	if request.Header.Get("HX-Request") == "true" {
		if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/users/index", "users-table", pageData); err != nil {
			handler.internalError(writer, request, "render users partial", err)
		}
		return
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/users/index", "admin", pageData); err != nil {
		handler.internalError(writer, request, "render users", err)
	}
}

func (handler *Handler) NewUser(writer http.ResponseWriter, request *http.Request) {
	handler.renderUserForm(writer, request, http.StatusOK, UserFormData{Errors: map[string]string{}})
}

func (handler *Handler) CreateUser(writer http.ResponseWriter, request *http.Request) {
	if !parseManagementForm(writer, request) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	form := UserFormData{
		Name:     strings.TrimSpace(request.PostFormValue("name")),
		Username: user.NormalizeUsername(request.PostFormValue("username")),
		Errors:   map[string]string{},
	}
	var roleID *uint64
	if _, submitted := request.PostForm["role_id"]; submitted {
		parsed, err := strconv.ParseUint(request.PostFormValue("role_id"), 10, 64)
		if err != nil || parsed == 0 {
			form.Errors["role_id"] = "select a valid role"
		} else {
			form.SelectedRole = parsed
			roleID = &parsed
		}
	}
	if len(form.Errors) != 0 {
		handler.renderUserForm(writer, request, http.StatusUnprocessableEntity, form)
		return
	}
	_, err := handler.service.CreateUser(request.Context(), principal.SecurityContext(), CreateUserInput{
		Name:                 form.Name,
		Username:             form.Username,
		Password:             request.PostFormValue("password"),
		PasswordConfirmation: request.PostFormValue("password_confirmation"),
		RoleID:               roleID,
	}, time.Now().UTC())
	if validation := validationErrors(err); validation != nil {
		form.Errors = validation
		handler.renderUserForm(writer, request, http.StatusUnprocessableEntity, form)
		return
	}
	if errors.Is(err, user.ErrUsernameTaken) {
		form.Errors["username"] = "Username is already taken."
		handler.renderUserForm(writer, request, http.StatusUnprocessableEntity, form)
		return
	}
	if err != nil {
		handler.handleMutationError(writer, request, "create user", err, "/users/new")
		return
	}
	http.Redirect(writer, request, "/users?notice=user-created", http.StatusSeeOther)
}

func (handler *Handler) User(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	handler.renderUserDetail(writer, request, id, http.StatusOK)
}

func (handler *Handler) EditUser(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	found, err := handler.service.FindUser(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "find edited user", err)
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/users/edit", "Edit User", UserFormData{
		Action: fmt.Sprintf("/users/%d", id), Name: found.Name, Username: found.Username, Errors: map[string]string{},
	})
}

func (handler *Handler) UpdateUser(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !parseManagementForm(writer, request) {
		return
	}
	form := UserFormData{
		Action: fmt.Sprintf("/users/%d", id), Name: strings.TrimSpace(request.PostFormValue("name")), Username: user.NormalizeUsername(request.PostFormValue("username")), Errors: map[string]string{},
	}
	if _, exists := request.PostForm["role_id"]; exists {
		form.Errors["form"] = "role must be changed using the role assignment action"
	}
	if _, exists := request.PostForm["is_active"]; exists {
		form.Errors["form"] = "status must be changed using the activation actions"
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/users/edit", "Edit User", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	_, err := handler.service.UpdateUser(request.Context(), principal.SecurityContext(), id, UpdateUserInput{Name: form.Name, Username: form.Username}, time.Now().UTC())
	if validation := validationErrors(err); validation != nil {
		form.Errors = validation
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/users/edit", "Edit User", form)
		return
	}
	if errors.Is(err, user.ErrUsernameTaken) {
		form.Errors["username"] = "Username is already taken."
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/users/edit", "Edit User", form)
		return
	}
	if err != nil {
		handler.handleMutationError(writer, request, "update user", err, fmt.Sprintf("/users/%d/edit", id))
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/users/%d?notice=user-updated", id), http.StatusSeeOther)
}

func (handler *Handler) AssignUserRole(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !parseManagementForm(writer, request) {
		return
	}
	roleID, err := strconv.ParseUint(request.PostFormValue("role_id"), 10, 64)
	if err != nil || roleID == 0 {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	if err := handler.service.AssignRole(request.Context(), principal.SecurityContext(), id, roleID, time.Now().UTC()); err != nil {
		handler.handleMutationError(writer, request, "assign user role", err, fmt.Sprintf("/users/%d", id))
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/users/%d?notice=role-assigned", id), http.StatusSeeOther)
}

func (handler *Handler) ActivateUser(writer http.ResponseWriter, request *http.Request) {
	handler.changeUserStatus(writer, request, true)
}

func (handler *Handler) DeactivateUser(writer http.ResponseWriter, request *http.Request) {
	handler.changeUserStatus(writer, request, false)
}

func (handler *Handler) changeUserStatus(writer http.ResponseWriter, request *http.Request, active bool) {
	id, ok := handler.routeID(writer, request)
	if !ok || !parseManagementForm(writer, request) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	if err := handler.service.SetActive(request.Context(), principal.SecurityContext(), id, active, time.Now().UTC()); err != nil {
		handler.handleMutationError(writer, request, "change user status", err, fmt.Sprintf("/users/%d", id))
		return
	}
	notice := "user-deactivated"
	if active {
		notice = "user-activated"
	}
	http.Redirect(writer, request, fmt.Sprintf("/users/%d?notice=%s", id, notice), http.StatusSeeOther)
}

func (handler *Handler) ResetPasswordPage(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	found, err := handler.service.FindUser(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "find password user", err)
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/users/reset_password", "Reset Password", PasswordFormData{User: found, Errors: map[string]string{}})
}

func (handler *Handler) ResetPassword(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !parseManagementForm(writer, request) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	err := handler.service.ResetPassword(request.Context(), principal.SecurityContext(), id, ResetPasswordInput{
		Password: request.PostFormValue("password"), PasswordConfirmation: request.PostFormValue("password_confirmation"),
	}, time.Now().UTC())
	if validation := validationErrors(err); validation != nil {
		found, findErr := handler.service.FindUser(request.Context(), id)
		if findErr != nil {
			handler.readError(writer, request, "reload password user", findErr)
			return
		}
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/users/reset_password", "Reset Password", PasswordFormData{User: found, Errors: validation})
		return
	}
	if err != nil {
		handler.handleMutationError(writer, request, "reset user password", err, fmt.Sprintf("/users/%d/reset-password", id))
		return
	}
	if principal.Actor.UserID == id {
		handler.cookies.Clear(writer)
		http.Redirect(writer, request, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/users/%d?notice=password-reset", id), http.StatusSeeOther)
}

func (handler *Handler) renderUserForm(writer http.ResponseWriter, request *http.Request, status int, form UserFormData) {
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	form.Action = "/users"
	form.ShowRoles = principal.Can(handler.assignRolePermission)
	if form.ShowRoles {
		roles, err := handler.service.AvailableRoles(request.Context(), principal.SecurityContext())
		if err != nil {
			handler.internalError(writer, request, "list user form roles", err)
			return
		}
		form.Roles = roles
		if form.SelectedRole == 0 {
			for _, role := range roles {
				if role.Slug == access.UserRoleSlug {
					form.SelectedRole = role.ID
					break
				}
			}
		}
	}
	handler.admin.RenderPage(writer, request, status, "features/users/new", "New User", form)
}

func (handler *Handler) renderUserDetail(writer http.ResponseWriter, request *http.Request, id uint64, status int) {
	found, err := handler.service.FindUser(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "find user detail", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	row := handler.userRow(principal, found)
	data := UserDetailData{
		User: found, CanEdit: row.CanEdit, CanAssignRole: row.CanAssignRole,
		CanChangeStatus: row.CanChangeStatus, CanResetPassword: row.CanResetPassword,
		CanImpersonate: handler.canImpersonate != nil && handler.canImpersonate(principal, found.ID, found.RoleSlug, found.IsActive),
	}
	if data.CanAssignRole {
		data.Roles, err = handler.service.AvailableRoles(request.Context(), principal.SecurityContext())
		if err != nil {
			handler.internalError(writer, request, "list user detail roles", err)
			return
		}
	}
	handler.admin.RenderPage(writer, request, status, "features/users/show", "User Details", data)
}

func (handler *Handler) userRow(principal browserauth.Principal, found UserRecord) UserRowView {
	adminTargetAllowed := !access.IsAdminRole(found.RoleSlug) || access.IsAdminRole(principal.RoleSlug)
	return UserRowView{
		User:             found,
		CanEdit:          principal.Can(PermissionUpdate) && adminTargetAllowed,
		CanAssignRole:    principal.Can(handler.assignRolePermission) && adminTargetAllowed && (access.IsAdminRole(principal.RoleSlug) || principal.UserID != found.ID),
		CanChangeStatus:  principal.Can(PermissionDisable) && adminTargetAllowed && (!found.IsActive || principal.UserID != found.ID),
		CanResetPassword: principal.Can(PermissionResetPassword) && adminTargetAllowed,
	}
}

func (handler *Handler) principal(writer http.ResponseWriter, request *http.Request) (browserauth.Principal, bool) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.internalError(writer, request, "management handler", errors.New("principal missing from request context"))
	}
	return principal, ok
}

func (handler *Handler) readError(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	if errors.Is(err, ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	handler.internalError(writer, request, operation, err)
}

func (handler *Handler) handleMutationError(writer http.ResponseWriter, request *http.Request, operation string, err error, backURL string) {
	switch {
	case errors.Is(err, ErrNotFound):
		handler.admin.NotFound(writer, request)
	case errors.Is(err, ErrAdminMutation), errors.Is(err, ErrRoleSubmissionForbidden), errors.Is(err, ErrSelfRoleChange):
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
	case errors.Is(err, ErrLastActiveAdmin):
		handler.renderConflict(writer, request, "At least one active administrator must remain.", backURL)
	case errors.Is(err, ErrSelfDeactivation):
		handler.renderConflict(writer, request, "You cannot deactivate your current account.", backURL)
	default:
		handler.internalError(writer, request, operation, err)
	}
}

func (handler *Handler) renderConflict(writer http.ResponseWriter, request *http.Request, message, backURL string) {
	handler.admin.RenderPage(writer, request, http.StatusConflict, "conflict", "Conflict", ConflictData{Message: message, BackURL: backURL})
}

func (handler *Handler) internalError(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	handler.admin.Internal(writer, request, operation, err)
}

func validationErrors(err error) map[string]string {
	var validation ValidationErrors
	if errors.As(err, &validation) {
		return map[string]string(validation)
	}
	return nil
}

func (handler *Handler) routeID(writer http.ResponseWriter, request *http.Request) (uint64, bool) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return 0, false
	}
	return id, true
}

func queryPage(request *http.Request) int {
	return webutil.Page(request)
}

func usersPageURL(query string, page int) string {
	if page == 0 {
		return ""
	}
	values := url.Values{"page": {strconv.Itoa(page)}}
	if query != "" {
		values.Set("q", query)
	}
	return "/users?" + values.Encode()
}

func parseManagementForm(writer http.ResponseWriter, request *http.Request) bool {
	return webutil.ParseForm(writer, request, maxManagementFormBody)
}
