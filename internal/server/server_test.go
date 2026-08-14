package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownLetsInflightRequestFinish(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})
	server := NewHTTPServer("127.0.0.1:0", handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	listener, err := net.Listen("tcp", server.server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.server.Serve(listener) }()
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String()) //nolint:gosec // local lifecycle test
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				err = errors.New("unexpected response status")
			}
		}
		requestDone <- err
	}()
	<-started
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before in-flight request completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve error=%v", err)
	}
}

func TestShutdownHonorsDeadline(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := NewHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	listener, err := net.Listen("tcp", server.server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go server.server.Serve(listener)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := http.Get("http://" + listener.Addr().String()) //nolint:gosec // local lifecycle test
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error=%v", err)
	}
	close(release)
	_ = server.Close()
	<-requestDone
}
