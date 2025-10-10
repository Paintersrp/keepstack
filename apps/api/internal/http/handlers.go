package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"

	"github.com/example/keepstack/apps/api/internal/config"
	"github.com/example/keepstack/apps/api/internal/db"
	"github.com/example/keepstack/apps/api/internal/observability"
	"github.com/example/keepstack/apps/api/internal/queue"
)

// Server wires together HTTP handlers and dependencies.
type queryProvider interface {
	CreateLink(context.Context, db.CreateLinkParams) (db.CreateLinkRow, error)
	ListLinks(context.Context, db.ListLinksParams) ([]db.ListLinksRow, error)
	CountLinks(context.Context, db.CountLinksParams) (int64, error)
	GetTagByName(context.Context, string) (db.Tag, error)
	ListTags(context.Context) ([]db.Tag, error)
	CreateTag(context.Context, string) (db.Tag, error)
	GetTag(context.Context, int32) (db.Tag, error)
	UpdateTag(context.Context, db.UpdateTagParams) (db.Tag, error)
	DeleteTag(context.Context, int32) error
	ListTagsForLink(context.Context, pgtype.UUID) ([]db.Tag, error)
	AddTagToLink(context.Context, db.AddTagToLinkParams) error
	RemoveTagFromLink(context.Context, db.RemoveTagFromLinkParams) error
	GetLink(context.Context, pgtype.UUID) (db.GetLinkRow, error)
	ListHighlightsByLink(context.Context, pgtype.UUID) ([]db.Highlight, error)
	CreateHighlight(context.Context, db.CreateHighlightParams) (db.Highlight, error)
	UpdateHighlight(context.Context, db.UpdateHighlightParams) (db.Highlight, error)
	DeleteHighlight(context.Context, pgtype.UUID) error
}

type Server struct {
	cfg       config.Config
	pool      *pgxpool.Pool
	queries   queryProvider
	publisher queue.Publisher
	metrics   *observability.Metrics

	highlightLimiters  map[uuid.UUID]*rate.Limiter
	highlightLimiterMu sync.Mutex
	highlightRate      rate.Limit
	highlightBurst     int
}

// NewServer builds a Server instance.
func NewServer(cfg config.Config, pool *pgxpool.Pool, publisher queue.Publisher, metrics *observability.Metrics) *Server {
	return &Server{
		cfg:               cfg,
		pool:              pool,
		queries:           db.New(pool),
		publisher:         publisher,
		metrics:           metrics,
		highlightLimiters: make(map[uuid.UUID]*rate.Limiter),
		highlightRate:     rate.Every(time.Minute / 20),
		highlightBurst:    10,
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

	api.GET("/tags", s.handleListTags)
	api.POST("/tags", s.handleCreateTag)
	api.GET("/tags/:id", s.handleGetTag)
	api.PUT("/tags/:id", s.handleUpdateTag)
	api.DELETE("/tags/:id", s.handleDeleteTag)

	api.GET("/links/:id/tags", s.handleListLinkTags)
	api.POST("/links/:id/tags", s.handleAddLinkTag)
	api.PUT("/links/:id/tags", s.handleReplaceLinkTags)
	api.DELETE("/links/:id/tags", s.handleClearLinkTags)

	api.GET("/links/:id/highlights", s.handleListHighlights)
	api.POST("/links/:id/highlights", s.handleCreateHighlight)
	api.PUT("/links/:id/highlights/:highlightID", s.handleUpdateHighlight)
	api.DELETE("/links/:id/highlights/:highlightID", s.handleDeleteHighlight)
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

type tagsResponse struct {
	Tags []tagResponse `json:"tags"`
}

type linkTagsResponse struct {
	Tags []tagResponse `json:"tags"`
}

type highlightsResponse struct {
	Highlights []highlightResponse `json:"highlights"`
}

type createTagRequest struct {
	Name string `json:"name"`
}

type updateTagRequest struct {
	Name string `json:"name"`
}

type linkTagRequest struct {
	Name string `json:"name"`
}

type replaceLinkTagsRequest struct {
	Tags []string `json:"tags"`
}

type highlightRequest struct {
	Quote      string  `json:"quote"`
	Annotation *string `json:"annotation"`
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

func (s *Server) handleListTags(c echo.Context) error {
	ctx := c.Request().Context()
	items, err := s.queries.ListTags(ctx)
	if err != nil {
		s.metrics.TagListFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to list tags"})
	}

	responses := make([]tagResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, tagResponse{ID: item.ID, Name: item.Name})
	}

	s.metrics.TagListSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, tagsResponse{Tags: responses})
}

func (s *Server) handleCreateTag(c echo.Context) error {
	var req createTagRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.TagCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.metrics.TagCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "name is required"})
	}

	ctx := c.Request().Context()
	tag, err := s.queries.GetTagByName(ctx, name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.metrics.TagCreateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to resolve tag"})
	}

	status := stdhttp.StatusOK
	if errors.Is(err, pgx.ErrNoRows) {
		tag, err = s.queries.CreateTag(ctx, name)
		if err != nil {
			s.metrics.TagCreateFailure.Inc()
			return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to create tag"})
		}
		status = stdhttp.StatusCreated
	}

	s.metrics.TagCreateSuccess.Inc()
	return c.JSON(status, tagResponse{ID: tag.ID, Name: tag.Name})
}

