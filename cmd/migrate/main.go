package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/dwhschema"
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
		return migrationUsageError()
	}

	command := arguments[0]
	if command == "create" {
		if len(arguments) != 2 || !migrationNamePattern.MatchString(arguments[1]) {
			return fmt.Errorf("usage: migrate create NAME (letters, numbers, underscores, and hyphens only)")
		}
		return goose.Create(nil, migrationDirectory, arguments[1], "sql")
	}

	confirmDatabase := ""
	if command == "up" && len(arguments) == 3 && arguments[1] == "--confirm-database" {
		confirmDatabase = strings.TrimSpace(arguments[2])
		if confirmDatabase == "" {
			return migrationUsageError()
		}
	} else if len(arguments) != 1 || (command != "up" && command != "down" && command != "status") {
		return migrationUsageError()
	}

	applicationConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := validateCommandPolicy(applicationConfig, command, confirmDatabase); err != nil {
		return err
	}
	databaseContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	databaseConnection, err := database.OpenMigrations(databaseContext, applicationConfig.Database)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer databaseConnection.Close()
	if command == "up" || command == "down" {
		if err := verifySelectedDatabase(ctx, databaseConnection, applicationConfig.Database.Name, confirmDatabase); err != nil {
			return err
		}
	}
	if command == "up" {
		if err := preflightUp(ctx, databaseConnection); err != nil {
			return fmt.Errorf("migration preflight: %w", err)
		}
	}

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.RunContext(ctx, command, databaseConnection.DB, migrationDirectory); err != nil {
		return fmt.Errorf("goose %s: %w", command, err)
	}
	if command == "up" {
		if err := dwhschema.VerifyRuntime(ctx, databaseConnection); err != nil {
			return fmt.Errorf("verify migrated schema: %w", err)
		}
	}
	return nil
}

func migrationUsageError() error {
	return fmt.Errorf("usage: migrate <up [--confirm-database NAME]|down|status|create NAME>")
}

func validateCommandPolicy(applicationConfig config.Config, command, confirmed string) error {
	if command != "status" && applicationConfig.Database.Name == "dwh2" {
		return fmt.Errorf("database dwh2 is legacy/reference-only; migration mutation refused")
	}
	if applicationConfig.App.Environment != "production" {
		return nil
	}
	if command == "down" {
		return fmt.Errorf("production migration down is disabled")
	}
	if command == "up" && confirmed != applicationConfig.Database.Name {
		return fmt.Errorf("production migration requires --confirm-database %s", applicationConfig.Database.Name)
	}
	return nil
}

func verifySelectedDatabase(ctx context.Context, db *sqlx.DB, configured, confirmed string) error {
	var selected string
	if err := db.GetContext(ctx, &selected, `SELECT DATABASE()`); err != nil {
		return fmt.Errorf("identify selected database: %w", err)
	}
	if selected == "" || selected != configured {
		return fmt.Errorf("selected database %q does not match configured database %q", selected, configured)
	}
	if confirmed != "" && selected != confirmed {
		return fmt.Errorf("selected database %q does not match confirmed database %q", selected, confirmed)
	}
	return nil
}

func preflightUp(ctx context.Context, db *sqlx.DB) error {
	state, err := dwhschema.ReadMigrationState(ctx, db)
	if err != nil {
		return err
	}
	applied, err := dwhschema.ValidateMigrationPrefix(state)
	if err != nil {
		return err
	}
	var tableCount int
	if err := db.GetContext(ctx, &tableCount, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()`); err != nil {
		return fmt.Errorf("inspect database contents: %w", err)
	}
	if !state.TableExists && tableCount != 0 {
		return fmt.Errorf("database has %d existing objects but no Goose history", tableCount)
	}
	if state.TableExists {
		if applied == 0 && tableCount != 1 {
			return fmt.Errorf("database has existing objects outside an empty Goose lineage")
		}
	}
	return nil
}
