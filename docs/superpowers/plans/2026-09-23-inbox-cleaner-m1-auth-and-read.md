# Inbox Cleaner — Milestone 1 (Auth and Read) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Get `emailcleaner setup` to complete OAuth against Gmail, create the six `cleaner/*` labels, and `emailcleaner list` to print the headers of every unprocessed inbox message — with no classification and no mutations.

**Architecture:** A single Go binary with a subcommand dispatcher. `internal/gmail` owns all Google API contact, exposes its own domain `Message` type, and is tested against `httptest` fake servers using the documented `option.WithEndpoint` seam. `internal/config` owns one YAML file. Nothing in this milestone imports Laya; nothing mutates the mailbox.

**Tech Stack:** Go 1.27, `google.golang.org/api/gmail/v1`, `golang.org/x/oauth2` + `.../google`, `gopkg.in/yaml.v3`, standard-library `net/http/httptest` for fakes.

**Spec:** `docs/superpowers/specs/2026-09-23-gmail-inbox-cleaner-design.md`

## Global Constraints

Copied verbatim from the spec. Every task below implicitly includes this section.

- Go module path is `emailcleaner`; Go directive `go 1.27`.
- **The only OAuth scope requested is `https://www.googleapis.com/auth/gmail.modify`.** Never add `https://mail.google.com/` — it would enable permanent deletion, which is out of scope by design.
- **No permanent deletion, ever.** `messages.delete` and `messages.batchDelete` must not appear in this codebase.
- `client_secret.json` and `token.json` are gitignored. The token file is written with mode `0600`.
- Exit codes: `0` success, `1` generic error, `2` usage/config error, `3` re-authentication required.
- Label names live in the config file, never hardcoded in logic. Namespace is `cleaner/`.
- Secrets come from the environment (`laya.api_key_env`), never from the config file. (No Laya code in this milestone, but the config field exists.)
- Gmail quota facts that shape the code: `messages.get` = 20 units, `messages.list` = 5 units, `labels.list` = 1 unit, `labels.create` = 5 units, `getProfile` = 1 unit. Per-user limit is 6,000 units/minute.
- One `messages.get` with `format=full` per message — it returns headers *and* body, and `metadata` costs the same 20 units while returning no body.
- All code comments, identifiers, and user-facing CLI text are in English.

---

## Prerequisite (manual, no code): Google Cloud Console setup

This must be done before Task 6's end-to-end verification can pass. Do it now — it takes ~10 minutes and there is nothing to code while waiting.

- [ ] **Step 1: Create a project**

Go to https://console.cloud.google.com/ and create a new project named `email-cleaner`. Select it.

- [ ] **Step 2: Enable the Gmail API**

Go to https://console.cloud.google.com/apis/library/gmail.googleapis.com and click **Enable**. Make sure the project selector at the top reads `email-cleaner`.

- [ ] **Step 3: Configure the OAuth consent screen**

Go to https://console.cloud.google.com/auth/branding (Google Auth Platform → Branding).

- App name: `email-cleaner`
- User support email: your own address
- Audience / User type: **External**
- Developer contact information: your own address

Save. Then go to **Audience → Test users → Add users** and add **your own Gmail address**. This is what lets an unverified app authorize your account.

- [ ] **Step 4: Add the scope**

Go to **Data Access → Add or remove scopes**, and add exactly one:

```
https://www.googleapis.com/auth/gmail.modify
```

Do not add `https://mail.google.com/`.

- [ ] **Step 5: Create the OAuth client**

Go to **Clients → Create client**:

- Application type: **Desktop app**
- Name: `emailcleaner-cli`

Click **Create**, then **Download JSON**. Move the downloaded file to the project root and rename it `client_secret.json`.

- [ ] **Step 6: Confirm it is gitignored**

Run: `cd /home/angeeeld/Code/go/email_cleaner && git check-ignore -v client_secret.json`
Expected: output naming `.gitignore` and the `client_secret.json` rule. **If this prints nothing, stop** — the file is not ignored and must not be committed.

---

### Task 1: Module and config loading

**Files:**
- Create: `go.mod`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `config.example.yaml`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config` with fields `Gmail`, `Laya`, `Policy`, `Labels`, `Extract`, `Audit`; `config.Load(path string) (*config.Config, error)`; `config.Default() *Config`; `config.Duration` (a `time.Duration` that decodes `"30s"`-style YAML strings) with method `Std() time.Duration`.

- [ ] **Step 1: Initialize the module**

```bash
cd /home/angeeeld/Code/go/email_cleaner
go mod init emailcleaner
go get gopkg.in/yaml.v3
go get golang.org/x/oauth2
go get google.golang.org/api/gmail/v1
go get google.golang.org/api/option
```

- [ ] **Step 2: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaultsForAbsentKeys(t *testing.T) {
	path := write(t, "gmail:\n  token_file: custom-token.json\n")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Gmail.TokenFile != "custom-token.json" {
		t.Errorf("Gmail.TokenFile = %q, want %q", got.Gmail.TokenFile, "custom-token.json")
	}
	if got.Gmail.CredentialsFile != "client_secret.json" {
		t.Errorf("Gmail.CredentialsFile = %q, want default %q", got.Gmail.CredentialsFile, "client_secret.json")
	}
	if got.Laya.Timeout.Std() != 30*time.Second {
		t.Errorf("Laya.Timeout = %v, want 30s", got.Laya.Timeout.Std())
	}
	if got.Policy.MinConfidenceJunk != 0.90 {
		t.Errorf("MinConfidenceJunk = %v, want 0.90", got.Policy.MinConfidenceJunk)
	}
	if got.Labels["people"] != "cleaner/people" {
		t.Errorf("Labels[people] = %q, want cleaner/people", got.Labels["people"])
	}
	if got.Extract.BodyPreviewChars != 800 {
		t.Errorf("BodyPreviewChars = %d, want 800", got.Extract.BodyPreviewChars)
	}
	if got.Audit.Dir != "audit" {
		t.Errorf("Audit.Dir = %q, want audit", got.Audit.Dir)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load() error = nil, want an error for a missing file")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	if _, err := Load(write(t, "gmail: [also not a map\n")); err == nil {
		t.Fatal("Load() error = nil, want a parse error")
	}
}

func TestLoadRejectsThresholdOutOfRange(t *testing.T) {
	for _, body := range []string{
		"policy:\n  min_confidence_junk: 1.5\n",
		"policy:\n  min_confidence_topic: 0\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("Load(%q) error = nil, want a range error", body)
		}
	}
}

func TestLoadOverridesOnlyPresentKeys(t *testing.T) {
	path := write(t, "labels:\n  people: my/people\n")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Labels["people"] != "my/people" {
		t.Errorf("Labels[people] = %q, want my/people", got.Labels["people"])
	}
	if got.Labels["security"] != "cleaner/security" {
		t.Errorf("Labels[security] = %q, want the default to survive", got.Labels["security"])
	}
}

func TestDurationRejectsInvalidString(t *testing.T) {
	if _, err := Load(write(t, "laya:\n  timeout: soon\n")); err == nil {
		t.Fatal("Load() error = nil, want a duration parse error")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 4: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config loads the single YAML file that configures the cleaner.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration decodes YAML duration strings such as "30s" or "2m".
// yaml.v3 does not do this for time.Duration on its own.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Gmail struct {
	CredentialsFile string `yaml:"credentials_file"`
	TokenFile       string `yaml:"token_file"`
}

type Laya struct {
	Endpoint  string   `yaml:"endpoint"`
	APIKeyEnv string   `yaml:"api_key_env"`
	Timeout   Duration `yaml:"timeout"`
	Workers   int      `yaml:"workers"`
}

type Policy struct {
	MinConfidenceJunk  float64 `yaml:"min_confidence_junk"`
	MinConfidenceTopic float64 `yaml:"min_confidence_topic"`
}

type Extract struct {
	BodyPreviewChars int `yaml:"body_preview_chars"`
}

type Audit struct {
	Dir string `yaml:"dir"`
}

type Config struct {
	Gmail   Gmail             `yaml:"gmail"`
	Laya    Laya              `yaml:"laya"`
	Policy  Policy            `yaml:"policy"`
	Labels  map[string]string `yaml:"labels"`
	Extract Extract           `yaml:"extract"`
	Audit   Audit             `yaml:"audit"`
}

// Default returns the full configuration with every value from the spec.
func Default() *Config {
	return &Config{
		Gmail: Gmail{
			CredentialsFile: "client_secret.json",
			TokenFile:       "token.json",
		},
		Laya: Laya{
			Endpoint:  "http://127.0.0.1:8000",
			APIKeyEnv: "LAYA_API_KEY",
			Timeout:   Duration(30 * time.Second),
			Workers:   8,
		},
		Policy: Policy{
			MinConfidenceJunk:  0.90,
			MinConfidenceTopic: 0.70,
		},
		Labels: map[string]string{
			"people":        "cleaner/people",
			"action":        "cleaner/action",
			"security":      "cleaner/security",
			"accounts":      "cleaner/accounts",
			"opportunities": "cleaner/opportunities",
			"unclassified":  "cleaner/unclassified",
		},
		Extract: Extract{BodyPreviewChars: 800},
		Audit:   Audit{Dir: "audit"},
	}
}

// Load reads path over the defaults. A missing file is an error: silently
// running with defaults would point the tool at the wrong mailbox.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Gmail.CredentialsFile == "" {
		return fmt.Errorf("gmail.credentials_file must not be empty")
	}
	if c.Gmail.TokenFile == "" {
		return fmt.Errorf("gmail.token_file must not be empty")
	}
	for name, v := range map[string]float64{
		"policy.min_confidence_junk":  c.Policy.MinConfidenceJunk,
		"policy.min_confidence_topic": c.Policy.MinConfidenceTopic,
	} {
		if v <= 0 || v > 1 {
			return fmt.Errorf("%s must be in (0, 1], got %v", name, v)
		}
	}
	if len(c.Labels) == 0 {
		return fmt.Errorf("labels must not be empty")
	}
	return nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS, all 6 tests.

- [ ] **Step 6: Write the example config**

Create `config.example.yaml`:

```yaml
# Copy to config.yaml and edit. config.yaml is gitignored.
gmail:
  credentials_file: client_secret.json
  token_file: token.json
