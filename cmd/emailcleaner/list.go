package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"emailcleaner/internal/gmail"
)

// unprocessedQuery selects inbox messages carrying none of the configured
// labels. Every label is excluded explicitly rather than relying on Gmail's
// parent-label matching, so the query means the same thing however the labels
// happen to be nested.
func unprocessedQuery(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for _, name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names)+1)
	parts = append(parts, "in:inbox")
	for _, name := range names {
		if strings.ContainsAny(name, " ") {
			name = `"` + name + `"`
		}
		parts = append(parts, "-label:"+name)
	}
	return strings.Join(parts, " ")
}

// formatMessage renders one line for the terminal. It favours readability over
// completeness: the full message is one click away in Gmail.
func formatMessage(m *gmail.Message) string {
	when := "----------"
	if !m.Date.IsZero() {
		when = m.Date.Format("2006-01-02")
	}
	return fmt.Sprintf("%s  %-24s  %s", when, truncate(m.FromDomain, 24), truncate(m.Subject, 60))
}

// truncate shortens s to at most max runes, appending an ellipsis. It counts
// runes so accented text is never cut mid-character.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func (a *app) list(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.configPath, "config", a.configPath, "path to the config file")
	limit := fs.Int("limit", 20, "maximum number of messages to examine (at least 1)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *limit <= 0 {
		fmt.Fprintf(a.stderr, "--limit must be at least 1\n")
		return exitUsage
	}

	cfg, code := a.loadConfig()
	if code != exitOK {
		return code
	}
	ctx := context.Background()

	if err := a.checkToken(ctx, cfg); err != nil {
		if isReAuth(err) {
			fmt.Fprintf(a.stderr, "gmail token expired or revoked; run `emailcleaner setup`\n")
			return exitReAuth
		}
		fmt.Fprintf(a.stderr, "token check failed: %v\n", err)
		return exitGeneric
	}

	client, err := a.openGmail(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open Gmail: %v\n", err)
		return exitGeneric
	}

	query := unprocessedQuery(cfg.Labels)
	ids, err := client.ListMessages(ctx, query, *limit)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot list messages: %v\n", err)
		return exitGeneric
	}

	fmt.Fprintf(a.stdout, "%d message(s) match %q\n\n", len(ids), query)
	for _, id := range ids {
		m, err := client.GetMessage(ctx, id)
		if err != nil {
			// One unreadable message must not abort the listing.
			fmt.Fprintf(a.stderr, "  skipping %s: %v\n", id, err)
			continue
		}
		fmt.Fprintln(a.stdout, formatMessage(m))
	}
	return exitOK
}
