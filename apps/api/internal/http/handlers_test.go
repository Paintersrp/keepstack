package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/example/keepstack/apps/api/internal/config"
	"github.com/example/keepstack/apps/api/internal/db"
	"github.com/example/keepstack/apps/api/internal/observability"
	"github.com/example/keepstack/apps/api/internal/queue"
)

func TestHandleCreateLink(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}

	now := time.Now()
	fakeDB := &stubDB{
		createLinkRow: db.CreateLinkRow{
			ID:        uuidToPg(uuid.New()),
			UserID:    uuidToPg(cfg.DevUserID),
			Url:       "https://example.com/path",
			Title:     pgtype.Text{Valid: false},
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
			ReadAt:    pgtype.Timestamptz{Valid: false},
			Favorite:  false,
		},
	}
	publisher := &stubPublisher{}
	metrics := newTestMetrics()

	srv := &Server{
		cfg:       cfg,
		queries:   db.New(fakeDB),
		publisher: publisher,
		metrics:   metrics,
	}

	e := echo.New()
	srv.RegisterRoutes(e)

	body := `{"url":"example.com/path"}`
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	if !publisher.called {
		t.Fatalf("expected publisher to be called")
	}

	if publisher.lastID == uuid.Nil {
		t.Fatalf("expected publisher to receive non-nil uuid")
	}

	if fakeDB.createLinkArgs == nil {
		t.Fatalf("expected database to be invoked")
	}

	if got := fakeDB.createLinkArgs[2]; got != "https://example.com/path" {
		t.Fatalf("expected normalized url to be stored, got %v", got)
	}

	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected response body: %v", err)
	}

	if response["url"] != "https://example.com/path" {
		t.Fatalf("expected response url to be normalized, got %q", response["url"])
	}
}

func TestHandleCreateLinkInvalidURL(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")}
	fakeDB := &stubDB{}
	publisher := &stubPublisher{}

	srv := &Server{
		cfg:       cfg,
		queries:   db.New(fakeDB),
		publisher: publisher,
		metrics:   newTestMetrics(),
	}

	e := echo.New()
	srv.RegisterRoutes(e)

	body := `{"url":"://bad"}`
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	if publisher.called {
		t.Fatalf("expected publisher not to be called for invalid payload")
	}

	if fakeDB.createLinkArgs != nil {
		t.Fatalf("expected database not to be invoked for invalid payload")
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "adds https", input: "example.com/path", want: "https://example.com/path"},
		{name: "preserves scheme", input: "http://example.com", want: "http://example.com"},
		{name: "invalid", input: "", wantErr: true},
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

type stubDB struct {
	createLinkArgs []interface{}
	createLinkRow  db.CreateLinkRow
	createLinkErr  error
}

func (s *stubDB) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec call: %s", sql)
}

func (s *stubDB) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query call: %s", sql)
}

func (s *stubDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	s.createLinkArgs = append([]interface{}(nil), args...)
	return &stubRow{parent: s}
}

type stubRow struct {
	parent *stubDB
}

func (s *stubRow) Scan(dest ...interface{}) error {
	if s.parent.createLinkErr != nil {
		return s.parent.createLinkErr
	}

	if len(dest) != 7 {
		return fmt.Errorf("unexpected dest len: %d", len(dest))
	}

	if id, ok := dest[0].(*pgtype.UUID); ok {
		*id = s.parent.createLinkRow.ID
	}
	if userID, ok := dest[1].(*pgtype.UUID); ok {
		*userID = s.parent.createLinkRow.UserID
	}
	if urlVal, ok := dest[2].(*string); ok {
		*urlVal = s.parent.createLinkRow.Url
	}
	if title, ok := dest[3].(*pgtype.Text); ok {
		*title = s.parent.createLinkRow.Title
	}
	if createdAt, ok := dest[4].(*pgtype.Timestamptz); ok {
		*createdAt = s.parent.createLinkRow.CreatedAt
	}
	if readAt, ok := dest[5].(*pgtype.Timestamptz); ok {
		*readAt = s.parent.createLinkRow.ReadAt
	}
	if favorite, ok := dest[6].(*bool); ok {
		*favorite = s.parent.createLinkRow.Favorite
	}

	return nil
}

type stubPublisher struct {
	called bool
	lastID uuid.UUID
}

func (s *stubPublisher) PublishLinkSaved(ctx context.Context, linkID uuid.UUID) error {
	s.called = true
	s.lastID = linkID
	return nil
}

func (s *stubPublisher) Close() {}

var _ queue.Publisher = (*stubPublisher)(nil)

func newTestMetrics() *observability.Metrics {
	return &observability.Metrics{
		LinkCreateSuccess: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_create_success_total", Help: ""}),
		LinkCreateFailure: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_create_failure_total", Help: ""}),
		LinkListSuccess:   prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_list_success_total", Help: ""}),
		LinkListFailure:   prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_list_failure_total", Help: ""}),
		ReadinessFailure:  prometheus.NewCounter(prometheus.CounterOpts{Name: "test_readiness_failure_total", Help: ""}),
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
