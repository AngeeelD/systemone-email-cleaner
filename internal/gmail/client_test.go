package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/option"
)

// newFakeServer builds a client pointed at a handler. Paths are matched by
// suffix because google-api-go-client prepends its own service prefix.
func newFakeServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := newClient(context.Background(),
		option.WithoutAuthentication(),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestListMessagesFollowsPagination(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	pages := []map[string]any{
		{"messages": []map[string]string{{"id": "a"}, {"id": "b"}}, "nextPageToken": "t1"},
		{"messages": []map[string]string{{"id": "c"}}},
	}
	call := 0

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users/me/messages") {
			t.Errorf("path = %q, want it to end with /users/me/messages", r.URL.Path)
		}
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("q"))
		page := pages[call]
		call++
		mu.Unlock()
		writeJSON(t, w, page)
	})

	got, err := c.ListMessages(context.Background(), "in:inbox", 0)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("ListMessages() = %v, want [a b c]", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("made %d list calls, want 2", len(queries))
	}
	if queries[1] != "in:inbox" {
		t.Errorf("second page query = %q, want the query repeated", queries[1])
	}
}

func TestListMessagesStopsAtMax(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"messages":      []map[string]string{{"id": "a"}, {"id": "b"}, {"id": "c"}},
			"nextPageToken": "t1",
		})
	})

	got, err := c.ListMessages(context.Background(), "in:inbox", 2)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListMessages() returned %d ids, want 2", len(got))
	}
}

func TestListMessagesHandlesEmptyInbox(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{})
	})

	got, err := c.ListMessages(context.Background(), "in:inbox", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMessages() = %v, want empty", got)
	}
}

func TestListMessagesSurfacesAPIFailure(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, map[string]any{"error": map[string]any{"code": 401, "message": "bad token"}})
	})

	if _, err := c.ListMessages(context.Background(), "in:inbox", 1); err == nil {
		t.Fatal("ListMessages() error = nil, want an API error")
	}
}

func TestGetMessageRequestsFullFormat(t *testing.T) {
	var gotFormat string
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotFormat = r.URL.Query().Get("format")
		writeJSON(t, w, map[string]any{
			"id": "a",
			"payload": map[string]any{
				"mimeType": "text/plain",
				"headers":  []map[string]string{{"name": "Subject", "value": "Hola"}},
				"body":     map[string]string{"data": b64("cuerpo")},
			},
		})
	})

	got, err := c.GetMessage(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if gotFormat != "full" {
		t.Errorf("format = %q, want full", gotFormat)
	}
	if got.Subject != "Hola" || got.BodyText != "cuerpo" {
		t.Errorf("GetMessage() = %+v, want subject Hola and body cuerpo", got)
	}
}

func TestProfileReturnsEmailAddress(t *testing.T) {
	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users/me/profile") {
			t.Errorf("path = %q, want it to end with /users/me/profile", r.URL.Path)
		}
		writeJSON(t, w, map[string]string{"emailAddress": "me@example.com"})
	})

	got, err := c.Profile(context.Background())
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if got != "me@example.com" {
		t.Errorf("Profile() = %q, want me@example.com", got)
	}
}
