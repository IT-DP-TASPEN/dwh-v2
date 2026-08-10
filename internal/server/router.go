package server

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/render"
)

type RouterDependencies struct {
	StaticFiles           fs.FS
	AllowRegistration     bool
	Authentication        *browserauth.HTTP
	RegisterAuthenticated func(chi.Router)
	Errors                *render.ErrorResponder
}

func NewRouter(dependencies RouterDependencies) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(dependencies.Errors.Recoverer)
	router.Use(securityHeaders)
	router.NotFound(dependencies.Errors.NotFound)

	router.Get("/health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("{\"status\":\"ok\"}\n"))
	})

	router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(dependencies.StaticFiles))))

	router.Group(func(web chi.Router) {
		web.Use(noStore)
		web.Use(http.NewCrossOriginProtection().Handler)
		web.Use(dependencies.Authentication.LoadPrincipal)

		web.Group(func(guest chi.Router) {
			guest.Use(dependencies.Authentication.RequireGuest)
			guest.Get("/login", dependencies.Authentication.LoginPage)
			guest.Post("/login", dependencies.Authentication.Login)
			if dependencies.AllowRegistration {
				guest.Get("/register", dependencies.Authentication.RegisterPage)
				guest.Post("/register", dependencies.Authentication.Register)
			}
		})
		web.Post("/logout", dependencies.Authentication.Logout)

		web.Group(func(authenticated chi.Router) {
			authenticated.Use(dependencies.Authentication.RequireAuth)
			if dependencies.RegisterAuthenticated != nil {
				dependencies.RegisterAuthenticated(authenticated)
			}
		})
	})

	return router
}
