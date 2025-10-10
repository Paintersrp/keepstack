package digest

import (
	"strings"
	"testing"
	"time"
)

func TestRenderHTML(t *testing.T) {
	cfg := Config{
		Limit:     5,
		Sender:    "sender@example.com",
		Recipient: "recipient@example.com",
		SMTPURL:   "log://",
		Transport: Transport{Scheme: "log"},
	}

	svc, err := New(nil, cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	links := []digestLink{
		{
			Title:     "Example",
			URL:       "https://example.com",
			Source:    "example.com",
			Byline:    "Editor",
			CreatedAt: time.Date(2024, time.January, 1, 15, 4, 5, 0, time.UTC),
		},
		{
			Title:     "Another",
			URL:       "https://another.test",
			Source:    "another.test",
			CreatedAt: time.Date(2024, time.January, 2, 8, 30, 0, 0, time.UTC),
		},
	}

	html, err := svc.renderHTML(links)
	if err != nil {
		t.Fatalf("render html: %v", err)
	}

	for _, expected := range []string{
		"You have 2 unread links",
		"https://example.com",
		"Editor",
		"another.test",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("expected html to contain %q", expected)
		}
	}
}