func (s *Server) handleGetTag(c echo.Context) error {
	id, err := parseInt32Param(c.Param("id"))
	if err != nil {
		s.metrics.TagReadFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid tag id"})
	}

	ctx := c.Request().Context()
	tag, err := s.queries.GetTag(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.metrics.TagReadFailure.Inc()
			return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "tag not found"})
		}
		s.metrics.TagReadFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to load tag"})
	}

	s.metrics.TagReadSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, tagResponse{ID: tag.ID, Name: tag.Name})
}

func (s *Server) handleUpdateTag(c echo.Context) error {
	id, err := parseInt32Param(c.Param("id"))
	if err != nil {
		s.metrics.TagUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid tag id"})
	}

	var req updateTagRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.TagUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.metrics.TagUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "name is required"})
	}

	ctx := c.Request().Context()
	tag, err := s.queries.UpdateTag(ctx, db.UpdateTagParams{ID: id, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.metrics.TagUpdateFailure.Inc()
			return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "tag not found"})
		}
		s.metrics.TagUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to update tag"})
	}

	s.metrics.TagUpdateSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, tagResponse{ID: tag.ID, Name: tag.Name})
}

func (s *Server) handleDeleteTag(c echo.Context) error {
	id, err := parseInt32Param(c.Param("id"))
	if err != nil {
		s.metrics.TagDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid tag id"})
	}

	ctx := c.Request().Context()
	if _, err := s.queries.GetTag(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.metrics.TagDeleteFailure.Inc()
			return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "tag not found"})
		}
		s.metrics.TagDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to load tag"})
	}

	if err := s.queries.DeleteTag(ctx, id); err != nil {
		s.metrics.TagDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to delete tag"})
	}

	s.metrics.TagDeleteSuccess.Inc()
	return c.NoContent(stdhttp.StatusNoContent)
}

func (s *Server) handleListLinkTags(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.LinkTagReadFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.LinkTagReadFailure.Inc()
		return respondWithError(c, err)
	}

	ctx := c.Request().Context()
	tags, err := s.queries.ListTagsForLink(ctx, uuidToPg(linkID))
	if err != nil {
		s.metrics.LinkTagReadFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to list tags"})
	}

	responses := make([]tagResponse, 0, len(tags))
	for _, tag := range tags {
		responses = append(responses, tagResponse{ID: tag.ID, Name: tag.Name})
	}

	s.metrics.LinkTagReadSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, linkTagsResponse{Tags: responses})
}

