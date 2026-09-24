package main

import (
	"context"
	"sync"
	"time"
)

// quotaGate pauses every worker when Gmail reports the per-minute query quota is
// exhausted. Retrying a single message after 1-4s does not help a window that
// resets on the minute, and each retry burns more quota on top of the exhausted
// budget, so the whole run waits the window out instead.
type quotaGate struct {
	mu     sync.Mutex
	until  time.Time
	window time.Duration
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

func newQuotaGate(window time.Duration, now func() time.Time) *quotaGate {
	if now == nil {
		now = time.Now
	}
	return &quotaGate{
		window: window,
		now:    now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// trip pauses the run for one window from now. Concurrent trips extend the
// deadline rather than stacking it, so N workers hitting quota together do not
// multiply the pause.
func (g *quotaGate) trip() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := g.now().Add(g.window); until.After(g.until) {
		g.until = until
	}
}

// wait blocks until the current pause elapses. It is safe to call from every
// worker; each re-reads the deadline because another worker may have extended
// it while this one slept.
func (g *quotaGate) wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		until := g.until
		g.mu.Unlock()

		d := until.Sub(g.now())
		if d <= 0 {
			return nil
		}
		if err := g.sleep(ctx, d); err != nil {
			return err
		}
	}
}
