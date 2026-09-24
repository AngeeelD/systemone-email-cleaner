package act

import (
	"context"
	"testing"
)

func TestFake_RecordsCalls(t *testing.T) {
	f := &Fake{}
	ctx := context.Background()
	if err := f.Modify(ctx, "id1", []string{"cleaner/people"}, nil); err != nil {
		t.Fatalf("Modify error: %v", err)
	}
	if len(f.Modifies) != 1 || f.Modifies[0].ID != "id1" {
		t.Errorf("Modifies = %v, want one with id1", f.Modifies)
	}
	if err := f.BatchModify(ctx, []string{"a", "b"}, []string{"TRASH"}, []string{"INBOX"}); err != nil {
		t.Fatalf("BatchModify error: %v", err)
	}
	if len(f.BatchCalls) != 1 || len(f.BatchCalls[0].IDs) != 2 {
		t.Errorf("BatchCalls = %v, want one with 2 ids", f.BatchCalls)
	}
	if err := f.Untrash(ctx, "id1"); err != nil {
		t.Fatalf("Untrash error: %v", err)
	}
	if len(f.Untrashes) != 1 || f.Untrashes[0] != "id1" {
		t.Errorf("Untrashes = %v, want [id1]", f.Untrashes)
	}
}

func TestGmailApplier_Delegates(t *testing.T) {
	called := false
	g := &GmailApplier{
		Modifier: func(_ context.Context, id string, _, _ []string) error {
			called = true
			if id != "x" {
				t.Errorf("id = %q, want x", id)
			}
			return nil
		},
	}
	if err := g.Modify(context.Background(), "x", nil, nil); err != nil {
		t.Fatalf("Modify error: %v", err)
	}
	if !called {
		t.Error("Modifier not called")
	}
}
