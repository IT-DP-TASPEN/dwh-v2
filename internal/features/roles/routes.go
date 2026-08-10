package roles

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/roles", handler.Roles)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Get("/roles/new", handler.NewRole)
	router.With(handler.admin.RequirePermission(PermissionCreate)).Post("/roles", handler.CreateRole)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/roles/{id}", handler.Role)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Get("/roles/{id}/edit", handler.EditRole)
	router.With(handler.admin.RequirePermission(PermissionUpdate)).Post("/roles/{id}", handler.UpdateRole)
	router.With(handler.admin.RequirePermission(PermissionManagePermissions)).Post("/roles/{id}/permissions", handler.ReplaceRolePermissions)
	router.With(handler.admin.RequirePermission(PermissionDelete)).Post("/roles/{id}/delete", handler.DeleteRole)
}
