package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/time/rate"

	"github.com/example/keepstack/apps/api/internal/config"
	"github.com/example/keepstack/apps/api/internal/db"
	"github.com/example/keepstack/apps/api/internal/observability"
	"github.com/example/keepstack/apps/api/internal/queue"
)

func TestHandleCreateLink(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}
	linkID := uuid.New()

	var capturedURL string
	queries := &mockQueries{
		createLinkFn: func(ctx context.Context, params db.CreateLinkParams) (db.CreateLinkRow, error) {
			capturedURL = params.Url
			return db.CreateLinkRow{
				ID:        uuidToPg(linkID),
				UserID:    uuidToPg(cfg.DevUserID),
				Url:       params.Url,
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				Favorite:  false,
			}, nil
		},
	}
	publisher := &stubPublisher{}
	srv := &Server{cfg: cfg, queries: queries, publisher: publisher, metrics: newTestMetrics()}

	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{"url":"Example.com/path?utm_source=news"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if capturedURL != "https://example.com/path" {
		t.Fatalf("expected normalized url, got %q", capturedURL)
	}
	if !publisher.called {
		t.Fatalf("expected publisher to be called")
	}
}

func TestHandleCreateLinkInvalidURL(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")}
	queries := &mockQueries{}
	publisher := &stubPublisher{}
	srv := &Server{cfg: cfg, queries: queries, publisher: publisher, metrics: newTestMetrics()}

	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{"url":"://bad"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	if queries.createLinkCalled {
		t.Fatalf("expected CreateLink not to be called")
	}
	if publisher.called {
		t.Fatalf("expected publisher not to be invoked")
	}
}

func TestHandleListLinksEmpty(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")}
	queries := &mockQueries{
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			if params.PageLimit != 20 || params.PageOffset != 0 {
				t.Fatalf("unexpected pagination params: limit=%d offset=%d", params.PageLimit, params.PageOffset)
			}
			return []db.ListLinksRow{}, nil
		},
		countLinksFn: func(ctx context.Context, params db.CountLinksParams) (int64, error) {
			return 0, nil
		},
	}

	publisher := &stubPublisher{}
	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics(), publisher: publisher}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/links", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp listLinksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.TotalCount != 0 {
		t.Fatalf("expected total count 0, got %d", resp.TotalCount)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(resp.Items))
	}
	if resp.Limit != 20 || resp.Offset != 0 {
		t.Fatalf("unexpected pagination metadata: limit=%d offset=%d", resp.Limit, resp.Offset)
	}
}

func TestHandleListLinksFavoriteFilter(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")}
	var capturedListFavorite interface{}
	var capturedCountFavorite interface{}
	queries := &mockQueries{
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			if params.PageLimit != 20 || params.PageOffset != 0 {
				t.Fatalf("unexpected pagination params: limit=%d offset=%d", params.PageLimit, params.PageOffset)
			}
			capturedListFavorite = params.Favorite
			return []db.ListLinksRow{}, nil
		},
		countLinksFn: func(ctx context.Context, params db.CountLinksParams) (int64, error) {
			capturedCountFavorite = params.Favorite
			return 0, nil
		},
	}

	publisher := &stubPublisher{}
	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics(), publisher: publisher}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/links?favorite=true", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	listFavorite, ok := capturedListFavorite.(pgtype.Bool)
	if !ok {
		t.Fatalf("expected list favorite filter to be pgtype.Bool, got %T", capturedListFavorite)
	}
	if !listFavorite.Valid {
		t.Fatalf("expected list favorite filter to be valid")
	}
	if !listFavorite.Bool {
		t.Fatalf("expected list favorite filter to be true")
	}

	countFavorite, ok := capturedCountFavorite.(pgtype.Bool)
	if !ok {
		t.Fatalf("expected count favorite filter to be pgtype.Bool, got %T", capturedCountFavorite)
	}
	if !countFavorite.Valid {
		t.Fatalf("expected count favorite filter to be valid")
	}
	if !countFavorite.Bool {
		t.Fatalf("expected count favorite filter to be true")
	}
}

func TestHandleListLinksSearchQuery(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("99999999-aaaa-bbbb-cccc-dddddddddddd")}

	var capturedListQuery interface{}
	var capturedCountQuery interface{}

	queries := &mockQueries{
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			capturedListQuery = params.Query
			return []db.ListLinksRow{}, nil
		},
		countLinksFn: func(ctx context.Context, params db.CountLinksParams) (int64, error) {
			capturedCountQuery = params.Query
			return 0, nil
		},
	}

	publisher := &stubPublisher{}
	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics(), publisher: publisher}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/links?q=  time  ", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if capturedListQuery == nil {
		t.Fatalf("expected list query to be captured")
	}

	listQuery, ok := capturedListQuery.(string)
	if !ok {
		t.Fatalf("expected list query to be string, got %T", capturedListQuery)
	}
	if listQuery != "time" {
		t.Fatalf("expected list query to be 'time', got %q", listQuery)
	}

	if capturedCountQuery == nil {
		t.Fatalf("expected count query to be captured")
	}

	countQuery, ok := capturedCountQuery.(string)
	if !ok {
		t.Fatalf("expected count query to be string, got %T", capturedCountQuery)
	}
	if countQuery != "time" {
		t.Fatalf("expected count query to be 'time', got %q", countQuery)
	}
}

