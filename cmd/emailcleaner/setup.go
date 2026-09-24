package main

import (
	"context"
	"flag"
	"fmt"
	"sort"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

// interactiveAuthorize runs the interactive OAuth flow, sending the consent URL
// to the app's stdout so the whole command's output stays capturable. It is
// named apart from the app.authorize seam because a field and a method on the
// same type may not share a name.
func (a *app) interactiveAuthorize(ctx context.Context, cfg *config.Config) error {
	_, err := gmail.Authorize(ctx, cfg.Gmail.CredentialsFile, cfg.Gmail.TokenFile, a.stdout)
	return err
}

// newGmailAccess builds one authenticated client for the whole command.
func newGmailAccess(ctx context.Context, cfg *config.Config) (gmailAccess, error) {
	ts, err := gmail.TokenSource(ctx, cfg.Gmail.CredentialsFile, cfg.Gmail.TokenFile)
	if err != nil {
		return nil, err
	}
	return gmail.NewClient(ctx, ts)
}

func (a *app) setup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.configPath, "config", a.configPath, "path to the config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, code := a.loadConfig()
	if code != exitOK {
		return code
	}
	ctx := context.Background()

	fmt.Fprintf(a.stdout, "Authorizing with Gmail...\n")
	if err := a.authorize(ctx, cfg); err != nil {
		fmt.Fprintf(a.stderr, "authorization failed: %v\n", err)
		return exitGeneric
	}

	if err := a.checkToken(ctx, cfg); err != nil {
		fmt.Fprintf(a.stderr, "token check failed after authorization: %v\n", err)
		return exitGeneric
	}

	client, err := a.openGmail(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open Gmail: %v\n", err)
		return exitGeneric
	}

	who, err := client.Profile(ctx)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot read the account profile: %v\n", err)
		return exitGeneric
	}

	names := make([]string, 0, len(cfg.Labels))
	for _, name := range cfg.Labels {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		id, err := client.EnsureLabel(ctx, name)
		if err != nil {
			fmt.Fprintf(a.stderr, "cannot ensure label %s: %v\n", name, err)
			return exitGeneric
		}
		fmt.Fprintf(a.stdout, "  label %-26s id=%s\n", name, id)
	}

	fmt.Fprintf(a.stdout, "\nAuthorized as %s with %d labels ready.\n", who, len(names))
	fmt.Fprintf(a.stdout, "Google expires this token in 7 days in Testing mode; re-run setup when it does.\n")
	return exitOK
}
