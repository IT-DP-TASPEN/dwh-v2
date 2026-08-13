package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ibldzn/go-admin/internal/adoption"
	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
)

const productionDatabase = "dwh2"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		slog.Error("dwh2 adoption failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 0 || (arguments[0] != "preflight" && arguments[0] != "apply") {
		return fmt.Errorf("usage: adopt-dwh2 <preflight|apply --confirm FINGERPRINT>")
	}
	confirmation := ""
	if arguments[0] == "apply" {
		if len(arguments) != 3 || arguments[1] != "--confirm" || arguments[2] == "" {
			return fmt.Errorf("usage: adopt-dwh2 apply --confirm FINGERPRINT")
		}
		confirmation = arguments[2]
	} else if len(arguments) != 1 {
		return fmt.Errorf("usage: adopt-dwh2 preflight")
	}
	applicationConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if applicationConfig.Database.Name != productionDatabase {
		return fmt.Errorf("production adoption is hard-restricted to database %q", productionDatabase)
	}
	databaseContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := database.OpenMigrations(databaseContext, applicationConfig.Database)
	if err != nil {
		return fmt.Errorf("open adoption database: %w", err)
	}
	defer db.Close()
	engine, err := adoption.New(db, adoption.Config{ExpectedDatabase: productionDatabase, MigrationDir: "migrations"})
	if err != nil {
		return err
	}
	if arguments[0] == "apply" {
		return engine.Apply(ctx, confirmation)
	}
	result, err := engine.Preflight(ctx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
