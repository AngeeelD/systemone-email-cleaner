// Package systemone implements an HTTP client for System One server at POST /v1/systemone.
// It is intentionally dependency-free beyond the standard library.
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/extract"
)

// Answer is a single choice answer with confidence and optional token probabilities.
type Answer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Answers maps question name to its answer.
type Answers map[string]Answer

// Question defines a choice question.
type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

// Questions maps question name to its definition.
type Questions map[string]Question

// DefaultQuestions is the collapsed five-question descriptive taxonomy. Each
// question is a 2-option choice with neutral keys A/B. Instructions are
// descriptive ("which best describes... / who is the sender / what is the
// email about") rather than decision-oriented to reduce overconfidence.
// Collapsed from 7 to 5 by removing the noisiest binaries (needs_action and
// is_opportunity) which caused 0.98-1.00 false positives on marketing.
var DefaultQuestions = Questions{
	"is_junk": {
		Type:         "choice",
		Instructions: "Which best describes this email? A) Unwanted bulk, marketing, promo, newsletter blast not addressed to you personally. B) Expected personal or transactional message.",
		Criteria: map[string]string{
			"A": "unwanted bulk or marketing",
			"B": "personal or transactional",
		},
	},
	"is_person": {
		Type:         "choice",
		Instructions: "Who is the sender? A) A single real person writing directly to you. B) An automated system, noreply, or bulk sender.",
		Criteria: map[string]string{
			"A": "real person direct",
			"B": "automated or bulk",
		},
	},
	"is_banking": {
		Type:         "choice",
		Instructions: "What is the email about? A) A specific bank or fintech transaction: deposit, withdrawal, transfer, card charge, balance, statement, recipient registration. B) Not about a bank transaction.",
		Criteria: map[string]string{
			"A": "bank transaction notification",
			"B": "not bank related",
		},
	},
	"is_purchase": {
		Type:         "choice",
		Instructions: "What is the email about? A) A purchase, order, receipt, invoice, shipping, delivery, reservation, ticket. B) Not about purchase/delivery.",
		Criteria: map[string]string{
			"A": "purchase/order/receipt/delivery",
			"B": "not purchase",
		},
	},
	"is_security": {
		Type:         "choice",
		Instructions: "What is the email about? A) An account or security event: sign-in alert, password change, 2FA code, banking alert, suspicious activity. B) Not security.",
		Criteria: map[string]string{
			"A": "account or security event",
			"B": "not security",
		},
	},
}

// detectLangHint returns a model hint for the Router backend from a paragraph
// string. It uses a deterministic heuristic without external dependencies:
//   - If the paragraph contains Spanish markers (ñ,á,é,í,ó,ú,ü,¿,¡) or common
//     Spanish words (de, la, el, con, para, por, cuenta, depósito, retiro,
//     transferencia) or a .mx/.es domain, return "multilingual".
//   - Otherwise return "english".
//
// The Router (aac6fef/laya-mlx 512 and aac6fef/laya-multilingual-mlx 1024)
// supports {"model":"multilingual"} or {"model":"english"} passthrough to
// force model selection per email. This hint is always set so the backend
// can route deterministically; callers should treat it as best-effort.
func detectLangHint(paragraph string) string {
	text := strings.ToLower(paragraph)
	if matched, _ := regexp.MatchString(`\.mx\b`, text); matched {
		return "multilingual"
	}
	if matched, _ := regexp.MatchString(`\.es\b`, text); matched {
		return "multilingual"
	}
	for _, ch := range []string{"ñ", "á", "é", "í", "ó", "ú", "ü", "¿", "¡"} {
		if strings.Contains(text, ch) {
			return "multilingual"
		}
	}
	spanishWords := []string{"de", "la", "el", "con", "para", "por", "cuenta", "depósito", "retiro", "transferencia"}
	for _, w := range spanishWords {
		pattern := `\b` + regexp.QuoteMeta(w) + `\b`
		if matched, _ := regexp.MatchString(pattern, text); matched {
			return "multilingual"
		}
	}
	return "english"
}

// detectLangHintFromState is a compatibility shim that builds a paragraph-like
// string from the legacy extract.State for callers still using the State object.
func detectLangHintFromState(state extract.State) string {
	para := state.BodyPreview + " " + state.Subject + " " + state.From + " " + state.FromDomain
	return detectLangHint(para)
}

// ErrUnprocessable signals HTTP 422 from System One server. A single bad message
// must not stop a run — callers should treat this as skip+log.
var ErrUnprocessable = errors.New("systemone: unprocessable entity")

