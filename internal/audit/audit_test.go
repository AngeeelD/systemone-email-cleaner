package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAndRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := Record{
		RunID:        "20260923T132105Z",
		MessageID:    "18f1abc",
		Subject:      "Factura #4411",
		Outcome:      "applied",
		Reason:       "is_junk conf=0.97",
		RoutingModel: "multilingual",
		Answers:      map[string]Answer{"is_junk": {Choice: "A", Confidence: 0.97}},
		LabelsBefore: []string{"INBOX", "UNREAD"},
		LabelsAfter:  []string{"UNREAD", "TRASH"},
		Action:       &Action{Add: []string{"TRASH"}, Remove: []string{"INBOX"}},
	}
	if err := Append(dir, rec); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	got, err := Read(dir, rec.RunID)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read len = %d, want 1", len(got))
	}
	if got[0].MessageID != rec.MessageID || got[0].Subject != rec.Subject || got[0].Outcome != rec.Outcome {
		t.Errorf("round trip mismatch: got %+v, want %+v", got[0], rec)
	}
	if got[0].Action.Add[0] != "TRASH" {
		t.Errorf("Action.Add = %v, want [TRASH]", got[0].Action.Add)
	}
}

func TestAppend_MultipleRecords(t *testing.T) {
	dir := t.TempDir()
	runID := "20260923T132106Z"
	for i := 0; i < 3; i++ {
		rec := Record{RunID: runID, MessageID: string(rune('a' + i)), Subject: "s", Outcome: "applied"}
		if err := Append(dir, rec); err != nil {
			t.Fatalf("Append %d error: %v", i, err)
		}
	}
	got, err := Read(dir, runID)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, rec := range got {
		want := string(rune('a' + i))
		if rec.MessageID != want {
			t.Errorf("rec[%d].MessageID = %q, want %q", i, rec.MessageID, want)
		}
	}
}

func TestRead_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir, "nonexistent"); err == nil {
		t.Fatal("Read missing file should error")
	}
}

func TestListRuns_Sorted(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"20260923T132107Z", "20260923T132105Z", "20260923T132106Z"}
	for _, id := range ids {
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(`{"run_id":"`+id+`"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Add non-jsonl file ignored
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ListRuns(dir)
	if err != nil {
		t.Fatalf("ListRuns error: %v", err)
	}
	if len(got) != 3 || got[0] != "20260923T132105Z" || got[2] != "20260923T132107Z" {
		t.Errorf("ListRuns = %v, want sorted", got)
	}
}

func TestLoadLatest(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"20260923T132105Z", "20260923T132106Z"} {
		rec := Record{RunID: id, MessageID: "m1", Outcome: "applied"}
		if err := Append(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	recs, latest, err := LoadLatest(dir)
	if err != nil {
		t.Fatalf("LoadLatest error: %v", err)
	}
	if latest != "20260923T132106Z" {
		t.Errorf("latest = %q, want 20260923T132106Z", latest)
	}
	if len(recs) != 1 || recs[0].RunID != latest {
		t.Errorf("recs = %v, want one with RunID %q", recs, latest)
	}
}

func TestShouldRevert(t *testing.T) {
	rec := Record{
		Outcome: "applied",
		Action:  &Action{Add: []string{"cleaner/people"}, Remove: []string{}},
	}
	if ok, _ := ShouldRevert(rec, []string{"cleaner/people", "INBOX"}); !ok {
		t.Error("ShouldRevert should succeed when add present")
	}
	if ok, reason := ShouldRevert(rec, []string{"INBOX"}); ok {
		t.Error("ShouldRevert should fail when add missing")
	} else if reason == "" {
		t.Error("reason should not be empty")
	}
	// Trash case: added TRASH removed INBOX
	rec2 := Record{
		Outcome: "applied",
		Action:  &Action{Add: []string{"TRASH"}, Remove: []string{"INBOX"}},
	}
	if ok, _ := ShouldRevert(rec2, []string{"TRASH", "UNREAD"}); !ok {
		t.Error("trash ShouldRevert should succeed when TRASH present and INBOX absent")
	}
	if ok, _ := ShouldRevert(rec2, []string{"INBOX", "TRASH"}); ok {
		t.Error("ShouldRevert should fail when removed label reappeared")
	}
	if ok, _ := ShouldRevert(rec2, []string{"UNREAD"}); ok {
		t.Error("ShouldRevert should fail when TRASH missing")
	}
	// Not applied should skip
	rec3 := Record{Outcome: "skipped", Action: &Action{Add: []string{"x"}}}
	if ok, _ := ShouldRevert(rec3, []string{"x"}); ok {
		t.Error("skipped record should not revert")
	}
}
