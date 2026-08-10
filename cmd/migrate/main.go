package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
)

const migrationDirectory = "migrations"

var migrationNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: migrate <up|down|status|create NAME>")
	}

	command := arguments[0]
	if command == "create" {
		if len(arguments) != 2 || !migrationNamePattern.MatchString(arguments[1]) {
			return fmt.Errorf("usage: migrate create NAME (letters, numbers, underscores, and hyphens only)")
		}
		return goose.Create(nil, migrationDirectory, arguments[1], "sql")
	}

	if len(arguments) != 1 || (command != "up" && command != "down" && command != "status") {
		return fmt.Errorf("usage: migrate <up|down|status|create NAME>")
	}

	applicationConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	databaseContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	databaseConnection, err := database.OpenMigrations(databaseContext, applicationConfig.Database)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer databaseConnection.Close()

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.RunContext(ctx, command, databaseConnection.DB, migrationDirectory); err != nil {
		return fmt.Errorf("goose %s: %w", command, err)
	}
	return nil
}
