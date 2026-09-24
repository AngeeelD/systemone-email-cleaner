package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

func readGolden(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	// Golden files end with a single newline for POSIX; trim only trailing \n/\r.
	s := string(b)
	s = strings.TrimRight(s, "\r\n")
	return s
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestHtmlToText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain stays plain", in: "hello world", want: "hello world"},
		{name: "strips tags and keeps text", in: "<p>Hello <b>World</b>!</p>", want: "Hello World!"},
		{name: "ignores script and style", in: "<style>body{}</style><p>keep</p><script>alert(1)</script>", want: "keep"},
		{name: "decodes entities", in: "<p>a &amp; b &lt;c&gt;</p>", want: "a & b <c>"},
		{name: "br produces separator", in: "a<br>b", want: "a\nb"},
		{name: "link text kept", in: `<a href="http://x">click here</a>`, want: "click here"},
		{name: "nested block separators", in: "<div>one</div><div>two</div>", want: "one\ntwo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := htmlToText(tc.in)
			// htmlToText leaves extra spaces/newlines; normalize via collapse for stable compare
			// but here we test raw htmlToText separator presence via containment, not exact collapse.
			// Use collapseWhitespace to compare normalized form where applicable.
			gotNorm := collapseWhitespace(got)
			wantNorm := collapseWhitespace(tc.want)
			if gotNorm != wantNorm {
				t.Errorf("htmlToText(%q) = %q, want %q (normalized)", tc.in, gotNorm, wantNorm)
			}
		})
	}
}

