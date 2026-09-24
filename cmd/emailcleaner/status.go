package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/gmail"
)

// checkToken validates the stored Gmail token without touching the mailbox.
func checkToken(ctx context.Context, cfg *config.Config) error {
	return gmail.CheckToken(ctx, cfg.Gmail.CredentialsFile, cfg.Gmail.TokenFile)
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
func isReAuth(err error) bool {
	return errors.Is(err, gmail.ErrTokenExpired)
}
