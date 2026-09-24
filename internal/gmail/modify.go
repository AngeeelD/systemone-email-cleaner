package gmail

import (
	"context"
	"fmt"

	gmailapi "google.golang.org/api/gmail/v1"
)

// Modify applies add/remove label IDs to a single message.
func (c *Client) Modify(ctx context.Context, messageID string, add, remove []string) error {
	req := &gmailapi.ModifyMessageRequest{
		AddLabelIds:    add,
		RemoveLabelIds: remove,
	}
	_, err := c.users.Messages.Modify("me", messageID, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("modify message %s: %w", messageID, err)
	}
	return nil
}

// BatchModify applies add/remove to many messages, up to 1000 per call.
func (c *Client) BatchModify(ctx context.Context, ids, add, remove []string) error {
	// Chunk into 1000 per Gmail limit.
	for start := 0; start < len(ids); start += 1000 {
		end := start + 1000
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		req := &gmailapi.BatchModifyMessagesRequest{
			Ids:            chunk,
			AddLabelIds:    add,
			RemoveLabelIds: remove,
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
