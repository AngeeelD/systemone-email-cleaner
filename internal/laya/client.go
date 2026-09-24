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

// DefaultQuestions is the seven-question taxonomy defined verbatim in the
// design spec (plus is_banking). Each question is a 2-option choice with neutral keys A/B.
var DefaultQuestions = Questions{
	"is_junk": {
		Type:         "choice",
		Instructions: "Is this email unwanted bulk mail the recipient should not keep? Count as junk: marketing and promotions, newsletters the recipient did not sign up for, mass automated notifications, spam, phishing. Answer no if a real person wrote to the recipient, or if it may contain an invoice, an order, an account or security alert, or anything the recipient may need to act on. When unsure, answer no. Bank deposits, withdrawals, transfers, and balance alerts are NOT junk; when in doubt answer B. Do not count account alerts as junk.",
		Criteria: map[string]string{
			"A": "yes, unwanted bulk or junk mail",
			"B": "no, this is not junk",
		},
	},
	"is_person": {
		Type:         "choice",
		Instructions: "Was this email written by a real human being addressing the recipient directly, as opposed to an automated system, mailing list, or bulk sender?",
		Criteria: map[string]string{
			"A": "yes, written by a real person",
			"B": "no, automated or bulk",
		},
	},
	"needs_action": {
		Type:         "choice",
		Instructions: "Does this email expect a response or an action from the recipient? Count: invoices to pay, contracts to sign, deadlines, questions asked directly. Do not count: informational notices, order confirmations the recipient only files, marketing.",
		Criteria: map[string]string{
			"A": "yes, the recipient must act or reply",
			"B": "no action is required",
		},
	},
	"is_security": {
		Type:         "choice",
		Instructions: "Is this an account or security notice from a service provider? Count: sign-in and access alerts, password changes, two-factor codes, banking and card transaction alerts, suspicious activity warnings, breach notifications.",
		Criteria: map[string]string{
			"A": "yes, account or security notice",
			"B": "no, not a security notice",
		},
	},
	"is_purchase": {
		Type:         "choice",
		Instructions: "Is this about a purchase or a delivery? Count: order confirmations, receipts, invoices for something bought, shipping and tracking updates, reservations, active subscription notices. Also count: order status updates (pedido confirmado/enviado/entregado/en proceso), delivery tracking, receipts (recibo de pago), tickets (Su Ticket).",
		Criteria: map[string]string{
			"A": "yes, purchase, receipt or delivery",
			"B": "no, not about a purchase",
		},
	},
	"is_opportunity": {
		Type:         "choice",
		Instructions: "Is this a professional opportunity the recipient may want to act on? Count: recruiters and job offers, freelance or contract proposals, networking, collaboration requests, commercial leads. Answer no for mass job-alert digests unless they name the recipient for a specific role.",
		Criteria: map[string]string{
			"A": "yes, a professional opportunity",
			"B": "no, not an opportunity",
		},
	},
	"is_banking": {
		Type:         "choice",
		Instructions: "Is this a bank or fintech notification about a deposit, withdrawal, transfer, card charge, balance alert, or account statement? Count: depósitos, retiros, transferencias, cargos, saldo, estado de cuenta, alta de destinatario. Answer yes only for financial transaction notifications, not for marketing about banking products.",
		Criteria: map[string]string{
			"A": "yes, bank/fintech transaction notification",
			"B": "no, not a banking notification",
		},
	},
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

	reqBody := struct {
		State     extract.State `json:"state"`
		Questions Questions     `json:"questions"`
	}{
		State:     state,
		Questions: qs,
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
