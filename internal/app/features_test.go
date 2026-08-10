package app

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/user"
)

func TestFeatureCompositionOwnsCompleteRouteMatrix(t *testing.T) {
	database := sqlx.NewDb(&sql.DB{}, "mysql")
	router := chi.NewRouter()
	registerFeatureRoutes(router, featureDependencies{
		database: database,
		users:    user.NewRepository(database),
		access:   access.NewRepository(database),
		admin:    adminshell.New(nil, nil, "Test", nil),
		cookies:  browserauth.NewCookieManager("session", false, time.Hour),
	})
	var routes []string
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, fmt.Sprintf("%s %s", method, route))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(routes)
	want := []string{
		"GET /", "GET /audit-logs", "GET /audit-logs/{id}", "GET /roles", "GET /roles/new", "GET /roles/{id}", "GET /roles/{id}/edit",
		"GET /users", "GET /users/new", "GET /users/{id}", "GET /users/{id}/edit", "GET /users/{id}/reset-password",
		"POST /impersonation/stop", "POST /roles", "POST /roles/{id}", "POST /roles/{id}/delete", "POST /roles/{id}/permissions",
		"POST /users", "POST /users/{id}", "POST /users/{id}/activate", "POST /users/{id}/deactivate", "POST /users/{id}/impersonate", "POST /users/{id}/reset-password", "POST /users/{id}/role",
	}
	sort.Strings(want)
	if !slices.Equal(routes, want) {
		t.Fatalf("routes:\n got %v\nwant %v", routes, want)
	}
}
