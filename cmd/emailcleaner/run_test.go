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
	"time"

	"emailcleaner/internal/act"
	"emailcleaner/internal/audit"
	"emailcleaner/internal/config"
	"emailcleaner/internal/extract"
	"emailcleaner/internal/gmail"
	"emailcleaner/internal/laya"
)

// fakeRunGmail is a test double for extendedGmailAccess.
type fakeRunGmail struct {
	ids      []string
	messages map[string]*gmail.Message
	listErr  error
	getErr   map[string]error
	modifies []act.ModifyCall
}

func (f *fakeRunGmail) EnsureLabel(_ context.Context, name string) (string, error) {
	return "Label_" + name, nil
}
func (f *fakeRunGmail) Profile(_ context.Context) (string, error) { return "me@example.com", nil }
func (f *fakeRunGmail) ListMessages(_ context.Context, _ string, max int) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if max > 0 && len(f.ids) > max {
		return f.ids[:max], nil
	}
	return f.ids, nil
}
func (f *fakeRunGmail) GetMessage(_ context.Context, id string) (*gmail.Message, error) {
	if err, ok := f.getErr[id]; ok && err != nil {
		return nil, err
	}
	if m, ok := f.messages[id]; ok {
		return m, nil
	}
	return nil, errors.New("no such message: " + id)
}
func (f *fakeRunGmail) Modify(_ context.Context, id string, add, remove []string) error {
	f.modifies = append(f.modifies, act.ModifyCall{ID: id, Add: append([]string(nil), add...), Remove: append([]string(nil), remove...)})
	return nil
}
func (f *fakeRunGmail) BatchModify(_ context.Context, ids []string, add, remove []string) error {
	return nil
}
func (f *fakeRunGmail) Untrash(_ context.Context, id string) error { return nil }

// fakeLaya implements layaPredictor.
type fakeLaya struct {
	predict func(context.Context, extract.State) (laya.Answers, error)
	predictParagraph func(context.Context, string) (laya.Answers, error)
	ping    func(context.Context) error
}

func (f *fakeLaya) Predict(ctx context.Context, s extract.State) (laya.Answers, error) {
	if f.predict != nil {
		return f.predict(ctx, s)
	}
	if f.predictParagraph != nil {
		para := s.BodyPreview
		return f.predictParagraph(ctx, para)
	}
	return laya.Answers{}, nil
}
func (f *fakeLaya) PredictParagraph(ctx context.Context, para string) (laya.Answers, error) {
	if f.predictParagraph != nil {
		return f.predictParagraph(ctx, para)
	}
	if f.predict != nil {
		return f.predict(ctx, extract.State{BodyPreview: para, Subject: para})
	}
	return laya.Answers{}, nil
}
func (f *fakeLaya) Ping(ctx context.Context) error {
	if f.ping != nil {
		return f.ping(ctx)
	}
	return nil
}

