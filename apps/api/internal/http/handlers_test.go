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
				Highlights:   []byte("[]"),
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
		listTagsFn: func(ctx context.Context) ([]db.Tag, error) {
			return []db.Tag{{ID: 1, Name: "alpha"}}, nil
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
	listRecommendationsForUserFn func(context.Context, db.ListRecommendationsForUserParams) ([]db.ListRecommendationsForUserRow, error)
	getTagByNameFn               func(context.Context, string) (db.Tag, error)
	listTagsFn                   func(context.Context) ([]db.Tag, error)
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

	createLinkCalled      bool
	createTagCalled       bool
	createHighlightCalled bool
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

func (m *mockQueries) GetTagByName(ctx context.Context, name string) (db.Tag, error) {
	if m.getTagByNameFn == nil {
		return db.Tag{}, fmt.Errorf("unexpected GetTagByName call")
	}
	return m.getTagByNameFn(ctx, name)
}

func (m *mockQueries) ListTags(ctx context.Context) ([]db.Tag, error) {
	if m.listTagsFn == nil {
		return nil, fmt.Errorf("unexpected ListTags call")
	}
	return m.listTagsFn(ctx)
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
