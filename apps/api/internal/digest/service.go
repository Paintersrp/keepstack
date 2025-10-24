package digest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/smtp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoUnreadLinks is returned when there are no unread links to include in the digest.
var ErrNoUnreadLinks = errors.New("no unread links")

// Service encapsulates the logic required to build and dispatch the digest email.
type Service struct {
	pool   *pgxpool.Pool
	config Config
	tmpl   *template.Template
}

// New constructs a Service backed by the provided database pool and configuration.
func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	tmpl, err := template.New("digest").Funcs(template.FuncMap{
		"formatDate": func(t time.Time) string {
			return t.Format("Jan 2, 2006 15:04 MST")
		},
	}).Parse(digestTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse digest template: %w", err)
	}

	return &Service{
		pool:   pool,
		config: cfg,
		tmpl:   tmpl,
	}, nil
}

// Send builds and emails the digest for the provided user. The returned integer represents
// the number of links included in the message and the string contains the rendered HTML body.
func (s *Service) Send(ctx context.Context, userID uuid.UUID) (int, string, error) {
	links, err := s.fetchUnreadLinks(ctx, userID)
	if err != nil {
		return 0, "", fmt.Errorf("fetch unread links: %w", err)
	}
	if len(links) == 0 {
		return 0, "", ErrNoUnreadLinks
	}

	htmlBody, err := s.renderHTML(links)
	if err != nil {
		return 0, "", fmt.Errorf("render digest: %w", err)
	}

	if err := s.dispatch(htmlBody, len(links)); err != nil {
		return 0, "", fmt.Errorf("send digest email: %w", err)
	}

	return len(links), htmlBody, nil
}

type digestLink struct {
	Title     string
	URL       string
	Source    string
	Byline    string
	CreatedAt time.Time
}

const unreadLinksQuery = `
SELECT
    l.url,
    COALESCE(NULLIF(l.title, ''), COALESCE(NULLIF(a.title, ''), 'Untitled')) AS title,
    COALESCE(l.source_domain, '') AS source,
    COALESCE(a.byline, '') AS byline,
    l.created_at
FROM links l
LEFT JOIN archives a ON a.link_id = l.id
WHERE l.user_id = $1
  AND l.read_at IS NULL
ORDER BY l.created_at ASC
LIMIT $2;
`

func (s *Service) fetchUnreadLinks(ctx context.Context, userID uuid.UUID) ([]digestLink, error) {
	rows, err := s.pool.Query(ctx, unreadLinksQuery, userID, s.config.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []digestLink
	for rows.Next() {
		var link digestLink
		if err := rows.Scan(&link.URL, &link.Title, &link.Source, &link.Byline, &link.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Service) renderHTML(links []digestLink) (string, error) {
	data := struct {
		GeneratedAt time.Time
		Links       []digestLink
		Count       int
	}{
		GeneratedAt: time.Now().UTC(),
		Links:       links,
		Count:       len(links),
	}

	var buf bytes.Buffer
	if err := s.tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Service) dispatch(htmlBody string, count int) error {
	subject := fmt.Sprintf("Keepstack Digest (%d links)", count)
	msg := bytes.Buffer{}
	msg.WriteString(fmt.Sprintf("From: %s\r\n", s.config.Sender))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", s.config.Recipient))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	switch s.config.Transport.Scheme {
	case "log":
		encoded := base64.StdEncoding.EncodeToString(msg.Bytes())
		log.Printf("keepstack digest log transport: subject=%q recipient=%s payload_base64=%s", subject, s.config.Recipient, encoded)
		return nil
	case "smtp":
		var auth smtp.Auth
		if s.config.Transport.Username != "" {
			auth = smtp.PlainAuth("", s.config.Transport.Username, s.config.Transport.Password, s.config.Transport.Host)
		}

		addr := fmt.Sprintf("%s:%d", s.config.Transport.Host, s.config.Transport.Port)
		return smtp.SendMail(addr, auth, s.config.Sender, []string{s.config.Recipient}, msg.Bytes())
	default:
		return fmt.Errorf("unsupported transport %q", s.config.Transport.Scheme)
	}
}

const digestTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>Keepstack Digest</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #1f2933; background-color: #f9fafb; margin: 0; padding: 24px; }
.container { max-width: 640px; margin: 0 auto; background-color: #ffffff; border-radius: 12px; padding: 24px; box-shadow: 0 10px 30px rgba(15, 23, 42, 0.08); }
h1 { margin-top: 0; font-size: 24px; }
ol { padding-left: 20px; }
li { margin-bottom: 18px; }
a { color: #2563eb; text-decoration: none; }
a:hover { text-decoration: underline; }
.meta { color: #52606d; font-size: 14px; margin-top: 4px; }
</style>
</head>
<body>
<div class="container">
  <h1>Keepstack Digest</h1>
  <p>You have {{ .Count }} unread link{{ if ne .Count 1 }}s{{ end }} waiting in your queue.</p>
  <ol>
  {{- range .Links }}
    <li>
      <div><a href="{{ .URL }}">{{ .Title }}</a></div>
      {{- if .Byline }}<div class="meta">{{ .Byline }}</div>{{ end }}
      <div class="meta">Saved {{ formatDate .CreatedAt }}{{ if .Source }} • {{ .Source }}{{ end }}</div>
    </li>
  {{- end }}
  </ol>
  <p class="meta">Generated at {{ formatDate .GeneratedAt }}.</p>
</div>
</body>
</html>
`