func writeRunConfig(t *testing.T, auditDir string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf("gmail:\n  token_file: token.json\n  credentials_file: client_secret.json\naudit:\n  dir: %s\n", auditDir)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func newRunApp(t *testing.T, gmailClient *fakeRunGmail, layaClient *fakeLaya, fakeApplier *act.Fake, auditDir string, stdin string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	cfgPath := writeRunConfig(t, auditDir)
	a := &app{
		configPath: cfgPath,
		stdout:     &out,
		stderr:     &errOut,
		stdin:      strings.NewReader(stdin),
		clock:      func() time.Time { return time.Date(2026, 9, 23, 13, 21, 5, 0, time.UTC) },
		sleep:      func(time.Duration) {},
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail: func(context.Context, *config.Config) (gmailAccess, error) {
			return gmailClient, nil
		},
		openLaya: func(context.Context, *config.Config) (layaPredictor, error) {
			return layaClient, nil
		},
		newApplier: func(extendedGmailAccess) applier {
			if fakeApplier != nil {
				return fakeApplier
			}
			return &act.Fake{}
		},
	}
	return a, &out, &errOut
}

func sampleAnswersForLabel() laya.Answers {
	return laya.Answers{
		"is_junk":     {Choice: "B", Confidence: 0.97},
		"is_person":   {Choice: "A", Confidence: 0.90},
		"is_security": {Choice: "B", Confidence: 0.99},
		"is_purchase": {Choice: "B", Confidence: 0.99},
		"is_banking":  {Choice: "B", Confidence: 0.99},
	}
}
func sampleAnswersForTrash() laya.Answers {
	return laya.Answers{
		"is_junk":     {Choice: "A", Confidence: 0.95},
		"is_person":   {Choice: "B", Confidence: 0.99},
		"is_security": {Choice: "B", Confidence: 0.99},
		"is_purchase": {Choice: "B", Confidence: 0.99},
		"is_banking":  {Choice: "B", Confidence: 0.99},
	}
}

func TestRun_DryRunWritesNothing(t *testing.T) {
	auditDir := t.TempDir()
	fg := &fakeRunGmail{
		ids: []string{"a", "b"},
		messages: map[string]*gmail.Message{
			"a": {ID: "a", Subject: "Hello", LabelIDs: []string{"INBOX"}, BodyText: "hello"},
			"b": {ID: "b", Subject: "World", LabelIDs: []string{"INBOX"}, BodyText: "world"},
		},
	}
	fl := &fakeLaya{
		predict: func(_ context.Context, _ extract.State) (laya.Answers, error) {
			return sampleAnswersForLabel(), nil
		},
	}
	fakeApplier := &act.Fake{}
	a, out, _ := newRunApp(t, fg, fl, fakeApplier, auditDir, "")
	if code := a.run([]string{"run", "--dry-run"}); code != exitOK {
		t.Fatalf("run dry-run exit = %d, want 0", code)
	}
	if len(fakeApplier.Modifies) != 0 {
		t.Errorf("dry-run should not call applier, got %v", fakeApplier.Modifies)
	}
	// Audit dir should be empty (no file created)
	entries, _ := os.ReadDir(auditDir)
	if len(entries) != 0 {
		t.Errorf("audit dir should be empty for dry-run, got %v", entries)
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("stdout = %q, want dry-run plan", out.String())
	}
	// Also test with --limit
}

func TestRun_AppliesLabelsAndWritesAudit(t *testing.T) {
	auditDir := t.TempDir()
	fg := &fakeRunGmail{
		ids: []string{"a"},
		messages: map[string]*gmail.Message{
			"a": {ID: "a", Subject: "Hello", LabelIDs: []string{"INBOX"}, BodyText: "hello"},
		},
	}
	fl := &fakeLaya{
		predict: func(_ context.Context, _ extract.State) (laya.Answers, error) {
			return sampleAnswersForLabel(), nil
		},
	}
	fakeApplier := &act.Fake{}
	a, out, _ := newRunApp(t, fg, fl, fakeApplier, auditDir, "")
	if code := a.run([]string{"run", "--workers", "1"}); code != exitOK {
		t.Fatalf("run exit = %d, want 0", code)
	}
	if len(fakeApplier.Modifies) != 1 {
		t.Fatalf("applier modifies = %d, want 1", len(fakeApplier.Modifies))
	}
	if len(fg.modifies) != 0 {
		// fg modifies is separate from applier; we use applier fake, so fg not used for modify
	}
	// Check audit file exists and contains applied record
	runs, err := audit.ListRuns(auditDir)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %v err %v, want 1", runs, err)
	}
	recs, err := audit.Read(auditDir, runs[0])
	if err != nil || len(recs) != 1 {
		t.Fatalf("Read = %v err %v, want 1", recs, err)
	}
	if recs[0].Outcome != "applied" {
		t.Errorf("outcome = %q, want applied", recs[0].Outcome)
	}
	if recs[0].Subject != "Hello" {
		t.Errorf("subject = %q, want Hello", recs[0].Subject)
	}
	if !strings.Contains(out.String(), "applied") {
		t.Errorf("stdout = %q, want applied summary", out.String())
	}
}

