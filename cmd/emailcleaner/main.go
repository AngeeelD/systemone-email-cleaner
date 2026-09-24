// Command emailcleaner groups a Gmail inbox using a hosted Laya decision model.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/AngeeelD/systemone-email-cleaner/internal/act"
	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/gmail"
	"github.com/AngeeelD/systemone-email-cleaner/internal/laya"
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
	openLaya   func(context.Context, *config.Config) (layaPredictor, error)
	newApplier func(extendedGmailAccess) applier
	stdin      io.Reader
	clock      clockFunc
	sleep      func(time.Duration)
}

func main() {
	a := &app{
		configPath: "config.yaml",
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		stdin:      os.Stdin,
		clock:      time.Now,
		sleep:      time.Sleep,
		checkToken: checkToken,
		openGmail:  newGmailAccess,
		openLaya:   newLayaClient,
		newApplier: newRealApplier,
	}
	a.authorize = a.interactiveAuthorize // method value; see ruling R8
	os.Exit(a.run(os.Args[1:]))
}

func newLayaClient(_ context.Context, cfg *config.Config) (layaPredictor, error) {
	return laya.New(cfg.Laya), nil
}

func newRealApplier(g extendedGmailAccess) applier {
	return &act.GmailApplier{
		Modifier: func(ctx context.Context, id string, add, remove []string) error {
			return g.Modify(ctx, id, add, remove)
		},
		Batcher: func(ctx context.Context, ids []string, add, remove []string) error {
			return g.BatchModify(ctx, ids, add, remove)
		},
		Untrasher: func(ctx context.Context, id string) error {
			return g.Untrash(ctx, id)
		},
	}
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
	case "run":
		return a.runCmd(rest)
	case "rollback":
		return a.rollbackCmd(rest)
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
  run      Classify and act on unprocessed messages.
  rollback Undo a run (default: latest).

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
