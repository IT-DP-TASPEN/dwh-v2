package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ibldzn/go-admin/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, input *os.File, output, errorOutput io.Writer) error {
	if len(arguments) == 0 {
		return app.Run(ctx)
	}

	switch arguments[0] {
	case "serve":
		if len(arguments) != 1 {
			return usageError()
		}
		return app.Run(ctx)
	case "admin":
		if len(arguments) < 2 || arguments[1] != "create" {
			return usageError()
		}
		return runAdminCreate(ctx, arguments[2:], input, output, errorOutput)
	case "-h", "--help":
		_, _ = fmt.Fprintln(output, usageText())
		return nil
	default:
		return usageError()
	}
}

func usageError() error {
	return fmt.Errorf("usage: %s", usageText())
}

func usageText() string {
	return "app [serve|admin create [--username USER] [--name NAME]]"
}
