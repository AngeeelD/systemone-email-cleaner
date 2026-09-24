// Command emailcleaner groups a Gmail inbox using a hosted Laya decision model.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

const (
	exitOK      = 0
	exitGeneric = 1
	exitUsage   = 2
	exitReAuth  = 3
)

// gmailAccess is everything the CLI needs from Gmail in this milestone.
type gmailAccess interface {
	EnsureLabel(ctx context.Context, name string) (string, error)
	Profile(ctx context.Context) (string, error)
	ListMessages(ctx context.Context, query string, max int) ([]string, error)
	GetMessage(ctx context.Context, id string) (*gmail.Message, error)
}

type app struct {
	configPath string
	stdout     io.Writer
	stderr     io.Writer

	// The following are fields so tests can run without Google or a browser.
	authorize  func(context.Context, *config.Config) error
	checkToken func(context.Context, *config.Config) error
	openGmail  func(context.Context, *config.Config) (gmailAccess, error)
}

func main() {
	a := &app{
		configPath: "config.yaml",
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		checkToken: checkToken,
		openGmail:  newGmailAccess,
	}
	a.authorize = a.interactiveAuthorize // method value; see ruling R8
	os.Exit(a.run(os.Args[1:]))
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		a.usage(a.stderr)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return a.status(rest)
	case "setup":
		return a.setup(rest)
	case "list":
		return a.list(rest)
	case "help", "-h", "--help":
		a.usage(a.stderr)
		return exitOK
	default:
		fmt.Fprintf(a.stderr, "unknown command %q\n\n", cmd)
		a.usage(a.stderr)
		return exitUsage
	}
}

func (a *app) usage(w io.Writer) {
	fmt.Fprintf(w, `usage: emailcleaner <command>

Commands:
  status   Report token health and the current label set.
  setup    Authorize with Gmail and create any missing labels. Idempotent.
  list     Print the headers of unprocessed inbox messages.

Flags are per command; run "emailcleaner <command> -h" for details.
`)
}

// loadConfig reads the config file and reports a usage error when it is absent.
func (a *app) loadConfig() (*config.Config, int) {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot load config: %v\n", err)
		fmt.Fprintf(a.stderr, "\nCopy config.example.yaml to %s and edit it.\n", a.configPath)
		return nil, exitUsage
	}
	return cfg, exitOK
}