laya:
  endpoint: http://192.168.1.50:8000
  api_key_env: LAYA_API_KEY
  timeout: 30s
  workers: 8
policy:
  min_confidence_junk: 0.90
  min_confidence_topic: 0.70
labels:
  people: cleaner/people
  action: cleaner/action
  security: cleaner/security
  accounts: cleaner/accounts
  opportunities: cleaner/opportunities
  unclassified: cleaner/unclassified
extract:
  body_preview_chars: 800
audit:
  dir: audit
```

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/config/ config.example.yaml
git commit -m "feat: add YAML config loading with spec defaults"
```

---

### Task 2: CLI dispatcher, exit codes, and `status`

**Files:**
- Create: `cmd/emailcleaner/main.go`
- Create: `cmd/emailcleaner/status.go`
- Create: `cmd/emailcleaner/main_test.go`

**Interfaces:**
- Consumes: `config.Load`, `config.Config`.
- Produces: `app` struct with fields `configPath string`, `stdout`, `stderr io.Writer`, `checkToken func(context.Context, *config.Config) error`; method `(*app).run(args []string) int`. Exit code constants `exitOK = 0`, `exitGeneric = 1`, `exitUsage = 2`, `exitReAuth = 3`. Later tasks add methods `setup([]string) int` and `list([]string) int` to the same struct.

- [ ] **Step 1: Write the failing test**

Create `cmd/emailcleaner/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"emailcleaner/internal/config"
)

func newTestApp(t *testing.T) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{
		configPath: writeConfig(t),
		stdout:     &out,
		stderr:     &errOut,
	}
	a.checkToken = func(context.Context, *config.Config) error { return nil }
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
		return errors.New("gmail token expired or revoked; run `emailcleaner setup`")
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
```

Add these two imports and this helper to that test file; the block above omits
them deliberately so the point is not lost in a wall of code. `"os"` and
`"path/filepath"` are needed by the helper, and **`newTestApp` must point at a
config file that actually exists**: a missing file is a usage error (exit 2),
which would mask the token behaviour the two `status` tests exist to assert.

```go
// writeConfig writes a minimal config file and returns its path.
func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("gmail:\n  token_file: token.json\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/emailcleaner/ -v`
Expected: FAIL — `undefined: app`.

- [ ] **Step 3: Write the dispatcher**

Create `cmd/emailcleaner/main.go`:

```go
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
```

**Note on the dispatcher.** The `switch` and the usage text contain only the commands that exist right now. Task 6 adds the `setup` case and its usage line; Task 7 adds `list`. Do not add cases for commands that are not implemented — referencing a missing method stops the whole package from compiling.

- [ ] **Step 4: Write the status command**

Create `cmd/emailcleaner/status.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"

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
```

Note: `isReAuth` is defined in Task 3 alongside `gmail.ErrTokenExpired`. For this step, add a temporary local definition so Task 2 compiles on its own, and Task 3 replaces it:

```go
// isReAuth reports whether err means the operator must re-run setup.
// Replaced by a call to gmail's sentinel in Task 3.
func isReAuth(err error) bool {
	return err != nil && strings.Contains(err.Error(), "expired or revoked")
}
```

Put that in `status.go` with `strings` imported. Task 3 Step 6 swaps it for `errors.Is(err, gmail.ErrTokenExpired)`.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/emailcleaner/ -v`
Expected: PASS, 5 tests.

- [ ] **Step 6: Commit**

```bash
git add cmd/emailcleaner/
git commit -m "feat: add CLI dispatcher with spec exit codes and status command"
```

---

### Task 3: OAuth loopback flow and token store

**Files:**
- Create: `internal/gmail/auth.go`
- Create: `internal/gmail/auth_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `var ErrTokenExpired error` — sentinel meaning "operator must re-run setup".
  - `Authorize(ctx context.Context, credentialsFile, tokenFile string, out io.Writer) (*oauth2.Token, error)`
  - `TokenSource(ctx context.Context, credentialsFile, tokenFile string) (oauth2.TokenSource, error)`
  - `CheckToken(ctx context.Context, credentialsFile, tokenFile string) error`
  - `SaveToken(path string, tok *oauth2.Token) error`
  - `LoadToken(path string) (*oauth2.Token, error)`
  - `credentialsConfig(credentialsFile string) (*oauth2.Config, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/gmail/auth_test.go`:

