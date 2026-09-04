//go:build integration

package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestApplicationStartsReadyAndShutsDownWithinBudget(t *testing.T) {
	db := integrationdb.Open(t)
	_ = db
	databaseConfig := integrationdb.Config(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_URL", fmt.Sprintf("http://127.0.0.1:%d", port))
	t.Setenv("APP_BIND_HOST", "127.0.0.1")
	t.Setenv("APP_PORT", strconv.Itoa(port))
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "1s")
	t.Setenv("ALLOW_REGISTRATION", "false")
	t.Setenv("SESSION_SECURE", "false")
	t.Setenv("DB_HOST", databaseConfig.Host)
	t.Setenv("DB_PORT", strconv.Itoa(databaseConfig.Port))
	t.Setenv("DB_NAME", databaseConfig.Name)
	t.Setenv("DB_USER", databaseConfig.User)
	t.Setenv("DB_PASSWORD", databaseConfig.Password)
	t.Setenv("FINCLOUD_BASE_URL", "https://127.0.0.1:1")
	t.Setenv("FINCLOUD_INSECURE_SKIP_VERIFY", "false")
	t.Setenv("APP_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	readyURL := fmt.Sprintf("http://127.0.0.1:%d/ready", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := http.Get(readyURL) //nolint:gosec // loopback-only integration composition
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("application did not become ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("shutdown exceeded configured budget: %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application shutdown did not return")
	}
}
