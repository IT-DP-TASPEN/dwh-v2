package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestInvalidCommandReturnsUsage(t *testing.T) {
	err := run(context.Background(), []string{"--invalid-command"}, os.Stdin, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "usage: app") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestInvalidAdminCommandReturnsUsage(t *testing.T) {
	err := run(context.Background(), []string{"admin", "delete"}, os.Stdin, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "usage: app") {
		t.Fatalf("expected usage error, got %v", err)
	}
}
