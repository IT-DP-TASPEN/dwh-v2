package main

import (
	"context"
	"io"
	"testing"
)

func TestProductionDatabaseIsHardRestricted(t *testing.T) {
	if productionDatabase != "dwh2" {
		t.Fatalf("production adoption database = %q", productionDatabase)
	}
	for _, arguments := range [][]string{{}, {"other"}, {"apply"}, {"preflight", "extra"}} {
		if err := run(context.Background(), arguments, io.Discard); err == nil {
			t.Fatalf("arguments %v accepted", arguments)
		}
	}
}