func TestHandleListLinksWithRFC3339NanoHighlights(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("aaaa1111-2222-3333-4444-555566667777")}
	linkID := uuid.New()
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	highlightCreatedStr := "2024-05-01T02:03:04.123456789Z"
	highlightUpdatedStr := "2024-05-02T03:04:05.987654321Z"
	highlightJSON := []byte(`[
               {
                       "id": "highlight-1",
                       "text": "A memorable passage",
                       "note": null,
                       "created_at": "` + highlightCreatedStr + `",
                       "updated_at": "` + highlightUpdatedStr + `"
               }
       ]`)

	metrics := newTestMetrics()

	queries := &mockQueries{
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			if params.PageLimit != 20 || params.PageOffset != 0 {
				t.Fatalf("unexpected pagination params: limit=%d offset=%d", params.PageLimit, params.PageOffset)
			}
			return []db.ListLinksRow{
				{
					ID:            uuidToPg(linkID),
					UserID:        uuidToPg(cfg.DevUserID),
					Url:           "https://example.com/article",
					Title:         pgtype.Text{String: "Example", Valid: true},
					SourceDomain:  pgtype.Text{String: "example.com", Valid: true},
					CreatedAt:     pgtype.Timestamptz{Time: createdAt, Valid: true},
					Favorite:      false,
					ArchiveTitle:  "Example archive",
					ArchiveByline: "Reporter",
					Lang:          "en",
					WordCount:     250,
					ExtractedText: "Body",
					TagIds:        nil,
					TagNames:      nil,
					Highlights:    string(highlightJSON),
				},
			}, nil
		},
		countLinksFn: func(ctx context.Context, params db.CountLinksParams) (int64, error) {
			return 1, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: metrics}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/links", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp listLinksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if len(resp.Items[0].Highlights) != 1 {
		t.Fatalf("expected 1 highlight, got %d", len(resp.Items[0].Highlights))
	}

	highlight := resp.Items[0].Highlights[0]
	wantCreated, err := time.Parse(time.RFC3339Nano, highlightCreatedStr)
	if err != nil {
		t.Fatalf("failed to parse expected highlight created_at: %v", err)
	}
	wantUpdated, err := time.Parse(time.RFC3339Nano, highlightUpdatedStr)
	if err != nil {
		t.Fatalf("failed to parse expected highlight updated_at: %v", err)
	}

	if !highlight.CreatedAt.Equal(wantCreated) {
		t.Fatalf("unexpected highlight created_at: got %v want %v", highlight.CreatedAt, wantCreated)
	}
	if !highlight.UpdatedAt.Equal(wantUpdated) {
		t.Fatalf("unexpected highlight updated_at: got %v want %v", highlight.UpdatedAt, wantUpdated)
	}
	if highlight.Note != nil {
		t.Fatalf("expected nil note, got %v", highlight.Note)
	}

	if got := testutil.ToFloat64(metrics.LinkListSuccess); got != 1 {
		t.Fatalf("unexpected LinkListSuccess metric: got %v want 1", got)
	}
	if got := testutil.ToFloat64(metrics.LinkListFailure); got != 0 {
		t.Fatalf("unexpected LinkListFailure metric: got %v want 0", got)
	}
}

func TestHandleListLinksWithoutHighlights(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")}
	linkID := uuid.New()
	createdAt := time.Unix(1_700_000_000, 0).UTC()

	var stored []db.ListLinksRow
	queries := &mockQueries{
		createLinkFn: func(ctx context.Context, params db.CreateLinkParams) (db.CreateLinkRow, error) {
			stored = []db.ListLinksRow{
				{
					ID:         uuidToPg(linkID),
					UserID:     uuidToPg(cfg.DevUserID),
					Url:        params.Url,
					CreatedAt:  pgtype.Timestamptz{Time: createdAt, Valid: true},
					Favorite:   false,
					TagIds:     nil,
					TagNames:   nil,
					Highlights: "[]",
				},
			}

			return db.CreateLinkRow{
				ID:        uuidToPg(linkID),
				UserID:    uuidToPg(cfg.DevUserID),
				Url:       params.Url,
				CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
				Favorite:  false,
			}, nil
		},
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			return stored, nil
		},
		countLinksFn: func(ctx context.Context, params db.CountLinksParams) (int64, error) {
			return int64(len(stored)), nil
		},
	}

	publisher := &stubPublisher{}
	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics(), publisher: publisher}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/links", nil)
	listRec := httptest.NewRecorder()
	e.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, listRec.Code)
	}

	var resp listLinksResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if len(resp.Items[0].Highlights) != 0 {
		t.Fatalf("expected no highlights, got %d", len(resp.Items[0].Highlights))
	}
}

func TestHandleListLinksQueryErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")}
	queries := &mockQueries{
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			return nil, errors.New("boom")
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/links?limit=10&offset=5&q=test", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

func TestRegisterRoutesHealthEndpoints(t *testing.T) {
	t.Parallel()

	srv := &Server{metrics: newTestMetrics(), pool: stubHealthPool{}}
	e := echo.New()
	srv.RegisterRoutes(e)

	tests := []struct {
		name string
		path string
	}{
		{name: "root readiness", path: "/healthz"},
		{name: "api readiness", path: "/api/healthz"},
		{name: "root liveness", path: "/livez"},
		{name: "api liveness", path: "/api/livez"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
			}
		})
	}
}

