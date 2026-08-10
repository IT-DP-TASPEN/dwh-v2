package auditlogs

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/audit-logs", handler.Index)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/audit-logs/{id}", handler.Show)
}
