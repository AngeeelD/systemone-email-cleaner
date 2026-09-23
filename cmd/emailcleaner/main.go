// Command emailcleaner groups a Gmail inbox using a hosted Laya decision model.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"emailcleaner/internal/config"
)

const (
	exitOK      = 0
	exitGeneric = 1
	exitUsage   = 2
	exitReAuth  = 3
)

type app struct {
	configPath string
	stdout     io.Writer
	stderr     io.Writer

	// checkToken is a field so tests can inject failures. The real value is
	// wired in main().
	checkToken func(context.Context, *config.Config) error
}

func main() {
	a := &app{
		configPath: "config.yaml",
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		checkToken: checkToken,
	}
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
