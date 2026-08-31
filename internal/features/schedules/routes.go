package schedules

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	view := handler.admin.RequirePermission(PermissionView)
	router.With(view).Get("/schedules", handler.Schedules)
	router.With(view, handler.admin.RequirePermission(PermissionCreate)).Get("/schedules/new", handler.New)
	router.With(view, handler.admin.RequirePermission(PermissionCreate)).Get("/schedules/bulk/new", handler.BulkNew)
	router.With(view, handler.admin.RequirePermission(PermissionCreate)).Post("/schedules", handler.Create)
	router.With(view, handler.admin.RequirePermission(PermissionCreate)).Post("/schedules/bulk", handler.BulkCreate)
	router.With(view, handler.admin.RequireAnyPermission(PermissionEnableDisable, PermissionArchive)).Post("/schedules/bulk-action", handler.BulkState)
	router.With(view).Get("/schedules/{id}", handler.Show)
	router.With(view, handler.admin.RequirePermission(PermissionUpdate)).Get("/schedules/{id}/edit", handler.Edit)
	router.With(view, handler.admin.RequirePermission(PermissionUpdate)).Post("/schedules/{id}", handler.Update)
	router.With(view, handler.admin.RequirePermission(PermissionEnableDisable)).Post("/schedules/{id}/enable", handler.Enable)
	router.With(view, handler.admin.RequirePermission(PermissionEnableDisable)).Post("/schedules/{id}/disable", handler.Disable)
	router.With(view, handler.admin.RequirePermission(PermissionArchive)).Post("/schedules/{id}/archive", handler.Archive)
	router.With(view).Get("/schedules/{id}/occurrences/{occurrenceID}", handler.Occurrence)
}
