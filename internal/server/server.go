package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type HTTPServer struct {
	server *http.Server
	logger *slog.Logger
}

func NewHTTPServer(address string, handler http.Handler, logger *slog.Logger) *HTTPServer {
	return &HTTPServer{
		server: &http.Server{
			Addr:              address,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		logger: logger,
	}
}

func (s *HTTPServer) Serve() error {
	s.logger.Info("http server listening", "address", s.server.Addr)
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

func (s *HTTPServer) Shutdown(ctx context.Context) error {
	s.logger.Info("http server shutting down")
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	s.logger.Info("http server stopped")
	return nil
}

func (s *HTTPServer) Close() error { return s.server.Close() }