func (s *Server) handleAddLinkTag(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return respondWithError(c, err)
	}

	var req linkTagRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "name is required"})
	}

	ctx := c.Request().Context()
	tags, err := s.queries.ListTagsForLink(ctx, uuidToPg(linkID))
	if err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to list tags"})
	}

	names := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	names = append(names, name)

	responses, err := s.setLinkTags(ctx, linkID, names)
	if err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return respondWithError(c, err)
	}

	s.metrics.LinkTagMutateSuccess.Inc()
	return c.JSON(stdhttp.StatusCreated, linkTagsResponse{Tags: responses})
}

func (s *Server) handleReplaceLinkTags(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return respondWithError(c, err)
	}

	var req replaceLinkTagsRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	ctx := c.Request().Context()
	responses, err := s.setLinkTags(ctx, linkID, req.Tags)
	if err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return respondWithError(c, err)
	}

	s.metrics.LinkTagMutateSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, linkTagsResponse{Tags: responses})
}

func (s *Server) handleClearLinkTags(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return respondWithError(c, err)
	}

	ctx := c.Request().Context()
	if _, err := s.setLinkTags(ctx, linkID, nil); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return respondWithError(c, err)
	}

	s.metrics.LinkTagMutateSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, linkTagsResponse{Tags: nil})
}

func (s *Server) handleListHighlights(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.HighlightListFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	link, err := s.ensureLinkAccess(c.Request().Context(), linkID)
	if err != nil {
		s.metrics.HighlightListFailure.Inc()
		return respondWithError(c, err)
	}

	ctx := c.Request().Context()
	items, err := s.queries.ListHighlightsByLink(ctx, link.ID)
	if err != nil {
		s.metrics.HighlightListFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to list highlights"})
	}

	responses := make([]highlightResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, toHighlightResponse(item))
	}

	s.metrics.HighlightListSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, highlightsResponse{Highlights: responses})
}

func (s *Server) handleCreateHighlight(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.HighlightCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	link, err := s.ensureLinkAccess(c.Request().Context(), linkID)
	if err != nil {
		s.metrics.HighlightCreateFailure.Inc()
		return respondWithError(c, err)
	}

	var req highlightRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.HighlightCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	quote, annotation, err := validateHighlightPayload(req)
	if err != nil {
		s.metrics.HighlightCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	limiter := s.highlightLimiterForUser(uuidFromPg(link.UserID))
	if limiter != nil && !limiter.Allow() {
		s.metrics.HighlightRateLimited.Inc()
		return c.JSON(stdhttp.StatusTooManyRequests, map[string]string{"error": "highlight rate limit exceeded"})
	}

	annotationText := pgtype.Text{}
	if annotation != nil {
		annotationText = pgtype.Text{String: *annotation, Valid: true}
	}

	ctx := c.Request().Context()
	start := time.Now()
	highlight, err := s.queries.CreateHighlight(ctx, db.CreateHighlightParams{
		LinkID:     link.ID,
		Quote:      quote,
		Annotation: annotationText,
	})
	if err != nil {
		s.metrics.HighlightCreateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to create highlight"})
	}
	s.metrics.HighlightProcessingSeconds.Observe(time.Since(start).Seconds())

	s.metrics.HighlightCreateSuccess.Inc()
	return c.JSON(stdhttp.StatusCreated, toHighlightResponse(highlight))
}

func (s *Server) handleUpdateHighlight(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.HighlightUpdateFailure.Inc()
		return respondWithError(c, err)
	}

	highlightID, err := parseUUIDParam(c.Param("highlightID"))
	if err != nil {
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid highlight id"})
	}

	var req highlightRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	quote, annotation, err := validateHighlightPayload(req)
	if err != nil {
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	annotationText := pgtype.Text{}
	if annotation != nil {
		annotationText = pgtype.Text{String: *annotation, Valid: true}
	}

	ctx := c.Request().Context()
	highlight, err := s.queries.UpdateHighlight(ctx, db.UpdateHighlightParams{
		Quote:      quote,
		Annotation: annotationText,
		ID:         uuidToPg(highlightID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.metrics.HighlightUpdateFailure.Inc()
			return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "highlight not found"})
		}
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to update highlight"})
	}

	if uuidFromPg(highlight.LinkID) != linkID {
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "highlight not found"})
	}

	s.metrics.HighlightUpdateSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, toHighlightResponse(highlight))
}

