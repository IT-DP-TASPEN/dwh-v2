package browserauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/user"
)

const maxFormBody = 32 << 10

type authenticationService interface {
	Login(context.Context, LoginInput, time.Time) (LoginResult, error)
	Register(context.Context, RegisterInput, time.Time) (user.User, error)
	ResolveSession(context.Context, [32]byte, time.Time) (Principal, error)
	Logout(context.Context, [32]byte) error
}

type HTTP struct {
	service           authenticationService
	renderer          *render.Renderer
	cookies           CookieManager
	appName           string
	allowRegistration bool
	logger            *slog.Logger
	appendAudit       func(context.Context, audit.Event) error
	errors            *render.ErrorResponder
}

type LoginForm struct {
	Username          string
	RememberMe        bool
	Next              string
	AllowRegistration bool
	Errors            map[string]string
}

type RegisterForm struct {
	Name     string
	Username string
	Errors   map[string]string
}

func NewHTTP(service authenticationService, renderer *render.Renderer, cookies CookieManager, appName string, allowRegistration bool, logger *slog.Logger, appendAudit func(context.Context, audit.Event) error, errorResponder *render.ErrorResponder) *HTTP {
	return &HTTP{service: service, renderer: renderer, cookies: cookies, appName: appName, allowRegistration: allowRegistration, logger: logger, appendAudit: appendAudit, errors: errorResponder}
}

func (h *HTTP) LoginPage(writer http.ResponseWriter, request *http.Request) {
	h.renderLogin(writer, request, http.StatusOK, LoginForm{
		Next:              SafeRedirect(request.URL.Query().Get("next")),
		AllowRegistration: h.allowRegistration,
		Errors:            map[string]string{},
	})
}

func (h *HTTP) Login(writer http.ResponseWriter, request *http.Request) {
	if !parseForm(writer, request) {
		return
	}
	form := LoginForm{
		Username:          user.NormalizeUsername(request.PostFormValue("username")),
		RememberMe:        request.PostFormValue("remember_me") != "",
		Next:              SafeRedirect(request.PostFormValue("next")),
		AllowRegistration: h.allowRegistration,
		Errors:            map[string]string{},
	}
	if err := user.ValidateUsername(form.Username); err != nil {
		form.Errors["username"] = err.Error()
	}
	password := request.PostFormValue("password")
	if password == "" {
		form.Errors["password"] = "password must not be empty"
	} else if len(password) > auth.MaxPasswordBytes {
		form.Errors["password"] = "password is too long"
	}
	if len(form.Errors) != 0 {
		h.renderLogin(writer, request, http.StatusUnprocessableEntity, form)
		return
	}

	result, err := h.service.Login(request.Context(), LoginInput{Username: form.Username, Password: password, RememberMe: form.RememberMe}, time.Now().UTC())
	if errors.Is(err, ErrInvalidCredentials) {
		form.Errors["credentials"] = "Invalid username or password."
		h.renderLogin(writer, request, http.StatusUnprocessableEntity, form)
		return
	}
	if err != nil {
		h.internalError(writer, request, "login", err)
		return
	}
	identity := audit.Identity{UserID: result.Session.UserID, Username: form.Username}
	h.appendBestEffortAudit(request, audit.Event{
		Attribution: audit.Attribution{Actor: &identity, Effective: &identity},
		Action:      audit.ActionAuthLogin, Resource: audit.ResourceUser, ResourceID: identity.UserID, CreatedAt: time.Now().UTC(),
	})
	h.cookies.Set(writer, result.RawToken, result.Session.RememberMe, result.Session.CreatedAt)
	http.Redirect(writer, request, form.Next, http.StatusSeeOther)
}

func (h *HTTP) RegisterPage(writer http.ResponseWriter, request *http.Request) {
	h.renderRegister(writer, request, http.StatusOK, RegisterForm{Errors: map[string]string{}})
}

