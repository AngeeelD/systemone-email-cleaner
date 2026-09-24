// Package extract converts a Gmail Message into a compact State suitable for
// the Laya decision model. It is pure (no I/O, no network) and enforces the
// token budget by truncating the body preview to a configurable rune count.
package extract

import (
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"emailcleaner/internal/config"
	"emailcleaner/internal/gmail"
)

// State is the JSON payload sent to Laya. It mirrors the schema from the
// design spec.
type State struct {
	From           string   `json:"from"`
	FromDomain     string   `json:"from_domain"`
	Subject        string   `json:"subject"`
	Date           string   `json:"date"` // RFC3339; empty when unknown
	Labels         []string `json:"labels,omitempty"`
	HasAttachments bool     `json:"has_attachments"`
	BodyPreview    string   `json:"body_preview"` // truncated to BodyPreviewChars runes
}

const defaultPreviewChars = 800

var (
	// On ... wrote:  e.g. "On Mon, 22 Sep 2026 at 10:00 AM, Ana wrote:"
	reOnWrote = regexp.MustCompile(`(?i)^On\s.+wrote:\s*$`)
	// El ... escribió/escribio: e.g. "El lun, 22 sept 2026 a las 10:00, Ana escribió:"
	reElEscribio = regexp.MustCompile(`(?i)^El\s.+escribi[óo]:\s*$`)
)

// StateFrom returns the Laya State for msg using the default budget (800 runes).
// It is provided for spec compatibility with the documented `extract.State(msg)`
// concept. Prefer ToState when a config value is available.
func StateFrom(msg gmail.Message) State {
	return ToState(msg, config.Extract{BodyPreviewChars: defaultPreviewChars})
}

// ToState converts msg into a State, applying HTML stripping, quoted-reply
// trimming, signature trimming, whitespace collapse, and rune-aware truncation.
// previewChars is read from cfg.BodyPreviewChars; values <=0 fall back to 800.
func ToState(msg gmail.Message, cfg config.Extract) State {
	previewChars := cfg.BodyPreviewChars
	if previewChars <= 0 {
		previewChars = defaultPreviewChars
	}

	// Domain: prefer the domain already extracted by gmail.Message, but
	// recompute from From as a fallback for hand-constructed messages.
	domain := msg.FromDomain
	if domain == "" {
		domain = domainOf(msg.From)
	}

	// Date: empty when the header was missing or unparseable.
	var dateStr string
	if !msg.Date.IsZero() {
		dateStr = msg.Date.Format(time.RFC3339)
	}

	// Body selection and pipeline.
	raw := msg.BodyText
	if raw == "" {
		raw = msg.BodyHTML
		if raw != "" {
			raw = htmlToText(raw)
		}
	}
	// Pipeline order per spec: HTML already handled, then quoted, signature, whitespace, budget.
	raw = trimQuotedReply(raw)
	raw = trimSignature(raw)
	raw = collapseWhitespace(raw)
	raw = truncateRunes(raw, previewChars)

	// Copy labels to avoid aliasing the input slice.
	var labels []string
	if len(msg.LabelIDs) > 0 {
		labels = make([]string, len(msg.LabelIDs))
		copy(labels, msg.LabelIDs)
	}

	return State{
		From:           msg.From,
		FromDomain:     domain,
		Subject:        msg.Subject,
		Date:           dateStr,
		Labels:         labels,
		HasAttachments: msg.HasAttachments,
		BodyPreview:    raw,
	}
}

// htmlToText strips HTML tags and returns visible text. Script, style, and
// head content are ignored. Block elements introduce a newline so that
// subsequent whitespace collapse does not join words that were in separate
// paragraphs. If the input contains no '<' it is returned unchanged.
func htmlToText(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	inScriptOrStyle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.EndTagToken:
			name, _ := z.TagName()
			tag := strings.ToLower(string(name))
			if tag == "script" || tag == "style" || tag == "head" {
				if tt == html.StartTagToken {
					inScriptOrStyle = true
				} else {
					inScriptOrStyle = false
				}
				continue
			}
			if inScriptOrStyle {
				continue
			}
			if isBlock(tag) {
				b.WriteString("\n")
			}
		case html.TextToken:
			if inScriptOrStyle {
				continue
			}
			txt := string(z.Text())
			// Tokenizer may leave some entities encoded; unescape for safety.
			txt = html.UnescapeString(txt)
			b.WriteString(txt)
		case html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := strings.ToLower(string(name))
			if tag == "br" || tag == "hr" {
				b.WriteString("\n")
			} else if isBlock(tag) {
				b.WriteString("\n")
			}
			// Void elements like <br> may also be tokenised as StartTag; handled above.
		}
	}
}

func isBlock(tag string) bool {
	switch tag {
	case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
		"li", "ul", "ol", "tr", "table", "blockquote", "hr",
		"section", "article", "header", "footer", "pre", "br":
		return true
	default:
		return false
	}
}

// trimQuotedReply drops quoted history. Everything from the first matching
// marker to the end of the body is discarded. Markers are:
//   - a line whose left-trimmed content starts with `>`
//   - `On ... wrote:` (English Gmail)
//   - `El ... escribió:` / `escribio:` (Spanish Gmail)
func trimQuotedReply(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // blank lines are not markers
		}
		if isQuoteHeader(trimmed) {
			return strings.Join(lines[:i], "\n")
		}
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), ">") {
			return strings.Join(lines[:i], "\n")
		}
	}
	return s
}

func isQuoteHeader(trimmed string) bool {
	return reOnWrote.MatchString(trimmed) || reElEscribio.MatchString(trimmed)
}

// trimSignature drops the signature delimited by a line that is exactly `--`
// after trimming spaces. The delimiter line and everything after it is removed.
func trimSignature(s string) string {
	if s == "" {
		return s
	}
	sNorm := strings.ReplaceAll(s, "\r\n", "\n")
	sNorm = strings.ReplaceAll(sNorm, "\r", "\n")
	lines := strings.Split(sNorm, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "--" {
			return strings.Join(lines[:i], "\n")
		}
	}
	return s
}

// collapseWhitespace replaces any run of Unicode whitespace with a single
// space and trims leading/trailing space.
func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// truncateRunes truncates s to at most n runes. If n <= 0 the empty string is
// returned. Truncation is rune-aware so multi-byte characters (e.g. emoji)
// are not split.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// domainOf extracts the lowercase domain from a From header. It mirrors
// gmail.domainOf and is used as a fallback when Message.FromDomain is empty.
func domainOf(from string) string {
	value := strings.TrimSpace(from)
	if value == "" {
		return ""
	}
	if i := strings.LastIndex(value, "<"); i >= 0 {
		if j := strings.Index(value[i:], ">"); j > 1 {
			value = value[i+1 : i+j]
		}
	}
	at := strings.LastIndex(value, "@")
	if at < 0 || at == len(value)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value[at+1:]))
}
