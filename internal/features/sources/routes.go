package sources

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	view := handler.admin.RequirePermission(PermissionView)
	router.With(view).Get("/sources", handler.Sources)
	router.With(view, handler.admin.RequirePermission("ingestion.run")).Get("/sources/{jobKey}/run", handler.RunPage)
	router.With(view, handler.admin.RequirePermission("ingestion.run")).Post("/sources/{jobKey}/runs", handler.Submit)
	router.With(view, handler.admin.RequirePermission(PermissionManage)).Post("/sources/{jobKey}/enable", handler.Enable)
	router.With(view, handler.admin.RequirePermission(PermissionManage)).Post("/sources/{jobKey}/disable", handler.Disable)
}
