package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AngeeelD/systemone-email-cleaner/internal/act"
	"github.com/AngeeelD/systemone-email-cleaner/internal/audit"
	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/gmail"
)

// fakeRollbackGmail extends fakeRunGmail with Untrash tracking for rollback.
type fakeRollbackGmail struct {
	messages  map[string]*gmail.Message
	modifies  []act.ModifyCall
	untrashes []string
}

func (f *fakeRollbackGmail) EnsureLabel(_ context.Context, name string) (string, error) {
	return "Label_" + name, nil
}
func (f *fakeRollbackGmail) Profile(_ context.Context) (string, error) { return "me@example.com", nil }
func (f *fakeRollbackGmail) ListMessages(_ context.Context, _ string, _ int) ([]string, error) {
	return nil, nil
}
func (f *fakeRollbackGmail) GetMessage(_ context.Context, id string) (*gmail.Message, error) {
	if m, ok := f.messages[id]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("no such message: %s", id)
}
func (f *fakeRollbackGmail) Modify(_ context.Context, id string, add, remove []string) error {
	f.modifies = append(f.modifies, act.ModifyCall{ID: id, Add: append([]string(nil), add...), Remove: append([]string(nil), remove...)})
	return nil
}
func (f *fakeRollbackGmail) BatchModify(_ context.Context, ids []string, add, remove []string) error {
	return nil
}
func (f *fakeRollbackGmail) Untrash(_ context.Context, id string) error {
	f.untrashes = append(f.untrashes, id)
	return nil
}

