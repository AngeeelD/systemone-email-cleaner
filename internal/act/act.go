// Package act applies label mutations to Gmail and is testable without Gmail.
package act

import (
	"context"
	"sync"
)

// Applier abstracts Gmail mutation.
type Applier interface {
	Modify(ctx context.Context, messageID string, add, remove []string) error
	BatchModify(ctx context.Context, ids []string, add, remove []string) error
	Untrash(ctx context.Context, messageID string) error
}

// GmailApplier wraps a Gmail client that satisfies Applier methods.
type GmailApplier struct {
	Modifier  func(ctx context.Context, id string, add, remove []string) error
	Batcher   func(ctx context.Context, ids []string, add, remove []string) error
	Untrasher func(ctx context.Context, id string) error
}

func (g *GmailApplier) Modify(ctx context.Context, id string, add, remove []string) error {
	if g.Modifier != nil {
		return g.Modifier(ctx, id, add, remove)
	}
	return nil
}
func (g *GmailApplier) BatchModify(ctx context.Context, ids []string, add, remove []string) error {
	if g.Batcher != nil {
		return g.Batcher(ctx, ids, add, remove)
	}
	return nil
}
func (g *GmailApplier) Untrash(ctx context.Context, id string) error {
	if g.Untrasher != nil {
		return g.Untrasher(ctx, id)
	}
	return nil
}

// Fake is a test double that records calls. It is safe for concurrent use.
type Fake struct {
	mu         sync.Mutex
	Modifies   []ModifyCall
	BatchCalls []BatchCall
	Untrashes  []string
	ModifyErr  error
	BatchErr   error
	UntrashErr error
}

type ModifyCall struct {
	ID     string
	Add    []string
	Remove []string
}
type BatchCall struct {
	IDs    []string
	Add    []string
	Remove []string
}

func (f *Fake) Modify(_ context.Context, id string, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Modifies = append(f.Modifies, ModifyCall{ID: id, Add: append([]string(nil), add...), Remove: append([]string(nil), remove...)})
	return f.ModifyErr
}
func (f *Fake) BatchModify(_ context.Context, ids []string, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.BatchCalls = append(f.BatchCalls, BatchCall{IDs: append([]string(nil), ids...), Add: append([]string(nil), add...), Remove: append([]string(nil), remove...)})
	return f.BatchErr
}
func (f *Fake) Untrash(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Untrashes = append(f.Untrashes, id)
	return f.UntrashErr
}
