package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/AngeeelD/systemone-email-cleaner/internal/audit"
)

// A non-terminal writer (a buffer, a redirected log, cron) must stay silent:
// no carriage returns or ANSI escapes in logs.
func TestRunProgressIsSilentOnANonTerminal(t *testing.T) {
	var buf bytes.Buffer
	p := newRunProgress(&buf, 3, nil)

	p.record(audit.Record{Outcome: "applied", Action: &audit.Action{Add: []string{"cleaner/banking"}}})
	p.record(audit.Record{Outcome: "error"})
	p.finish()

	if buf.Len() != 0 {
		t.Errorf("progress wrote %q to a non-terminal, want nothing", buf.String())
	}
}

func TestRunProgressCountsAndDrawsTheFinalLine(t *testing.T) {
	var buf bytes.Buffer
	p := newRunProgress(&buf, 3, func() time.Time { return time.Unix(1000, 0) })
	p.enabled = true // force terminal rendering on a buffer

	p.record(audit.Record{Outcome: "applied", Action: &audit.Action{Add: []string{"cleaner/banking"}}})
	p.record(audit.Record{Outcome: "applied", Action: &audit.Action{Add: []string{"TRASH"}, Remove: []string{"INBOX"}}})
	p.record(audit.Record{Outcome: "error"})

	got := buf.String()
	for _, want := range []string{"3/3", "1 labels", "1 trash", "1 err"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line %q missing %q", got, want)
		}
	}
}

// A frozen clock means every redraw but the first and the final one is throttled.
func TestRunProgressThrottlesRedraws(t *testing.T) {
	var buf bytes.Buffer
	frozen := time.Unix(1000, 0)
	p := newRunProgress(&buf, 5, func() time.Time { return frozen })
	p.enabled = true

	for i := 0; i < 5; i++ {
		p.record(audit.Record{Outcome: "applied"})
	}

	if got := strings.Count(buf.String(), "\r\033[K"); got != 2 {
		t.Errorf("draws = %d, want 2 (throttled, plus the final line)", got)
	}
}