```go
package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestSaveTokenUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")

	if err := SaveToken(path, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("token file mode = %o, want 600", got)
	}
}

func TestLoadTokenRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	want := &oauth2.Token{AccessToken: "a", RefreshToken: "r", TokenType: "Bearer"}

	if err := SaveToken(path, want); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}
	got, err := LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if got.RefreshToken != want.RefreshToken || got.AccessToken != want.AccessToken {
		t.Errorf("LoadToken() = %+v, want %+v", got, want)
	}
}

func TestLoadTokenRejectsMissingFile(t *testing.T) {
	if _, err := LoadToken(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("LoadToken() error = nil, want an error")
	}
}

func tokenServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func expiredSource(srv *httptest.Server) oauth2.TokenSource {
	cfg := &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
	}
	return cfg.TokenSource(context.Background(), &oauth2.Token{
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(-time.Hour),
	})
}

func TestCheckTokenSourceReportsExpiredToken(t *testing.T) {
	srv := tokenServer(t, http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)

	err := checkTokenSource(expiredSource(srv))
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("checkTokenSource() = %v, want ErrTokenExpired", err)
	}
}

func TestCheckTokenSourceAcceptsFreshToken(t *testing.T) {
	srv := tokenServer(t, http.StatusOK,
		`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`)

	if err := checkTokenSource(expiredSource(srv)); err != nil {
		t.Errorf("checkTokenSource() = %v, want nil", err)
	}
}

func TestCheckTokenSourceReportsOtherFailures(t *testing.T) {
	srv := tokenServer(t, http.StatusInternalServerError, `{"error":"backend_error"}`)

	err := checkTokenSource(expiredSource(srv))
	if err == nil {
		t.Fatal("checkTokenSource() = nil, want an error")
	}
	if errors.Is(err, ErrTokenExpired) {
		t.Error("checkTokenSource() = ErrTokenExpired, want a generic error")
	}
}

func TestLoadTokenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path); err == nil {
		t.Fatal("LoadToken() error = nil, want a parse error")
	}
}

func TestCredentialsConfigReadsDesktopClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client_secret.json")
	body, _ := json.Marshal(map[string]any{
		"installed": map[string]string{
			"client_id":     "id.apps.googleusercontent.com",
			"client_secret": "secret",
			"auth_uri":      "https://accounts.google.com/o/oauth2/auth",
			"token_uri":     "https://oauth2.googleapis.com/token",
		},
	})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := credentialsConfig(path)
	if err != nil {
		t.Fatalf("credentialsConfig() error = %v", err)
	}
	if cfg.ClientID != "id.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q, want the desktop client id", cfg.ClientID)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != "https://www.googleapis.com/auth/gmail.modify" {
		t.Errorf("Scopes = %v, want exactly gmail.modify", cfg.Scopes)
	}
}

func TestCredentialsConfigRejectsMissingFile(t *testing.T) {
	if _, err := credentialsConfig(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("credentialsConfig() error = nil, want an error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/gmail/ -v`
Expected: FAIL — `undefined: SaveToken` (and the rest).

- [ ] **Step 3: Write the implementation**

Create `internal/gmail/auth.go`:

```go
// Package gmail talks to the Gmail API on behalf of one account.
package gmail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
)

// GmailModifyScope is the only scope this tool requests. It permits reading,
// label changes and Trash. It deliberately does not permit permanent deletion.
const GmailModifyScope = "https://www.googleapis.com/auth/gmail.modify"

// ErrTokenExpired means the operator must re-run `emailcleaner setup`.
var ErrTokenExpired = errors.New("gmail token expired or revoked")

func credentialsConfig(credentialsFile string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %w", credentialsFile, err)
	}
	cfg, err := google.ConfigFromJSON(b, GmailModifyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", credentialsFile, err)
	}
	return cfg, nil
}

// Authorize runs the OAuth loopback flow, prints the consent URL to out, and
// stores the resulting token at tokenFile. Google allows any loopback port for
// Desktop-type clients, so a random free port is used and no fixed port or
// hosted redirect URL is needed.
func Authorize(ctx context.Context, credentialsFile, tokenFile string, out io.Writer) (*oauth2.Token, error) {
	cfg, err := credentialsConfig(credentialsFile)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start loopback listener: %w", err)
	}
	defer ln.Close()
	cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if e := r.URL.Query().Get("error"); e != "" {
			fmt.Fprintf(w, "Authorization failed: %s. You can close this tab.", e)
			errCh <- fmt.Errorf("authorization denied: %s", e)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			fmt.Fprint(w, "Authorization failed: no code returned. You can close this tab.")
			errCh <- errors.New("callback received no authorization code")
			return
		}
		fmt.Fprint(w, "Authorization complete. You can close this tab.")
		codeCh <- code
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// AccessTypeOffline requests a refresh token; prompt=consent forces Google
	// to re-issue one, which is required on every re-authorization.
	authURL := cfg.AuthCodeURL("state",
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	fmt.Fprintf(out, "Open this URL to authorize:\n\n%s\n\nWaiting for the callback...\n", authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}
	if err := SaveToken(tokenFile, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// TokenSource returns a source that refreshes the stored token as needed.
func TokenSource(ctx context.Context, credentialsFile, tokenFile string) (oauth2.TokenSource, error) {
	cfg, err := credentialsConfig(credentialsFile)
	if err != nil {
		return nil, err
	}
	tok, err := LoadToken(tokenFile)
	if err != nil {
		return nil, err
	}
	return cfg.TokenSource(ctx, tok), nil
}

// CheckToken forces a token refresh so an expired or revoked token surfaces
// before any mailbox work begins.
func CheckToken(ctx context.Context, credentialsFile, tokenFile string) error {
	ts, err := TokenSource(ctx, credentialsFile, tokenFile)
	if err != nil {
		return err
	}
	return checkTokenSource(ts)
}

func checkTokenSource(ts oauth2.TokenSource) error {
	if _, err := ts.Token(); err != nil {
		if isInvalidGrant(err) {
			return fmt.Errorf("%w: %v", ErrTokenExpired, err)
		}
		return fmt.Errorf("refresh gmail token: %w", err)
	}
	return nil
}

// isInvalidGrant recognises the Google error meaning the refresh token is dead.
func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_grant" {
			return true
		}
		if strings.Contains(re.ErrorDescription, "invalid_grant") {
			return true
		}
	}
	return strings.Contains(err.Error(), "invalid_grant")
}

// SaveToken writes the token with owner-only permissions.
func SaveToken(path string, tok *oauth2.Token) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create token directory: %w", err)
		}
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write token %s: %w", path, err)
	}
	// WriteFile does not change the mode of an existing file.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set token permissions: %w", err)
	}
	return nil
}

// LoadToken reads a token previously written by SaveToken.
func LoadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token %s: %w", path, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("parse token %s: %w", path, err)
	}
	return &tok, nil
}
```

Add `"encoding/json"` to the import block.

