package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestMFAResetRequiresExplicitConfirmation(t *testing.T) {
	for _, confirmation := range []string{"", "yes\n", "RESET MFA OTHER\n", "RESET MFA Alice\n"} {
		t.Run(strings.TrimSpace(confirmation), func(t *testing.T) {
			input, err := os.CreateTemp(t.TempDir(), "confirmation")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err := input.WriteString(confirmation); err != nil {
				t.Fatal(err)
			}
			if _, err := input.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			// Cancellation must happen before configuration/database access.
			t.Setenv("APP_ENV", "invalid-environment")
			var output bytes.Buffer
			err = run(context.Background(), []string{"user", "mfa-reset", "--username", " ALICE "}, input, &output, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "MFA reset not confirmed") {
				t.Fatalf("cancellation failed: %v", err)
			}
			if !strings.Contains(output.String(), `"RESET MFA alice"`) || !strings.Contains(output.String(), "revokes all sessions") {
				t.Fatalf("missing deliberate confirmation: %q", output.String())
			}
			if strings.Contains(output.String(), "Next login") {
				t.Fatal("cancelled command reported success")
			}
		})
	}
}

func TestMFAResetRejectsInvalidArgumentsBeforeRecovery(t *testing.T) {
	for _, arguments := range [][]string{
		{"user", "mfa-reset"},
		{"user", "mfa-reset", "--username", strings.Repeat("a", 256)},
		{"user", "mfa-reset", "--username", "alice", "extra"},
		{"user", "disable-mfa", "--username", "alice"},
	} {
		var output bytes.Buffer
		err := run(context.Background(), arguments, nil, &output, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "usage: app") || output.Len() != 0 {
			t.Fatalf("invalid recovery command accepted: %v output=%q err=%v", arguments, output.String(), err)
		}
	}
}
