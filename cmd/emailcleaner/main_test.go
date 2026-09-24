package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/gmail"
)

// writeConfig writes a minimal valid config file and returns its path. The
// token-check tests need config.Load to succeed before checkToken runs.
func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("gmail:\n  token_file: token.json\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func newTestApp(t *testing.T) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{
		configPath: writeConfig(t),
		stdout:     &out,
		stderr:     &errOut,
	}
	a.authorize = func(context.Context, *config.Config) error { return nil }
	a.checkToken = func(context.Context, *config.Config) error { return nil }
	a.openGmail = func(context.Context, *config.Config) (gmailAccess, error) {
		return &fakeGmail{account: "me@example.com"}, nil
	}
	return a, &out, &errOut
}

func TestRunRejectsNoArguments(t *testing.T) {
	a, _, errOut := newTestApp(t)

	if got := a.run(nil); got != exitUsage {
		t.Errorf("run(nil) = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("stderr = %q, want usage text", errOut.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	a, _, errOut := newTestApp(t)

	if got := a.run([]string{"frobnicate"}); got != exitUsage {
		t.Errorf("run(frobnicate) = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("stderr = %q, want an unknown-command message", errOut.String())
	}
}

func TestRunHelpSucceeds(t *testing.T) {
	a, _, errOut := newTestApp(t)

	if got := a.run([]string{"help"}); got != exitOK {
		t.Errorf("run(help) = %d, want %d", got, exitOK)
	}
	if !strings.Contains(errOut.String(), "status") {
		t.Errorf("stderr = %q, want the command list", errOut.String())
	}
}

func TestStatusReportsExpiredTokenAsExitCodeThree(t *testing.T) {
	a, _, errOut := newTestApp(t)
	a.checkToken = func(context.Context, *config.Config) error {
		return gmail.ErrTokenExpired
	}

	if got := a.run([]string{"status"}); got != exitReAuth {
		t.Errorf("run(status) = %d, want %d", got, exitReAuth)
	}
	if !strings.Contains(errOut.String(), "setup") {
		t.Errorf("stderr = %q, want a re-authentication instruction", errOut.String())
	}
}

func TestStatusReportsGenericFailureAsExitCodeOne(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.checkToken = func(context.Context, *config.Config) error {
		return errors.New("network unreachable")
	}

	if got := a.run([]string{"status"}); got != exitGeneric {
		t.Errorf("run(status) = %d, want %d", got, exitGeneric)
	}
}
