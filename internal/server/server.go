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

func (s *HTTPServer) Run(ctx context.Context) error {
	errorsCh := make(chan error, 1)
	go func() {
		s.logger.Info("http server listening", "address", s.server.Addr)
		errorsCh <- s.server.ListenAndServe()
	}()

	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}

	s.logger.Info("http server shutting down")
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.server.Shutdown(shutdownContext); err != nil {
		_ = s.server.Close()
		return fmt.Errorf("shutdown http server: %w", err)
	}

	if err := <-errorsCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http during shutdown: %w", err)
	}
	s.logger.Info("http server stopped")
	return nil
}
