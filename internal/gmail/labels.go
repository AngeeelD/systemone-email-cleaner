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
