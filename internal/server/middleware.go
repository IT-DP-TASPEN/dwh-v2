package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/logging"
)

// SecurityHeaders is also used by the browser fixture, so frontend tests enforce
// the same CSP as production. Only style attributes are allowed inline for
// Alpine visibility/transitions and context-menu positioning.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "same-origin")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(writer, request)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func securityHeaders(next http.Handler) http.Handler { return SecurityHeaders(next) }

// quietRoutes are high-frequency, low-value routes (probes, assets, and the
// HTMX status polls) whose successful responses are not access-logged.
var quietRoutes = map[string]bool{
	"/health": true, "/ready": true, "/static/*": true,
	"/ingestion/summary": true, "/runs/{id}/status": true, "/runs/{id}/children": true,
	"/runs/scheduler-wave": true, "/custom-datasets/{id}/status": true,
}

// accessLog emits one http.request.completed record per request. It logs the
// chi route pattern, never the raw path or query string.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			started := time.Now()
			wrapped := middleware.NewWrapResponseWriter(writer, request.ProtoMajor)
			defer func() {
				status := wrapped.Status()
				if status == 0 {
					status = http.StatusOK
				}
				route := logging.Route(request)
				if status < http.StatusBadRequest && quietRoutes[route] {
					return
				}
				logger.InfoContext(request.Context(), "http request completed", "event", "http.request.completed",
					"request_id", middleware.GetReqID(request.Context()), "method", request.Method, "route", route,
					"status", status, "duration_ms", time.Since(started).Milliseconds(), "response_bytes", wrapped.BytesWritten(),
					"protocol", request.Proto)
			}()
			next.ServeHTTP(wrapped, request)
		})
	}
}
