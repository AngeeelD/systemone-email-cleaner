package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/AngeeelD/systemone-email-cleaner/internal/audit"
)

// isTerminal reports whether w is an interactive terminal. A cron run or a
// redirected stream must not collect carriage returns or ANSI escapes.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// runProgress renders a single live status line while a run is in flight. On a
// terminal it rewrites the line in place; on anything else it stays silent, so
// a cron job or a redirected log stays clean. The caller prints the final
// summary.
type runProgress struct {
	mu      sync.Mutex
	w       io.Writer
	enabled bool
	total   int
	done    int
	labels  int
	trash   int
	errs    int
	last    time.Time
	now     func() time.Time
	every   time.Duration
}

func newRunProgress(w io.Writer, total int, now func() time.Time) *runProgress {
	if now == nil {
		now = time.Now
	}
	return &runProgress{
		w:       w,
		enabled: isTerminal(w),
		total:   total,
		now:     now,
		every:   100 * time.Millisecond,
	}
}

// record counts one finished message. It redraws at most once per `every`
// interval, plus always on the final message, so a fast run does not thrash the
// terminal and the last state is always visible.
func (p *runProgress) record(rec audit.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.done++
	switch progressClass(rec) {
	case "trash":
		p.trash++
	case "error":
		p.errs++
	default:
		p.labels++
	}

	if !p.enabled {
		return
	}
	if t := p.now(); p.done != p.total && t.Sub(p.last) < p.every {
		return
	}
	p.last = p.now()
	fmt.Fprintf(p.w, "\r\033[K  %d/%d · %d labels · %d trash · %d err", p.done, p.total, p.labels, p.trash, p.errs)
}

// finish clears the live line so the final summary starts on a clean line.
func (p *runProgress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled {
		fmt.Fprint(p.w, "\r\033[K")
	}
}

// progressClass buckets a finished record into trash, error, or label.
func progressClass(rec audit.Record) string {
	if rec.Outcome == "error" {
		return "error"
	}
	if rec.Action != nil {
		for _, l := range rec.Action.Add {
			if l == "TRASH" {
				return "trash"
			}
		}
	}
	return "label"
}