func TestRun_Skips422AndContinues(t *testing.T) {
	auditDir := t.TempDir()
	fg := &fakeRunGmail{
		ids: []string{"bad", "good"},
		messages: map[string]*gmail.Message{
			"bad":  {ID: "bad", Subject: "Bad", LabelIDs: []string{"INBOX"}, BodyText: "bad"},
			"good": {ID: "good", Subject: "Good", LabelIDs: []string{"INBOX"}, BodyText: "good"},
		},
	}
	call := 0
	fl := &fakeLaya{
		predict: func(_ context.Context, _ extract.State) (laya.Answers, error) {
			call++
			if call == 1 {
				return nil, fmt.Errorf("%w: state too large", laya.ErrUnprocessable)
			}
			return sampleAnswersForLabel(), nil
		},
	}
	fakeApplier := &act.Fake{}
	a, _, _ := newRunApp(t, fg, fl, fakeApplier, auditDir, "")
	if code := a.run([]string{"run", "--workers", "1"}); code != exitOK {
		t.Fatalf("run exit = %d", code)
	}
	runs, _ := audit.ListRuns(auditDir)
	recs, _ := audit.Read(auditDir, runs[0])
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2 (one skipped, one applied)", len(recs))
	}
	var skipped, applied int
	for _, r := range recs {
		switch r.Outcome {
		case "skipped":
			skipped++
			if !strings.Contains(r.Reason, "unprocessable") {
				t.Errorf("skipped reason = %q, want unprocessable", r.Reason)
			}
		case "applied":
			applied++
		}
	}
	if skipped != 1 || applied != 1 {
		t.Errorf("skipped=%d applied=%d, want 1 each", skipped, applied)
	}
	if len(fakeApplier.Modifies) != 1 {
		t.Errorf("applier modifies = %d, want 1 (good only)", len(fakeApplier.Modifies))
	}
}

func TestRun_CircuitBreakerAfter5(t *testing.T) {
	auditDir := t.TempDir()
	ids := []string{"1", "2", "3", "4", "5", "6"}
	msgs := make(map[string]*gmail.Message)
	for _, id := range ids {
		msgs[id] = &gmail.Message{ID: id, Subject: "S" + id, LabelIDs: []string{"INBOX"}, BodyText: "body"}
	}
	fg := &fakeRunGmail{ids: ids, messages: msgs}
	fl := &fakeLaya{
		predict: func(_ context.Context, _ extract.State) (laya.Answers, error) {
			return nil, errors.New("connection refused")
		},
		ping: func(_ context.Context) error { return nil },
	}
	fakeApplier := &act.Fake{}
	// stdin provides ENTER for pause
	a, _, errOut := newRunApp(t, fg, fl, fakeApplier, auditDir, "\n")
	if code := a.run([]string{"run", "--workers", "1"}); code != exitOK {
		t.Fatalf("run exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "laya-serve not reachable") {
		t.Errorf("stderr = %q, want circuit breaker pause message", errOut.String())
	}
	// After 5 failures, breaker should have paused at least once. Verify audit has 6 skipped records.
	runs, _ := audit.ListRuns(auditDir)
	if len(runs) == 0 {
		t.Fatal("no audit runs")
	}
	recs, _ := audit.Read(auditDir, runs[0])
	if len(recs) != 6 {
		t.Fatalf("records = %d, want 6", len(recs))
	}
}

func TestRun_429BackoffRetries(t *testing.T) {
	auditDir := t.TempDir()
	fg := &fakeRunGmail{
		ids: []string{"a"},
		messages: map[string]*gmail.Message{
			"a": {ID: "a", Subject: "Hi", LabelIDs: []string{"INBOX"}, BodyText: "hi"},
		},
	}
	calls := 0
	fl := &fakeLaya{
		predict: func(_ context.Context, _ extract.State) (laya.Answers, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("429 Too Many Requests")
			}
			return sampleAnswersForLabel(), nil
		},
	}
	fakeApplier := &act.Fake{}
	var slept []time.Duration
	a, _, _ := newRunApp(t, fg, fl, fakeApplier, auditDir, "")
	a.sleep = func(d time.Duration) { slept = append(slept, d) }
	if code := a.run([]string{"run", "--workers", "1"}); code != exitOK {
		t.Fatalf("run exit = %d", code)
	}
	if calls != 2 {
		t.Errorf("predict calls = %d, want 2 (retry after 429)", calls)
	}
	if len(slept) == 0 {
		t.Error("expected backoff sleep after 429")
	}
	runs, _ := audit.ListRuns(auditDir)
	recs, _ := audit.Read(auditDir, runs[0])
	if recs[0].Outcome != "applied" {
		t.Errorf("outcome = %q, want applied after 429 retry", recs[0].Outcome)
	}
}