func TestHandleUpdateLinkFavorite(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")}
	linkID := uuid.New()
	createdAt := time.Unix(1_700_000_000, 0).UTC()

	metrics := newTestMetrics()
	ensureCalled := false

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			ensureCalled = true
			if uuidFromPg(id) != linkID {
				return db.GetLinkRow{}, fmt.Errorf("unexpected link id: %s", uuidFromPg(id))
			}
			return db.GetLinkRow{
				ID:        uuidToPg(linkID),
				UserID:    uuidToPg(cfg.DevUserID),
				Url:       "https://example.com",
				CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
				Favorite:  false,
			}, nil
		},
		updateLinkFavoriteFn: func(ctx context.Context, params db.UpdateLinkFavoriteParams) (db.UpdateLinkFavoriteRow, error) {
			if uuidFromPg(params.ID) != linkID {
				return db.UpdateLinkFavoriteRow{}, fmt.Errorf("unexpected update id: %s", uuidFromPg(params.ID))
			}
			if !params.Favorite {
				return db.UpdateLinkFavoriteRow{}, fmt.Errorf("expected favorite to be true")
			}
			return db.UpdateLinkFavoriteRow{
				ID:            uuidToPg(linkID),
				UserID:        uuidToPg(cfg.DevUserID),
				Url:           "https://example.com",
				Title:         pgtype.Text{String: "Example article", Valid: true},
				SourceDomain:  pgtype.Text{String: "example.com", Valid: true},
				CreatedAt:     pgtype.Timestamptz{Time: createdAt, Valid: true},
				Favorite:      true,
				ArchiveTitle:  "Archived article",
				ArchiveByline: "Author",
				Lang:          "en",
				WordCount:     1200,
				ExtractedText: "Summary",
				TagIds:        []int32{1},
				TagNames:      []string{"reading"},
				Highlights:    "[]",
			}, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: metrics}

	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPatch, "/api/links/"+linkID.String(), strings.NewReader(`{"favorite":true}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if !ensureCalled {
		t.Fatalf("expected ensureLinkAccess to be called")
	}
	if got := testutil.ToFloat64(metrics.LinkUpdateSuccess); got != 1 {
		t.Fatalf("unexpected success metric: got %v want 1", got)
	}
	if got := testutil.ToFloat64(metrics.LinkUpdateFailure); got != 0 {
		t.Fatalf("unexpected failure metric: got %v want 0", got)
	}

	var resp linkResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ID != linkID.String() {
		t.Fatalf("unexpected link id: got %s want %s", resp.ID, linkID)
	}
	if !resp.Favorite {
		t.Fatalf("expected favorite to be true")
	}
}

func TestDecodeHighlightsPostgresTimestamps(t *testing.T) {
	t.Parallel()

	payload := []byte(`[
                {
                        "id": "abc123",
                        "text": "first",
                        "note": null,
                        "created_at": "2024-04-10 12:34:56.123456+00",
                        "updated_at": "2024-04-11 13:14:15.654321+00"
                },
                {
                        "id": "def456",
                        "text": "second",
                        "note": "remember",
                        "created_at": "2024-04-12 08:00:00+00",
                        "updated_at": "2024-04-12 09:00:00+00"
                }
        ]`)

	highlights, err := decodeHighlights(payload)
	if err != nil {
		t.Fatalf("decodeHighlights returned error: %v", err)
	}

	if len(highlights) != 2 {
		t.Fatalf("expected 2 highlights, got %d", len(highlights))
	}

	firstCreated := time.Date(2024, time.April, 10, 12, 34, 56, 123456000, time.UTC)
	firstUpdated := time.Date(2024, time.April, 11, 13, 14, 15, 654321000, time.UTC)
	if !highlights[0].CreatedAt.Equal(firstCreated) {
		t.Fatalf("unexpected created_at: %v", highlights[0].CreatedAt)
	}
	if !highlights[0].UpdatedAt.Equal(firstUpdated) {
		t.Fatalf("unexpected updated_at: %v", highlights[0].UpdatedAt)
	}
	if highlights[0].Note != nil {
		t.Fatalf("expected nil note for first highlight")
	}

	secondCreated := time.Date(2024, time.April, 12, 8, 0, 0, 0, time.UTC)
	secondUpdated := time.Date(2024, time.April, 12, 9, 0, 0, 0, time.UTC)
	if !highlights[1].CreatedAt.Equal(secondCreated) {
		t.Fatalf("unexpected created_at for second: %v", highlights[1].CreatedAt)
	}
	if !highlights[1].UpdatedAt.Equal(secondUpdated) {
		t.Fatalf("unexpected updated_at for second: %v", highlights[1].UpdatedAt)
	}
	if highlights[1].Note == nil || *highlights[1].Note != "remember" {
		t.Fatalf("unexpected note for second highlight: %v", highlights[1].Note)
	}
}

func TestDecodeHighlightsRFC3339NanoTimestamps(t *testing.T) {
	t.Parallel()

	createdAt := "2024-04-15T10:11:12.123456789Z"
	updatedAt := "2024-04-16T11:12:13.987654321Z"
	payload := []byte(`[
               {
                       "id": "nano",
                       "text": "timestamp",
                       "note": null,
                       "created_at": "` + createdAt + `",
                       "updated_at": "` + updatedAt + `"
               }
       ]`)

	highlights, err := decodeHighlights(payload)
	if err != nil {
		t.Fatalf("decodeHighlights returned error: %v", err)
	}

	if len(highlights) != 1 {
		t.Fatalf("expected 1 highlight, got %d", len(highlights))
	}

	wantCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t.Fatalf("failed to parse expected created_at: %v", err)
	}
	wantUpdated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		t.Fatalf("failed to parse expected updated_at: %v", err)
	}

	if !highlights[0].CreatedAt.Equal(wantCreated) {
		t.Fatalf("unexpected created_at: got %v want %v", highlights[0].CreatedAt, wantCreated)
	}
	if !highlights[0].UpdatedAt.Equal(wantUpdated) {
		t.Fatalf("unexpected updated_at: got %v want %v", highlights[0].UpdatedAt, wantUpdated)
	}
}

func TestHandleUpdateLinkFavoriteNotFound(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")}
	linkID := uuid.New()

	metrics := newTestMetrics()

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			if uuidFromPg(id) != linkID {
				return db.GetLinkRow{}, fmt.Errorf("unexpected link id: %s", uuidFromPg(id))
			}
			return db.GetLinkRow{
				ID:        uuidToPg(linkID),
				UserID:    uuidToPg(uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")),
				Url:       "https://example.com",
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				Favorite:  false,
			}, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: metrics}

	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPatch, "/api/links/"+linkID.String(), strings.NewReader(`{"favorite":false}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
	if queries.updateLinkFavoriteCalled {
		t.Fatalf("expected UpdateLinkFavorite not to be called")
	}
	if got := testutil.ToFloat64(metrics.LinkUpdateFailure); got != 1 {
		t.Fatalf("unexpected failure metric: got %v want 1", got)
	}
	if got := testutil.ToFloat64(metrics.LinkUpdateSuccess); got != 0 {
		t.Fatalf("unexpected success metric: got %v want 0", got)
	}
}

