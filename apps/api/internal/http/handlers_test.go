package httpapi

import "testing"

func TestNormalizeURL(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {name: "adds https", input: "example.com/path", want: "https://example.com/path"},
        {name: "preserves scheme", input: "http://example.com", want: "http://example.com"},
        {name: "invalid", input: "" , wantErr: true},
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            got, err := normalizeURL(tc.input)
            if tc.wantErr {
                if err == nil {
                    t.Fatalf("expected error, got nil")
                }
                return
            }
            if err != nil {
                t.Fatalf("unexpected error: %v", err)
            }
            if got != tc.want {
                t.Fatalf("expected %q, got %q", tc.want, got)
            }
        })
    }
}

func TestParsePagination(t *testing.T) {
    limit, offset, err := parsePagination("50", "10")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if limit != 50 || offset != 10 {
        t.Fatalf("unexpected pagination values: %d %d", limit, offset)
    }

    if _, _, err := parsePagination("-1", "0"); err == nil {
        t.Fatalf("expected error for negative limit")
    }
}
