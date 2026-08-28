package ingestion

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	view := handler.admin.RequirePermission(PermissionView)
	router.With(handler.admin.RequireAnyPermission(PermissionView, "sources.view", "schedules.view")).Get("/ingestion", handler.Overview)
	router.With(handler.admin.RequireAnyPermission(PermissionView, "sources.view", "schedules.view")).Get("/ingestion/summary", handler.Summary)
	router.With(view).Get("/runs", handler.Runs)
	router.With(view, handler.admin.RequirePermission(PermissionRunAll)).Get("/runs/run-all", handler.RunAllPage)
	router.With(view, handler.admin.RequirePermission(PermissionRunAll)).Post("/runs/run-all", handler.SubmitRunAll)
	router.With(view).Get("/runs/{id}/children", handler.RunAllChildren)
	router.With(view).Get("/runs/{id}", handler.Run)
	router.With(view).Get("/runs/{id}/status", handler.RunStatus)
	router.With(view, handler.admin.RequirePermission(PermissionCancel)).Post("/runs/{id}/cancel", handler.Cancel)
	router.With(view, handler.admin.RequirePermission(PermissionRecoverAbandoned)).Post("/runs/{id}/recover-abandoned", handler.RecoverAbandoned)
}