func TestHandleCreateClaim(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")}
	linkID := uuid.New()
	claimID := uuid.New()
	claimedAt := time.Unix(1_700_000_000, 0).UTC()

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			if uuidFromPg(id) != linkID {
				return db.GetLinkRow{}, fmt.Errorf("unexpected link id: %s", uuidFromPg(id))
			}
			return db.GetLinkRow{
				ID:        uuidToPg(linkID),
				UserID:    uuidToPg(cfg.DevUserID),
				Url:       "https://example.com",
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				Favorite:  false,
			}, nil
		},
		createClaimFn: func(ctx context.Context, params db.CreateClaimParams) (db.CreateClaimRow, error) {
			if uuidFromPg(params.LinkID) != linkID {
				return db.CreateClaimRow{}, fmt.Errorf("unexpected claim link id: %s", uuidFromPg(params.LinkID))
			}
			if uuidFromPg(params.UserID) != cfg.DevUserID {
				return db.CreateClaimRow{}, fmt.Errorf("unexpected claim user id: %s", uuidFromPg(params.UserID))
			}
			return db.CreateClaimRow{
				ID:        uuidToPg(claimID),
				LinkID:    params.LinkID,
				UserID:    params.UserID,
				ClaimedAt: pgtype.Timestamptz{Time: claimedAt, Valid: true},
				Inserted:  true,
			}, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/claims", strings.NewReader(fmt.Sprintf(`{"link_id":"%s"}`, linkID)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if !queries.createClaimCalled {
		t.Fatalf("expected CreateClaim to be called")
	}

	var resp claimResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != claimID.String() {
		t.Fatalf("unexpected claim id: got %s want %s", resp.ID, claimID)
	}
	if resp.LinkID != linkID.String() {
		t.Fatalf("unexpected claim link id: got %s want %s", resp.LinkID, linkID)
	}
	if resp.UserID != cfg.DevUserID.String() {
		t.Fatalf("unexpected claim user id: got %s want %s", resp.UserID, cfg.DevUserID)
	}
	if !resp.Created {
		t.Fatalf("expected created flag to be true")
	}
	if resp.ClaimedAt.UTC() != claimedAt {
		t.Fatalf("unexpected claimed_at: got %s want %s", resp.ClaimedAt, claimedAt)
	}
}

func TestHandleCreateClaimDuplicate(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")}
	linkID := uuid.New()

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{
				ID:        uuidToPg(linkID),
				UserID:    uuidToPg(cfg.DevUserID),
				Url:       "https://example.com",
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				Favorite:  false,
			}, nil
		},
		createClaimFn: func(ctx context.Context, params db.CreateClaimParams) (db.CreateClaimRow, error) {
			return db.CreateClaimRow{
				ID:        uuidToPg(uuid.New()),
				LinkID:    params.LinkID,
				UserID:    params.UserID,
				ClaimedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				Inserted:  false,
			}, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/claims", strings.NewReader(fmt.Sprintf(`{"link_id":"%s"}`, linkID)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp claimResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Created {
		t.Fatalf("expected created flag to be false")
	}
}

