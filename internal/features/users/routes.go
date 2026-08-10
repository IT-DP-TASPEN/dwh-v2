package users

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/users", handler.Users)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Get("/users/new", handler.NewUser)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Post("/users", handler.CreateUser)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/users/{id}", handler.User)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Get("/users/{id}/edit", handler.EditUser)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Post("/users/{id}", handler.UpdateUser)
	router.With(handler.admin.RequirePermission(handler.assignRolePermission)).Post("/users/{id}/role", handler.AssignUserRole)
	router.With(handler.admin.RequirePermission(PermissionDisable)).Post("/users/{id}/activate", handler.ActivateUser)
	router.With(handler.admin.RequirePermission(PermissionDisable)).Post("/users/{id}/deactivate", handler.DeactivateUser)
	router.With(handler.admin.RequirePermission(PermissionResetPassword)).Get("/users/{id}/reset-password", handler.ResetPasswordPage)
	router.With(handler.admin.RequirePermission(PermissionResetPassword)).Post("/users/{id}/reset-password", handler.ResetPassword)
}
