package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/dwhschema"
	"github.com/ibldzn/go-admin/internal/fixedcoverage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("Fixed coverage backfill failed", "error", err)
		os.Exit(1)
	}
}
func validateTarget(configured, actual, confirmation string) error {
	if strings.EqualFold(configured, "dwh2") || strings.EqualFold(actual, "dwh2") {
		return fmt.Errorf("dwh2 is read-only; coverage backfill refused")
	}
	if configured == "" || actual != configured || confirmation != actual {
		return fmt.Errorf("backfill requires --confirm-database matching the configured and selected database")
	}
	return nil
}
func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("fixed-coverage-backfill", flag.ContinueOnError)
	confirmation := flags.String("confirm-database", "", "exact database to prepare")
	batch := flags.Int("batch-size", 1000, "rows per resumable transaction (1..5000)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *batch < 1 || *batch > 5000 {
		return fmt.Errorf("usage: fixed-coverage-backfill --confirm-database NAME [--batch-size 1000]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := validateTarget(cfg.Database.Name, cfg.Database.Name, *confirmation); err != nil {
		return err
	}
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	var actual string
	if err := db.GetContext(ctx, &actual, `SELECT DATABASE()`); err != nil {
		return err
	}
	if err := validateTarget(cfg.Database.Name, actual, *confirmation); err != nil {
		return err
	}
	if err := dwhschema.VerifyRuntime(ctx, db); err != nil {
		return err
	}
	if err := fixedcoverage.Backfill(ctx, db, *batch); err != nil {
		return err
	}
	slog.Info("Fixed coverage ready", "database", actual)
	return nil
}
