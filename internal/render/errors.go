package render

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"
)

type ErrorResponder struct {
	renderer *Renderer
	appName  string
	logger   *slog.Logger
}

type ErrorPageData struct {
	Heading   string
	Message   string
	RequestID string
}

func NewErrorResponder(renderer *Renderer, appName string, logger *slog.Logger) *ErrorResponder {
	if logger == nil {
		logger = slog.Default()
	}
	return &ErrorResponder{renderer: renderer, appName: appName, logger: logger}
}

func (responder *ErrorResponder) NotFound(writer http.ResponseWriter, request *http.Request) {
	responder.renderError(writer, request, http.StatusNotFound, "not_found", ErrorPageData{
		Heading: "Page not found",
		Message: "The page may have moved, or the address may be incorrect.",
	})
}

func (responder *ErrorResponder) Internal(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	responder.logger.ErrorContext(request.Context(), "unexpected request error",
		"request_id", middleware.GetReqID(request.Context()),
		"method", request.Method,
		"path", request.URL.Path,
		"operation", operation,
		"error", err,
	)
	responder.renderInternal(writer, request)
}

func (responder *ErrorResponder) Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		wrapped := middleware.NewWrapResponseWriter(writer, request.ProtoMajor)
		defer func() {
			if recovered := recover(); recovered != nil {
				responder.logger.ErrorContext(request.Context(), "request panic recovered",
					"request_id", middleware.GetReqID(request.Context()),
					"method", request.Method,
					"path", request.URL.Path,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				responder.renderInternal(wrapped, request)
			}
		}()
		next.ServeHTTP(wrapped, request)
	})
}

func (responder *ErrorResponder) renderInternal(writer http.ResponseWriter, request *http.Request) {
	responder.renderError(writer, request, http.StatusInternalServerError, "internal_server_error", ErrorPageData{
		Heading:   "Something went wrong",
		Message:   "The request could not be completed. Please try again.",
		RequestID: middleware.GetReqID(request.Context()),
	})
}

func (responder *ErrorResponder) renderError(writer http.ResponseWriter, request *http.Request, status int, page string, data ErrorPageData) {
	if committed(writer) {
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	pageData := PageData{Title: http.StatusText(status), AppName: responder.appName, Data: data}
	if err := responder.renderer.RenderPageWithLayout(writer, status, page, "error", pageData); err != nil && !committed(writer) {
		responder.logger.ErrorContext(request.Context(), "render error page",
			"request_id", middleware.GetReqID(request.Context()),
			"method", request.Method,
			"path", request.URL.Path,
			"status", status,
			"error", err,
		)
		http.Error(writer, http.StatusText(status), status)
	}
}

func committed(writer http.ResponseWriter) bool {
	tracked, ok := writer.(interface{ Status() int })
	return ok && tracked.Status() != 0
}
