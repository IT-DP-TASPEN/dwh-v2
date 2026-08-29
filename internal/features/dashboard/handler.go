package dashboard

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ibldzn/go-admin/internal/browserauth"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
)

type Handler struct {
	admin   *adminshell.Shell
	service dashboardService
}

type dashboardService interface {
	Load(context.Context, browserauth.Principal, time.Time) (Data, error)
}

func NewHandler(admin *adminshell.Shell, service dashboardService) *Handler {
	return &Handler{admin: admin, service: service}
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.admin.Internal(writer, request, "load dashboard principal", errors.New("principal missing"))
		return
	}
	if !principal.Can(ingestionfeature.PermissionView) {
		if principal.Can(reports.PermissionView) {
			http.Redirect(writer, request, "/reports", http.StatusSeeOther)
			return
		}
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	data, err := handler.service.Load(request.Context(), principal, time.Now().UTC())
	if err != nil {
		handler.admin.Internal(writer, request, "load operational dashboard", err)
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/dashboard/index", "Dashboard", data)
}
