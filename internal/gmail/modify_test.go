package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestModifyResolvesLabelNamesToIDs is the regression test for the bug where
// messages.modify was sent label NAMES ("cleaner/banking"). Gmail rejects those
// with "400 Invalid label: cleaner/banking" because the API takes label IDs.
func TestModifyResolvesLabelNamesToIDs(t *testing.T) {
	var sentAdd, sentRemove []string

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodGet:
			writeJSON(t, w, map[string]any{
				"labels": []map[string]string{
					{"id": "Label_7", "name": "cleaner/banking"},
					{"id": "INBOX", "name": "INBOX"},
					{"id": "TRASH", "name": "TRASH"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/modify") && r.Method == http.MethodPost:
			var body struct {
				AddLabelIds    []string `json:"addLabelIds"`
				RemoveLabelIds []string `json:"removeLabelIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode modify body: %v", err)
			}
			sentAdd, sentRemove = body.AddLabelIds, body.RemoveLabelIds
			writeJSON(t, w, map[string]any{"id": "msg1"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	if err := c.Modify(context.Background(), "msg1", []string{"cleaner/banking"}, []string{"INBOX"}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	if len(sentAdd) != 1 || sentAdd[0] != "Label_7" {
		t.Errorf("addLabelIds = %v, want [Label_7] (the label ID, not the name)", sentAdd)
	}
	if len(sentRemove) != 1 || sentRemove[0] != "INBOX" {
		t.Errorf("removeLabelIds = %v, want [INBOX]", sentRemove)
	}
}

// TestModifyPassesThroughSystemAndUnknownLabels pins that system labels resolve
// to themselves and an unknown name is forwarded unchanged, so the API reports
// it instead of the message being silently mislabelled.
func TestModifyPassesThroughSystemAndUnknownLabels(t *testing.T) {
	var sentAdd []string

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodGet:
			writeJSON(t, w, map[string]any{"labels": []map[string]string{{"id": "TRASH", "name": "TRASH"}}})
		case strings.HasSuffix(r.URL.Path, "/modify") && r.Method == http.MethodPost:
			var body struct {
				AddLabelIds []string `json:"addLabelIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sentAdd = body.AddLabelIds
			writeJSON(t, w, map[string]any{"id": "msg1"})
		}
	})

	if err := c.Modify(context.Background(), "msg1", []string{"TRASH", "cleaner/nope"}, nil); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	if len(sentAdd) != 2 || sentAdd[0] != "TRASH" || sentAdd[1] != "cleaner/nope" {
		t.Errorf("addLabelIds = %v, want [TRASH cleaner/nope]", sentAdd)
	}
}

// TestModifyCachesLabelList pins that the label list is fetched once, not per
// message: a 1000-message run must not list labels 1000 times.
func TestModifyCachesLabelList(t *testing.T) {
	lists := 0

	c := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me/labels") && r.Method == http.MethodGet:
			lists++
			writeJSON(t, w, map[string]any{"labels": []map[string]string{{"id": "Label_7", "name": "cleaner/banking"}}})
		case strings.HasSuffix(r.URL.Path, "/modify") && r.Method == http.MethodPost:
			writeJSON(t, w, map[string]any{"id": "msg1"})
		}
	})

	for i := 0; i < 5; i++ {
		if err := c.Modify(context.Background(), "msg1", []string{"cleaner/banking"}, nil); err != nil {
			t.Fatalf("Modify() error = %v", err)
		}
	}
	if lists != 1 {
		t.Errorf("labels listed %d times, want 1 (the list must be cached)", lists)
	}
}
