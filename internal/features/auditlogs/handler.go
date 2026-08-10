package auditlogs

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
)

type auditService interface {
	List(context.Context, string, int) (Page, error)
	Find(context.Context, uint64) (Record, error)
}

type Handler struct {
	admin   *adminshell.Shell
	service auditService
}

type ListData struct {
	Page        Page
	PreviousURL string
	NextURL     string
}

func NewHandler(admin *adminshell.Shell, service auditService) *Handler {
	return &Handler{admin: admin, service: service}
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	page, err := handler.service.List(request.Context(), request.URL.Query().Get("action"), webutil.Page(request))
	if err != nil {
		handler.admin.Internal(writer, request, "list audit logs", err)
		return
	}
	data := ListData{Page: page, PreviousURL: pageURL(page.Action, page.Pagination.Previous), NextURL: pageURL(page.Action, page.Pagination.Next)}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/auditlogs/index", "Audit Logs", data)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	record, err := handler.service.Find(request.Context(), id)
	if errors.Is(err, ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find audit log", err)
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/auditlogs/show", "Audit Log", record)
}

func pageURL(action string, page int) string {
	if page == 0 {
		return ""
	}
	values := url.Values{"page": {strconv.Itoa(page)}}
	if action != "" {
		values.Set("action", action)
	}
	return "/audit-logs?" + values.Encode()
}