func TestRun_TokenExpiredExitCode(t *testing.T) {
	auditDir := t.TempDir()
	fg := &fakeRunGmail{}
	fl := &fakeLaya{}
	a, _, errOut := newRunApp(t, fg, fl, &act.Fake{}, auditDir, "")
	a.checkToken = func(context.Context, *config.Config) error { return gmail.ErrTokenExpired }
	if code := a.run([]string{"run"}); code != exitReAuth {
		t.Errorf("run exit = %d, want %d (re-auth)", code, exitReAuth)
	}
	if !strings.Contains(errOut.String(), "setup") {
		t.Errorf("stderr = %q, want setup hint", errOut.String())
	}
}

func TestRun_ReprocessAndLimitFlags(t *testing.T) {
	auditDir := t.TempDir()
	var capturedQuery string
	fg := &fakeRunGmail{
		ids: []string{"a", "b", "c"},
		messages: map[string]*gmail.Message{
			"a": {ID: "a", Subject: "A", LabelIDs: []string{"INBOX"}, BodyText: "a"},
			"b": {ID: "b", Subject: "B", LabelIDs: []string{"INBOX"}, BodyText: "b"},
			"c": {ID: "c", Subject: "C", LabelIDs: []string{"INBOX"}, BodyText: "c"},
		},
	}
	origList := fg.ListMessages
	_ = origList
	fg2 := &fakeRunGmail{
		messages: fg.messages,
	}
	// Wrap List to capture query
	fakeListGmail := &queryCapturingGmail{fakeRunGmail: fg2, ids: []string{"a", "b", "c"}, messages: fg.messages, captured: &capturedQuery}
	fl := &fakeLaya{predict: func(_ context.Context, _ extract.State) (laya.Answers, error) {
		return sampleAnswersForTrash(), nil
	}}
	// Build app manually to allow custom gmail type
	var out, errOut bytes.Buffer
	cfgPath := writeRunConfig(t, auditDir)
	a := &app{
		configPath: cfgPath,
		stdout:     &out,
		stderr:     &errOut,
		stdin:      strings.NewReader(""),
		clock:      func() time.Time { return time.Date(2026, 9, 23, 13, 21, 5, 0, time.UTC) },
		sleep:      func(time.Duration) {},
		checkToken: func(context.Context, *config.Config) error { return nil },
		openGmail:  func(context.Context, *config.Config) (gmailAccess, error) { return fakeListGmail, nil },
		openLaya:   func(context.Context, *config.Config) (layaPredictor, error) { return fl, nil },
		newApplier: func(extendedGmailAccess) applier { return &act.Fake{} },
	}
	_ = out // keep
	if code := a.run([]string{"run", "--reprocess=unclassified", "--limit", "2", "--workers", "1"}); code != exitOK {
		t.Fatalf("run exit = %d", code)
	}
	if !strings.Contains(capturedQuery, "label:cleaner/unclassified") {
		t.Errorf("query = %q, want reprocess label", capturedQuery)
	}
	runs, _ := audit.ListRuns(auditDir)
	recs, _ := audit.Read(auditDir, runs[0])
	if len(recs) != 2 {
		t.Errorf("records = %d, want 2 (limit)", len(recs))
	}
}

type queryCapturingGmail struct {
	*fakeRunGmail
	ids      []string
	messages map[string]*gmail.Message
	captured *string
}

func (q *queryCapturingGmail) ListMessages(ctx context.Context, query string, max int) ([]string, error) {
	*q.captured = query
	if max > 0 && len(q.ids) > max {
		return q.ids[:max], nil
	}
	return q.ids, nil
}
func (q *queryCapturingGmail) GetMessage(ctx context.Context, id string) (*gmail.Message, error) {
	return q.fakeRunGmail.GetMessage(ctx, id)
}