func (s *Server) handleDeleteHighlight(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.HighlightDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.HighlightDeleteFailure.Inc()
		return respondWithError(c, err)
	}

	highlightID, err := parseUUIDParam(c.Param("highlightID"))
	if err != nil {
		s.metrics.HighlightDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid highlight id"})
	}

	ctx := c.Request().Context()
	existing, err := s.queries.ListHighlightsByLink(ctx, uuidToPg(linkID))
	if err != nil {
		s.metrics.HighlightDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to load highlights"})
	}

	found := false
	for _, item := range existing {
		if uuidFromPg(item.ID) == highlightID {
			found = true
			break
		}
	}
	if !found {
		s.metrics.HighlightDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "highlight not found"})
	}

	if err := s.queries.DeleteHighlight(ctx, uuidToPg(highlightID)); err != nil {
		s.metrics.HighlightDeleteFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to delete highlight"})
	}

	s.metrics.HighlightDeleteSuccess.Inc()
	return c.NoContent(stdhttp.StatusNoContent)
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

type apiError struct {
	Code    int
	Message string
}

func (e apiError) Error() string {
	return e.Message
}

func respondWithError(c echo.Context, err error) error {
	var apiErr apiError
	if errors.As(err, &apiErr) {
		return c.JSON(apiErr.Code, map[string]string{"error": apiErr.Message})
	}
	return err
}

func (s *Server) ensureLinkAccess(ctx context.Context, linkID uuid.UUID) (db.GetLinkRow, error) {
	link, err := s.queries.GetLink(ctx, uuidToPg(linkID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.GetLinkRow{}, apiError{Code: stdhttp.StatusNotFound, Message: "link not found"}
		}
		return db.GetLinkRow{}, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to load link"}
	}
	if uuidFromPg(link.UserID) != s.cfg.DevUserID {
		return db.GetLinkRow{}, apiError{Code: stdhttp.StatusNotFound, Message: "link not found"}
	}
	return link, nil
}

func parseUUIDParam(raw string) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimSpace(raw))
}

func parseInt32Param(raw string) (int32, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	return int32(value), nil
}

func (s *Server) setLinkTags(ctx context.Context, linkID uuid.UUID, names []string) ([]tagResponse, error) {
	desired := make(map[string]string)
	for _, raw := range names {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := desired[key]; !exists {
			desired[key] = trimmed
		}
	}

	if len(names) > 0 && len(desired) == 0 {
		return nil, apiError{Code: stdhttp.StatusBadRequest, Message: "tag names must be non-empty"}
	}

	current, err := s.queries.ListTagsForLink(ctx, uuidToPg(linkID))
	if err != nil {
		return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to list tags"}
	}

	currentByID := make(map[int32]db.Tag, len(current))
	currentByKey := make(map[string]db.Tag, len(current))
	for _, tag := range current {
		currentByID[tag.ID] = tag
		currentByKey[strings.ToLower(tag.Name)] = tag
	}

	desiredTags := make(map[int32]db.Tag)
	for key, name := range desired {
		var tag db.Tag
		if existing, ok := currentByKey[key]; ok {
			tag = existing
		} else {
			tag, err = s.queries.GetTagByName(ctx, name)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					tag, err = s.queries.CreateTag(ctx, name)
					if err != nil {
						return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to create tag"}
					}
				} else {
					return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to resolve tag"}
				}
			}
		}

		desiredTags[tag.ID] = tag
		if _, ok := currentByID[tag.ID]; !ok {
			if err := s.queries.AddTagToLink(ctx, db.AddTagToLinkParams{LinkID: uuidToPg(linkID), TagID: tag.ID}); err != nil {
				return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to assign tag"}
			}
		}
	}

	for id := range currentByID {
		if _, ok := desiredTags[id]; !ok {
			if err := s.queries.RemoveTagFromLink(ctx, db.RemoveTagFromLinkParams{LinkID: uuidToPg(linkID), TagID: id}); err != nil {
				return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to remove tag"}
			}
		}
	}

	if len(desiredTags) == 0 {
		return nil, nil
	}

	tags := make([]tagResponse, 0, len(desiredTags))
	for _, tag := range desiredTags {
		tags = append(tags, tagResponse{ID: tag.ID, Name: tag.Name})
	}

	sort.Slice(tags, func(i, j int) bool {
		return strings.ToLower(tags[i].Name) < strings.ToLower(tags[j].Name)
	})

	return tags, nil
}

