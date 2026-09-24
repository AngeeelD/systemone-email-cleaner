// Package extract converts a Gmail Message into a compact State suitable for
// the Laya decision model. It is pure (no I/O, no network) and enforces the
// token budget by truncating the body preview to a configurable rune count.
package extract

import (
	"fmt"
	"regexp"
	"strings"

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
	Date           string   `json:"date"` // short YYYY-MM-DD; empty when unknown
	Labels         []string `json:"labels,omitempty"`
	HasAttachments bool     `json:"has_attachments"`
	BodyPreview    string   `json:"body_preview"` // truncated sandwich to budget runes
}

const defaultPreviewChars = 800
const defaultPreviewCharsMultilingual = 1600

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
// trimming, signature trimming, whitespace collapse, dynamic budget selection,
// and sandwich signal duplication.
func ToState(msg gmail.Message, cfg config.Extract) State {
	previewChars := chooseBudget(msg, cfg)

	// Domain: prefer the domain already extracted by gmail.Message, but
	// recompute from From as a fallback for hand-constructed messages.
	domain := msg.FromDomain
	if domain == "" {
		domain = domainOf(msg.From)
	}

	// Date: short YYYY-MM-DD to save tokens; empty when unknown.
	var dateStr string
	if !msg.Date.IsZero() {
		dateStr = msg.Date.Format("2006-01-02")
	}

	// Body selection and pipeline (without truncation — sandwich handles budget).
	raw := msg.BodyText
	if raw == "" {
		raw = msg.BodyHTML
		if raw != "" {
			raw = htmlToText(raw)
		}
	}
	raw = trimQuotedReply(raw)
	raw = trimSignature(raw)
	raw = collapseWhitespace(raw)

	// Sandwich signal duplication.
	bodyPreview := buildSandwich(msg.From, domain, msg.Subject, dateStr, msg.HasAttachments, raw, previewChars)

	// Drop low-signal Labels field entirely (omitempty will omit).
	return State{
		From:           msg.From,
		FromDomain:     domain,
		Subject:        msg.Subject,
		Date:           dateStr,
		Labels:         nil,
		HasAttachments: msg.HasAttachments,
		BodyPreview:    bodyPreview,
	}
}

// chooseBudget selects 800 for English and 1600 for multilingual content.
func chooseBudget(msg gmail.Message, cfg config.Extract) int {
	englishBudget := cfg.BodyPreviewChars
	if englishBudget <= 0 {
		englishBudget = defaultPreviewChars
	}
	multiBudget := cfg.BodyPreviewCharsMultilingual
	if multiBudget <= 0 {
		multiBudget = defaultPreviewCharsMultilingual
	}
	if isMultilingual(msg) {
		return multiBudget
	}
	return englishBudget
}

// isMultilingual detects Spanish/multilingual content via markers, keywords, or domain.
func isMultilingual(msg gmail.Message) bool {
	text := strings.ToLower(msg.Subject + " " + msg.BodyText + " " + msg.BodyHTML + " " + msg.From)
	domain := strings.ToLower(strings.TrimSpace(msg.FromDomain))
	if domain == "" {
		domain = strings.ToLower(domainOf(msg.From))
	}
	if strings.HasSuffix(domain, ".mx") || strings.HasSuffix(domain, ".es") {
		return true
	}
	for _, ch := range []string{"ñ", "á", "é", "í", "ó", "ú", "ü", "¿", "¡"} {
		if strings.Contains(text, ch) {
			return true
		}
	}
	spanishKeywords := []string{"de", "la", "el", "cuenta", "depósito", "retiro", "transferencia", "banco"}
	for _, w := range spanishKeywords {
		pattern := `\b` + regexp.QuoteMeta(w) + `\b`
		if matched, _ := regexp.MatchString(pattern, text); matched {
			return true
		}
	}
	return false
}

// buildSandwich composes the body preview with duplicated sender/subject/date
// signals at both ends to mitigate recency bias, verbalizes HasAttachments,
// and adds a bank notification hint when the sender looks like a bank.
func buildSandwich(from, domain, subject, shortDate string, hasAttachments bool, body string, budget int) string {
	if budget <= 0 {
		return ""
	}
	prefix := fmt.Sprintf("Sender: %s (%s) | Subject: %s | Date: %s | ", from, domain, subject, shortDate)
	if hasAttachments {
		prefix += "Attachment: yes | "
	} else {
		prefix += "Attachment: none | "
	}
	lowerFrom := strings.ToLower(from + " " + domain)
	if strings.Contains(lowerFrom, "banamex") || strings.Contains(lowerFrom, "banco") || strings.Contains(lowerFrom, "santander") || strings.Contains(lowerFrom, "bbva") {
		prefix += "bank notification | "
	}
	suffix := fmt.Sprintf(" | Sender: %s %s, Subject: %s", from, domain, subject)
	bodyLabel := "Body: "
	overhead := len([]rune(prefix)) + len([]rune(bodyLabel)) + len([]rune(suffix))
	bodyBudget := budget - overhead
	if bodyBudget < 0 {
		bodyBudget = 0
	}
	truncatedBody := truncateRunes(body, bodyBudget)
	full := prefix + bodyLabel + truncatedBody + suffix
	return truncateRunes(full, budget)
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
