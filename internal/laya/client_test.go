package laya

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"emailcleaner/internal/config"
	"emailcleaner/internal/extract"
)

func testState() extract.State {
	return extract.State{
		From:           "Ana <ana@example.com>",
		FromDomain:     "example.com",
		Subject:        "Invoice #4411",
		Date:           "2026-09-23T10:00:00Z",
		Labels:         []string{"INBOX"},
		HasAttachments: false,
		BodyPreview:    "Please pay invoice 4411.",
	}
}

func okResponse() map[string]any {
	return map[string]any{
		"answers": map[string]any{
			"is_junk":        map[string]any{"choice": "B", "confidence": 0.97},
			"is_person":      map[string]any{"choice": "A", "confidence": 0.82},
			"needs_action":   map[string]any{"choice": "A", "confidence": 0.71},
			"is_security":    map[string]any{"choice": "B", "confidence": 0.99},
			"is_purchase":    map[string]any{"choice": "A", "confidence": 0.88},
			"is_opportunity": map[string]any{"choice": "B", "confidence": 0.76},
		},
		"routing": map[string]any{"model": "multilingual"},
		"model":   "multilingual",
	}
}

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.Laya{
		Endpoint:  srv.URL,
		APIKeyEnv: "",
		Timeout:   config.Duration(5 * time.Second),
	}
	return New(cfg)
}

func TestPredict_Success_AllSixAnswers(t *testing.T) {
	var gotState extract.State
	var gotQuestions Questions
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q, want /v1/systemone", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var req struct {
			State     extract.State `json:"state"`
			Questions Questions     `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotState = req.State
		gotQuestions = req.Questions

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResponse())
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	ans, err := c.Predict(context.Background(), testState())
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	if len(ans) != 6 {
		t.Fatalf("answers len = %d, want 6", len(ans))
	}
	for _, key := range []string{"is_junk", "is_person", "needs_action", "is_security", "is_purchase", "is_opportunity"} {
		a, ok := ans[key]
		if !ok {
			t.Errorf("missing answer %q", key)
			continue
		}
		if a.Choice != "A" && a.Choice != "B" {
			t.Errorf("answer %q choice = %q, want A or B", key, a.Choice)
		}
		if a.Confidence < 0 || a.Confidence > 1 {
			t.Errorf("answer %q confidence = %v, want [0,1]", key, a.Confidence)
		}
	}
	// Verify state was forwarded.
	if gotState.Subject != "Invoice #4411" {
		t.Errorf("forwarded state subject = %q, want %q", gotState.Subject, "Invoice #4411")
	}
	if len(gotQuestions) != 6 {
		t.Errorf("forwarded questions len = %d, want 6", len(gotQuestions))
	}
	if _, ok := gotQuestions["is_junk"]; !ok {
		t.Error("forwarded questions missing is_junk")
	}
}

func TestPredict_APIKeyHeaderSent(t *testing.T) {
	t.Setenv("LAYA_API_KEY", "secret-123")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResponse())
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, APIKeyEnv: "LAYA_API_KEY", Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	if want := "Bearer secret-123"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestPredict_MissingKey_NoHeader(t *testing.T) {
	_ = os.Unsetenv("LAYA_API_KEY_MISSING_TEST")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResponse())
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, APIKeyEnv: "LAYA_API_KEY_MISSING_TEST", Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (no key)", gotAuth)
	}
}

func TestPredict_422ReturnsErrUnprocessable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"state too large"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err == nil {
		t.Fatal("expected error for 422, got nil")
	}
	if !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("error = %v, want ErrUnprocessable via errors.Is", err)
	}
	if !strings.Contains(err.Error(), "state too large") && !strings.Contains(err.Error(), "422") {
		t.Errorf("error message = %q, should mention body or status", err.Error())
	}
}

func TestPredict_500ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal server error`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if errors.Is(err, ErrUnprocessable) {
		t.Fatal("500 should not be ErrUnprocessable")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, should contain status 500", err.Error())
	}
}

func TestPredict_ContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResponse())
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Predict(ctx, testState())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		// http.Client may wrap the context error; also check message.
		if !strings.Contains(strings.ToLower(err.Error()), "deadline") && !strings.Contains(strings.ToLower(err.Error()), "canceled") && !strings.Contains(strings.ToLower(err.Error()), "context") {
			t.Fatalf("error = %v, want context deadline/canceled", err)
		}
	}
}

func TestPredict_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResponse())
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Predict(ctx, testState())
	if err == nil {
		t.Fatal("expected canceled error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		if !strings.Contains(strings.ToLower(err.Error()), "canceled") && !strings.Contains(strings.ToLower(err.Error()), "cancel") {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	}
}

func TestPredict_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "decode") {
		t.Errorf("error = %q, should mention decode", err.Error())
	}
}