Note: `gmailapi` is imported for `GmailModifyScope`-adjacent use in later tasks. If the linter complains that it is unused in this task, either remove it here and add it in Task 4, or use `gmailapi.GmailModifyScope` instead of the local constant. **Prefer removing the import in this task and adding it in Task 4** — this file must compile on its own.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/gmail/ -v`
Expected: PASS, 9 tests.

- [ ] **Step 5: Verify the token really is ignored by git**

Run: `cd /home/angeeeld/Code/go/email_cleaner && printf 'x' > token.json && git check-ignore -v token.json; rm token.json`
Expected: output naming `.gitignore`. If nothing prints, stop and fix `.gitignore` before continuing.

- [ ] **Step 6: Wire the real token check into the CLI**

In `cmd/emailcleaner/status.go`, replace the placeholder `checkToken` and the temporary `isReAuth` helper:

```go
import (
	"context"
	"flag"
	"fmt"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

// checkToken validates the stored Gmail token without touching the mailbox.
func checkToken(ctx context.Context, cfg *config.Config) error {
	return gmail.CheckToken(ctx, cfg.Gmail.CredentialsFile, cfg.Gmail.TokenFile)
}

// isReAuth reports whether err means the operator must re-run setup.
func isReAuth(err error) bool {
	return errors.Is(err, gmail.ErrTokenExpired)
}
```

Add `"errors"` to the imports and delete the `strings.Contains` version.

This changes what `isReAuth` recognises, so **the Task 2 test must change with it**. In `cmd/emailcleaner/main_test.go`, `TestStatusReportsExpiredTokenAsExitCodeThree` currently returns `errors.New("gmail token expired or revoked; run \`emailcleaner setup\`")`, which `errors.Is` will no longer match. Change its stub to return the sentinel:

```go
	a.checkToken = func(context.Context, *config.Config) error {
		return gmail.ErrTokenExpired
	}
```

and add `"emailcleaner/internal/gmail"` to that file's imports. If you skip this the test fails, and the failure is the test's fault, not the code's.

- [ ] **Step 7: Run all tests**

Run: `go test ./... -v`
Expected: PASS in all three packages.

- [ ] **Step 8: Commit**

```bash
git add internal/gmail/ cmd/emailcleaner/
git commit -m "feat: add Gmail OAuth loopback flow and token store"
```

---

### Task 4: Gmail client — list, get, profile, and message translation

**Files:**
- Create: `internal/gmail/client.go`
- Create: `internal/gmail/message.go`
- Create: `internal/gmail/client_test.go`
- Create: `internal/gmail/message_test.go`

**Interfaces:**
- Consumes: `oauth2.TokenSource` from Task 3.
- Produces:
  - `type Message struct { ID, ThreadID, From, FromDomain, Subject string; Date time.Time; LabelIDs []string; HasAttachments bool; BodyText, BodyHTML string }`
  - `func NewClient(ctx context.Context, ts oauth2.TokenSource) (*Client, error)`
  - `func newClient(ctx context.Context, opts ...option.ClientOption) (*Client, error)` — the test seam
  - `(*Client).ListMessages(ctx context.Context, query string, max int) ([]string, error)`
  - `(*Client).GetMessage(ctx context.Context, id string) (*Message, error)`
  - `(*Client).Profile(ctx context.Context) (string, error)`
  - `func toMessage(m *gmailapi.Message) (*Message, error)`
  - `func domainOf(from string) string`
  - `func decodeBody(data string) (string, error)`

- [ ] **Step 1: Write the failing translation test**

Create `internal/gmail/message_test.go`:

```go
package gmail

import (
	"encoding/base64"
	"testing"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
)

func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func header(name, value string) *gmailapi.MessagePartHeader {
	return &gmailapi.MessagePartHeader{Name: name, Value: value}
}

func TestDomainOf(t *testing.T) {
	tests := []struct {
		name string
		from string
		want string
	}{
		{"bare address", "user@example.com", "example.com"},
		{"display name", "Ana Gomez <ana@Example.COM>", "example.com"},
		{"quoted display name", "\"Gomez, Ana\" <ana@mail.example.com>", "mail.example.com"},
		{"surrounding spaces", "  ana@example.com  ", "example.com"},
		{"no at sign", "postmaster", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domainOf(tt.from); got != tt.want {
				t.Errorf("domainOf(%q) = %q, want %q", tt.from, got, tt.want)
			}
		})
	}
}

func TestToMessageReadsHeadersAndPlainBody(t *testing.T) {
	date := "Mon, 22 Sep 2026 14:03:11 -0500"
	m := &gmailapi.Message{
		Id:       "18f1a",
		ThreadId: "18f1a",
		LabelIds: []string{"INBOX", "UNREAD"},
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/alternative",
			Headers: []*gmailapi.MessagePartHeader{
				header("From", "Ana Gomez <ana@example.com>"),
				header("Subject", "Factura #4411"),
				header("Date", date),
			},
			Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: b64("Hola, adjunto la factura.")}},
				{MimeType: "text/html", Body: &gmailapi.MessagePartBody{Data: b64("<p>Hola</p>")}},
			},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}

	if got.ID != "18f1a" || got.ThreadID != "18f1a" {
		t.Errorf("ID/ThreadID = %q/%q, want 18f1a/18f1a", got.ID, got.ThreadID)
	}
	if got.From != "Ana Gomez <ana@example.com>" {
		t.Errorf("From = %q", got.From)
	}
	if got.FromDomain != "example.com" {
		t.Errorf("FromDomain = %q, want example.com", got.FromDomain)
	}
	if got.Subject != "Factura #4411" {
		t.Errorf("Subject = %q", got.Subject)
	}
	if got.BodyText != "Hola, adjunto la factura." {
		t.Errorf("BodyText = %q, want the plain part", got.BodyText)
	}
	if got.BodyHTML != "" {
		t.Errorf("BodyHTML = %q, want empty when a plain part exists", got.BodyHTML)
	}
	want, _ := time.Parse(time.RFC1123Z, date)
	if !got.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", got.Date, want)
	}
}

func TestToMessageFallsBackToHTML(t *testing.T) {
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/html",
			Headers:  []*gmailapi.MessagePartHeader{header("Subject", "Solo HTML")},
			Body:     &gmailapi.MessagePartBody{Data: b64("<p>Solo HTML</p>")},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}
	if got.BodyText != "" {
		t.Errorf("BodyText = %q, want empty", got.BodyText)
	}
	if got.BodyHTML != "<p>Solo HTML</p>" {
		t.Errorf("BodyHTML = %q", got.BodyHTML)
	}
}

func TestToMessageFlagsAttachments(t *testing.T) {
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/mixed",
			Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: b64("adjunto")}},
				{
					MimeType:   "application/pdf",
					Filename:   "factura.pdf",
					Body:       &gmailapi.MessagePartBody{AttachmentId: "a1", Size: 1234},
				},
			},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}
	if !got.HasAttachments {
		t.Error("HasAttachments = false, want true")
	}
}

func TestToMessageToleratesPaddedBase64(t *testing.T) {
	padded := base64.URLEncoding.EncodeToString([]byte("hola"))
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Body:     &gmailapi.MessagePartBody{Data: padded},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}
	if got.BodyText != "hola" {
		t.Errorf("BodyText = %q, want hola", got.BodyText)
	}
}

func TestToMessageRejectsNilPayload(t *testing.T) {
	if _, err := toMessage(&gmailapi.Message{Id: "x"}); err == nil {
		t.Fatal("toMessage() error = nil, want an error for a missing payload")
	}
}

func TestToMessageToleratesUnparseableDate(t *testing.T) {
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			Headers: []*gmailapi.MessagePartHeader{header("Date", "not a date")},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v, want no error", err)
	}
	if !got.Date.IsZero() {
		t.Errorf("Date = %v, want the zero time", got.Date)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/gmail/ -run 'TestDomainOf|TestToMessage' -v`
Expected: FAIL — `undefined: domainOf`.

- [ ] **Step 3: Write the message translation**

Create `internal/gmail/message.go`:

```go
package gmail

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
)

// Message is this project's own view of a Gmail message. It keeps the rest of
// the codebase free of Google API types.
type Message struct {
	ID             string
	ThreadID       string
	From           string
	FromDomain     string
	Subject        string
	Date           time.Time
	LabelIDs       []string
	HasAttachments bool
	BodyText       string // decoded text/plain part, when present
	BodyHTML       string // decoded text/html part, used only when there is no plain part
}

// toMessage converts a Gmail API message into the domain type. A message with
// no payload is treated as an error: silently returning an empty Message would
// classify on the subject alone and produce a confident wrong answer.
func toMessage(m *gmailapi.Message) (*Message, error) {
	if m == nil || m.Payload == nil {
		return nil, errors.New("message has no payload")
	}

	out := &Message{
		ID:       m.Id,
		ThreadID: m.ThreadId,
		LabelIDs: m.LabelIds,
	}

	for _, h := range m.Payload.Headers {
		if h == nil {
			continue
		}
		switch strings.ToLower(h.Name) {
		case "from":
			out.From = h.Value
			out.FromDomain = domainOf(h.Value)
		case "subject":
			out.Subject = h.Value
		case "date":
			if t, err := mail.ParseDate(h.Value); err == nil {
				out.Date = t
			}
			// An unparseable Date is tolerated: it is not worth failing a
			// message over a malformed header.
		}
	}

	text, html := walkParts(m.Payload, &out.HasAttachments)
	if text != "" {
		out.BodyText = text
	} else {
		out.BodyHTML = html
	}
	return out, nil
}

