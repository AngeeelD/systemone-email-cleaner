package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/gmail"
)

func TestUnprocessedQueryExcludesEveryConfiguredLabel(t *testing.T) {
	got := unprocessedQuery(map[string]string{
		"people":   "cleaner/people",
		"security": "cleaner/security",
	})

	if !strings.HasPrefix(got, "in:inbox ") {
		t.Errorf("query = %q, want it to start with in:inbox", got)
	}
	for _, want := range []string{"-label:cleaner/people", "-label:cleaner/security"} {
		if !strings.Contains(got, want) {
			t.Errorf("query = %q, want it to contain %q", got, want)
		}
	}
}

func TestUnprocessedQueryIsDeterministic(t *testing.T) {
	labels := map[string]string{
		"opportunities": "cleaner/opportunities",
		"accounts":      "cleaner/accounts",
		"action":        "cleaner/action",
	}

	first := unprocessedQuery(labels)
	for i := 0; i < 20; i++ {
		if got := unprocessedQuery(labels); got != first {
			t.Fatalf("query changed between calls:\n%q\n%q", first, got)
		}
	}
}

func TestUnprocessedQueryQuotesLabelsContainingSpaces(t *testing.T) {
	got := unprocessedQuery(map[string]string{"x": "my label"})

	if !strings.Contains(got, `-label:"my label"`) {
		t.Errorf("query = %q, want the label quoted", got)
	}
}

func TestFormatMessageUsesDomainDateAndSubject(t *testing.T) {
	m := &gmail.Message{
		FromDomain: "example.com",
		Subject:    "Factura #4411",
		Date:       time.Date(2026, 9, 22, 14, 3, 11, 0, time.UTC),
	}

	got := formatMessage(m)
	for _, want := range []string{"2026-09-22", "example.com", "Factura #4411"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatMessage() = %q, want it to contain %q", got, want)
		}
	}
}

func TestFormatMessageHandlesZeroDate(t *testing.T) {
	got := formatMessage(&gmail.Message{Subject: "sin fecha"})

	if strings.Contains(got, "0001-01-01") {
		t.Errorf("formatMessage() = %q, want a placeholder rather than the zero time", got)
	}
}

func TestTruncateCountsRunesNotBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than max", "hola", 10, "hola"},
		{"exactly max", "hola", 4, "hola"},
		{"accents are not split", "facturación electrónica", 10, "facturaci…"},
		{"trimmed first", "  hola  ", 10, "hola"},
		{"negative width returns empty", "hola", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.max); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

func TestListPrintsEachMatchingMessage(t *testing.T) {
	fake := &fakeGmail{
		account: "me@example.com",
		ids:     []string{"a", "b"},
		messages: map[string]*gmail.Message{
			"a": {FromDomain: "ana.example.com", Subject: "Factura #4411"},
			"b": {FromDomain: "banco.example.com", Subject: "Alerta de acceso"},
		},
	}
	a, out, errOut := newSetupApp(t, fake)

	if got := a.run([]string{"list", "--limit", "10"}); got != exitOK {
		t.Fatalf("list exit = %d, want %d (stderr: %s)", got, exitOK, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "Factura #4411") || !strings.Contains(text, "Alerta de acceso") {
		t.Errorf("stdout = %q, want both subjects", text)
	}
	if !strings.Contains(text, "-label:cleaner/people") {
		t.Errorf("stdout = %q, want the query that was used", text)
	}
}

func TestListSkipsUnreadableMessageAndContinues(t *testing.T) {
	fake := &fakeGmail{
		account:  "me@example.com",
		ids:      []string{"missing", "good"},
		messages: map[string]*gmail.Message{"good": {Subject: "Legible"}},
	}
	a, out, errOut := newSetupApp(t, fake)

	if got := a.run([]string{"list"}); got != exitOK {
		t.Fatalf("list exit = %d, want %d", got, exitOK)
	}
	if !strings.Contains(out.String(), "Legible") {
		t.Errorf("stdout = %q, want the readable message", out.String())
	}
	if !strings.Contains(errOut.String(), "missing") {
		t.Errorf("stderr = %q, want the skipped id reported", errOut.String())
	}
}

func TestListRejectsNegativeLimit(t *testing.T) {
	a, _, _ := newSetupApp(t, &fakeGmail{})

	if got := a.run([]string{"list", "--limit", "-1"}); got != exitUsage {
		t.Errorf("list exit = %d, want %d", got, exitUsage)
	}
}

func TestListRejectsZeroLimit(t *testing.T) {
	a, _, errOut := newSetupApp(t, &fakeGmail{})

	if got := a.run([]string{"list", "--limit", "0"}); got != exitUsage {
		t.Errorf("list exit = %d, want %d (stderr: %s)", got, exitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "at least 1") {
		t.Errorf("stderr = %q, want it to state the minimum", errOut.String())
	}
}

func TestListReportsExpiredTokenAsExitCodeThree(t *testing.T) {
	fake := &fakeGmail{account: "me@example.com"}
	a, _, errOut := newSetupApp(t, fake)
	a.checkToken = func(context.Context, *config.Config) error {
		return gmail.ErrTokenExpired
	}

	if got := a.run([]string{"list"}); got != exitReAuth {
		t.Errorf("list exit = %d, want %d", got, exitReAuth)
	}
	if !strings.Contains(errOut.String(), "emailcleaner setup") {
		t.Errorf("stderr = %q, want a re-authentication instruction", errOut.String())
	}
}