func TestHandleCreateClaimInvalidLinkID(t *testing.T) {
	t.Parallel()

	srv := &Server{queries: &mockQueries{}, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/claims", strings.NewReader(`{"link_id":"not-a-uuid"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestNormalizeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "adds https", input: "example.com/path", want: "https://example.com/path"},
		{name: "drops tracking", input: "https://Example.com/Read?utm_source=news&foo=bar#section", want: "https://example.com/Read?foo=bar"},
		{name: "removes default port", input: "http://example.com:80/path", want: "http://example.com/path"},
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

func TestHandleListLinksTagFilter(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")}
	linkID := uuid.New()

	var capturedTagIDs interface{}
	requestedTags := make([]string, 0)

	queries := &mockQueries{
		getTagByNameFn: func(ctx context.Context, name string) (db.Tag, error) {
			requestedTags = append(requestedTags, name)
			switch name {
			case "news":
				return db.Tag{ID: 2, Name: "news"}, nil
			case "tech":
				return db.Tag{ID: 3, Name: "tech"}, nil
			default:
				return db.Tag{}, pgx.ErrNoRows
			}
		},
		listLinksFn: func(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
			capturedTagIDs = params.TagIds
			return []db.ListLinksRow{{
				ID:           uuidToPg(linkID),
				UserID:       uuidToPg(cfg.DevUserID),
				Url:          "https://example.com",
				CreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
				Favorite:     false,
				Title:        pgtype.Text{Valid: false},
				SourceDomain: pgtype.Text{Valid: false},
				Highlights:   "[]",
			}}, nil
		},
		countLinksFn: func(ctx context.Context, params db.CountLinksParams) (int64, error) {
			return 1, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/links?tags=news,tech", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if len(requestedTags) != 2 {
		t.Fatalf("expected two tag lookups, got %d", len(requestedTags))
	}
	tagIDs, ok := capturedTagIDs.([]int32)
	if !ok {
		t.Fatalf("expected captured tag IDs slice, got %T", capturedTagIDs)
	}
	if len(tagIDs) != 2 || tagIDs[0] != 2 || tagIDs[1] != 3 {
		t.Fatalf("unexpected tag ids: %v", tagIDs)
	}
}

func TestHandleCreateTag(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{
		getTagByNameFn: func(ctx context.Context, name string) (db.Tag, error) {
			return db.Tag{}, pgx.ErrNoRows
		},
		createTagFn: func(ctx context.Context, name string) (db.Tag, error) {
			if name != "alpha" {
				t.Fatalf("unexpected tag name: %s", name)
			}
			return db.Tag{ID: 1, Name: name}, nil
		},
	}

	srv := &Server{cfg: config.Config{}, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/tags", strings.NewReader(`{"name":"alpha"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
}

func TestHandleCreateTagDuplicate(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{
		getTagByNameFn: func(ctx context.Context, name string) (db.Tag, error) {
			if name != "alpha" {
				t.Fatalf("unexpected tag name lookup: %s", name)
			}
			return db.Tag{ID: 1, Name: name}, nil
		},
	}

	srv := &Server{cfg: config.Config{}, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/tags", strings.NewReader(`{"name":"alpha"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, rec.Code)
	}
	if queries.createTagCalled {
		t.Fatalf("expected CreateTag not to be called")
	}
}

func TestHandleListTags(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{
		listTagLinkCountsFn: func(ctx context.Context) ([]db.ListTagLinkCountsRow, error) {
			return []db.ListTagLinkCountsRow{{ID: 1, Name: "alpha", LinkCount: 3}}, nil
		},
	}

	srv := &Server{cfg: config.Config{}, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var payload []tagResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(payload))
	}
	tag := payload[0]
	if tag.ID != 1 || tag.Name != "alpha" {
		t.Fatalf("unexpected tag payload: %+v", tag)
	}
	if tag.LinkCount == nil || *tag.LinkCount != 3 {
		t.Fatalf("expected link_count 3, got %+v", tag.LinkCount)
	}
}

func TestHandleGetTag(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{
		getTagFn: func(ctx context.Context, id int32) (db.Tag, error) {
			if id != 7 {
				t.Fatalf("unexpected tag id: %d", id)
			}
			return db.Tag{ID: id, Name: "alpha"}, nil
		},
	}

	srv := &Server{cfg: config.Config{}, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/api/tags/7", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestHandleUpdateTag(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{
		updateTagFn: func(ctx context.Context, params db.UpdateTagParams) (db.Tag, error) {
			if params.ID != 5 || params.Name != "beta" {
				t.Fatalf("unexpected params: %+v", params)
			}
			return db.Tag{ID: params.ID, Name: params.Name}, nil
		},
	}

	srv := &Server{cfg: config.Config{}, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPut, "/api/tags/5", strings.NewReader(`{"name":"beta"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestHandleDeleteTag(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{
		getTagFn: func(ctx context.Context, id int32) (db.Tag, error) {
			return db.Tag{ID: id, Name: "alpha"}, nil
		},
		deleteTagFn: func(ctx context.Context, id int32) error {
			if id != 9 {
				t.Fatalf("unexpected delete id: %d", id)
			}
			return nil
		},
	}

	srv := &Server{cfg: config.Config{}, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodDelete, "/api/tags/9", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestHandleReplaceLinkTags(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")}
	linkID := uuid.New()

	currentTags := []db.Tag{{ID: 1, Name: "alpha"}, {ID: 2, Name: "beta"}}
	added := make([]int32, 0)
	removed := make([]int32, 0)

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{ID: id, UserID: uuidToPg(cfg.DevUserID)}, nil
		},
		listTagsForLinkFn: func(ctx context.Context, id pgtype.UUID) ([]db.Tag, error) {
			copyTags := make([]db.Tag, len(currentTags))
			copy(copyTags, currentTags)
			return copyTags, nil
		},
		getTagFn: func(ctx context.Context, id int32) (db.Tag, error) {
			switch id {
			case 2:
				return db.Tag{ID: 2, Name: "beta"}, nil
			case 3:
				return db.Tag{ID: 3, Name: "gamma"}, nil
			default:
				return db.Tag{}, fmt.Errorf("unexpected tag lookup: %d", id)
			}
		},
		addTagToLinkFn: func(ctx context.Context, params db.AddTagToLinkParams) error {
			added = append(added, params.TagID)
			currentTags = append(currentTags, db.Tag{ID: params.TagID, Name: "gamma"})
			return nil
		},
		removeTagFromLinkFn: func(ctx context.Context, params db.RemoveTagFromLinkParams) error {
			removed = append(removed, params.TagID)
			filtered := currentTags[:0]
			for _, tag := range currentTags {
				if tag.ID != params.TagID {
					filtered = append(filtered, tag)
				}
			}
			currentTags = filtered
			return nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	body := `{"tagIds":[2,3]}`
	req := httptest.NewRequest(http.MethodPut, "/api/links/"+linkID.String()+"/tags", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if len(added) != 1 || added[0] != 3 {
		t.Fatalf("expected to add tag 3, got %v", added)
	}
	if len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("expected to remove tag 1, got %v", removed)
	}
}

func TestHandleReplaceLinkTagsIdempotent(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("abababab-abab-abab-abab-abababababab")}
	linkID := uuid.New()

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{ID: id, UserID: uuidToPg(cfg.DevUserID)}, nil
		},
		listTagsForLinkFn: func(ctx context.Context, id pgtype.UUID) ([]db.Tag, error) {
			return []db.Tag{{ID: 1, Name: "alpha"}, {ID: 2, Name: "beta"}}, nil
		},
		getTagFn: func(ctx context.Context, id int32) (db.Tag, error) {
			switch id {
			case 1:
				return db.Tag{ID: 1, Name: "alpha"}, nil
			case 2:
				return db.Tag{ID: 2, Name: "beta"}, nil
			default:
				return db.Tag{}, fmt.Errorf("unexpected tag lookup: %d", id)
			}
		},
		addTagToLinkFn: func(ctx context.Context, params db.AddTagToLinkParams) error {
			t.Fatalf("unexpected AddTagToLink call: %v", params.TagID)
			return nil
		},
		removeTagFromLinkFn: func(ctx context.Context, params db.RemoveTagFromLinkParams) error {
			t.Fatalf("unexpected RemoveTagFromLink call: %v", params.TagID)
			return nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	body := `{"tagIds":[1,2,1]}`
	req := httptest.NewRequest(http.MethodPut, "/api/links/"+linkID.String()+"/tags", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestHandleAddLinkTagsReplacesAll(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("12121212-1212-1212-1212-121212121212")}
	linkID := uuid.New()

	currentTags := []db.Tag{{ID: 5, Name: "old"}}
	added := make([]int32, 0)
	removed := make([]int32, 0)

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{ID: id, UserID: uuidToPg(cfg.DevUserID)}, nil
		},
		listTagsForLinkFn: func(ctx context.Context, id pgtype.UUID) ([]db.Tag, error) {
			copyTags := make([]db.Tag, len(currentTags))
			copy(copyTags, currentTags)
			return copyTags, nil
		},
		getTagFn: func(ctx context.Context, id int32) (db.Tag, error) {
			switch id {
			case 6:
				return db.Tag{ID: 6, Name: "new"}, nil
			default:
				return db.Tag{}, fmt.Errorf("unexpected tag lookup: %d", id)
			}
		},
		addTagToLinkFn: func(ctx context.Context, params db.AddTagToLinkParams) error {
			added = append(added, params.TagID)
			return nil
		},
		removeTagFromLinkFn: func(ctx context.Context, params db.RemoveTagFromLinkParams) error {
			removed = append(removed, params.TagID)
			return nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	body := `{"tagIds":[6]}`
	req := httptest.NewRequest(http.MethodPost, "/api/links/"+linkID.String()+"/tags", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if len(added) != 1 || added[0] != 6 {
		t.Fatalf("expected to add tag 6, got %v", added)
	}
	if len(removed) != 1 || removed[0] != 5 {
		t.Fatalf("expected to remove tag 5, got %v", removed)
	}
}

func TestHandleClearLinkTags(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")}
	linkID := uuid.New()
	removed := make([]int32, 0)

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{ID: id, UserID: uuidToPg(cfg.DevUserID)}, nil
		},
		listTagsForLinkFn: func(ctx context.Context, id pgtype.UUID) ([]db.Tag, error) {
			return []db.Tag{{ID: 4, Name: "alpha"}}, nil
		},
		removeTagFromLinkFn: func(ctx context.Context, params db.RemoveTagFromLinkParams) error {
			removed = append(removed, params.TagID)
			return nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodDelete, "/api/links/"+linkID.String()+"/tags", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if len(removed) != 1 || removed[0] != 4 {
		t.Fatalf("expected to remove tag 4, got %v", removed)
	}
}

func TestHighlightLifecycle(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")}
	linkID := uuid.New()
	highlights := make(map[uuid.UUID]db.Highlight)

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{ID: id, UserID: uuidToPg(cfg.DevUserID)}, nil
		},
		createHighlightFn: func(ctx context.Context, params db.CreateHighlightParams) (db.Highlight, error) {
			id := uuid.New()
			highlight := db.Highlight{
				ID:         uuidToPg(id),
				LinkID:     params.LinkID,
				Quote:      params.Text,
				Annotation: params.Note,
				CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
				UpdatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
			}
			highlights[id] = highlight
			return highlight, nil
		},
		listHighlightsByLinkFn: func(ctx context.Context, id pgtype.UUID) ([]db.Highlight, error) {
			items := make([]db.Highlight, 0, len(highlights))
			for _, h := range highlights {
				items = append(items, h)
			}
			return items, nil
		},
		updateHighlightFn: func(ctx context.Context, params db.UpdateHighlightParams) (db.Highlight, error) {
			hid := uuidFromPg(params.ID)
			highlight, ok := highlights[hid]
			if !ok {
				return db.Highlight{}, pgx.ErrNoRows
			}
			highlight.Quote = params.Text
			highlight.Annotation = params.Note
			highlight.UpdatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			highlights[hid] = highlight
			return highlight, nil
		},
		deleteHighlightFn: func(ctx context.Context, id pgtype.UUID) error {
			delete(highlights, uuidFromPg(id))
			return nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics()}
	e := echo.New()
	srv.RegisterRoutes(e)

	// Create highlight
	createBody := `{"text":"hello world"}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/links/"+linkID.String()+"/highlights", strings.NewReader(createBody))
	createReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	createRec := httptest.NewRecorder()
	e.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, createRec.Code)
	}

	var created highlightResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected highlight id in response")
	}

	// Update highlight
	updateBody := `{"text":"updated","note":"note"}`
	updateReq := httptest.NewRequest(http.MethodPut, "/api/links/"+linkID.String()+"/highlights/"+created.ID, strings.NewReader(updateBody))
	updateReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	updateRec := httptest.NewRecorder()
	e.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, updateRec.Code)
	}

	// List highlights
	listReq := httptest.NewRequest(http.MethodGet, "/api/links/"+linkID.String()+"/highlights", nil)
	listRec := httptest.NewRecorder()
	e.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, listRec.Code)
	}

	// Delete highlight
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/links/"+linkID.String()+"/highlights/"+created.ID, nil)
	deleteRec := httptest.NewRecorder()
	e.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, deleteRec.Code)
	}

	if len(highlights) != 0 {
		t.Fatalf("expected highlights map to be empty")
	}
}

