package impersonation

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequireImpersonationActorAdmin).Post("/users/{id}/impersonate", handler.Start)
	router.With(handler.admin.RequireImpersonationActorAdmin).Post("/impersonation/stop", handler.Stop)
}
