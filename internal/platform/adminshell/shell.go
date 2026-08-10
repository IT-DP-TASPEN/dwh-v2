package adminshell

import (
	"errors"
	"net/http"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
)

type Shell struct {
	renderer *render.Renderer
	registry *navigation.Registry
	appName  string
	errors   *render.ErrorResponder
}

type PageData struct {
	Title       string
	AppName     string
	Principal   browserauth.Principal
	Navigation  []navigation.GroupView
	CurrentPath string
	Notice      *render.Notice
	Data        any
}

func New(renderer *render.Renderer, registry *navigation.Registry, appName string, errorResponder *render.ErrorResponder) *Shell {
	return &Shell{renderer: renderer, registry: registry, appName: appName, errors: errorResponder}
}

func (handler *Shell) RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			principal, ok := browserauth.CurrentPrincipal(request.Context())
			if !ok {
				handler.errors.Internal(writer, request, "permission middleware", errors.New("principal missing from request context"))
				return
			}
			if !principal.Can(permission) {
				handler.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
				return
			}
			next.ServeHTTP(writer, request)
		})
	}
}

func (handler *Shell) RequireImpersonationActorAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := browserauth.CurrentPrincipal(request.Context())
		if !ok {
			handler.errors.Internal(writer, request, "impersonation actor middleware", errors.New("principal missing from request context"))
			return
		}
		if !access.IsAdminRole(principal.Actor.RoleSlug) {
			handler.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (handler *Shell) RenderPage(writer http.ResponseWriter, request *http.Request, status int, page, title string, data any) {
	pageData, ok := handler.PageData(request, title, data)
	if !ok {
		handler.errors.Internal(writer, request, "prepare admin page", errors.New("principal missing from request context"))
		return
	}
	if err := handler.renderer.RenderPage(writer, status, page, pageData); err != nil {
		handler.errors.Internal(writer, request, "render admin page "+page, err)
	}
}

func (handler *Shell) PageData(request *http.Request, title string, data any) (PageData, bool) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		return PageData{}, false
	}
	return PageData{
		Title:       title,
		AppName:     handler.appName,
		Principal:   principal,
		Navigation:  handler.registry.Prepare(request.URL.Path, principal.Can),
		CurrentPath: request.URL.Path,
		Notice:      render.NoticeFromID(request.URL.Query().Get("notice")),
		Data:        data,
	}, true
}

func (handler *Shell) RenderPartial(writer http.ResponseWriter, status int, page, name string, data any) error {
	return handler.renderer.RenderPartial(writer, status, page, name, data)
}

func (handler *Shell) NotFound(writer http.ResponseWriter, request *http.Request) {
	handler.errors.NotFound(writer, request)
}

func (handler *Shell) Internal(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	handler.errors.Internal(writer, request, operation, err)
}