func TestPredict_ConfidenceParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Use distinct confidences to verify parsing is not truncated.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"is_junk":        map[string]any{"choice": "A", "confidence": 0.123456},
				"is_person":      map[string]any{"choice": "B", "confidence": 0.999},
				"needs_action":   map[string]any{"choice": "A", "confidence": 0.0},
				"is_security":    map[string]any{"choice": "B", "confidence": 1.0},
				"is_purchase":    map[string]any{"choice": "B", "confidence": 0.5},
				"is_opportunity": map[string]any{"choice": "A", "confidence": 0.70001},
			},
		})
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	ans, err := c.Predict(context.Background(), testState())
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	tests := map[string]float64{
		"is_junk":        0.123456,
		"is_person":      0.999,
		"needs_action":   0.0,
		"is_security":    1.0,
		"is_purchase":    0.5,
		"is_opportunity": 0.70001,
	}
	for k, want := range tests {
		got := ans[k].Confidence
		if got != want {
			t.Errorf("answer %q confidence = %v, want %v", k, got, want)
		}
	}
}

func TestPredict_CustomQuestions(t *testing.T) {
	custom := Questions{
		"is_junk": {
			Type:         "choice",
			Instructions: "custom junk check",
			Criteria:     map[string]string{"A": "yes", "B": "no"},
		},
	}
	var gotQs Questions
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions Questions `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotQs = req.Questions
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"is_junk": map[string]any{"choice": "A", "confidence": 0.91},
			},
		})
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	ans, err := c.PredictWithQuestions(context.Background(), testState(), custom)
	if err != nil {
		t.Fatalf("PredictWithQuestions error: %v", err)
	}
	if len(gotQs) != 1 || gotQs["is_junk"].Instructions != "custom junk check" {
		t.Errorf("forwarded questions = %+v, want custom single question", gotQs)
	}
	if ans["is_junk"].Choice != "A" {
		t.Errorf("answer choice = %q, want A", ans["is_junk"].Choice)
	}
}

func TestPredict_EmptyEndpoint(t *testing.T) {
	cfg := config.Laya{Endpoint: "", Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err == nil {
		t.Fatal("expected error for empty endpoint, got nil")
	}
}

func TestPredict_NonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>bad gateway</html>`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err == nil {
		t.Fatal("expected error for 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error = %q, should contain 502", err.Error())
	}
}

func TestPredict_UnknownFieldsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"is_junk": map[string]any{"choice": "B", "confidence": 0.95, "extra": "ignore"},
			},
			"routing":      map[string]any{"model": "english", "extra": 123},
			"model":        "english",
			"extra_top":    "ignored",
			"anotherField": 42,
		})
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	ans, err := c.Predict(context.Background(), testState())
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	if ans["is_junk"].Choice != "B" {
		t.Errorf("choice = %q, want B", ans["is_junk"].Choice)
	}
}

func TestPredict_HTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(okResponse())
	}))
	t.Cleanup(srv.Close)

	// Client timeout shorter than server sleep.
	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(50 * time.Millisecond)}
	c := New(cfg)
	_, err := c.Predict(context.Background(), testState())
	if err == nil {
		t.Fatal("expected timeout error from http.Client, got nil")
	}
}

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q, want /health", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping error: %v", err)
	}
}

func TestPing_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`unavailable`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected Ping error for 503, got nil")
	}
}

func TestPing_WithAPIKey(t *testing.T) {
	t.Setenv("LAYA_API_KEY_PING", "ping-key")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Laya{Endpoint: srv.URL, APIKeyEnv: "LAYA_API_KEY_PING", Timeout: config.Duration(5 * time.Second)}
	c := New(cfg)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping error: %v", err)
	}
	if gotAuth != "Bearer ping-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer ping-key")
	}
}

func TestDefaultQuestions_Complete(t *testing.T) {
	if len(DefaultQuestions) != 6 {
		t.Fatalf("DefaultQuestions len = %d, want 6", len(DefaultQuestions))
	}
	for _, key := range []string{"is_junk", "is_person", "needs_action", "is_security", "is_purchase", "is_opportunity"} {
		q, ok := DefaultQuestions[key]
		if !ok {
			t.Errorf("missing question %q", key)
			continue
		}
		if q.Type != "choice" {
			t.Errorf("question %q type = %q, want choice", key, q.Type)
		}
		if len(q.Criteria) != 2 {
			t.Errorf("question %q criteria len = %d, want 2", key, len(q.Criteria))
		}
		if _, ok := q.Criteria["A"]; !ok {
			t.Errorf("question %q missing criteria A", key)
		}
		if _, ok := q.Criteria["B"]; !ok {
			t.Errorf("question %q missing criteria B", key)
		}
		if q.Instructions == "" {
			t.Errorf("question %q instructions empty", key)
		}
	}
}
