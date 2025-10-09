package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/example/keepstack/apps/api/internal/config"
	"github.com/example/keepstack/apps/api/internal/db"
	"github.com/example/keepstack/apps/api/internal/observability"
	"github.com/example/keepstack/apps/api/internal/queue"
)

// Server wires together HTTP handlers and dependencies.
type Server struct {
	cfg       config.Config
	pool      *pgxpool.Pool
	queries   *db.Queries
	publisher queue.Publisher
	metrics   *observability.Metrics
}

// NewServer builds a Server instance.
func NewServer(cfg config.Config, pool *pgxpool.Pool, publisher queue.Publisher, metrics *observability.Metrics) *Server {
	return &Server{
		cfg:       cfg,
		pool:      pool,
		queries:   db.New(pool),
		publisher: publisher,
		metrics:   metrics,
	}
}

// RegisterRoutes attaches routes to the provided Echo router.
func (s *Server) RegisterRoutes(e *echo.Echo) {
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	e.GET("/healthz", s.handleHealthz)
	e.GET("/livez", s.handleLivez)
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	api := e.Group("/api")
	api.POST("/links", s.handleCreateLink)
	api.GET("/links", s.handleListLinks)
}

func (s *Server) handleHealthz(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
	defer cancel()

	if _, err := s.pool.Exec(ctx, "SELECT 1"); err != nil {
		s.metrics.ReadinessFailure.Inc()
		c.Logger().Errorf("readiness check: SELECT 1 failed: %v", err)
		return c.JSON(stdhttp.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
	}

	var count int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(1) FROM links").Scan(&count); err != nil {
		s.metrics.ReadinessFailure.Inc()
		c.Logger().Errorf("readiness check: links table query failed: %v", err)
		return c.JSON(stdhttp.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
	}
	_ = count

	return c.JSON(stdhttp.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleLivez(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return c.JSON(stdhttp.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
	}
	return c.JSON(stdhttp.StatusOK, map[string]string{"status": "ok"})
}

type createLinkRequest struct {
	URL      string  `json:"url"`
	Title    *string `json:"title"`
	Favorite *bool   `json:"favorite"`
}

type linkResponse struct {
	ID            string              `json:"id"`
	URL           string              `json:"url"`
	Title         string              `json:"title"`
	SourceDomain  string              `json:"source_domain"`
	Favorite      bool                `json:"favorite"`
	CreatedAt     time.Time           `json:"created_at"`
	ReadAt        *time.Time          `json:"read_at,omitempty"`
	ArchiveTitle  string              `json:"archive_title"`
	Byline        string              `json:"byline"`
	Lang          string              `json:"lang"`
	WordCount     int                 `json:"word_count"`
	ExtractedText string              `json:"extracted_text"`
	Tags          []tagResponse       `json:"tags"`
	Highlights    []highlightResponse `json:"highlights"`
}

type tagResponse struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type highlightResponse struct {
	ID         string    `json:"id"`
	Quote      string    `json:"quote"`
	Annotation *string   `json:"annotation,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type listLinksResponse struct {
	Items      []linkResponse `json:"items"`
	TotalCount int64          `json:"total_count"`
	Limit      int            `json:"limit"`
	Offset     int            `json:"offset"`
}

func (s *Server) handleCreateLink(c echo.Context) error {
	var req createLinkRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	normalizedURL, err := normalizeURL(strings.TrimSpace(req.URL))
	if err != nil {
		s.metrics.LinkCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid url"})
	}

	linkID := uuid.New()
	title := pgtype.Text{}
	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if trimmed != "" {
			title = pgtype.Text{String: trimmed, Valid: true}
		}
	}

	favorite := pgtype.Bool{}
	if req.Favorite != nil {
		favorite = pgtype.Bool{Bool: *req.Favorite, Valid: true}
	}

	params := db.CreateLinkParams{
		ID:       uuidToPg(linkID),
		UserID:   uuidToPg(s.cfg.DevUserID),
		Url:      normalizedURL,
		Title:    title,
		Favorite: favorite,
	}

	ctx := c.Request().Context()
	if _, err := s.queries.CreateLink(ctx, params); err != nil {
		s.metrics.LinkCreateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to store link"})
	}

	if err := s.publisher.PublishLinkSaved(ctx, linkID); err != nil {
		s.metrics.LinkCreateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to enqueue link"})
	}

	s.metrics.LinkCreateSuccess.Inc()
	return c.JSON(stdhttp.StatusCreated, map[string]string{
		"id":  linkID.String(),
		"url": normalizedURL,
	})
}

func (s *Server) handleListLinks(c echo.Context) error {
	ctx := c.Request().Context()
	limit, offset, err := parsePagination(c.QueryParam("limit"), c.QueryParam("offset"))
	if err != nil {
		s.metrics.LinkListFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	favoriteParam := c.QueryParam("favorite")
	favorite := pgtype.Bool{}
	if favoriteParam != "" {
		parsed, err := strconv.ParseBool(favoriteParam)
		if err != nil {
			s.metrics.LinkListFailure.Inc()
			return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "favorite must be boolean"})
		}
		favorite = pgtype.Bool{Bool: parsed, Valid: true}
	}

	queryText := strings.TrimSpace(c.QueryParam("q"))
	queryArg := pgtype.Text{}
	if queryText != "" {
		queryArg = pgtype.Text{String: queryText, Valid: true}
	}

	tagsParam := strings.TrimSpace(c.QueryParam("tags"))
	var tagIDs []int32
	if tagsParam != "" {
		seen := make(map[string]struct{})
		for _, part := range strings.Split(tagsParam, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			if _, ok := seen[strings.ToLower(name)]; ok {
				continue
			}
			seen[strings.ToLower(name)] = struct{}{}

			tag, err := s.queries.GetTagByName(ctx, name)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					s.metrics.LinkListFailure.Inc()
					return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown tag: %s", name)})
				}
				s.metrics.LinkListFailure.Inc()
				return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to resolve tags"})
			}
			tagIDs = append(tagIDs, tag.ID)
		}
	}

	var tagArg interface{}
	if len(tagIDs) > 0 {
		tagArg = tagIDs
	}

	listParams := db.ListLinksParams{
		UserID:     uuidToPg(s.cfg.DevUserID),
		Favorite:   favorite,
		Query:      queryArg,
		TagIds:     tagArg,
		PageLimit:  int32(limit),
		PageOffset: int32(offset),
	}

	items, err := s.queries.ListLinks(ctx, listParams)
	if err != nil {
		s.metrics.LinkListFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to fetch links"})
	}

	countParams := db.CountLinksParams{
		UserID:   uuidToPg(s.cfg.DevUserID),
		Favorite: favorite,
		Query:    queryArg,
	}
	if len(tagIDs) > 0 {
		countParams.TagIds = tagIDs
	}

	count, err := s.queries.CountLinks(ctx, countParams)
	if err != nil {
		s.metrics.LinkListFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to count links"})
	}

	responses := make([]linkResponse, 0, len(items))
	for _, item := range items {
		resp, err := toLinkResponse(item)
		if err != nil {
			s.metrics.LinkListFailure.Inc()
			return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to format response"})
		}
		responses = append(responses, resp)
	}

	s.metrics.LinkListSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, listLinksResponse{
		Items:      responses,
		TotalCount: count,
		Limit:      limit,
		Offset:     offset,
	})
}

func toLinkResponse(row db.ListLinksRow) (linkResponse, error) {
	var readAt *time.Time
	if row.ReadAt.Valid {
		t := row.ReadAt.Time
		readAt = &t
	}

	title := ""
	if row.Title.Valid {
		title = row.Title.String
	}

	sourceDomain := ""
	if row.SourceDomain.Valid {
		sourceDomain = row.SourceDomain.String
	}

	tags := mergeTagArrays(row.TagIds, row.TagNames)
	highlights, err := decodeHighlights(row.Highlights)
	if err != nil {
		return linkResponse{}, err
	}

	return linkResponse{
		ID:            uuidFromPg(row.ID).String(),
		URL:           row.Url,
		Title:         title,
		SourceDomain:  sourceDomain,
		Favorite:      row.Favorite,
		CreatedAt:     row.CreatedAt.Time,
		ReadAt:        readAt,
		ArchiveTitle:  row.ArchiveTitle,
		Byline:        row.ArchiveByline,
		Lang:          row.Lang,
		WordCount:     int(row.WordCount),
		ExtractedText: row.ExtractedText,
		Tags:          tags,
		Highlights:    highlights,
	}, nil
}

func mergeTagArrays(idsRaw, namesRaw interface{}) []tagResponse {
	ids := extractInt32Slice(idsRaw)
	names := extractStringSlice(namesRaw)
	n := len(ids)
	if len(names) < n {
		n = len(names)
	}
	if n == 0 {
		return nil
	}
	tags := make([]tagResponse, 0, n)
	for i := 0; i < n; i++ {
		tags = append(tags, tagResponse{ID: ids[i], Name: names[i]})
	}
	return tags
}

func extractInt32Slice(value interface{}) []int32 {
	switch v := value.(type) {
	case nil:
		return nil
	case []int32:
		return v
	case []int64:
		out := make([]int32, len(v))
		for i, val := range v {
			out[i] = int32(val)
		}
		return out
	case []interface{}:
		out := make([]int32, 0, len(v))
		for _, item := range v {
			switch vv := item.(type) {
			case int32:
				out = append(out, vv)
			case int64:
				out = append(out, int32(vv))
			case float64:
				out = append(out, int32(vv))
			}
		}
		return out
	case pgtype.FlatArray[pgtype.Int4]:
		out := make([]int32, 0, len(v))
		for _, elem := range v {
			if elem.Valid {
				out = append(out, elem.Int32)
			}
		}
		return out
	default:
		return nil
	}
}

func extractStringSlice(value interface{}) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case pgtype.FlatArray[pgtype.Text]:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			if elem.Valid {
				out = append(out, elem.String)
			}
		}
		return out
	default:
		return nil
	}
}

type highlightPayload struct {
	ID         string    `json:"id"`
	Quote      string    `json:"quote"`
	Annotation *string   `json:"annotation"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func decodeHighlights(data []byte) ([]highlightResponse, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw []highlightPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	highlights := make([]highlightResponse, 0, len(raw))
	for _, item := range raw {
		highlights = append(highlights, highlightResponse{
			ID:         item.ID,
			Quote:      item.Quote,
			Annotation: item.Annotation,
			CreatedAt:  item.CreatedAt,
			UpdatedAt:  item.UpdatedAt,
		})
	}
	return highlights, nil
}

func parsePagination(limitRaw, offsetRaw string) (int, int, error) {
	limit := 20
	offset := 0
	if limitRaw != "" {
		parsed, err := strconv.Atoi(limitRaw)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
		if parsed > 100 {
			parsed = 100
		}
		limit = parsed
	}

	if offsetRaw != "" {
		parsed, err := strconv.Atoi(offsetRaw)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = parsed
	}
	return limit, offset, nil
}

func normalizeURL(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		parsed, err = url.Parse("https://" + raw)
		if err != nil {
			return "", err
		}
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("url missing host")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func uuidToPg(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func uuidFromPg(id pgtype.UUID) uuid.UUID {
	if !id.Valid {
		return uuid.Nil
	}
	return uuid.UUID(id.Bytes)
}