// walkParts returns the first text/plain and the first text/html body found in
// the MIME tree, and records whether any part is an attachment.
func walkParts(part *gmailapi.MessagePart, hasAttachments *bool) (text, html string) {
	if part == nil {
		return "", ""
	}

	if part.Filename != "" && part.Body != nil && part.Body.AttachmentId != "" {
		*hasAttachments = true
	}

	switch strings.ToLower(part.MimeType) {
	case "text/plain":
		if part.Body != nil && part.Body.Data != "" && text == "" {
			if decoded, err := decodeBody(part.Body.Data); err == nil {
				text = decoded
			}
		}
	case "text/html":
		if part.Body != nil && part.Body.Data != "" && html == "" {
			if decoded, err := decodeBody(part.Body.Data); err == nil {
				html = decoded
			}
		}
	}

	for _, child := range part.Parts {
		childText, childHTML := walkParts(child, hasAttachments)
		if text == "" && childText != "" {
			text = childText
		}
		if html == "" && childHTML != "" {
			html = childHTML
		}
	}
	return text, html
}

// decodeBody decodes Gmail's body data, which is base64url and usually padded
// inconsistently. Both encodings are tried.
func decodeBody(data string) (string, error) {
	if b, err := base64.RawURLEncoding.DecodeString(data); err == nil {
		return string(b), nil
	}
	b, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("decode body: %w", err)
	}
	return string(b), nil
}

// domainOf extracts the lowercase domain from a From header. It returns "" when
// there is no parseable address.
func domainOf(from string) string {
	value := strings.TrimSpace(from)
	if value == "" {
		return ""
	}
	if i := strings.LastIndex(value, "<"); i >= 0 {
		if j := strings.Index(value[i:], ">"); j > 1 {
			value = value[i+1 : i+j]
		}
	}
	at := strings.LastIndex(value, "@")
	if at < 0 || at == len(value)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value[at+1:]))
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/gmail/ -run 'TestDomainOf|TestToMessage' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing client test**

Create `internal/gmail/client_test.go`:

```go
package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/option"
)

// newFakeServer builds a client pointed at a handler. Paths are matched by
// suffix because google-api-go-client prepends its own service prefix.
func newFakeServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := newClient(context.Background(),
		option.WithoutAuthentication(),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestListMessagesFollowsPagination(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	pages := []map[string]any{
		{"messages": []map[string]string{{"id": "a"}, {"id": "b"}}, "nextPageToken": "t1"},
		{"messages": []map[string]string{{"id": "c"}}},
	}
	call := 0

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users/me/messages") {
			t.Errorf("path = %q, want it to end with /users/me/messages", r.URL.Path)
		}
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("q"))
		page := pages[call]
		call++
		mu.Unlock()
		writeJSON(t, w, page)
	})

	got, err := c.ListMessages(context.Background(), "in:inbox", 0)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("ListMessages() = %v, want [a b c]", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("made %d list calls, want 2", len(queries))
	}
	if queries[1] != "in:inbox" {
		t.Errorf("second page query = %q, want the query repeated", queries[1])
	}
}

func TestListMessagesStopsAtMax(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"messages":      []map[string]string{{"id": "a"}, {"id": "b"}, {"id": "c"}},
			"nextPageToken": "t1",
		})
	})

	got, err := c.ListMessages(context.Background(), "in:inbox", 2)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListMessages() returned %d ids, want 2", len(got))
	}
}

func TestListMessagesHandlesEmptyInbox(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})

	got, err := c.ListMessages(context.Background(), "in:inbox", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMessages() = %v, want empty", got)
	}
}

func TestListMessagesSurfacesAPIFailure(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, map[string]any{"error": map[string]any{"code": 401, "message": "bad token"}})
	})

	if _, err := c.ListMessages(context.Background(), "in:inbox", 1); err == nil {
		t.Fatal("ListMessages() error = nil, want an API error")
	}
}

func TestGetMessageRequestsFullFormat(t *testing.T) {
	var gotFormat string
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotFormat = r.URL.Query().Get("format")
		writeJSON(t, w, map[string]any{
			"id": "a",
			"payload": map[string]any{
				"mimeType": "text/plain",
				"headers":  []map[string]string{{"name": "Subject", "value": "Hola"}},
				"body":     map[string]string{"data": b64("cuerpo")},
			},
		})
	})

	got, err := c.GetMessage(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if gotFormat != "full" {
		t.Errorf("format = %q, want full", gotFormat)
	}
	if got.Subject != "Hola" || got.BodyText != "cuerpo" {
		t.Errorf("GetMessage() = %+v, want subject Hola and body cuerpo", got)
	}
}

func TestProfileReturnsEmailAddress(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users/me/profile") {
			t.Errorf("path = %q, want it to end with /users/me/profile", r.URL.Path)
		}
		writeJSON(t, w, map[string]string{"emailAddress": "me@example.com"})
	})

	got, err := c.Profile(context.Background())
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if got != "me@example.com" {
		t.Errorf("Profile() = %q, want me@example.com", got)
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/gmail/ -run 'TestList|TestGet|TestProfile' -v`
Expected: FAIL — `undefined: newClient`.

- [ ] **Step 7: Write the client**

Create `internal/gmail/client.go`:

```go
package gmail

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// maxPageSize is the Gmail API maximum for messages.list.
const maxPageSize = 500

// Client is a thin wrapper over the Gmail API that returns domain types.
type Client struct {
	users *gmailapi.UsersService
}

// NewClient builds a client authenticated with the given token source.
func NewClient(ctx context.Context, ts oauth2.TokenSource) (*Client, error) {
	return newClient(ctx, option.WithTokenSource(ts))
}

// newClient is the test seam: tests pass option.WithoutAuthentication() and
// option.WithEndpoint(server.URL) to route traffic at a fake.
func newClient(ctx context.Context, opts ...option.ClientOption) (*Client, error) {
	svc, err := gmailapi.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}
	return &Client{users: svc.Users}, nil
}

// ListMessages returns message IDs matching query, newest first. A max of zero
// or less means no limit.
func (c *Client) ListMessages(ctx context.Context, query string, max int) ([]string, error) {
	var ids []string
	pageToken := ""

	for {
		pageSize := maxPageSize
		if max > 0 {
			remaining := max - len(ids)
			if remaining <= 0 {
				break
			}
			if remaining < pageSize {
				pageSize = remaining
			}
		}

		call := c.users.Messages.List("me").Q(query).MaxResults(int64(pageSize))
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list messages: %w", err)
		}

		for _, m := range resp.Messages {
			if m == nil || m.Id == "" {
				continue
			}
			ids = append(ids, m.Id)
			// Cap here rather than trusting MaxResults: the API is free to
			// return more than asked, and the caller's limit is a contract.
			if max > 0 && len(ids) == max {
				return ids, nil
			}
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return ids, nil
}

// GetMessage fetches one message with format=full, which returns both headers
// and body for the same 20 quota units that format=metadata costs.
func (c *Client) GetMessage(ctx context.Context, id string) (*Message, error) {
	m, err := c.users.Messages.Get("me", id).Format("full").Do()
	if err != nil {
		return nil, fmt.Errorf("get message %s: %w", id, err)
	}
	return toMessage(m)
}

// Profile returns the authenticated account's email address.
func (c *Client) Profile(ctx context.Context) (string, error) {
	p, err := c.users.GetProfile("me").Do()
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	return p.EmailAddress, nil
}
```

- [ ] **Step 8: Run it to verify it passes**

Run: `go test ./internal/gmail/ -v`
Expected: PASS. If `TestListMessagesFollowsPagination` reports a path mismatch, adjust the suffix in the handler — the service prefix differs between library versions and the assertion is intentionally anchored to the suffix.

- [ ] **Step 9: Commit**

```bash
git add internal/gmail/
git commit -m "feat: add Gmail client with paginated listing and message translation"
```

---

### Task 5: Idempotent label creation

**Files:**
- Create: `internal/gmail/labels.go`
- Create: `internal/gmail/labels_test.go`

**Interfaces:**
- Consumes: `*Client` from Task 4.
- Produces: `(*Client).EnsureLabel(ctx context.Context, name string) (string, error)` returning the Gmail label ID.

