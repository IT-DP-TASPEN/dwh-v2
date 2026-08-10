package impersonation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
)

const maxFormBody = 32 << 10

type lifecycleService interface {
	Start(context.Context, browserauth.Principal, [32]byte, uint64, time.Time) (Result, error)
	Stop(context.Context, browserauth.Principal, [32]byte, time.Time) (Result, error)
}

type Handler struct {
	admin   *adminshell.Shell
	service lifecycleService
	cookies browserauth.CookieManager
}

type ConflictData struct {
	Message string
	BackURL string
}

func NewHandler(admin *adminshell.Shell, service lifecycleService, cookies browserauth.CookieManager) *Handler {
	return &Handler{admin: admin, service: service, cookies: cookies}
}

func (handler *Handler) Start(writer http.ResponseWriter, request *http.Request) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	if !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	tokenHash, ok := handler.sessionTokenHash(writer, request)
	if !ok {
		return
	}
	now := time.Now().UTC()
	result, err := handler.service.Start(request.Context(), principal, tokenHash, id, now)
	if err != nil {
		handler.handleError(writer, request, "start impersonation", err, fmt.Sprintf("/users/%d", id))
		return
	}
	handler.cookies.SetForSession(writer, result.RawToken, result.Session, now)
	http.Redirect(writer, request, "/?notice=impersonation-started", http.StatusSeeOther)
}

func (handler *Handler) Stop(writer http.ResponseWriter, request *http.Request) {
	if !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	tokenHash, ok := handler.sessionTokenHash(writer, request)
	if !ok {
		return
	}
	now := time.Now().UTC()
	result, err := handler.service.Stop(request.Context(), principal, tokenHash, now)
	if err != nil {
		handler.handleError(writer, request, "stop impersonation", err, "/")
		return
	}
	handler.cookies.SetForSession(writer, result.RawToken, result.Session, now)
	http.Redirect(writer, request, "/?notice=impersonation-stopped", http.StatusSeeOther)
}

func (handler *Handler) principal(writer http.ResponseWriter, request *http.Request) (browserauth.Principal, bool) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.admin.Internal(writer, request, "impersonation handler", errors.New("principal missing from request context"))
	}
	return principal, ok
}

func (handler *Handler) sessionTokenHash(writer http.ResponseWriter, request *http.Request) ([32]byte, bool) {
	rawToken, err := handler.cookies.Read(request)
	if err != nil {
		handler.redirectUnauthenticated(writer, request)
		return [32]byte{}, false
	}
	return auth.HashToken(rawToken), true
}

func (handler *Handler) handleError(writer http.ResponseWriter, request *http.Request, operation string, err error, backURL string) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		handler.redirectUnauthenticated(writer, request)
	case errors.Is(err, ErrTargetNotFound):
		handler.admin.NotFound(writer, request)
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrTargetAdmin):
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
	case errors.Is(err, ErrTargetInactive):
		handler.conflict(writer, request, "Inactive users cannot be impersonated.", backURL)
	case errors.Is(err, ErrSelf):
		handler.conflict(writer, request, "You cannot impersonate your own account.", backURL)
	case errors.Is(err, ErrAlreadyActive):
		handler.conflict(writer, request, "Return to the administrator account before impersonating another user.", backURL)
	case errors.Is(err, ErrNotActive):
		handler.conflict(writer, request, "This session is not impersonating a user.", backURL)
	default:
		handler.admin.Internal(writer, request, operation, err)
	}
}

func (handler *Handler) conflict(writer http.ResponseWriter, request *http.Request, message, backURL string) {
	handler.admin.RenderPage(writer, request, http.StatusConflict, "conflict", "Conflict", ConflictData{Message: message, BackURL: backURL})
}

func (handler *Handler) redirectUnauthenticated(writer http.ResponseWriter, request *http.Request) {
	handler.cookies.Clear(writer)
	if request.Header.Get("HX-Request") == "true" {
		writer.Header().Set("HX-Redirect", "/login")
		http.Error(writer, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	http.Redirect(writer, request, "/login", http.StatusSeeOther)
}