func (h *HTTP) Register(writer http.ResponseWriter, request *http.Request) {
	if !parseForm(writer, request) {
		return
	}
	form := RegisterForm{
		Name:     strings.TrimSpace(request.PostFormValue("name")),
		Username: user.NormalizeUsername(request.PostFormValue("username")),
		Errors:   map[string]string{},
	}
	if err := user.ValidateName(form.Name); err != nil {
		form.Errors["name"] = err.Error()
	}
	if err := user.ValidateUsername(form.Username); err != nil {
		form.Errors["username"] = err.Error()
	}
	password := request.PostFormValue("password")
	confirmation := request.PostFormValue("password_confirmation")
	if err := auth.ValidatePassword(password); err != nil {
		form.Errors["password"] = err.Error()
	}
	if password != confirmation {
		form.Errors["password_confirmation"] = "password confirmation does not match"
	}
	if len(form.Errors) != 0 {
		h.renderRegister(writer, request, http.StatusUnprocessableEntity, form)
		return
	}

	created, err := h.service.Register(request.Context(), RegisterInput{
		Name:                 form.Name,
		Username:             form.Username,
		Password:             password,
		PasswordConfirmation: confirmation,
	}, time.Now().UTC())
	if errors.Is(err, user.ErrUsernameTaken) {
		form.Errors["username"] = "Username is already taken."
		h.renderRegister(writer, request, http.StatusUnprocessableEntity, form)
		return
	}
	if err != nil {
		h.internalError(writer, request, "register", err)
		return
	}
	h.appendBestEffortAudit(request, audit.Event{
		Action: audit.ActionAuthRegistration, Resource: audit.ResourceUser, ResourceID: created.ID, CreatedAt: time.Now().UTC(),
	})
	http.Redirect(writer, request, "/login?notice=registered", http.StatusSeeOther)
}

func (h *HTTP) Logout(writer http.ResponseWriter, request *http.Request) {
	principal, hasPrincipal := CurrentPrincipal(request.Context())
	rawToken, err := h.cookies.Read(request)
	if errors.Is(err, http.ErrNoCookie) {
		h.cookies.Clear(writer)
		http.Redirect(writer, request, "/login", http.StatusSeeOther)
		return
	}
	if err != nil {
		h.internalError(writer, request, "read logout cookie", err)
		return
	}
	if !validToken(rawToken) {
		h.cookies.Clear(writer)
		http.Redirect(writer, request, "/login", http.StatusSeeOther)
		return
	}
	if err := h.service.Logout(request.Context(), auth.HashToken(rawToken)); err != nil {
		h.internalError(writer, request, "logout", err)
		return
	}
	if hasPrincipal {
		attribution := auditAttributionFromPrincipal(principal)
		h.appendBestEffortAudit(request, audit.Event{
			Attribution: attribution, Action: audit.ActionAuthLogout,
			Resource: audit.ResourceUser, ResourceID: principal.Actor.UserID, CreatedAt: time.Now().UTC(),
		})
	}
	h.cookies.Clear(writer)
	http.Redirect(writer, request, "/login", http.StatusSeeOther)
}

func (h *HTTP) appendBestEffortAudit(request *http.Request, event audit.Event) {
	if h.appendAudit == nil {
		return
	}
	if err := h.appendAudit(request.Context(), event); err != nil {
		h.logger.WarnContext(request.Context(), "append authentication audit",
			"request_id", middleware.GetReqID(request.Context()),
			"method", request.Method,
			"path", request.URL.Path,
			"action", event.Action,
			"error", err,
		)
	}
}

func (h *HTTP) renderLogin(writer http.ResponseWriter, request *http.Request, status int, form LoginForm) {
	data := render.PageData{Title: "Login", AppName: h.appName, Notice: render.NoticeFromID(request.URL.Query().Get("notice")), Data: form}
	if err := h.renderer.RenderPageWithLayout(writer, status, "login", "auth", data); err != nil {
		h.internalError(writer, request, "render login page", err)
	}
}

func (h *HTTP) renderRegister(writer http.ResponseWriter, request *http.Request, status int, form RegisterForm) {
	data := render.PageData{Title: "Register", AppName: h.appName, Data: form}
	if err := h.renderer.RenderPageWithLayout(writer, status, "register", "auth", data); err != nil {
		h.internalError(writer, request, "render registration page", err)
	}
}

func (h *HTTP) internalError(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	h.errors.Internal(writer, request, operation, err)
}

func parseForm(writer http.ResponseWriter, request *http.Request) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxFormBody)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(writer, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		}
		return false
	}
	return true
}