func TestHighlightRateLimit(t *testing.T) {
	t.Parallel()

	cfg := config.Config{DevUserID: uuid.MustParse("12121212-1212-1212-1212-121212121212")}
	linkID := uuid.New()

	queries := &mockQueries{
		getLinkFn: func(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
			return db.GetLinkRow{ID: id, UserID: uuidToPg(cfg.DevUserID)}, nil
		},
	}

	srv := &Server{cfg: cfg, queries: queries, metrics: newTestMetrics(), highlightLimiters: make(map[uuid.UUID]*rate.Limiter), highlightRate: rate.Every(time.Minute), highlightBurst: 1}
	srv.highlightLimiters[cfg.DevUserID] = rateLimiterZero()

	e := echo.New()
	srv.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodPost, "/api/links/"+linkID.String()+"/highlights", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}
	if queries.createHighlightCalled {
		t.Fatalf("expected create highlight not to be called")
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

// --- Helpers ---

type mockQueries struct {
	createLinkFn                 func(context.Context, db.CreateLinkParams) (db.CreateLinkRow, error)
	listLinksFn                  func(context.Context, db.ListLinksParams) ([]db.ListLinksRow, error)
	countLinksFn                 func(context.Context, db.CountLinksParams) (int64, error)
	updateLinkFavoriteFn         func(context.Context, db.UpdateLinkFavoriteParams) (db.UpdateLinkFavoriteRow, error)
	listRecommendationsForUserFn func(context.Context, db.ListRecommendationsForUserParams) ([]db.ListRecommendationsForUserRow, error)
	createClaimFn                func(context.Context, db.CreateClaimParams) (db.CreateClaimRow, error)
	getTagByNameFn               func(context.Context, string) (db.Tag, error)
	listTagLinkCountsFn          func(context.Context) ([]db.ListTagLinkCountsRow, error)
	createTagFn                  func(context.Context, string) (db.Tag, error)
	getTagFn                     func(context.Context, int32) (db.Tag, error)
	updateTagFn                  func(context.Context, db.UpdateTagParams) (db.Tag, error)
	deleteTagFn                  func(context.Context, int32) error
	listTagsForLinkFn            func(context.Context, pgtype.UUID) ([]db.Tag, error)
	addTagToLinkFn               func(context.Context, db.AddTagToLinkParams) error
	removeTagFromLinkFn          func(context.Context, db.RemoveTagFromLinkParams) error
	getLinkFn                    func(context.Context, pgtype.UUID) (db.GetLinkRow, error)
	listHighlightsByLinkFn       func(context.Context, pgtype.UUID) ([]db.Highlight, error)
	createHighlightFn            func(context.Context, db.CreateHighlightParams) (db.Highlight, error)
	updateHighlightFn            func(context.Context, db.UpdateHighlightParams) (db.Highlight, error)
	deleteHighlightFn            func(context.Context, pgtype.UUID) error

	createLinkCalled         bool
	createClaimCalled        bool
	createTagCalled          bool
	createHighlightCalled    bool
	updateLinkFavoriteCalled bool
}

func (m *mockQueries) CreateLink(ctx context.Context, params db.CreateLinkParams) (db.CreateLinkRow, error) {
	m.createLinkCalled = true
	if m.createLinkFn == nil {
		return db.CreateLinkRow{}, fmt.Errorf("unexpected CreateLink call")
	}
	return m.createLinkFn(ctx, params)
}

func (m *mockQueries) ListLinks(ctx context.Context, params db.ListLinksParams) ([]db.ListLinksRow, error) {
	if m.listLinksFn == nil {
		return nil, fmt.Errorf("unexpected ListLinks call")
	}
	return m.listLinksFn(ctx, params)
}

func (m *mockQueries) ListRecommendationsForUser(ctx context.Context, params db.ListRecommendationsForUserParams) ([]db.ListRecommendationsForUserRow, error) {
	if m.listRecommendationsForUserFn == nil {
		return nil, fmt.Errorf("unexpected ListRecommendationsForUser call")
	}
	return m.listRecommendationsForUserFn(ctx, params)
}

func (m *mockQueries) CountLinks(ctx context.Context, params db.CountLinksParams) (int64, error) {
	if m.countLinksFn == nil {
		return 0, fmt.Errorf("unexpected CountLinks call")
	}
	return m.countLinksFn(ctx, params)
}

func (m *mockQueries) UpdateLinkFavorite(ctx context.Context, params db.UpdateLinkFavoriteParams) (db.UpdateLinkFavoriteRow, error) {
	m.updateLinkFavoriteCalled = true
	if m.updateLinkFavoriteFn == nil {
		return db.UpdateLinkFavoriteRow{}, fmt.Errorf("unexpected UpdateLinkFavorite call")
	}
	return m.updateLinkFavoriteFn(ctx, params)
}

func (m *mockQueries) CreateClaim(ctx context.Context, params db.CreateClaimParams) (db.CreateClaimRow, error) {
	m.createClaimCalled = true
	if m.createClaimFn == nil {
		return db.CreateClaimRow{}, fmt.Errorf("unexpected CreateClaim call")
	}
	return m.createClaimFn(ctx, params)
}

func (m *mockQueries) GetTagByName(ctx context.Context, name string) (db.Tag, error) {
	if m.getTagByNameFn == nil {
		return db.Tag{}, fmt.Errorf("unexpected GetTagByName call")
	}
	return m.getTagByNameFn(ctx, name)
}

func (m *mockQueries) ListTagLinkCounts(ctx context.Context) ([]db.ListTagLinkCountsRow, error) {
	if m.listTagLinkCountsFn == nil {
		return nil, fmt.Errorf("unexpected ListTagLinkCounts call")
	}
	return m.listTagLinkCountsFn(ctx)
}

func (m *mockQueries) CreateTag(ctx context.Context, name string) (db.Tag, error) {
	m.createTagCalled = true
	if m.createTagFn == nil {
		return db.Tag{}, fmt.Errorf("unexpected CreateTag call")
	}
	return m.createTagFn(ctx, name)
}

func (m *mockQueries) GetTag(ctx context.Context, id int32) (db.Tag, error) {
	if m.getTagFn == nil {
		return db.Tag{}, fmt.Errorf("unexpected GetTag call")
	}
	return m.getTagFn(ctx, id)
}

func (m *mockQueries) UpdateTag(ctx context.Context, params db.UpdateTagParams) (db.Tag, error) {
	if m.updateTagFn == nil {
		return db.Tag{}, fmt.Errorf("unexpected UpdateTag call")
	}
	return m.updateTagFn(ctx, params)
}

func (m *mockQueries) DeleteTag(ctx context.Context, id int32) error {
	if m.deleteTagFn == nil {
		return fmt.Errorf("unexpected DeleteTag call")
	}
	return m.deleteTagFn(ctx, id)
}

func (m *mockQueries) ListTagsForLink(ctx context.Context, id pgtype.UUID) ([]db.Tag, error) {
	if m.listTagsForLinkFn == nil {
		return nil, fmt.Errorf("unexpected ListTagsForLink call")
	}
	return m.listTagsForLinkFn(ctx, id)
}

func (m *mockQueries) AddTagToLink(ctx context.Context, params db.AddTagToLinkParams) error {
	if m.addTagToLinkFn == nil {
		return fmt.Errorf("unexpected AddTagToLink call")
	}
	return m.addTagToLinkFn(ctx, params)
}

func (m *mockQueries) RemoveTagFromLink(ctx context.Context, params db.RemoveTagFromLinkParams) error {
	if m.removeTagFromLinkFn == nil {
		return fmt.Errorf("unexpected RemoveTagFromLink call")
	}
	return m.removeTagFromLinkFn(ctx, params)
}

func (m *mockQueries) GetLink(ctx context.Context, id pgtype.UUID) (db.GetLinkRow, error) {
	if m.getLinkFn == nil {
		return db.GetLinkRow{}, fmt.Errorf("unexpected GetLink call")
	}
	return m.getLinkFn(ctx, id)
}

func (m *mockQueries) ListHighlightsByLink(ctx context.Context, id pgtype.UUID) ([]db.Highlight, error) {
	if m.listHighlightsByLinkFn == nil {
		return nil, fmt.Errorf("unexpected ListHighlightsByLink call")
	}
	return m.listHighlightsByLinkFn(ctx, id)
}

func (m *mockQueries) CreateHighlight(ctx context.Context, params db.CreateHighlightParams) (db.Highlight, error) {
	m.createHighlightCalled = true
	if m.createHighlightFn == nil {
		return db.Highlight{}, fmt.Errorf("unexpected CreateHighlight call")
	}
	return m.createHighlightFn(ctx, params)
}

func (m *mockQueries) UpdateHighlight(ctx context.Context, params db.UpdateHighlightParams) (db.Highlight, error) {
	if m.updateHighlightFn == nil {
		return db.Highlight{}, fmt.Errorf("unexpected UpdateHighlight call")
	}
	return m.updateHighlightFn(ctx, params)
}

func (m *mockQueries) DeleteHighlight(ctx context.Context, id pgtype.UUID) error {
	if m.deleteHighlightFn == nil {
		return fmt.Errorf("unexpected DeleteHighlight call")
	}
	return m.deleteHighlightFn(ctx, id)
}

var _ queryProvider = (*mockQueries)(nil)

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
		LinkCreateSuccess:          prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_create_success_total", Help: ""}),
		LinkCreateFailure:          prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_create_failure_total", Help: ""}),
		LinkListSuccess:            prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_list_success_total", Help: ""}),
		LinkListFailure:            prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_list_failure_total", Help: ""}),
		LinkUpdateSuccess:          prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_update_success_total", Help: ""}),
		LinkUpdateFailure:          prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_update_failure_total", Help: ""}),
		ClaimCreateSuccess:         prometheus.NewCounter(prometheus.CounterOpts{Name: "test_claim_create_success_total", Help: ""}),
		ClaimCreateFailure:         prometheus.NewCounter(prometheus.CounterOpts{Name: "test_claim_create_failure_total", Help: ""}),
		ReadinessFailure:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_readiness_failure_total", Help: ""}),
		ReadinessMigrationGap:      prometheus.NewCounter(prometheus.CounterOpts{Name: "test_readiness_migration_gap_total", Help: ""}),
		TagCreateSuccess:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_create_success_total", Help: ""}),
		TagCreateFailure:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_create_failure_total", Help: ""}),
		TagListSuccess:             prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_list_success_total", Help: ""}),
		TagListFailure:             prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_list_failure_total", Help: ""}),
		TagReadSuccess:             prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_read_success_total", Help: ""}),
		TagReadFailure:             prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_read_failure_total", Help: ""}),
		TagUpdateSuccess:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_update_success_total", Help: ""}),
		TagUpdateFailure:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_update_failure_total", Help: ""}),
		TagDeleteSuccess:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_delete_success_total", Help: ""}),
		TagDeleteFailure:           prometheus.NewCounter(prometheus.CounterOpts{Name: "test_tag_delete_failure_total", Help: ""}),
		LinkTagReadSuccess:         prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_tag_read_success_total", Help: ""}),
		LinkTagReadFailure:         prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_tag_read_failure_total", Help: ""}),
		LinkTagMutateSuccess:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_tag_mutate_success_total", Help: ""}),
		LinkTagMutateFailure:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_link_tag_mutate_failure_total", Help: ""}),
		HighlightListSuccess:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_list_success_total", Help: ""}),
		HighlightListFailure:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_list_failure_total", Help: ""}),
		HighlightCreateSuccess:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_create_success_total", Help: ""}),
		HighlightCreateFailure:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_create_failure_total", Help: ""}),
		HighlightUpdateSuccess:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_update_success_total", Help: ""}),
		HighlightUpdateFailure:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_update_failure_total", Help: ""}),
		HighlightDeleteSuccess:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_delete_success_total", Help: ""}),
		HighlightDeleteFailure:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_delete_failure_total", Help: ""}),
		HighlightRateLimited:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_highlight_rate_limited_total", Help: ""}),
		HighlightProcessingSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "test_highlight_processing_seconds", Help: ""}),
	}
}

