package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock shared with a gate.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestQuotaGateWaitsOutTheWindow(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	g := newQuotaGate(60*time.Second, clk.Now)

	var slept []time.Duration
	g.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		clk.advance(d)
		return nil
	}

	// No trip yet: waiting must not sleep.
	if err := g.wait(context.Background()); err != nil {
		t.Fatalf("wait() error = %v", err)
	}
	if len(slept) != 0 {
		t.Fatalf("slept %v with no pause, want none", slept)
	}

	g.trip()
	if err := g.wait(context.Background()); err != nil {
		t.Fatalf("wait() error = %v", err)
	}
	if len(slept) != 1 || slept[0] != 60*time.Second {
		t.Fatalf("slept %v, want one 60s sleep", slept)
	}

	// The pause already elapsed: a second wait is a no-op.
	if err := g.wait(context.Background()); err != nil {
		t.Fatalf("wait() error = %v", err)
	}
	if len(slept) != 1 {
		t.Errorf("slept again (%v), want none after the window elapsed", slept)
	}
}

// Concurrent trips within the same window extend to one deadline, not N.
func TestQuotaGateTripsDoNotStack(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	g := newQuotaGate(60*time.Second, clk.Now)

	var slept time.Duration
	g.sleep = func(_ context.Context, d time.Duration) error {
		slept += d
		clk.advance(d)
		return nil
	}

	g.trip()
	g.trip()
	g.trip()

	if err := g.wait(context.Background()); err != nil {
		t.Fatalf("wait() error = %v", err)
	}
	if slept != 60*time.Second {
		t.Errorf("total sleep = %v, want 60s (trips extend, they do not stack)", slept)
	}
}

// An extended pause is honoured: a worker that wakes early keeps waiting.
func TestQuotaGateHonoursAnExtendedPause(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	g := newQuotaGate(60*time.Second, clk.Now)

	var slept []time.Duration
	g.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		clk.advance(d)
		if len(slept) == 1 {
			// Another worker trips again while this one was sleeping.
			g.trip()
		}
		return nil
	}

	g.trip()
	if err := g.wait(context.Background()); err != nil {
		t.Fatalf("wait() error = %v", err)
	}
	if len(slept) != 2 {
		t.Errorf("slept %v, want two sleeps (the pause was extended)", slept)
	}
}

func TestQuotaGateStopsOnCancelledContext(t *testing.T) {
	g := newQuotaGate(60*time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	g.trip()
	if err := g.wait(ctx); err == nil {
		t.Fatal("wait() error = nil, want a context error while paused")
	}
}