func writeRollbackConfig(t *testing.T, auditDir string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf("gmail:\n  token_file: token.json\n  credentials_file: client_secret.json\naudit:\n  dir: %s\n", auditDir)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestRollback_InvertsLabels(t *testing.T) {
	auditDir := t.TempDir()
	runID := "20260923T132105Z"
	rec := audit.Record{
		RunID:        runID,
		MessageID:    "msg1",
		Subject:      "Hello",
		Outcome:      "applied",
		Reason:       "is_person conf=0.90",
		LabelsBefore: []string{"INBOX"},
		LabelsAfter:  []string{"INBOX", "cleaner/people"},
		Action:       &audit.Action{Add: []string{"cleaner/people"}},
	}
	if err := audit.Append(auditDir, rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	fg := &fakeRollbackGmail{
		messages: map[string]*gmail.Message{
			"msg1": {ID: "msg1", LabelIDs: []string{"INBOX", "cleaner/people"}},
		},
	}
	fakeApplier := &act.Fake{}
	var out, errOut bytes.Buffer
	cfgPath := writeRollbackConfig(t, auditDir)
	a := &app{
		configPath: cfgPath,
		stdout:     &out,
		stderr:     &errOut,
		stdin:      strings.NewReader("\n"),
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail:  func(context.Context, *config.Config) (gmailAccess, error) { return fg, nil },
		newApplier: func(extendedGmailAccess) applier { return fakeApplier },
	}
	if code := a.run([]string{"rollback", "--run-id", runID}); code != exitOK {
		t.Fatalf("rollback exit = %d, want 0 (stderr %s)", code, errOut.String())
	}
	if len(fakeApplier.Modifies) != 1 {
		t.Fatalf("modifies = %d, want 1", len(fakeApplier.Modifies))
	}
	// Inverted: should remove cleaner/people
	mc := fakeApplier.Modifies[0]
	if len(mc.Remove) != 1 || mc.Remove[0] != "cleaner/people" {
		t.Errorf("invert remove = %v, want [cleaner/people]", mc.Remove)
	}
	if !strings.Contains(out.String(), "REVERT") {
		t.Errorf("stdout = %q, want REVERT", out.String())
	}
}

func TestRollback_SkipsWhenLabelMissing(t *testing.T) {
	auditDir := t.TempDir()
	runID := "20260923T132106Z"
	rec := audit.Record{
		RunID:        runID,
		MessageID:    "msg2",
		Subject:      "Subject2",
		Outcome:      "applied",
		LabelsBefore: []string{"INBOX"},
		LabelsAfter:  []string{"INBOX", "cleaner/people"},
		Action:       &audit.Action{Add: []string{"cleaner/people"}},
	}
	if err := audit.Append(auditDir, rec); err != nil {
		t.Fatal(err)
	}
	// Message no longer has cleaner/people (user removed)
	fg := &fakeRollbackGmail{
		messages: map[string]*gmail.Message{
			"msg2": {ID: "msg2", LabelIDs: []string{"INBOX"}},
		},
	}
	fakeApplier := &act.Fake{}
	var out, errOut bytes.Buffer
	cfgPath := writeRollbackConfig(t, auditDir)
	a := &app{
		configPath: cfgPath,
		stdout:     &out,
		stderr:     &errOut,
		stdin:      strings.NewReader("\n"),
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail:  func(context.Context, *config.Config) (gmailAccess, error) { return fg, nil },
		newApplier: func(extendedGmailAccess) applier { return fakeApplier },
	}
	if code := a.run([]string{"rollback", "--run-id", runID}); code != exitOK {
		t.Fatalf("rollback exit = %d", code)
	}
	if len(fakeApplier.Modifies) != 0 {
		t.Errorf("should skip, but modifies = %v", fakeApplier.Modifies)
	}
	if !strings.Contains(out.String(), "SKIP") {
		t.Errorf("stdout = %q, want SKIP", out.String())
	}
}

func TestRollback_UntrashForTrash(t *testing.T) {
	auditDir := t.TempDir()
	runID := "20260923T132107Z"
	rec := audit.Record{
		RunID:        runID,
		MessageID:    "msg3",
		Subject:      "Spam",
		Outcome:      "applied",
		LabelsBefore: []string{"INBOX"},
		LabelsAfter:  []string{"TRASH"},
		Action:       &audit.Action{Add: []string{"TRASH"}, Remove: []string{"INBOX"}},
	}
	if err := audit.Append(auditDir, rec); err != nil {
		t.Fatal(err)
	}
	fg := &fakeRollbackGmail{
		messages: map[string]*gmail.Message{
			"msg3": {ID: "msg3", LabelIDs: []string{"TRASH"}},
		},
	}
	fakeApplier := &act.Fake{}
	var out, errOut bytes.Buffer
	cfgPath := writeRollbackConfig(t, auditDir)
	a := &app{
		configPath: cfgPath,
		stdout:     &out,
		stderr:     &errOut,
		stdin:      strings.NewReader("\n"),
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail:  func(context.Context, *config.Config) (gmailAccess, error) { return fg, nil },
		newApplier: func(extendedGmailAccess) applier { return fakeApplier },
	}
	if code := a.run([]string{"rollback", "--run-id", runID}); code != exitOK {
		t.Fatalf("rollback exit = %d", code)
	}
	if len(fakeApplier.Untrashes) != 1 || fakeApplier.Untrashes[0] != "msg3" {
		t.Errorf("untrashes = %v, want [msg3]", fakeApplier.Untrashes)
	}
	if !strings.Contains(out.String(), "UNTRASH") {
		t.Errorf("stdout = %q, want UNTRASH", out.String())
	}
}

func TestRollback_DefaultsToLatest(t *testing.T) {
	auditDir := t.TempDir()
	for _, id := range []string{"20260923T132105Z", "20260923T132109Z"} {
		rec := audit.Record{RunID: id, MessageID: "m", Subject: "s", Outcome: "applied", LabelsBefore: []string{"INBOX"}, LabelsAfter: []string{"INBOX", "cleaner/people"}, Action: &audit.Action{Add: []string{"cleaner/people"}}}
		if err := audit.Append(auditDir, rec); err != nil {
			t.Fatal(err)
		}
	}
	fg := &fakeRollbackGmail{
		messages: map[string]*gmail.Message{
			"m": {ID: "m", LabelIDs: []string{"INBOX", "cleaner/people"}},
		},
	}
	fakeApplier := &act.Fake{}
	var out bytes.Buffer
	cfgPath := writeRollbackConfig(t, auditDir)
	a := &app{
		configPath: cfgPath,
		stdout:     &out,
		stderr:     &bytes.Buffer{},
		stdin:      strings.NewReader("\n"),
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail:  func(context.Context, *config.Config) (gmailAccess, error) { return fg, nil },
		newApplier: func(extendedGmailAccess) applier { return fakeApplier },
	}
	if code := a.run([]string{"rollback"}); code != exitOK {
		t.Fatalf("rollback exit = %d", code)
	}
	if !strings.Contains(out.String(), "20260923T132109Z") {
		t.Errorf("stdout = %q, want latest run ID", out.String())
	}
}