- [ ] **Step 1: Write the failing test**

Create `internal/gmail/labels_test.go`:

```go
package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestEnsureLabelReturnsExistingLabelWithoutCreating(t *testing.T) {
	var mu sync.Mutex
	created := 0

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodGet:
			writeJSON(t, w, map[string]any{
				"labels": []map[string]string{
					{"id": "Label_1", "name": "cleaner/people"},
					{"id": "Label_2", "name": "INBOX"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodPost:
			mu.Lock()
			created++
			mu.Unlock()
			writeJSON(t, w, map[string]string{"id": "new", "name": "cleaner/people"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	got, err := c.EnsureLabel(context.Background(), "cleaner/people")
	if err != nil {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
	if got != "Label_1" {
		t.Errorf("EnsureLabel() = %q, want Label_1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 0 {
		t.Errorf("created %d labels, want 0 for an existing label", created)
	}
}

func TestEnsureLabelCreatesMissingLabelWithVisibility(t *testing.T) {
	var sentName string
	var sentVisibility string

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX"}}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create body: %v", err)
		}
		sentName, _ = body["name"].(string)
		sentVisibility, _ = body["labelListVisibility"].(string)
		writeJSON(t, w, map[string]string{"id": "Label_9", "name": sentName})
	})

	got, err := c.EnsureLabel(context.Background(), "cleaner/security")
	if err != nil {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
	if got != "Label_9" {
		t.Errorf("EnsureLabel() = %q, want Label_9", got)
	}
	if sentName != "cleaner/security" {
		t.Errorf("created name = %q, want cleaner/security", sentName)
	}
	if sentVisibility != "labelShow" {
		t.Errorf("labelListVisibility = %q, want labelShow so the label is visible", sentVisibility)
	}
}

func TestEnsureLabelSurfacesAPIFailure(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(t, w, map[string]any{"error": map[string]any{"code": 403, "message": "insufficient scope"}})
	})

	if _, err := c.EnsureLabel(context.Background(), "cleaner/people"); err == nil {
		t.Fatal("EnsureLabel() error = nil, want an API error")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/gmail/ -run TestEnsureLabel -v`
Expected: FAIL — `c.EnsureLabel undefined`.

- [ ] **Step 3: Write the implementation**

Create `internal/gmail/labels.go`:

```go
package gmail

import (
	"context"
	"fmt"

	gmailapi "google.golang.org/api/gmail/v1"
)

// EnsureLabel returns the ID of the named label, creating it when absent.
// It is idempotent, so `setup` can be re-run any number of times.
//
// Gmail nests labels by "/", so creating "cleaner/people" also creates the
// "cleaner" parent.
func (c *Client) EnsureLabel(ctx context.Context, name string) (string, error) {
	list, err := c.users.Labels.List("me").Do()
	if err != nil {
		return "", fmt.Errorf("list labels: %w", err)
	}
	for _, l := range list.Labels {
		if l != nil && l.Name == name {
			return l.Id, nil
		}
	}

	created, err := c.users.Labels.Create("me", &gmailapi.Label{
		Name:                  name,
		LabelListVisibility:   "labelShow",
		MessageListVisibility: "show",
	}).Do()
	if err != nil {
		return "", fmt.Errorf("create label %s: %w", name, err)
	}
	return created.Id, nil
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/gmail/ -run TestEnsureLabel -v`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/gmail/
git commit -m "feat: add idempotent Gmail label creation"
```

---

### Task 6: The `setup` command, verified against real Gmail

**Files:**
- Create: `cmd/emailcleaner/setup.go`
- Create: `cmd/emailcleaner/setup_test.go`
- Modify: `cmd/emailcleaner/main.go` (app struct fields, `main()` wiring, the `setup` case in the dispatcher and its usage line)
- Modify: `cmd/emailcleaner/main_test.go` (helpers)

**Interfaces:**
- Consumes: `config.Load`; `gmail.Authorize`, `gmail.CheckToken`, `gmail.TokenSource`, `gmail.NewClient`, `(*gmail.Client).EnsureLabel`, `(*gmail.Client).Profile`.
- Produces:
  - `type gmailAccess interface { EnsureLabel(ctx context.Context, name string) (string, error); Profile(ctx context.Context) (string, error) }`
  - `func newGmailAccess(ctx context.Context, cfg *config.Config) (gmailAccess, error)`
  - `(*app).setup(args []string) int`
  - `app` fields: `authorize func(context.Context, *config.Config) error`, `openGmail func(context.Context, *config.Config) (gmailAccess, error)`

**One seam, not four.** `setup` opens the Gmail client once and reuses it for the profile read and all six label creations. Opening a client per label would re-read the credentials file and the token file seven times.

- [ ] **Step 1: Write the failing test**

Create `cmd/emailcleaner/setup_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"emailcleaner/internal/config"
)

type fakeGmail struct {
	account  string
	ensured  []string
	labelErr error
}

func (f *fakeGmail) EnsureLabel(_ context.Context, name string) (string, error) {
	if f.labelErr != nil {
		return "", f.labelErr
	}
	f.ensured = append(f.ensured, name)
	return "Label_" + name, nil
}

func (f *fakeGmail) Profile(context.Context) (string, error) { return f.account, nil }

func newSetupApp(t *testing.T, fake *fakeGmail) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	return &app{
		configPath: writeConfig(t),
		stdout:     &out,
		stderr:     &errOut,
		authorize:  func(context.Context, *config.Config) error { return nil },
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail:  func(context.Context, *config.Config) (gmailAccess, error) { return fake, nil },
	}, &out, &errOut
}

func TestSetupCreatesEveryConfiguredLabel(t *testing.T) {
	fake := &fakeGmail{account: "me@example.com"}
	a, out, errOut := newSetupApp(t, fake)

	if got := a.run([]string{"setup"}); got != exitOK {
		t.Fatalf("setup exit = %d, want %d (stderr: %s)", got, exitOK, errOut.String())
	}

	want := []string{
		"cleaner/accounts", "cleaner/action", "cleaner/opportunities",
		"cleaner/people", "cleaner/security", "cleaner/unclassified",
	}
	if len(fake.ensured) != len(want) {
		t.Fatalf("ensured %v, want %v", fake.ensured, want)
	}
	for i := range want {
		if fake.ensured[i] != want[i] {
			t.Errorf("ensured[%d] = %q, want %q", i, fake.ensured[i], want[i])
		}
	}
	if !strings.Contains(out.String(), "me@example.com") {
		t.Errorf("stdout = %q, want the authorized account", out.String())
	}
}

func TestSetupStopsWhenAuthorizationFails(t *testing.T) {
	fake := &fakeGmail{account: "me@example.com"}
	a, _, errOut := newSetupApp(t, fake)
	a.authorize = func(context.Context, *config.Config) error { return errors.New("user denied access") }

	if got := a.run([]string{"setup"}); got != exitGeneric {
		t.Errorf("setup exit = %d, want %d", got, exitGeneric)
	}
	if len(fake.ensured) != 0 {
		t.Errorf("ensured %v, want no label work after a failed authorization", fake.ensured)
	}
	if !strings.Contains(errOut.String(), "user denied access") {
		t.Errorf("stderr = %q, want the authorization error", errOut.String())
	}
}

func TestSetupStopsWhenLabelCreationFails(t *testing.T) {
	fake := &fakeGmail{labelErr: errors.New("403 insufficient scope")}
	a, _, errOut := newSetupApp(t, fake)

	if got := a.run([]string{"setup"}); got != exitGeneric {
		t.Errorf("setup exit = %d, want %d", got, exitGeneric)
	}
	if !strings.Contains(errOut.String(), "403 insufficient scope") {
		t.Errorf("stderr = %q, want the label error", errOut.String())
	}
}

