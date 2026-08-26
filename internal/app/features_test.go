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
		"GET /", "GET /audit-logs", "GET /audit-logs/{id}", "GET /ingestion", "GET /ingestion/summary",
		"GET /datasources", "GET /datasources/new", "GET /datasources/{id}", "GET /datasources/{id}/edit",
		"GET /report-templates", "GET /report-templates/new", "GET /report-templates/{id}", "GET /report-templates/{id}/edit", "GET /report-templates/{id}/access",
		"GET /reports", "GET /reports/{id}", "GET /exports", "GET /exports/{id}/download",
		"GET /roles", "GET /roles/new", "GET /roles/{id}", "GET /roles/{id}/edit",
		"GET /runs", "GET /runs/run-all", "GET /runs/{id}", "GET /runs/{id}/status",
		"GET /sources", "GET /sources/{jobKey}/run",
		"GET /schedules", "GET /schedules/new", "GET /schedules/bulk/new", "GET /schedules/{id}", "GET /schedules/{id}/edit", "GET /schedules/{id}/occurrences/{occurrenceID}",
		"GET /users", "GET /users/new", "GET /users/{id}", "GET /users/{id}/edit", "GET /users/{id}/reset-password",
		"POST /impersonation/stop", "POST /roles", "POST /roles/{id}", "POST /roles/{id}/delete", "POST /roles/{id}/permissions",
		"POST /datasources", "POST /datasources/{id}", "POST /datasources/{id}/test", "POST /datasources/{id}/state",
		"POST /report-templates", "POST /report-templates/{id}", "POST /report-templates/{id}/test", "POST /report-templates/{id}/test-options", "POST /report-templates/{id}/state", "POST /report-templates/{id}/access/{userID}",
		"POST /reports/{id}/run", "POST /reports/{id}/export", "POST /reports/{id}/parameters/{key}/options",
		"POST /runs/run-all", "POST /runs/{id}/cancel", "POST /runs/{id}/recover-abandoned",
		"POST /sources/{jobKey}/runs", "POST /sources/{jobKey}/enable", "POST /sources/{jobKey}/disable",
		"POST /schedules", "POST /schedules/bulk", "POST /schedules/{id}", "POST /schedules/{id}/enable", "POST /schedules/{id}/disable", "POST /schedules/{id}/archive",
		"POST /users", "POST /users/{id}", "POST /users/{id}/activate", "POST /users/{id}/deactivate", "POST /users/{id}/impersonate", "POST /users/{id}/reset-password", "POST /users/{id}/role",
	}
	sort.Strings(want)
	if !slices.Equal(routes, want) {
		t.Fatalf("routes:\n got %v\nwant %v", routes, want)
	}
}
