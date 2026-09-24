package gmail

import (
	"encoding/base64"
	"testing"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
)

func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func header(name, value string) *gmailapi.MessagePartHeader {
	return &gmailapi.MessagePartHeader{Name: name, Value: value}
}

func TestDomainOf(t *testing.T) {
	tests := []struct {
		name string
		from string
		want string
	}{
		{"bare address", "user@example.com", "example.com"},
		{"display name", "Ana Gomez <ana@Example.COM>", "example.com"},
		{"quoted display name", "\"Gomez, Ana\" <ana@mail.example.com>", "mail.example.com"},
		{"surrounding spaces", "  ana@example.com  ", "example.com"},
		{"no at sign", "postmaster", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domainOf(tt.from); got != tt.want {
				t.Errorf("domainOf(%q) = %q, want %q", tt.from, got, tt.want)
			}
		})
	}
}

func TestToMessageReadsHeadersAndPlainBody(t *testing.T) {
	date := "Mon, 22 Sep 2026 14:03:11 -0500"
	m := &gmailapi.Message{
		Id:       "18f1a",
		ThreadId: "18f1a",
		LabelIds: []string{"INBOX", "UNREAD"},
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/alternative",
			Headers: []*gmailapi.MessagePartHeader{
				header("From", "Ana Gomez <ana@example.com>"),
				header("Subject", "Factura #4411"),
				header("Date", date),
			},
			Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: b64("Hola, adjunto la factura.")}},
				{MimeType: "text/html", Body: &gmailapi.MessagePartBody{Data: b64("<p>Hola</p>")}},
			},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}

	if got.ID != "18f1a" || got.ThreadID != "18f1a" {
		t.Errorf("ID/ThreadID = %q/%q, want 18f1a/18f1a", got.ID, got.ThreadID)
	}
	if got.From != "Ana Gomez <ana@example.com>" {
		t.Errorf("From = %q", got.From)
	}
	if got.FromDomain != "example.com" {
		t.Errorf("FromDomain = %q, want example.com", got.FromDomain)
	}
	if got.Subject != "Factura #4411" {
		t.Errorf("Subject = %q", got.Subject)
	}
	if got.BodyText != "Hola, adjunto la factura." {
		t.Errorf("BodyText = %q, want the plain part", got.BodyText)
	}
	if got.BodyHTML != "" {
		t.Errorf("BodyHTML = %q, want empty when a plain part exists", got.BodyHTML)
	}
	want, _ := time.Parse(time.RFC1123Z, date)
	if !got.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", got.Date, want)
	}
}

func TestToMessageFallsBackToHTML(t *testing.T) {
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/html",
			Headers:  []*gmailapi.MessagePartHeader{header("Subject", "Solo HTML")},
			Body:     &gmailapi.MessagePartBody{Data: b64("<p>Solo HTML</p>")},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}
	if got.BodyText != "" {
		t.Errorf("BodyText = %q, want empty", got.BodyText)
	}
	if got.BodyHTML != "<p>Solo HTML</p>" {
		t.Errorf("BodyHTML = %q", got.BodyHTML)
	}
}

func TestToMessageFlagsAttachments(t *testing.T) {
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			MimeType: "multipart/mixed",
			Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: b64("adjunto")}},
				{
					MimeType: "application/pdf",
					Filename: "factura.pdf",
					Body:     &gmailapi.MessagePartBody{AttachmentId: "a1", Size: 1234},
				},
			},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}
	if !got.HasAttachments {
		t.Error("HasAttachments = false, want true")
	}
}

func TestToMessageToleratesPaddedBase64(t *testing.T) {
	padded := base64.URLEncoding.EncodeToString([]byte("hola"))
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			MimeType: "text/plain",
			Body:     &gmailapi.MessagePartBody{Data: padded},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v", err)
	}
	if got.BodyText != "hola" {
		t.Errorf("BodyText = %q, want hola", got.BodyText)
	}
}

func TestToMessageRejectsNilPayload(t *testing.T) {
	if _, err := toMessage(&gmailapi.Message{Id: "x"}); err == nil {
		t.Fatal("toMessage() error = nil, want an error for a missing payload")
	}
}

func TestToMessageToleratesUnparseableDate(t *testing.T) {
	m := &gmailapi.Message{
		Id: "x",
		Payload: &gmailapi.MessagePart{
			Headers: []*gmailapi.MessagePartHeader{header("Date", "not a date")},
		},
	}

	got, err := toMessage(m)
	if err != nil {
		t.Fatalf("toMessage() error = %v, want no error", err)
	}
	if !got.Date.IsZero() {
		t.Errorf("Date = %v, want the zero time", got.Date)
	}
}
