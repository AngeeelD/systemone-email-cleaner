package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"emailcleaner/internal/config"
)

// checkToken validates the stored Gmail token without touching the mailbox.
// It is the real implementation behind app.checkToken.
func checkToken(_ context.Context, _ *config.Config) error {
	return nil // wired in Task 3
}

func (a *app) status(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.configPath, "config", a.configPath, "path to the config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, code := a.loadConfig()
	if code != exitOK {
		return code
	}

	if err := a.checkToken(context.Background(), cfg); err != nil {
		if isReAuth(err) {
			fmt.Fprintf(a.stderr, "gmail token expired or revoked; run `emailcleaner setup`\n")
			return exitReAuth
		}
		fmt.Fprintf(a.stderr, "token check failed: %v\n", err)
		return exitGeneric
	}

	fmt.Fprintf(a.stdout, "token: ok\n")
	fmt.Fprintf(a.stdout, "credentials: %s\n", cfg.Gmail.CredentialsFile)
	fmt.Fprintf(a.stdout, "labels: %d configured\n", len(cfg.Labels))
	return exitOK
}

// isReAuth reports whether err means the operator must re-run setup.
// Replaced by a call to gmail's sentinel in Task 3.
func isReAuth(err error) bool {
	return err != nil && strings.Contains(err.Error(), "expired or revoked")
}
