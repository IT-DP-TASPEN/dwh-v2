// Package logging holds the few helpers shared by operational log call sites.
// It deliberately is not a framework: records still go through *slog.Logger to
// stdout, and collectors/backends are a deployment concern.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-sql-driver/mysql"
)

// New returns the process logger: JSON at INFO outside development, text at
// DEBUG in development. Every record carries service and environment.
func New(output io.Writer, service, environment string) *slog.Logger {
	var handler slog.Handler = slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})
	if environment == "development" {
		handler = slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	return slog.New(handler).With("service", service, "environment", environment)
}

// Route returns the matched chi route pattern, never the raw path or query, so
// HTTP records stay low-cardinality and cannot carry identifiers or tokens.
func Route(request *http.Request) string {
	if routeContext := chi.RouteContext(request.Context()); routeContext != nil {
		if pattern := routeContext.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}

// ErrorAttrs describes err for stdout. MySQL server messages can quote the
// offending value or SQL fragment, so only their number and SQLSTATE are kept.
func ErrorAttrs(err error) []any {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		return []any{"error_type", fmt.Sprintf("%T", mysqlError), "mysql_error", mysqlError.Number, "sqlstate", string(mysqlError.SQLState[:])}
	}
	return []any{"error", err}
}
