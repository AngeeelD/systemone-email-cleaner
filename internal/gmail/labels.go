package gmail

import (
	"context"
	"fmt"

	gmailapi "google.golang.org/api/gmail/v1"
)

// EnsureLabel returns the ID of the named label, creating it when absent.
// It is idempotent, so `setup` can be re-run any number of times.
//
// Gmail nests labels by "/", so creating "cleaner/people" also creates the
// "cleaner" parent.
func (c *Client) EnsureLabel(ctx context.Context, name string) (string, error) {
	// Context(ctx) must be set on every generated call: the client only carries
	// a context into the request through this method, and without it the
	// caller's cancellation and deadlines are silently dropped.
	list, err := c.users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("list labels: %w", err)
	}
	for _, l := range list.Labels {
		if l != nil && l.Name == name {
			return l.Id, nil
		}
	}

	created, err := c.users.Labels.Create("me", &gmailapi.Label{
		Name:                  name,
		LabelListVisibility:   "labelShow",
		MessageListVisibility: "show",
	}).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("create label %s: %w", name, err)
	}
	return created.Id, nil
}

// labelIDsByLabelName returns a name -> ID map of every label in the mailbox,
// fetching it once and caching it for the rest of the process.
func (c *Client) labelIDsByLabelName(ctx context.Context) (map[string]string, error) {
	c.labelsMu.Lock()
	if c.labelIDs != nil {
		m := c.labelIDs
		c.labelsMu.Unlock()
		return m, nil
	}
	c.labelsMu.Unlock()

	list, err := c.users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	m := make(map[string]string, len(list.Labels))
	for _, l := range list.Labels {
		if l != nil {
			m[l.Name] = l.Id
		}
	}

	c.labelsMu.Lock()
	c.labelIDs = m
	c.labelsMu.Unlock()
	return m, nil
}

// toLabelIDs translates label names into the IDs Gmail's modify endpoints
// require. System labels (INBOX, TRASH, UNREAD, ...) already have id == name and
// resolve to themselves; a name that is not in the mailbox is passed through, so
// the API reports the unknown label instead of the message being mislabelled.
func (c *Client) toLabelIDs(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return names, nil
	}
	byName, err := c.labelIDsByLabelName(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(names))
	for i, name := range names {
		if id, ok := byName[name]; ok {
			out[i] = id
		} else {
			out[i] = name
		}
	}
	return out, nil
}
