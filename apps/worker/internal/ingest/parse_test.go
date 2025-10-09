package ingest

import "testing"

func TestParse(t *testing.T) {
    html := []byte(`<!doctype html><html><head><title>Example</title></head><body><h1>Hello</h1><p>World</p></body></html>`)
    article, err := Parse("https://example.com", html)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if article.Title != "Example" {
        t.Fatalf("expected title Example, got %q", article.Title)
    }
    if article.WordCount == 0 {
        t.Fatalf("expected word count > 0")
    }
}
