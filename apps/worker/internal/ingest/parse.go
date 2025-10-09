package ingest

import (
    "bytes"
    "fmt"
    "strings"

    "github.com/PuerkitoBio/goquery"
    "github.com/microcosm-cc/bluemonday"
)

// Article represents parsed content from a web page.
type Article struct {
    Title        string
    TextContent  string
    HTMLContent  string
    WordCount    int
    Language     string
}

// Parse extracts readable content from HTML bytes.
func Parse(targetURL string, html []byte) (Article, error) {
    _ = targetURL
    reader := bytes.NewReader(html)
    doc, err := goquery.NewDocumentFromReader(reader)
    if err != nil {
        return Article{}, fmt.Errorf("parse document: %w", err)
    }

    doc.Find("script, style, noscript").Remove()

    title := strings.TrimSpace(doc.Find("title").First().Text())
    bodySelection := doc.Find("body")
    if bodySelection.Length() == 0 {
        bodySelection = doc.Selection
    }

    text := strings.Join(strings.Fields(bodySelection.Text()), " ")

    sanitizer := bluemonday.StrictPolicy()
    sanitizedHTML := sanitizer.SanitizeReader(bytes.NewReader(html))
    cleanedBuf := new(bytes.Buffer)
    if _, err := cleanedBuf.ReadFrom(sanitizedHTML); err != nil {
        return Article{}, fmt.Errorf("sanitize html: %w", err)
    }

    return Article{
        Title:       title,
        TextContent: text,
        HTMLContent: cleanedBuf.String(),
        WordCount:   len(strings.Fields(text)),
        Language:    "",
    }, nil
}
