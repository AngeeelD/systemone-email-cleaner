package gmail

import (
	"context"
	"fmt"

	gmailapi "google.golang.org/api/gmail/v1"
)

// Modify applies add/remove label names to a single message. Gmail's API takes
// label IDs, so names are resolved first (see toLabelIDs).
func (c *Client) Modify(ctx context.Context, messageID string, add, remove []string) error {
	addIDs, err := c.toLabelIDs(ctx, add)
	if err != nil {
		return fmt.Errorf("modify message %s: resolve add labels: %w", messageID, err)
	}
	removeIDs, err := c.toLabelIDs(ctx, remove)
	if err != nil {
		return fmt.Errorf("modify message %s: resolve remove labels: %w", messageID, err)
	}
	req := &gmailapi.ModifyMessageRequest{
		AddLabelIds:    addIDs,
		RemoveLabelIds: removeIDs,
	}
	_, err = c.users.Messages.Modify("me", messageID, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("modify message %s: %w", messageID, err)
	}
	return nil
}

// BatchModify applies add/remove label names to many messages, up to 1000 per
// call. Names are resolved to IDs the same way Modify does.
func (c *Client) BatchModify(ctx context.Context, ids, add, remove []string) error {
	addIDs, err := c.toLabelIDs(ctx, add)
	if err != nil {
		return fmt.Errorf("batch modify: resolve add labels: %w", err)
	}
	removeIDs, err := c.toLabelIDs(ctx, remove)
	if err != nil {
		return fmt.Errorf("batch modify: resolve remove labels: %w", err)
	}
	// Chunk into 1000 per Gmail limit.
	for start := 0; start < len(ids); start += 1000 {
		end := start + 1000
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		req := &gmailapi.BatchModifyMessagesRequest{
			Ids:            chunk,
			AddLabelIds:    addIDs,
			RemoveLabelIds: removeIDs,
		}
		if err := c.users.Messages.BatchModify("me", req).Context(ctx).Do(); err != nil {
			return fmt.Errorf("batch modify: %w", err)
		}
	}
	return nil
}

// Trash moves a message to Trash.
func (c *Client) Trash(ctx context.Context, messageID string) error {
	_, err := c.users.Messages.Trash("me", messageID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("trash message %s: %w", messageID, err)
	}
	return nil
}

// Untrash removes a message from Trash.
func (c *Client) Untrash(ctx context.Context, messageID string) error {
	_, err := c.users.Messages.Untrash("me", messageID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("untrash message %s: %w", messageID, err)
	}
	return nil
}
