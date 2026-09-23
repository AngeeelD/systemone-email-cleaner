package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/option"
)

func TestEnsureLabelReturnsExistingLabelWithoutCreating(t *testing.T) {
	var mu sync.Mutex
	created := 0

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodGet:
			writeJSON(t, w, map[string]any{
				"labels": []map[string]string{
					{"id": "Label_1", "name": "cleaner/people"},
					{"id": "Label_2", "name": "INBOX"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodPost:
			mu.Lock()
			created++
			mu.Unlock()
			writeJSON(t, w, map[string]string{"id": "new", "name": "cleaner/people"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	got, err := c.EnsureLabel(context.Background(), "cleaner/people")
	if err != nil {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
	if got != "Label_1" {
		t.Errorf("EnsureLabel() = %q, want Label_1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 0 {
		t.Errorf("created %d labels, want 0 for an existing label", created)
	}
}

func TestEnsureLabelCreatesMissingLabelWithVisibility(t *testing.T) {
	var sentName string
	var sentVisibility string
	var sentMessageVisibility string

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX"}}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create body: %v", err)
		}
		sentName, _ = body["name"].(string)
		sentVisibility, _ = body["labelListVisibility"].(string)
		sentMessageVisibility, _ = body["messageListVisibility"].(string)
		writeJSON(t, w, map[string]string{"id": "Label_9", "name": sentName})
	})

	got, err := c.EnsureLabel(context.Background(), "cleaner/security")
	if err != nil {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
	if got != "Label_9" {
		t.Errorf("EnsureLabel() = %q, want Label_9", got)
	}
	if sentName != "cleaner/security" {
		t.Errorf("created name = %q, want cleaner/security", sentName)
	}
	if sentVisibility != "labelShow" {
		t.Errorf("labelListVisibility = %q, want labelShow so the label is visible", sentVisibility)
	}
	if sentMessageVisibility != "show" {
		t.Errorf("messageListVisibility = %q, want show so messages keep their label", sentMessageVisibility)
	}
}

func TestEnsureLabelSurfacesAPIFailure(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(t, w, map[string]any{"error": map[string]any{"code": 403, "message": "insufficient scope"}})
	})

	if _, err := c.EnsureLabel(context.Background(), "cleaner/people"); err == nil {
		t.Fatal("EnsureLabel() error = nil, want an API error")
	}
}

// The generated client only carries a context into the request when the call's
// Context method is used; without it, cancellation is silently dropped. The two
// tests below pass a cancelled context and require the call to fail rather than
// reach the fake server and succeed.

// TestEnsureLabelHonoursCancelledContext covers the list call. The fake server
// returns the requested label, so a list call that ignored the cancelled
// context would short-circuit and report success.
func TestEnsureLabelHonoursCancelledContext(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"labels": []map[string]string{{"id": "Label_1", "name": "cleaner/people"}},
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.EnsureLabel(ctx, "cleaner/people"); err == nil {
		t.Fatal("EnsureLabel() error = nil, want a context error on the list call")
	}
}

// cancelAfterFirstResponse cancels ctx once the first request's response has
// been fully read. That lets the list call succeed and the create call start
// with an already-cancelled context, which is the only way to prove the create
// call carries the caller's context without making the list call fail first.
type cancelAfterFirstResponse struct {
	base   http.RoundTripper
	cancel context.CancelFunc
	calls  int
}

func (t *cancelAfterFirstResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.calls++
	if t.calls == 1 {
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		t.cancel()
	}
	return resp, nil
}

// TestEnsureLabelCreateHonoursCancelledContext covers the create call. The list
// succeeds, the context is then cancelled, and a create call that ignored the
// cancelled context would still reach the fake server and return an ID.
func TestEnsureLabelCreateHonoursCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX"}}})
			return
		}
		writeJSON(t, w, map[string]string{"id": "Label_9", "name": "cleaner/people"})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	rt := &cancelAfterFirstResponse{base: http.DefaultTransport, cancel: cancel}
	c, err := newClient(ctx,
		option.WithoutAuthentication(),
		option.WithEndpoint(srv.URL),
		option.WithHTTPClient(&http.Client{Transport: rt}),
	)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	if _, err := c.EnsureLabel(ctx, "cleaner/people"); err == nil {
		t.Fatal("EnsureLabel() error = nil, want a context error on the create call")
	}
}