func rateLimiterZero() *rate.Limiter {
	limiter := rate.NewLimiter(rate.Every(time.Hour), 0)
	return limiter
}

func TestClassifyReadinessError(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err              error
		wantMessage      string
		wantMigrationGap bool
	}{
		"undefined table": {
			err: &pgconn.PgError{
				Code:      pgerrcode.UndefinedTable,
				TableName: "highlights",
			},
			wantMessage:      "database schema missing table \"highlights\"",
			wantMigrationGap: true,
		},
		"undefined column": {
			err: &pgconn.PgError{
				Code:       pgerrcode.UndefinedColumn,
				TableName:  "archives",
				ColumnName: "title",
			},
			wantMessage:      "database schema missing column \"title\" on table \"archives\"",
			wantMigrationGap: true,
		},
		"generic error": {
			err:              errors.New("boom"),
			wantMessage:      "boom",
			wantMigrationGap: false,
		},
	}

	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg, migrationGap := classifyReadinessError(tc.err)
			if msg != tc.wantMessage {
				t.Fatalf("unexpected message: got %q want %q", msg, tc.wantMessage)
			}
			if migrationGap != tc.wantMigrationGap {
				t.Fatalf("unexpected migration gap flag: got %t want %t", migrationGap, tc.wantMigrationGap)
			}
		})
	}
}

type stubHealthPool struct{}

func (stubHealthPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func (stubHealthPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &stubRows{}, nil
}

func (stubHealthPool) QueryRow(context.Context, string, ...any) pgx.Row {
	return stubRow{}
}

func (stubHealthPool) Ping(context.Context) error {
	return nil
}

type stubRow struct{}

func (stubRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		switch d := dest[0].(type) {
		case *int:
			*d = 0
		}
	}
	return nil
}

type stubRows struct{}

func (*stubRows) Close() {}

func (*stubRows) Err() error { return nil }

func (*stubRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

func (*stubRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (*stubRows) Next() bool { return false }

func (*stubRows) Scan(...any) error { return nil }

func (*stubRows) Values() ([]any, error) { return nil, nil }

func (*stubRows) RawValues() [][]byte { return nil }

func (*stubRows) Conn() *pgx.Conn { return nil }
