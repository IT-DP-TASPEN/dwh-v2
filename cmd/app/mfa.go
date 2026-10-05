package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/mfa"
	"github.com/ibldzn/go-admin/internal/user"
)

func runMFAReset(ctx context.Context, arguments []string, input *os.File, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("user mfa-reset", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	username := flags.String("username", "", "username requiring fresh MFA enrollment")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	*username = user.NormalizeUsername(*username)
	if flags.NArg() != 0 || user.ValidateUsername(*username) != nil {
		return fmt.Errorf("usage: app user mfa-reset --username USER")
	}
	expected := "RESET MFA " + *username
	confirmation, err := promptLine(bufio.NewReader(input), output, fmt.Sprintf("This revokes all sessions and MFA credentials. Type %q to confirm: ", expected))
	if err != nil {
		return err
	}
	if confirmation != expected {
		return fmt.Errorf("MFA reset not confirmed")
	}
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	db, err := database.Open(ctx, configuration.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	target, err := user.NewRepository(db).FindByUsername(ctx, *username)
	if err != nil {
		return err
	}
	store := &mfa.Store{DB: db}
	if err = store.Reset(ctx, target.ID, 0, 0, audit.Attribution{}, time.Now().UTC()); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "MFA reset for %q. Next login requires password and fresh enrollment.\n", target.Username)
	return err
}
