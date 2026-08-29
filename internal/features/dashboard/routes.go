package dashboard

import (
	"github.com/go-chi/chi/v5"

	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/reports"
)

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequireAnyPermission(ingestionfeature.PermissionView, reports.PermissionView)).Get("/", handler.Index)
}