func TestTrimQuotedReply(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no quote unchanged", in: "hello\nworld", want: "hello\nworld"},
		{name: "gt line trimmed", in: "hi\n> quoted\nmore", want: "hi"},
		{name: "gt with leading spaces", in: "hi\n   > quoted", want: "hi"},
		{name: "On wrote header", in: "hi\nOn Mon, 22 Sep 2026 at 10:00 AM, Ana <a@b> wrote:\n> quoted", want: "hi"},
		{name: "On wrote case-insensitive", in: "hi\nON Mon, 22 Sep 2026 AT 10:00 am, Ana wrote:\n> q", want: "hi"},
		{name: "El escribio with accent", in: "hola\nEl lun, 22 sept 2026 a las 10:00, Ana escribió:\n> citado", want: "hola"},
		{name: "El escribio without accent", in: "hola\nEl lun, 22 sept 2026 a las 10:00, Ana escribio:\n> citado", want: "hola"},
		{name: "El uppercase", in: "hola\nEL lun, 22 sept 2026 a las 10:00, Ana ESCRIBIÓ:\n> citado", want: "hola"},
		{name: "keeps content before marker", in: "line1\nline2\nOn Mon, 1 Jan 2024 at 10:00, Bob wrote:\n> old", want: "line1\nline2"},
		{name: "blank lines are not markers", in: "a\n\nb", want: "a\n\nb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimQuotedReply(tc.in); got != tc.want {
				t.Errorf("trimQuotedReply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTrimSignature(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no signature", in: "hello\nworld", want: "hello\nworld"},
		{name: "dash dash delimiter", in: "hello\n--\nJohn\nAcme", want: "hello"},
		{name: "dash dash with trailing space trimmed", in: "hello\n-- \nJohn", want: "hello"},
		{name: "delimiter with surrounding spaces", in: "hello\n  --  \nJohn", want: "hello"},
		{name: "dash inside line not delimiter", in: "a -- b", want: "a -- b"},
		{name: "multiple dashes not signature", in: "hello\n---\nJohn", want: "hello\n---\nJohn"},
		{name: "keeps before delimiter", in: "line1\nline2\n--\nsig", want: "line1\nline2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimSignature(tc.in); got != tc.want {
				t.Errorf("trimSignature(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCollapseWhitespace(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "spaces", in: "  hello   world  ", want: "hello world"},
		{name: "newlines and tabs", in: "hello \t\n  world\n\nfoo\t\tbar", want: "hello world foo bar"},
		{name: "only whitespace empty", in: "  \n\t  ", want: ""},
		{name: "empty unchanged", in: "", want: ""},
		{name: "single word", in: "hello", want: "hello"},
		{name: "unicode spaces collapsed", in: "a  \n  b", want: "a b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := collapseWhitespace(tc.in); got != tc.want {
				t.Errorf("collapseWhitespace(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "shorter than limit", in: "hello", n: 10, want: "hello"},
		{name: "exact limit", in: "hello", n: 5, want: "hello"},
		{name: "truncates", in: "hello world", n: 5, want: "hello"},
		{name: "zero returns empty", in: "hello", n: 0, want: ""},
		{name: "negative returns empty", in: "hello", n: -1, want: ""},
		{name: "emoji rune-aware", in: "a😀b😀c", n: 2, want: "a😀"},
		{name: "emoji truncate to 3", in: "😀😀😀😀", n: 3, want: "😀😀😀"},
		{name: "multibyte not split", in: "café", n: 3, want: "caf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.in, tc.n); got != tc.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			// Also ensure rune count respects limit
			if got := truncateRunes(tc.in, tc.n); tc.n > 0 && len([]rune(got)) > tc.n {
				t.Errorf("rune count %d exceeds limit %d", len([]rune(got)), tc.n)
			}
		})
	}
}

func TestDomainOf(t *testing.T) {
	tests := []struct {
		name string
		from string
		want string
	}{
		{name: "bare", from: "user@example.com", want: "example.com"},
		{name: "display name", from: "Ana <ana@Example.COM>", want: "example.com"},
		{name: "spaces", from: "  ana@example.com  ", want: "example.com"},
		{name: "empty", from: "", want: ""},
		{name: "no at", from: "postmaster", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domainOf(tc.from); got != tc.want {
				t.Errorf("domainOf(%q) = %q, want %q", tc.from, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ToState unit tests
// ---------------------------------------------------------------------------

func TestToState_BasicFields(t *testing.T) {
	date := time.Date(2026, 9, 22, 14, 3, 11, 0, time.FixedZone("EST", -5*3600))
	msg := gmail.Message{
		From:           "Ana Gomez <ana@example.com>",
		FromDomain:     "example.com",
		Subject:        "Factura #4411",
		Date:           date,
		LabelIDs:       []string{"INBOX", "UNREAD"},
		HasAttachments: true,
		BodyText:       "Hola mundo",
	}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	if got.From != msg.From {
		t.Errorf("From = %q, want %q", got.From, msg.From)
	}
	if got.FromDomain != "example.com" {
		t.Errorf("FromDomain = %q, want example.com", got.FromDomain)
	}
	if got.Subject != "Factura #4411" {
		t.Errorf("Subject = %q", got.Subject)
	}
	if got.Date != date.Format(time.RFC3339) {
		t.Errorf("Date = %q, want %q", got.Date, date.Format(time.RFC3339))
	}
	wantLabels := []string{"INBOX", "UNREAD"}
	if len(got.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", got.Labels, wantLabels)
	}
	for i, v := range wantLabels {
		if got.Labels[i] != v {
			t.Errorf("Labels[%d] = %q, want %q", i, got.Labels[i], v)
		}
	}
	if !got.HasAttachments {
		t.Error("HasAttachments = false, want true")
	}
	if got.BodyPreview != "Hola mundo" {
		t.Errorf("BodyPreview = %q, want %q", got.BodyPreview, "Hola mundo")
	}
	// Ensure JSON omitempty behavior: Labels should be present when non-empty
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"labels"`) {
		t.Error("JSON should contain labels when non-empty")
	}
}

func TestToState_DateZeroEmpty(t *testing.T) {
	msg := gmail.Message{Subject: "no date", BodyText: "hi"}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	if got.Date != "" {
		t.Errorf("Date = %q, want empty for zero time", got.Date)
	}
}

func TestToState_FromDomainFallback(t *testing.T) {
	msg := gmail.Message{From: "Bob <bob@Fallback.ORG>", BodyText: "hi"}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	if got.FromDomain != "fallback.org" {
		t.Errorf("FromDomain = %q, want fallback.org", got.FromDomain)
	}
}

func TestToState_PrefersTextOverHTML(t *testing.T) {
	msg := gmail.Message{
		BodyText: "plain wins",
		BodyHTML: "<p>html should be ignored</p>",
	}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	if got.BodyPreview != "plain wins" {
		t.Errorf("BodyPreview = %q, want plain wins", got.BodyPreview)
	}
}

func TestToState_HTMLFallback(t *testing.T) {
	msg := gmail.Message{
		BodyHTML: `<p>Hello <b>World</b>!</p><script>alert(1)</script><div>keep &amp; more</div>`,
	}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	// After pipeline, should be collapsed whitespace single line
	want := "Hello World! keep & more"
	if got.BodyPreview != want {
		t.Errorf("BodyPreview = %q, want %q", got.BodyPreview, want)
	}
}

func TestToState_EmptyBody(t *testing.T) {
	msg := gmail.Message{Subject: "empty"}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	if got.BodyPreview != "" {
		t.Errorf("BodyPreview = %q, want empty", got.BodyPreview)
	}
}

func TestToState_PreviewCharsZeroUsesDefault(t *testing.T) {
	long := strings.Repeat("a", 1000)
	msg := gmail.Message{BodyText: long}
	got := ToState(msg, config.Extract{BodyPreviewChars: 0})
	if len([]rune(got.BodyPreview)) != 800 {
		t.Errorf("len BodyPreview runes = %d, want 800 (default)", len([]rune(got.BodyPreview)))
	}
}

func TestToState_TruncationRuneAware(t *testing.T) {
	// 5 emoji + 5 ascii, preview 7 should keep 5 emoji + 2 ascii as runes
	body := "😀😀😀😀😀hello world"
	msg := gmail.Message{BodyText: body}
	got := ToState(msg, config.Extract{BodyPreviewChars: 7})
	want := "😀😀😀😀😀he"
	if got.BodyPreview != want {
		t.Errorf("BodyPreview = %q, want %q", got.BodyPreview, want)
	}
	if len([]rune(got.BodyPreview)) != 7 {
		t.Errorf("rune len = %d, want 7", len([]rune(got.BodyPreview)))
	}
}

func TestToState_LabelCopyIsolation(t *testing.T) {
	msg := gmail.Message{LabelIDs: []string{"INBOX"}, BodyText: "hi"}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	msg.LabelIDs[0] = "MUTATED"
	if got.Labels[0] == "MUTATED" {
		t.Error("ToState should copy label slice, not alias")
	}
}

func TestToState_PipelineOrder_SignatureBeforeQuotedStillTrimmed(t *testing.T) {
	body := "Hello there\n\n--\nJohn Doe\nAcme\n\nOn Mon, 22 Sep 2026 at 10:00 AM, Ana wrote:\n> quoted"
	msg := gmail.Message{BodyText: body}
	got := ToState(msg, config.Extract{BodyPreviewChars: 800})
	want := "Hello there"
	if got.BodyPreview != want {
		t.Errorf("BodyPreview = %q, want %q", got.BodyPreview, want)
	}
}

// ---------------------------------------------------------------------------
// Golden files
// ---------------------------------------------------------------------------

func TestToState_Golden(t *testing.T) {
	// Each case builds a message, runs ToState with 800 chars, and compares
	// BodyPreview to the golden file in testdata/*.golden.
	cases := []struct {
		name string
		msg  gmail.Message
		cfg  config.Extract
	}{
		{
			name: "dirty_html",
			msg: gmail.Message{
				Subject:  "Dirty HTML",
				BodyHTML: `<html><head><style>body{color:red}</style></head><body><p>Hello <b>World</b>!</p><script>alert('xss')</script><div>Line <a href="http://example.com">link</a> &amp; more</div><p>  multiple   spaces </p><!-- comment --></body></html>`,
			},
			cfg: config.Extract{BodyPreviewChars: 800},
		},
		{
			name: "quoted_en",
			msg: gmail.Message{
				Subject:  "Re: PR",
				BodyText: "Hi team,\n\nPlease review the PR before EOD.\n\nOn Mon, 22 Sep 2026 at 10:00 AM, Ana Gomez <ana@example.com> wrote:\n> Previous message body\n> More quoted text\n\nThanks again!",
			},
			cfg: config.Extract{BodyPreviewChars: 800},
		},
		{
			name: "quoted_es",
			msg: gmail.Message{
				Subject:  "Re: documento",
				BodyText: "Hola equipo,\n\nPor favor revisa el documento.\n\nEl lun, 22 sept 2026 a las 10:00, Ana Gómez <ana@example.com> escribió:\n> Mensaje anterior\n> Otra línea citada\n\nGracias!",
			},
			cfg: config.Extract{BodyPreviewChars: 800},
		},
		{
			name: "signature",
			msg: gmail.Message{
				Subject:  "Report",
				BodyText: "Hello,\n\nHere is the report.\n\n--\nJohn Doe\nSenior Engineer\nAcme Corp\n",
			},
			cfg: config.Extract{BodyPreviewChars: 800},
		},
		{
			name: "whitespace",
			msg: gmail.Message{
				Subject:  "Whitespace",
				BodyText: "  Hello \t\n  world \n\n  foo\t\tbar   \n\n   baz   ",
			},
			cfg: config.Extract{BodyPreviewChars: 800},
		},
		{
			name: "truncation",
			msg: gmail.Message{
				Subject:  "Long",
				BodyText: strings.Repeat("a", 500) + " " + strings.Repeat("😀", 10) + " " + strings.Repeat("b", 500),
			},
			cfg: config.Extract{BodyPreviewChars: 800},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToState(tc.msg, tc.cfg)
			goldenPath := filepath.Join("testdata", tc.name+".golden")
			want := readGolden(t, goldenPath)
			if got.BodyPreview != want {
				t.Errorf("BodyPreview mismatch for %s:\n got  %q\n want %q\n got len %d want len %d", tc.name, got.BodyPreview, want, len([]rune(got.BodyPreview)), len([]rune(want)))
				// Also show diff-friendly line when UPDATE_EXPECT is set
				if os.Getenv("UPDATE_EXPECT") == "1" {
					_ = os.WriteFile(goldenPath, []byte(got.BodyPreview+"\n"), 0o644)
					t.Logf("updated golden %s", goldenPath)
				}
			}
			// Ensure truncation budget respected
			if len([]rune(got.BodyPreview)) > tc.cfg.BodyPreviewChars {
				t.Errorf("BodyPreview rune len %d exceeds budget %d", len([]rune(got.BodyPreview)), tc.cfg.BodyPreviewChars)
			}
		})
	}
}

func TestState_JSONRoundTrip(t *testing.T) {
	msg := gmail.Message{
		From:           "Ana <ana@example.com>",
		FromDomain:     "example.com",
		Subject:        "Hello",
		Date:           time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		LabelIDs:       []string{"INBOX"},
		HasAttachments: false,
		BodyText:       "body",
	}
	s := ToState(msg, config.Extract{BodyPreviewChars: 800})
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if out.From != s.From || out.Subject != s.Subject || out.BodyPreview != s.BodyPreview {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", out, s)
	}
	if out.Date != "2026-09-22T10:00:00Z" {
		t.Errorf("Date JSON = %q, want RFC3339", out.Date)
	}
}