func TestSetupReportsMissingConfigAsUsageError(t *testing.T) {
	a, _, errOut := newSetupApp(t, &fakeGmail{})
	a.configPath = t.TempDir() + "/absent.yaml"

	if got := a.run([]string{"setup"}); got != exitUsage {
		t.Errorf("setup exit = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(errOut.String(), "config.example.yaml") {
		t.Errorf("stderr = %q, want a hint pointing at config.example.yaml", errOut.String())
	}
}
```

`writeConfig` already exists in `cmd/emailcleaner/main_test.go` from Task 2 — do not add a second copy. Only `newTestApp` changes here, to set the two new injected fields:

```go
func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("gmail:\n  token_file: token.json\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
```

And update `newTestApp` in `main_test.go` so the existing tests still compile:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/emailcleaner/ -run TestSetup -v`
Expected: FAIL — `a.setup undefined`.

- [ ] **Step 3: Extend the app struct**

In `cmd/emailcleaner/main.go`, replace the `app` struct and `main()`:

```go
// gmailAccess is everything the CLI needs from Gmail in this milestone.
type gmailAccess interface {
	EnsureLabel(ctx context.Context, name string) (string, error)
	Profile(ctx context.Context) (string, error)
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
		authorize:  authorize,
		checkToken: checkToken,
		openGmail:  newGmailAccess,
	}
	os.Exit(a.run(os.Args[1:]))
}
```

Then add the command to the dispatcher in the same file: the `setup` case in `run`, and its line in the `usage` text.

```go
	case "setup":
		return a.setup(rest)
```

```go
  setup    Authorize with Gmail and create any missing labels. Idempotent.
```

- [ ] **Step 4: Write the setup command**

Create `cmd/emailcleaner/setup.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

// authorize runs the interactive OAuth flow, sending the consent URL to stdout.
func authorize(ctx context.Context, cfg *config.Config) error {
	_, err := gmail.Authorize(ctx, cfg.Gmail.CredentialsFile, cfg.Gmail.TokenFile, os.Stdout)
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
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/emailcleaner/ -run TestSetup -v`
Expected: PASS, 4 tests.

- [ ] **Step 6: Run the whole suite**

Run: `go test ./... 2>&1 | tail -20`
Expected: all packages `ok`.

- [ ] **Step 7: Verify against real Gmail**

Prerequisite: the Google Cloud Console steps at the top of this plan are complete and `client_secret.json` is in the project root.

```bash
cp config.example.yaml config.yaml
go run ./cmd/emailcleaner setup
```

Expected: a URL is printed. Open it, authorize with your own account, and the browser reports success. The command then prints `Authorized as <your address> with 6 labels ready.` followed by six `label cleaner/... id=...` lines.

Verify in the Gmail web UI under **Labels** that six nested labels now exist.

Run it a second time: `go run ./cmd/emailcleaner setup`
Expected: it authorizes again and prints the **same** label IDs — nothing is duplicated. This is the idempotency requirement, verified for real.

- [ ] **Step 8: Commit**

```bash
git add cmd/emailcleaner/
git commit -m "feat: add setup command that authorizes and creates labels"
```

---

### Task 7: The `list` command

**Files:**
- Create: `cmd/emailcleaner/list.go`
- Create: `cmd/emailcleaner/list_test.go`
- Modify: `cmd/emailcleaner/main.go` (`gmailAccess` gains two read methods, plus the `list` case in the dispatcher and its usage line)
- Modify: `cmd/emailcleaner/setup_test.go` (`fakeGmail` gains the two read methods)

**Interfaces:**
- Consumes: `gmail.Message`, `(*gmail.Client).ListMessages`, `(*gmail.Client).GetMessage`, `config.Labels`.
- Produces: `func unprocessedQuery(labels map[string]string) string`; `func formatMessage(m *gmail.Message) string`; `func truncate(s string, max int) string`; `(*app).list(args []string) int`.

**Deliberate spec addition:** the approved spec's CLI table lists `setup`, `status`, `run` and `rollback`. This task adds a fifth, read-only command, `list`. It exists because Milestone 1's entire deliverable is "prove we can read the mailbox", and `run --dry-run` cannot demonstrate that yet — there is no policy to dry-run. It stays useful afterwards as a debugging command, so this is a permanent addition and the spec's CLI table should be updated to match.

- [ ] **Step 1: Write the failing pure-function test**

Create `cmd/emailcleaner/list_test.go`:

```go
package main

import (
	"strings"
	"testing"
	"time"

	"emailcleaner/internal/gmail"
)

func TestUnprocessedQueryExcludesEveryConfiguredLabel(t *testing.T) {
	got := unprocessedQuery(map[string]string{
		"people":   "cleaner/people",
		"security": "cleaner/security",
	})

	if !strings.HasPrefix(got, "in:inbox ") {
		t.Errorf("query = %q, want it to start with in:inbox", got)
	}
	for _, want := range []string{"-label:cleaner/people", "-label:cleaner/security"} {
		if !strings.Contains(got, want) {
			t.Errorf("query = %q, want it to contain %q", got, want)
		}
	}
}

func TestUnprocessedQueryIsDeterministic(t *testing.T) {
	labels := map[string]string{
		"opportunities": "cleaner/opportunities",
		"accounts":      "cleaner/accounts",
		"action":        "cleaner/action",
	}

	first := unprocessedQuery(labels)
	for i := 0; i < 20; i++ {
		if got := unprocessedQuery(labels); got != first {
			t.Fatalf("query changed between calls:\n%q\n%q", first, got)
		}
	}
}

func TestUnprocessedQueryQuotesLabelsContainingSpaces(t *testing.T) {
	got := unprocessedQuery(map[string]string{"x": "my label"})

	if !strings.Contains(got, `-label:"my label"`) {
		t.Errorf("query = %q, want the label quoted", got)
	}
}

func TestFormatMessageUsesDomainDateAndSubject(t *testing.T) {
	m := &gmail.Message{
		FromDomain: "example.com",
		Subject:    "Factura #4411",
		Date:       time.Date(2026, 9, 22, 14, 3, 11, 0, time.UTC),
	}

	got := formatMessage(m)
	for _, want := range []string{"2026-09-22", "example.com", "Factura #4411"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatMessage() = %q, want it to contain %q", got, want)
		}
	}
}

func TestFormatMessageHandlesZeroDate(t *testing.T) {
	got := formatMessage(&gmail.Message{Subject: "sin fecha"})

	if strings.Contains(got, "0001-01-01") {
		t.Errorf("formatMessage() = %q, want a placeholder rather than the zero time", got)
	}
}

func TestTruncateCountsRunesNotBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than max", "hola", 10, "hola"},
		{"exactly max", "hola", 4, "hola"},
		{"accents are not split", "facturación electrónica", 10, "facturaci…"},
		{"trimmed first", "  hola  ", 10, "hola"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.max); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/emailcleaner/ -run 'TestUnprocessedQuery|TestFormatMessage|TestTruncate' -v`
Expected: FAIL — `undefined: unprocessedQuery`.

- [ ] **Step 3: Write the pure functions and the command**

Create `cmd/emailcleaner/list.go`:

```go
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
	limit := fs.Int("limit", 20, "maximum number of messages to examine")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *limit < 0 {
		fmt.Fprintf(a.stderr, "--limit must not be negative\n")
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
```

- [ ] **Step 4: Extend the interface and its fake**

In `cmd/emailcleaner/main.go`, extend `gmailAccess` and add the gmail import:

```go
type gmailAccess interface {
	EnsureLabel(ctx context.Context, name string) (string, error)
	Profile(ctx context.Context) (string, error)
	ListMessages(ctx context.Context, query string, max int) ([]string, error)
	GetMessage(ctx context.Context, id string) (*gmail.Message, error)
}
```

In `cmd/emailcleaner/setup_test.go`, add the two new fields and two methods to `fakeGmail`:

```go
type fakeGmail struct {
	account  string
	ensured  []string
	labelErr error
	ids      []string
	messages map[string]*gmail.Message
}

func (f *fakeGmail) ListMessages(context.Context, string, int) ([]string, error) {
	return f.ids, nil
}

func (f *fakeGmail) GetMessage(_ context.Context, id string) (*gmail.Message, error) {
	if m, ok := f.messages[id]; ok {
		return m, nil
	}
	return nil, errors.New("no such message: " + id)
}
```

Add `"emailcleaner/internal/gmail"` to that file's imports.

Then add the command to the dispatcher in the same file: the `list` case in `run`, and its line in the `usage` text.

```go
	case "list":
		return a.list(rest)
```

```go
  list     Print the headers of unprocessed inbox messages.
```

- [ ] **Step 5: Add the listing tests**

Append to `cmd/emailcleaner/list_test.go` (adding `"errors"` to its imports is not needed; `gmail` already is):

```go
func TestListPrintsEachMatchingMessage(t *testing.T) {
	fake := &fakeGmail{
		account: "me@example.com",
		ids:     []string{"a", "b"},
		messages: map[string]*gmail.Message{
			"a": {FromDomain: "ana.example.com", Subject: "Factura #4411"},
			"b": {FromDomain: "banco.example.com", Subject: "Alerta de acceso"},
		},
	}
	a, out, errOut := newSetupApp(t, fake)

	if got := a.run([]string{"list", "--limit", "10"}); got != exitOK {
		t.Fatalf("list exit = %d, want %d (stderr: %s)", got, exitOK, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "Factura #4411") || !strings.Contains(text, "Alerta de acceso") {
		t.Errorf("stdout = %q, want both subjects", text)
	}
	if !strings.Contains(text, "-label:cleaner/people") {
		t.Errorf("stdout = %q, want the query that was used", text)
	}
}

func TestListSkipsUnreadableMessageAndContinues(t *testing.T) {
	fake := &fakeGmail{
		account:  "me@example.com",
		ids:      []string{"missing", "good"},
		messages: map[string]*gmail.Message{"good": {Subject: "Legible"}},
	}
	a, out, errOut := newSetupApp(t, fake)

	if got := a.run([]string{"list"}); got != exitOK {
		t.Fatalf("list exit = %d, want %d", got, exitOK)
	}
	if !strings.Contains(out.String(), "Legible") {
		t.Errorf("stdout = %q, want the readable message", out.String())
	}
	if !strings.Contains(errOut.String(), "missing") {
		t.Errorf("stderr = %q, want the skipped id reported", errOut.String())
	}
}

func TestListRejectsNegativeLimit(t *testing.T) {
	a, _, _ := newSetupApp(t, &fakeGmail{})

	if got := a.run([]string{"list", "--limit", "-1"}); got != exitUsage {
		t.Errorf("list exit = %d, want %d", got, exitUsage)
	}
}

func TestListReportsExpiredTokenAsExitCodeThree(t *testing.T) {
	fake := &fakeGmail{account: "me@example.com"}
	a, _, errOut := newSetupApp(t, fake)
	a.checkToken = func(context.Context, *config.Config) error {
		return gmail.ErrTokenExpired
	}

	if got := a.run([]string{"list"}); got != exitReAuth {
		t.Errorf("list exit = %d, want %d", got, exitReAuth)
	}
	if !strings.Contains(errOut.String(), "emailcleaner setup") {
		t.Errorf("stderr = %q, want a re-authentication instruction", errOut.String())
	}
}
```

`TestListReportsExpiredTokenAsExitCodeThree` needs `"emailcleaner/internal/config"` in the imports of `list_test.go`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cmd/emailcleaner/ -v`
Expected: PASS, no tests skipped.

- [ ] **Step 7: Verify against real Gmail**

```bash
go run ./cmd/emailcleaner list --limit 10
```

Expected: the query is printed, then ten lines of `DATE  DOMAIN  SUBJECT` from the real inbox. This is the milestone deliverable — proof the tool reads the mailbox.

```bash
go run ./cmd/emailcleaner status; echo "exit=$?"
```

Expected: `token: ok`, the credentials path, `labels: 6 configured`, `exit=0`.

- [ ] **Step 8: Commit**

```bash
git add cmd/emailcleaner/
git commit -m "feat: add list command that prints unprocessed inbox headers"
```

---

## Self-Review

Run after the whole plan was written, against the spec.

**1. Spec coverage.**

| Spec section | Covered by |
|---|---|
| Package layout: `cmd/`, `internal/config`, `internal/gmail` | Tasks 1, 2, 4, 5, 6, 7 |
| One `messages.get` with `format=full` per message | Task 4, `TestGetMessageRequestsFullFormat` |
| Scope is `gmail.modify` only | Task 3, `GmailModifyScope`, asserted in `TestCredentialsConfigReadsDesktopClient` |
| No permanent deletion | Enforced by construction: `messages.delete` and `batchDelete` appear nowhere in this plan |
| Token mode `0600`, gitignored | Task 3, `TestSaveTokenUsesOwnerOnlyPermissions`; Task 3 Step 5; Prerequisite Step 6 |
| Exit codes 0/1/2/3 | Task 2, and exit 3 exercised in Tasks 2 and 7 |
| Labels live in config, namespaced `cleaner/` | Task 1 defaults, Task 5 creation, Task 6 iteration, Task 7 query |
| Idempotent `setup` | Task 5, verified for real in Task 6 Step 7 |
| `in:inbox -label:cleaner/*` query | Task 7, `unprocessedQuery` |
| Config file shape | Task 1, and `config.example.yaml` matches the spec verbatim |
| 7-day token expiry handling | Task 3 (`ErrTokenExpired`), Task 2 and Task 7 (exit 3) |

**Out of scope for this milestone**, each owned by a later plan: `internal/extract`, `internal/laya`, `internal/policy`, `internal/act`, `internal/audit` and rollback, `run --dry-run`, the circuit breaker and pause, and the `run` and `rollback` commands.

**2. Placeholder scan.** No TBD, TODO, "add error handling", or "similar to Task N". One deliberate temporary state exists, with an explicit removal instruction: the `isReAuth` helper written in Task 2 is replaced in Task 3 Step 6. Every code step carries real code.

**3. Type consistency.** `gmail.Message` field names used by Task 7's `formatMessage` match the struct defined in Task 4. `gmailAccess`'s four methods match `*gmail.Client`'s real methods, which is what makes the interface satisfiable by the production type. `config.Load`, `config.Default`, `config.Duration.Std()` keep one spelling across Tasks 1, 2, 6 and 7. `checkToken` and `isReAuth` keep one spelling across `status.go` and `setup.go`. `unprocessedQuery`, `formatMessage` and `truncate` are each defined exactly once. `usage` takes an `io.Writer` and all three call sites pass `a.stderr`, so `help` prints usage to stderr and returns exit 0 — the **exit code**, not the stream, is what distinguishes help from a usage error.

**4. Test expectations checked against implementations, not just asserted.** Every expected value in this plan was compared against the code that must produce it. Three mismatches were found and fixed while writing: `usage` wrote to stdout while the no-arguments test asserted stderr; `ListMessages` appended every returned ID without honouring `max`, which would have failed `TestListMessagesStopsAtMax`; and Task 3 Step 6 changes what `isReAuth` matches, which invalidated the Task 2 status test unless it also switches to the `gmail.ErrTokenExpired` sentinel.

## Execution Handoff

Two options for executing this plan:

1. **Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks, fast iteration. Requires `superpowers:subagent-driven-development`.
2. **Inline Execution** — tasks executed in this session with checkpoints for review. Requires `superpowers:executing-plans`.

**Before starting:** complete the manual Google Cloud Console prerequisites at the top of this plan. Task 6's real-Gmail verification cannot pass without them. Tasks 1–5 do not need them and can run in the meantime.
