package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/app"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/user"
)

func runAdminCreate(ctx context.Context, arguments []string, input *os.File, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("admin create", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	username := flags.String("username", "", "administrator username")
	name := flags.String("name", "", "administrator display name")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}

	applicationConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := app.NewLogger(applicationConfig.App.Environment)
	slog.SetDefault(logger)

	databaseContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	databaseConnection, err := database.Open(databaseContext, applicationConfig.Database)
	cancel()
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer databaseConnection.Close()

	bootstrapContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = access.Bootstrap(bootstrapContext, databaseConnection, app.PermissionDefinitions(), time.Now().UTC())
	cancel()
	if err != nil {
		return fmt.Errorf("initialize access control: %w", err)
	}

	reader := bufio.NewReader(input)
	if strings.TrimSpace(*username) == "" {
		*username, err = promptLine(reader, output, "Username: ")
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(*name) == "" {
		*name, err = promptLine(reader, output, "Display name: ")
		if err != nil {
			return err
		}
	}
	password, err := promptPassword(input, output, "Password: ")
	if err != nil {
		return err
	}
	confirmation, err := promptPassword(input, output, "Confirm password: ")
	if err != nil {
		return err
	}

	transaction, err := databaseConnection.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin administrator creation: %w", err)
	}
	defer transaction.Rollback()
	now := time.Now().UTC()
	created, err := app.CreateAdministrator(
		ctx,
		access.NewRepository(transaction),
		user.NewRepository(transaction),
		app.AdministratorInput{
			Username:             *username,
			Name:                 *name,
			Password:             password,
			PasswordConfirmation: confirmation,
		},
		now,
	)
	if err != nil {
		return err
	}
	if err := audit.Append(ctx, transaction, audit.Event{
		Action: audit.ActionAdminBootstrap, Resource: audit.ResourceUser, ResourceID: created.ID, CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("audit administrator creation: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit administrator creation: %w", err)
	}
	_, err = fmt.Fprintf(output, "Administrator %q created.\n", created.Username)
	return err
}

func promptLine(reader *bufio.Reader, output io.Writer, label string) (string, error) {
	if _, err := fmt.Fprint(output, label); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read prompt: %w", err)
	}
	return strings.TrimSpace(value), nil
}

func promptPassword(input *os.File, output io.Writer, label string) (string, error) {
	descriptor := int(input.Fd())
	if !term.IsTerminal(descriptor) {
		return "", fmt.Errorf("password input requires an interactive terminal")
	}
	if _, err := fmt.Fprint(output, label); err != nil {
		return "", fmt.Errorf("write password prompt: %w", err)
	}
	password, err := term.ReadPassword(descriptor)
	_, _ = fmt.Fprintln(output)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(password), nil
}
