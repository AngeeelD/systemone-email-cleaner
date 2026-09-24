// Package laya implements an HTTP client for laya-serve at POST /v1/systemone.
// It is intentionally dependency-free beyond the standard library.
package laya

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

	"emailcleaner/internal/config"
	"emailcleaner/internal/extract"
)

// Answer is a single Laya choice answer with confidence.
type Answer struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}

// Answers maps question name to its answer.
type Answers map[string]Answer

// Question defines a Laya choice question.
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

// detectLangHint returns a model hint for the Router backend. It uses a
// deterministic heuristic without external dependencies:
//   - If BodyPreview+Subject+From/FromDomain contains Spanish markers
//     (ñ,á,é,í,ó,ú,ü,¿,¡) or common Spanish words (de, la, el, con, para,
//     por, cuenta, depósito, retiro, transferencia) or a .mx/.es domain,
//     return "multilingual".
//   - Otherwise return "english".
//
// The Router (aac6fef/laya-mlx 512 and aac6fef/laya-multilingual-mlx 1024)
// supports {"model":"multilingual"} or {"model":"english"} passthrough to
// force model selection per email. This hint is always set so the backend
// can route deterministically; callers should treat it as best-effort.
func detectLangHint(state extract.State) string {
	text := strings.ToLower(state.BodyPreview + " " + state.Subject + " " + state.From + " " + state.FromDomain)
	domain := strings.ToLower(strings.TrimSpace(state.FromDomain))
	if strings.HasSuffix(domain, ".mx") || strings.HasSuffix(domain, ".es") {
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

// ErrUnprocessable signals HTTP 422 from laya-serve. A single bad message
// must not stop a run — callers should treat this as skip+log.
var ErrUnprocessable = errors.New("laya: unprocessable entity")

// Client talks to laya-serve.
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// New creates a Client from config.Laya. It reads the API key from the
// environment variable named in cfg.APIKeyEnv when set; a missing variable
// means no Authorization header is sent. Timeout defaults to 30s when unset.
func New(cfg config.Laya) *Client {
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
// the answers. It is the primary entry point for the cleaner pipeline.
func (c *Client) Predict(ctx context.Context, state extract.State) (Answers, error) {
	return c.PredictWithQuestions(ctx, state, DefaultQuestions)
}

// PredictWithQuestions sends state with an explicit questions map. It exists so
// callers and tests can override or extend the taxonomy without forking the
// client. Pass nil to send no questions (useful only for testing error paths).
func (c *Client) PredictWithQuestions(ctx context.Context, state extract.State, qs Questions) (Answers, error) {
	if c.endpoint == "" {
		return nil, errors.New("laya: endpoint is empty")
	}
	if qs == nil {
		qs = DefaultQuestions
	}

	modelHint := detectLangHint(state)
	reqBody := struct {
		State     extract.State `json:"state"`
		Questions Questions     `json:"questions"`
		Model     string        `json:"model,omitempty"`
	}{
		State:     state,
		Questions: qs,
		Model:     modelHint,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("laya: marshal request: %w", err)
	}

	url := c.endpoint + "/v1/systemone"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("laya: create request: %w", err)
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
		return nil, fmt.Errorf("laya: do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("laya: read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusUnprocessableEntity: // 422
		return nil, fmt.Errorf("%w: %s", ErrUnprocessable, truncate(respBody, 500))
	case http.StatusOK, http.StatusCreated:
		// continue to decode
	default:
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("laya: unexpected status %d: %s", resp.StatusCode, truncate(respBody, 500))
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
		return nil, fmt.Errorf("laya: decode response: %w", err)
	}
	// Decode answers specifically; if missing, the error below surfaces.
	if v, ok := raw["answers"]; ok {
		if err := json.Unmarshal(v, &payload.Answers); err != nil {
			return nil, fmt.Errorf("laya: decode answers: %w", err)
		}
	} else {
		// No answers field at all — try full decode for better error message.
		if err := json.Unmarshal(respBody, &payload); err != nil {
			return nil, fmt.Errorf("laya: decode response: %w", err)
		}
		if payload.Answers == nil {
			return nil, fmt.Errorf("laya: response missing answers field: %s", truncate(respBody, 500))
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
		return nil, fmt.Errorf("laya: response missing answers field: %s", truncate(respBody, 500))
	}

	return payload.Answers, nil
}

type routingInfo struct {
	Model string `json:"model"`
}

// Ping checks laya-serve liveness via GET /health. It returns nil on 2xx,
// otherwise an error describing the failure. Context cancellation is preserved.
func (c *Client) Ping(ctx context.Context) error {
	if c.endpoint == "" {
		return errors.New("laya: endpoint is empty")
	}
	url := c.endpoint + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("laya: create ping request: %w", err)
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
		return fmt.Errorf("laya: ping: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("laya: ping unexpected status %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return nil
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