const (
	maxHighlightQuoteLength      = 2000
	maxHighlightAnnotationLength = 5000
)

func validateHighlightPayload(req highlightRequest) (string, *string, error) {
	quote := strings.TrimSpace(req.Quote)
	if quote == "" {
		return "", nil, fmt.Errorf("quote is required")
	}
	if utf8.RuneCountInString(quote) > maxHighlightQuoteLength {
		return "", nil, fmt.Errorf("quote exceeds maximum length")
	}

	var annotationPtr *string
	if req.Annotation != nil {
		trimmed := strings.TrimSpace(*req.Annotation)
		if trimmed != "" {
			if utf8.RuneCountInString(trimmed) > maxHighlightAnnotationLength {
				return "", nil, fmt.Errorf("annotation exceeds maximum length")
			}
			annotationPtr = &trimmed
		}
	}

	return quote, annotationPtr, nil
}

func toHighlightResponse(item db.Highlight) highlightResponse {
	var annotation *string
	if item.Annotation.Valid {
		val := item.Annotation.String
		annotation = &val
	}
	return highlightResponse{
		ID:         uuidFromPg(item.ID).String(),
		Quote:      item.Quote,
		Annotation: annotation,
		CreatedAt:  item.CreatedAt.Time,
		UpdatedAt:  item.UpdatedAt.Time,
	}
}

func (s *Server) highlightLimiterForUser(userID uuid.UUID) *rate.Limiter {
	if s.highlightRate == 0 {
		return nil
	}

	s.highlightLimiterMu.Lock()
	defer s.highlightLimiterMu.Unlock()

	limiter, ok := s.highlightLimiters[userID]
	if !ok {
		limiter = rate.NewLimiter(s.highlightRate, s.highlightBurst)
		s.highlightLimiters[userID] = limiter
	}
	return limiter
}

var trackingParameters = map[string]struct{}{
	"utm_source":   {},
	"utm_medium":   {},
	"utm_campaign": {},
	"utm_term":     {},
	"utm_content":  {},
	"utm_name":     {},
	"utm_id":       {},
	"utm_creative": {},
	"gclid":        {},
	"fbclid":       {},
	"mc_cid":       {},
	"mc_eid":       {},
	"igshid":       {},
	"mkt_tok":      {},
	"ref":          {},
	"ref_src":      {},
	"ref_url":      {},
}

func isTrackingParam(name string) bool {
	key := strings.ToLower(name)
	if strings.HasPrefix(key, "utm_") {
		return true
	}
	_, found := trackingParameters[key]
	return found
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

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("url missing host")
	}

	port := parsed.Port()
	if port != "" {
		if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
			port = ""
		}
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}

	query := parsed.Query()
	for key := range query {
		if isTrackingParam(key) {
			query.Del(key)
		}
	}
	if len(query) == 0 {
		parsed.RawQuery = ""
	} else {
		parsed.RawQuery = query.Encode()
	}

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
