package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

type fakeGmail struct {
	account  string
	ensured  []string
	labelErr error
	ids      []string
	messages map[string]*gmail.Message
}

func (f *fakeGmail) EnsureLabel(_ context.Context, name string) (string, error) {
	if f.labelErr != nil {
		return "", f.labelErr
	}
	f.ensured = append(f.ensured, name)
	return "Label_" + name, nil
}

func (f *fakeGmail) Profile(context.Context) (string, error) { return f.account, nil }

func (f *fakeGmail) ListMessages(context.Context, string, int) ([]string, error) {
	return f.ids, nil
}

func (f *fakeGmail) GetMessage(_ context.Context, id string) (*gmail.Message, error) {
	if m, ok := f.messages[id]; ok {
		return m, nil
	}
	return nil, errors.New("no such message: " + id)
}

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
		"cleaner/accounts", "cleaner/action", "cleaner/banking", "cleaner/opportunities",
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

// TestAuthorizeSendsConsentURLToInjectedStdout proves the consent URL flows to
// the app's injected writer rather than the process-global os.Stdout. An
// already-cancelled context keeps gmail.Authorize from blocking on a callback:
// the URL is printed before it selects on ctx.Done().
func TestAuthorizeSendsConsentURLToInjectedStdout(t *testing.T) {
	const creds = `{"installed":{"client_id":"id","client_secret":"secret",` +
		`"auth_uri":"https://accounts.google.com/o/oauth2/auth",` +
		`"token_uri":"https://oauth2.googleapis.com/token",` +
		`"redirect_uris":["http://localhost"]}}`
	dir := t.TempDir()
	credsPath := filepath.Join(dir, "client_secret.json")
	if err := os.WriteFile(credsPath, []byte(creds), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := fmt.Sprintf("gmail:\n  credentials_file: %s\n  token_file: %s\n",
		credsPath, filepath.Join(dir, "token.json"))
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	var out bytes.Buffer
	a := &app{stdout: &out, stderr: &bytes.Buffer{}}
	// Mirror main()'s wiring: the injected seam points at the real method.
	a.authorize = a.interactiveAuthorize

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.authorize(ctx, cfg); err == nil {
		t.Error("authorize = nil error, want a cancellation error")
	}
	if !strings.Contains(out.String(), "Open this URL") {
		t.Errorf("stdout = %q, want the consent URL from the injected writer", out.String())
	}
}
