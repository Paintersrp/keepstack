package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseReadabilityExtraction(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fixture  string
		url      string
		wantLang string
		minWords int
	}{
		{
			name:     "english_article",
			fixture:  "english_article.html",
			url:      "https://example.com/articles/english",
			wantLang: "en",
			minWords: 20,
		},
		{
			name:     "spanish_article",
			fixture:  "spanish_article.html",
			url:      "https://example.com/articulos/spanish",
			wantLang: "es",
			minWords: 20,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			html := readFixture(t, tc.fixture)
			article, diagnostics, err := Parse(tc.url, html)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}

			if article.Title == "" {
				t.Fatalf("expected non-empty title")
			}
			if article.TextContent == "" {
				t.Fatalf("expected non-empty text content")
			}
			if article.HTMLContent == "" {
				t.Fatalf("expected non-empty HTML content")
			}
			if article.WordCount < tc.minWords {
				t.Fatalf("expected word count >= %d, got %d", tc.minWords, article.WordCount)
			}
			if article.Language != tc.wantLang {
				t.Fatalf("expected language %q, got %q", tc.wantLang, article.Language)
			}
			if !diagnostics.LangDetected {
				t.Fatalf("expected language detection to succeed")
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}
