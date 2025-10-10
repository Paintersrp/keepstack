package ingest

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/abadojack/whatlanggo"
	readability "github.com/go-shiori/go-readability"
	"github.com/microcosm-cc/bluemonday"
)

// Article represents parsed content from a web page.
type Article struct {
	Title       string
	Byline      string
	TextContent string
	HTMLContent string
	WordCount   int
	Language    string
}

// ParseDiagnostics captures metadata generated while parsing content.
type ParseDiagnostics struct {
	LangDetectDuration time.Duration
	LangDetected       bool
}

// Parse extracts readable content from HTML bytes.
func Parse(targetURL string, html []byte) (Article, ParseDiagnostics, error) {
	reader := bytes.NewReader(html)

	var pageURL *url.URL
	if targetURL != "" {
		if parsed, err := url.Parse(targetURL); err == nil {
			pageURL = parsed
		}
	}

	extracted, err := readability.FromReader(reader, pageURL)
	if err != nil {
		return Article{}, ParseDiagnostics{}, fmt.Errorf("readability extract: %w", err)
	}

	title := strings.TrimSpace(extracted.Title)
	byline := strings.TrimSpace(extracted.Byline)
	text := strings.TrimSpace(extracted.TextContent)
	cleanedHTML := sanitizeHTML(extracted.Content)
	if cleanedHTML == "" {
		cleanedHTML = sanitizeHTML(string(html))
	}
	if text == "" {
		text = strings.TrimSpace(bluemonday.StrictPolicy().Sanitize(string(html)))
	}

	lang, detectDuration, langDetected := detectLanguage(text)

	article := Article{
		Title:       title,
		Byline:      byline,
		TextContent: text,
		HTMLContent: cleanedHTML,
		WordCount:   len(strings.Fields(text)),
		Language:    lang,
	}

	diagnostics := ParseDiagnostics{
		LangDetectDuration: detectDuration,
		LangDetected:       langDetected,
	}

	return article, diagnostics, nil
}

func detectLanguage(text string) (string, time.Duration, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", 0, false
	}

	start := time.Now()
	info := whatlanggo.Detect(trimmed)
	duration := time.Since(start)

	if !info.IsReliable() {
		return "", duration, false
	}

	lang := info.Lang.Iso6391()
	if lang == "" {
		lang = whatlanggo.LangToString(info.Lang)
	}

	lang = strings.TrimSpace(lang)

	return lang, duration, lang != ""
}

func sanitizeHTML(raw string) string {
	policy := bluemonday.UGCPolicy()
	sanitized := policy.Sanitize(raw)
	return strings.TrimSpace(sanitized)
}
