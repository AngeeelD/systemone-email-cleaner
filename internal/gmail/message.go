package gmail

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
)

// Message is this project's own view of a Gmail message. It keeps the rest of
// the codebase free of Google API types.
type Message struct {
	ID             string
	ThreadID       string
	From           string
	FromDomain     string
	Subject        string
	Date           time.Time
	LabelIDs       []string
	HasAttachments bool
	BodyText       string // decoded text/plain part, when present
	BodyHTML       string // decoded text/html part, used only when there is no plain part
}

// toMessage converts a Gmail API message into the domain type. A message with
// no payload is treated as an error: silently returning an empty Message would
// classify on the subject alone and produce a confident wrong answer.
func toMessage(m *gmailapi.Message) (*Message, error) {
	if m == nil || m.Payload == nil {
		return nil, errors.New("message has no payload")
	}

	out := &Message{
		ID:       m.Id,
		ThreadID: m.ThreadId,
		LabelIDs: m.LabelIds,
	}

	for _, h := range m.Payload.Headers {
		if h == nil {
			continue
		}
		switch strings.ToLower(h.Name) {
		case "from":
			out.From = h.Value
			out.FromDomain = domainOf(h.Value)
		case "subject":
			out.Subject = h.Value
		case "date":
			if t, err := mail.ParseDate(h.Value); err == nil {
				out.Date = t
			}
			// An unparseable Date is tolerated: it is not worth failing a
			// message over a malformed header.
		}
	}

	text, html := walkParts(m.Payload, &out.HasAttachments)
	if text != "" {
		out.BodyText = text
	} else {
		out.BodyHTML = html
	}
	return out, nil
}

// walkParts returns the first text/plain and the first text/html body found in
// the MIME tree, and records whether any part is an attachment.
func walkParts(part *gmailapi.MessagePart, hasAttachments *bool) (text, html string) {
	if part == nil {
		return "", ""
	}

	if part.Filename != "" && part.Body != nil && part.Body.AttachmentId != "" {
		*hasAttachments = true
	}

	switch strings.ToLower(part.MimeType) {
	case "text/plain":
		if part.Body != nil && part.Body.Data != "" && text == "" {
			if decoded, err := decodeBody(part.Body.Data); err == nil {
				text = decoded
			}
		}
	case "text/html":
		if part.Body != nil && part.Body.Data != "" && html == "" {
			if decoded, err := decodeBody(part.Body.Data); err == nil {
				html = decoded
			}
		}
	}

	for _, child := range part.Parts {
		childText, childHTML := walkParts(child, hasAttachments)
		if text == "" && childText != "" {
			text = childText
		}
		if html == "" && childHTML != "" {
			html = childHTML
		}
	}
	return text, html
}

// decodeBody decodes Gmail's body data, which is base64url and usually padded
// inconsistently. Both encodings are tried.
func decodeBody(data string) (string, error) {
	if b, err := base64.RawURLEncoding.DecodeString(data); err == nil {
		return string(b), nil
	}
	b, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("decode body: %w", err)
	}
	return string(b), nil
}

// domainOf extracts the lowercase domain from a From header. It returns "" when
// there is no parseable address.
func domainOf(from string) string {
	value := strings.TrimSpace(from)
	if value == "" {
		return ""
	}
	if i := strings.LastIndex(value, "<"); i >= 0 {
		if j := strings.Index(value[i:], ">"); j > 1 {
			value = value[i+1 : i+j]
		}
	}
	at := strings.LastIndex(value, "@")
	if at < 0 || at == len(value)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value[at+1:]))
}
