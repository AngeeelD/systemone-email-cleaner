package gmail

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/oauth2"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// maxPageSize is the Gmail API maximum for messages.list.
const maxPageSize = 500

// Client is a thin wrapper over the Gmail API that returns domain types.
type Client struct {
	users *gmailapi.UsersService

	// labelsMu guards labelIDs, a label name -> ID cache loaded on first use.
	// Gmail's modify endpoints take label IDs, and a run touches many messages,
	// so the label list is fetched once and reused instead of per message.
	labelsMu sync.Mutex
	labelIDs map[string]string
}

// NewClient builds a client authenticated with the given token source.
func NewClient(ctx context.Context, ts oauth2.TokenSource) (*Client, error) {
	return newClient(ctx, option.WithTokenSource(ts))
}

// newClient is the test seam: tests pass option.WithoutAuthentication() and
// option.WithEndpoint(server.URL) to route traffic at a fake.
func newClient(ctx context.Context, opts ...option.ClientOption) (*Client, error) {
	svc, err := gmailapi.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}
	return &Client{users: svc.Users}, nil
}

// ListMessages returns message IDs matching query, newest first. A max of zero
// or less means no limit.
func (c *Client) ListMessages(ctx context.Context, query string, max int) ([]string, error) {
	var ids []string
	pageToken := ""

	for {
		pageSize := maxPageSize
		if max > 0 {
			remaining := max - len(ids)
			if remaining <= 0 {
				break
			}
			if remaining < pageSize {
				pageSize = remaining
			}
		}

		// Context(ctx) must be set on every iteration: the generated client
		// only carries a context into the request through this method, and the
		// call is rebuilt on each page.
		call := c.users.Messages.List("me").Q(query).MaxResults(int64(pageSize)).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list messages: %w", err)
		}

		for _, m := range resp.Messages {
			if m == nil || m.Id == "" {
				continue
			}
			ids = append(ids, m.Id)
			// Cap here rather than trusting MaxResults: the API is free to
			// return more than asked, and the caller's limit is a contract.
			if max > 0 && len(ids) == max {
				return ids, nil
			}
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return ids, nil
}

// GetMessage fetches one message with format=full, which returns both headers
// and body for the same 20 quota units that format=metadata costs.
func (c *Client) GetMessage(ctx context.Context, id string) (*Message, error) {
	m, err := c.users.Messages.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("get message %s: %w", id, err)
	}
	return toMessage(m)
}

// Profile returns the authenticated account's email address.
func (c *Client) Profile(ctx context.Context) (string, error) {
	p, err := c.users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	return p.EmailAddress, nil
}