// Client talks to System One server.
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// New creates a Client from config.SystemOne. It reads the API key from the
// environment variable named in cfg.APIKeyEnv when set; a missing variable
// means no Authorization header is sent. Timeout defaults to 30s when unset.
func New(cfg config.SystemOne) *Client {
	apiKey := ""
	if cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
	}
	timeout := cfg.Timeout.Std()
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	return &Client{
		endpoint:   endpoint,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Predict sends state with DefaultQuestions to POST /v1/systemone and returns
// the answers. It is kept for backward compatibility and delegates to the
// paragraph path. New callers should use PredictParagraph.
func (c *Client) Predict(ctx context.Context, state extract.State) (Answers, error) {
	return c.PredictWithQuestions(ctx, state, DefaultQuestions)
}

// PredictWithQuestions sends state with an explicit questions map. Deprecated:
// use PredictParagraphWithQuestions with a paragraph string.
func (c *Client) PredictWithQuestions(ctx context.Context, state extract.State, qs Questions) (Answers, error) {
	para := stateToParagraph(state)
	return c.PredictParagraphWithQuestions(ctx, para, qs)
}

// PredictParagraph sends a natural-language paragraph with DefaultQuestions.
func (c *Client) PredictParagraph(ctx context.Context, paragraph string) (Answers, error) {
	return c.PredictParagraphWithQuestions(ctx, paragraph, DefaultQuestions)
}

// PredictParagraphWithQuestions sends paragraph with an explicit questions map.
func (c *Client) PredictParagraphWithQuestions(ctx context.Context, paragraph string, qs Questions) (Answers, error) {
	if c.endpoint == "" {
		return nil, errors.New("systemone: endpoint is empty")
	}
	if qs == nil {
		qs = DefaultQuestions
	}

	modelHint := detectLangHint(paragraph)
	reqBody := struct {
		State     string    `json:"state"`
		Questions Questions `json:"questions"`
		Model     string    `json:"model,omitempty"`
	}{
		State:     paragraph,
		Questions: qs,
		Model:     modelHint,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("systemone: marshal request: %w", err)
	}

	url := c.endpoint + "/v1/systemone"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("systemone: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Preserve context cancellation/timeout for callers.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// http.Client wraps context errors; check via context cause.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("systemone: do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("systemone: read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusUnprocessableEntity: // 422
		return nil, fmt.Errorf("%w: %s", ErrUnprocessable, truncate(respBody, 500))
	case http.StatusOK, http.StatusCreated:
		// continue to decode
	default:
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("systemone: unexpected status %d: %s", resp.StatusCode, truncate(respBody, 500))
		}
	}

	// Flexible response: unknown fields are ignored. Both "answers" and
	// top-level routing/model are optional.
	var payload struct {
		Answers Answers      `json:"answers"`
		Routing *routingInfo `json:"routing"`
		Model   string       `json:"model"`
		// Some deployments may return routing as a plain string.
		RawRouting json.RawMessage `json:"-"`
	}
	// Use a map-aware decode to handle alternative shapes gracefully.
	// First try the typed struct; if routing is a string it will still decode
	// because we keep it as interface fallback below.
	type rawResp map[string]json.RawMessage
	var raw rawResp
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("systemone: decode response: %w", err)
	}
	// Decode answers specifically; if missing, the error below surfaces.
	if v, ok := raw["answers"]; ok {
		if err := json.Unmarshal(v, &payload.Answers); err != nil {
			return nil, fmt.Errorf("systemone: decode answers: %w", err)
		}
	} else {
		// No answers field at all — try full decode for better error message.
		if err := json.Unmarshal(respBody, &payload); err != nil {
			return nil, fmt.Errorf("systemone: decode response: %w", err)
		}
		if payload.Answers == nil {
			return nil, fmt.Errorf("systemone: response missing answers field: %s", truncate(respBody, 500))
		}
	}
	// Attempt to capture routing/model for completeness; ignore errors.
	if v, ok := raw["routing"]; ok {
		var ri routingInfo
		if err := json.Unmarshal(v, &ri); err == nil {
			payload.Routing = &ri
		}
	}
	if v, ok := raw["model"]; ok {
		var m string
		if err := json.Unmarshal(v, &m); err == nil {
			payload.Model = m
		}
	}

	if payload.Answers == nil {
		return nil, fmt.Errorf("systemone: response missing answers field: %s", truncate(respBody, 500))
	}

	return payload.Answers, nil
}

type routingInfo struct {
	Model string `json:"model"`
}

// Ping checks System One server liveness via GET /health. It returns nil on 2xx,
// otherwise an error describing the failure. Context cancellation is preserved.
func (c *Client) Ping(ctx context.Context) error {
	if c.endpoint == "" {
		return errors.New("systemone: endpoint is empty")
	}
	url := c.endpoint + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("systemone: create ping request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("systemone: ping: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("systemone: ping unexpected status %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return nil
}

// stateToParagraph converts a legacy State JSON object into the paragraph
// format used by PredictParagraph. It mirrors extract.ToParagraph but
// operates on already-extracted State fields.
func stateToParagraph(state extract.State) string {
	var dateStr string
	if state.Date != "" {
		// State.Date is RFC3339; extract short date 2006-01-02 for paragraph.
		if t, err := time.Parse(time.RFC3339, state.Date); err == nil {
			dateStr = t.Format("2006-01-02")
		} else if len(state.Date) >= 10 {
			dateStr = state.Date[:10]
		} else {
			dateStr = state.Date
		}
	}
	var b strings.Builder
	if state.FromDomain != "" {
		b.WriteString("From: ")
		b.WriteString(state.From)
		b.WriteString(" (")
		b.WriteString(state.FromDomain)
		b.WriteString(")")
	} else {
		b.WriteString("From: ")
		b.WriteString(state.From)
	}
	b.WriteString("\nSubject: ")
	b.WriteString(state.Subject)
	b.WriteString("\nDate: ")
	b.WriteString(dateStr)
	b.WriteString("\nBody: ")
	b.WriteString(state.BodyPreview)
	if state.HasAttachments {
		b.WriteString("\nHas attachment: yes")
	}
	return b.String()
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
