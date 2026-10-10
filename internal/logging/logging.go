// Package logging holds the few helpers shared by operational log call sites.
// It deliberately is not a framework: records still go through *slog.Logger to
// stdout, and collectors/backends are a deployment concern.
package logging

import (
	"context"
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

// Err describes err for operational stdout without its message. Arbitrary
// error text can quote SQL, bound values, CSV cells or upstream payloads, so
// only the error's type and structured codes are emitted; rich diagnostics
// belong in the durable DB stores. MySQL errors keep number and SQLSTATE but
// never the server message. The attr is an inline group: pass it directly to
// a slog call; a nil err adds nothing.
func Err(err error) slog.Attr {
	return slog.Attr{Value: slog.GroupValue(errorAttrs(err)...)}
}

func errorAttrs(err error) []slog.Attr {
	var mysqlError *mysql.MySQLError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return []slog.Attr{slog.String("error_type", errorType(err)), slog.String("error_kind", "cancelled")}
	case errors.Is(err, context.DeadlineExceeded):
		return []slog.Attr{slog.String("error_type", errorType(err)), slog.String("error_kind", "deadline_exceeded")}
	case errors.As(err, &mysqlError):
		return []slog.Attr{slog.String("error_type", fmt.Sprintf("%T", mysqlError)),
			slog.Any("mysql_error", mysqlError.Number), slog.String("sqlstate", string(mysqlError.SQLState[:]))}
	}
	return []slog.Attr{slog.String("error_type", errorType(err))}
}

// errorType names the first error in the chain that is not a plain fmt
// wrapper, so "load x: %w" chains report the underlying type.
func errorType(err error) string {
	for {
		name := fmt.Sprintf("%T", err)
		next := errors.Unwrap(err)
		if name != "*fmt.wrapError" || next == nil {
			return name
		}
		err = next
	}
}

// Panic describes a recovered panic value by type only: the value itself can
// carry request or source data.
func Panic(recovered any) slog.Attr {
	return slog.String("panic_type", fmt.Sprintf("%T", recovered))
}
