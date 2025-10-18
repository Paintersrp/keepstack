package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	UpdateLinkFavorite(context.Context, db.UpdateLinkFavoriteParams) (db.UpdateLinkFavoriteRow, error)
	ListRecommendationsForUser(context.Context, db.ListRecommendationsForUserParams) ([]db.ListRecommendationsForUserRow, error)
	CreateClaim(context.Context, db.CreateClaimParams) (db.CreateClaimRow, error)
	GetTagByName(context.Context, string) (db.Tag, error)
	ListTagLinkCounts(context.Context) ([]db.ListTagLinkCountsRow, error)
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

type healthPool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Ping(context.Context) error
}

type Server struct {
	cfg       config.Config
	pool      healthPool
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
	e.Use(MetricsMiddleware(s.metrics))

	e.GET("/healthz", s.handleHealthz)
	e.GET("/livez", s.handleLivez)
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	api := e.Group("/api")
	api.GET("/healthz", s.handleHealthz)
	api.GET("/livez", s.handleLivez)
	api.POST("/links", s.handleCreateLink)
	api.GET("/links", s.handleListLinks)
	api.PATCH("/links/:id", s.handleUpdateLink)
	api.GET("/recommendations", s.handleListRecommendations)
	api.POST("/claims", s.handleCreateClaim)

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

	checks := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "postgres connectivity",
			run: func(ctx context.Context) error {
				_, err := s.pool.Exec(ctx, "SELECT 1")
				return err
			},
		},
		{
			name: "links table",
			run: func(ctx context.Context) error {
				var count int
				if err := s.pool.QueryRow(ctx, "SELECT COUNT(1) FROM links").Scan(&count); err != nil {
					return err
				}
				return nil
			},
		},
		{
			name: "links source_domain column",
			run: func(ctx context.Context) error {
				return runReadinessQuery(ctx, s.pool, "SELECT source_domain FROM links LIMIT 0")
			},
		},
		{
			name: "archives metadata columns",
			run: func(ctx context.Context) error {
				return runReadinessQuery(ctx, s.pool, "SELECT title, byline, lang, word_count FROM archives LIMIT 0")
			},
		},
		{
			name: "highlights table",
			run: func(ctx context.Context) error {
				return runReadinessQuery(ctx, s.pool, "SELECT 1 FROM highlights LIMIT 1")
			},
		},
	}

	for _, check := range checks {
		if err := check.run(ctx); err != nil {
			s.metrics.ReadinessFailure.Inc()

			message, migrationGap := classifyReadinessError(err)
			if migrationGap {
				s.metrics.ReadinessMigrationGap.Inc()
			}

			c.Logger().Errorf("readiness check: %s failed: %v", check.name, err)

			response := map[string]string{
				"status": "unhealthy",
				"error":  message,
			}
			if migrationGap {
				response["hint"] = "apply outstanding database migrations"
			}

			return c.JSON(stdhttp.StatusServiceUnavailable, response)
		}
	}

	return c.JSON(stdhttp.StatusOK, map[string]string{"status": "ok"})
}

func runReadinessQuery(ctx context.Context, pool healthPool, query string, args ...any) error {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

func classifyReadinessError(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgerrcode.UndefinedTable:
			table := pgErr.TableName
			if table == "" {
				table = "required"
			}
			return fmt.Sprintf("database schema missing table %q", table), true
		case pgerrcode.UndefinedColumn:
			column := pgErr.ColumnName
			if column == "" {
				column = "required"
			}
			table := pgErr.TableName
			if table != "" {
				return fmt.Sprintf("database schema missing column %q on table %q", column, table), true
			}
			return fmt.Sprintf("database schema missing column %q", column), true
		}
	}

	return err.Error(), false
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

type updateLinkRequest struct {
	Favorite *bool    `json:"favorite"`
	Tags     []string `json:"tags"`
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
	ID        int32  `json:"id"`
	Name      string `json:"name"`
	LinkCount *int32 `json:"link_count,omitempty"`
}

type highlightResponse struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Note      *string   `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type createClaimRequest struct {
	LinkID string `json:"link_id"`
}

type claimResponse struct {
	ID        string    `json:"id"`
	LinkID    string    `json:"link_id"`
	UserID    string    `json:"user_id"`
	ClaimedAt time.Time `json:"claimed_at"`
	Created   bool      `json:"created"`
}

type listLinksResponse struct {
	Items      []linkResponse `json:"items"`
	TotalCount int64          `json:"total_count"`
	Limit      int            `json:"limit"`
	Offset     int            `json:"offset"`
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

type linkTagsRequest struct {
	TagIDs []int32 `json:"tagIds"`
}

type highlightRequest struct {
	Text string  `json:"text"`
	Note *string `json:"note"`
}

func (s *Server) handleCreateLink(c echo.Context) error {
	var req createLinkRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkCreateFailure.Inc()
		c.Logger().Warnf("create link: bind payload failed: %v", err)
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	normalizedURL, err := normalizeURL(strings.TrimSpace(req.URL))
	if err != nil {
		s.metrics.LinkCreateFailure.Inc()
		c.Logger().Warnf("create link: invalid url %q: %v", req.URL, err)
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
		c.Logger().Errorf("create link: store link failed: %v", err)
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to store link"})
	}

	if err := s.publisher.PublishLinkSaved(ctx, linkID); err != nil {
		s.metrics.LinkCreateFailure.Inc()
		c.Logger().Errorf("create link: publish link saved failed: %v", err)
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to enqueue link"})
	}

	s.metrics.LinkCreateSuccess.Inc()
	c.Logger().Infof("create link: created link %s for %s", linkID, normalizedURL)
	return c.JSON(stdhttp.StatusCreated, map[string]string{
		"id":  linkID.String(),
		"url": normalizedURL,
	})
}

func (s *Server) handleUpdateLink(c echo.Context) error {
	linkID, err := parseUUIDParam(c.Param("id"))
	if err != nil {
		s.metrics.LinkUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link id"})
	}

	var req updateLinkRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	if req.Favorite == nil {
		s.metrics.LinkUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "favorite is required"})
	}

	if _, err := s.ensureLinkAccess(c.Request().Context(), linkID); err != nil {
		s.metrics.LinkUpdateFailure.Inc()
		return respondWithError(c, err)
	}

	row, err := s.queries.UpdateLinkFavorite(c.Request().Context(), db.UpdateLinkFavoriteParams{
		Favorite: *req.Favorite,
		ID:       uuidToPg(linkID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.metrics.LinkUpdateFailure.Inc()
			return c.JSON(stdhttp.StatusNotFound, map[string]string{"error": "link not found"})
		}
		s.metrics.LinkUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to update link"})
	}

	response, err := toLinkResponse(db.ListLinksRow{
		ID:            row.ID,
		UserID:        row.UserID,
		Url:           row.Url,
		Title:         row.Title,
		SourceDomain:  row.SourceDomain,
		CreatedAt:     row.CreatedAt,
		ReadAt:        row.ReadAt,
		Favorite:      row.Favorite,
		ArchiveTitle:  row.ArchiveTitle,
		ArchiveByline: row.ArchiveByline,
		Lang:          row.Lang,
		WordCount:     row.WordCount,
		ExtractedText: row.ExtractedText,
		TagIds:        row.TagIds,
		TagNames:      row.TagNames,
		Highlights:    row.Highlights,
	})
	if err != nil {
		s.metrics.LinkUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to format link"})
	}

	s.metrics.LinkUpdateSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, response)
}

func (s *Server) handleCreateClaim(c echo.Context) error {
	var req createClaimRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.ClaimCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	linkIDRaw := strings.TrimSpace(req.LinkID)
	if linkIDRaw == "" {
		s.metrics.ClaimCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "link_id is required"})
	}

	linkID, err := uuid.Parse(linkIDRaw)
	if err != nil {
		s.metrics.ClaimCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid link_id"})
	}

	ctx := c.Request().Context()
	link, err := s.ensureLinkAccess(ctx, linkID)
	if err != nil {
		s.metrics.ClaimCreateFailure.Inc()
		return respondWithError(c, err)
	}

	result, err := s.queries.CreateClaim(ctx, db.CreateClaimParams{
		LinkID: uuidToPg(linkID),
		UserID: link.UserID,
	})
	if err != nil {
		s.metrics.ClaimCreateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to record claim"})
	}

	claimedAt := time.Now()
	if result.ClaimedAt.Valid {
		claimedAt = result.ClaimedAt.Time
	}

	response := claimResponse{
		ID:        uuidFromPg(result.ID).String(),
		LinkID:    uuidFromPg(result.LinkID).String(),
		UserID:    uuidFromPg(result.UserID).String(),
		ClaimedAt: claimedAt,
		Created:   result.Inserted,
	}

	status := stdhttp.StatusOK
	if result.Inserted {
		status = stdhttp.StatusCreated
	}

	s.metrics.ClaimCreateSuccess.Inc()
	return c.JSON(status, response)
}

func (s *Server) handleListLinks(c echo.Context) error {
	ctx := c.Request().Context()
	rawLimit := c.QueryParam("limit")
	rawOffset := c.QueryParam("offset")
	limit, offset, err := parsePagination(rawLimit, rawOffset)
	if err != nil {
		s.metrics.LinkListFailure.Inc()
		c.Logger().Errorf("list links: failed to parse pagination (limit=%q offset=%q): %v", rawLimit, rawOffset, err)
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	favoriteParam := strings.TrimSpace(c.QueryParam("favorite"))
	favoriteFilter := pgtype.Bool{}
	favoriteLogValue := "unset"
	if favoriteParam != "" {
		parsed, err := strconv.ParseBool(favoriteParam)
		if err != nil {
			s.metrics.LinkListFailure.Inc()
			c.Logger().Errorf("list links: invalid favorite filter %q: %v", favoriteParam, err)
			return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "favorite must be boolean"})
		}
		favoriteFilter = pgtype.Bool{Bool: parsed, Valid: true}
		favoriteLogValue = strconv.FormatBool(parsed)
	}

	queryText := strings.TrimSpace(c.QueryParam("q"))
	queryFilter := pgtype.Text{}
	if queryText != "" {
		queryFilter = pgtype.Text{String: queryText, Valid: true}
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
					c.Logger().Errorf("list links: unknown tag %q in filter (limit=%d offset=%d favorite=%s query=%q)", name, limit, offset, favoriteParam, queryText)
					return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown tag: %s", name)})
				}
				s.metrics.LinkListFailure.Inc()
				c.Logger().Errorf("list links: failed to resolve tag %q (limit=%d offset=%d favorite=%s query=%q tags=%q): %v", name, limit, offset, favoriteParam, queryText, tagsParam, err)
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
		UserID:         uuidToPg(s.cfg.DevUserID),
		Favorite:       favoriteFilter,
		Query:          queryFilter,
		EnableFullText: true,
		TagIds:         tagArg,
		PageLimit:      int32(limit),
		PageOffset:     int32(offset),
	}

	countParams := db.CountLinksParams{
		UserID:         uuidToPg(s.cfg.DevUserID),
		Favorite:       favoriteFilter,
		Query:          queryFilter,
		EnableFullText: true,
	}
	if len(tagIDs) > 0 {
		countParams.TagIds = tagIDs
	}

	items, err := s.queries.ListLinks(ctx, listParams)
	if err != nil {
		if listParams.EnableFullText && isFullTextParseError(err) {
			c.Logger().Warnf(
				"list links: full-text parse error for query %q, retrying with partial search: %v",
				queryText, err,
			)
			listParams.EnableFullText = false
			countParams.EnableFullText = false
			items, err = s.queries.ListLinks(ctx, listParams)
		}

		if err != nil {
			s.metrics.LinkListFailure.Inc()

			var pgErr *pgconn.PgError
			migrationMessage := ""
			migrationGap := false
			if errors.As(err, &pgErr) {
				migrationMessage, migrationGap = classifyReadinessError(err)
			}

			logTemplate := "list links: queries.ListLinks failed (limit=%d offset=%d favorite=%s query=%q tags=%q tagIDs=%v enableFullText=%t): %v"
			logArgs := []any{limit, offset, favoriteLogValue, queryText, tagsParam, tagIDs, listParams.EnableFullText, err}
			if migrationGap {
				logTemplate += " (classification=%s)"
				logArgs = append(logArgs, migrationMessage)
			}
			c.Logger().Errorf(logTemplate, logArgs...)

			if migrationGap {
				return c.JSON(stdhttp.StatusServiceUnavailable, map[string]string{
					"error": migrationMessage,
					"hint":  "apply outstanding database migrations",
				})
			}

			return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to fetch links"})
		}
	}

	count, err := s.queries.CountLinks(ctx, countParams)
	if err != nil {
		if countParams.EnableFullText && isFullTextParseError(err) {
			c.Logger().Warnf(
				"list links: full-text parse error when counting query %q, retrying with partial search: %v",
				queryText, err,
			)
			countParams.EnableFullText = false
			count, err = s.queries.CountLinks(ctx, countParams)
		}

		if err != nil {
			s.metrics.LinkListFailure.Inc()

			var pgErr *pgconn.PgError
			migrationMessage := ""
			migrationGap := false
			if errors.As(err, &pgErr) {
				migrationMessage, migrationGap = classifyReadinessError(err)
			}

			logTemplate := "list links: queries.CountLinks failed (limit=%d offset=%d favorite=%s query=%q tags=%q tagIDs=%v enableFullText=%t): %v"
			logArgs := []any{limit, offset, favoriteLogValue, queryText, tagsParam, tagIDs, countParams.EnableFullText, err}
			if migrationGap {
				logTemplate += " (classification=%s)"
				logArgs = append(logArgs, migrationMessage)
			}
			c.Logger().Errorf(logTemplate, logArgs...)

			if migrationGap {
				return c.JSON(stdhttp.StatusServiceUnavailable, map[string]string{
					"error": migrationMessage,
					"hint":  "apply outstanding database migrations",
				})
			}

			return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to count links"})
		}
	}

	responses := make([]linkResponse, 0, len(items))
	for _, item := range items {
		resp, err := toLinkResponse(item)
		if err != nil {
			s.metrics.LinkListFailure.Inc()
			c.Logger().Errorf(
				"list links: toLinkResponse failed for link %s (limit=%d offset=%d favorite=%v query=%q tags=%q tagIDs=%v): %v",
				uuidFromPg(item.ID), limit, offset, item.Favorite, queryText, tagsParam, tagIDs, err,
			)
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

func (s *Server) handleListRecommendations(c echo.Context) error {
	limit := 20
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if limit > 100 {
		limit = 100
	}

	ctx := c.Request().Context()
	rows, err := s.queries.ListRecommendationsForUser(ctx, db.ListRecommendationsForUserParams{
		UserID: uuidToPg(s.cfg.DevUserID),
		Limit:  int32(limit),
	})
	if err != nil {
		s.metrics.LinkListFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to load recommendations"})
	}

	responses := make([]linkResponse, 0, len(rows))
	for _, row := range rows {
		resp, err := s.buildRecommendationResponse(ctx, row)
		if err != nil {
			s.metrics.LinkListFailure.Inc()
			return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to expand recommendations"})
		}
		responses = append(responses, resp)
	}

	s.metrics.LinkListSuccess.Inc()

	return c.JSON(stdhttp.StatusOK, map[string]any{
		"items": responses,
		"limit": limit,
		"count": len(responses),
	})
}

func isFullTextParseError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	switch pgErr.Code {
	case pgerrcode.InvalidParameterValue, pgerrcode.SyntaxError, pgerrcode.InvalidTextRepresentation:
		return true
	default:
		return false
	}
}

func (s *Server) buildRecommendationResponse(ctx context.Context, row db.ListRecommendationsForUserRow) (linkResponse, error) {
	tags, err := s.queries.ListTagsForLink(ctx, row.ID)
	if err != nil {
		return linkResponse{}, err
	}

	tagResponses := make([]tagResponse, 0, len(tags))
	for _, tag := range tags {
		tagResponses = append(tagResponses, tagResponse{ID: tag.ID, Name: tag.Name})
	}

	highlights, err := s.queries.ListHighlightsByLink(ctx, row.ID)
	if err != nil {
		return linkResponse{}, err
	}

	highlightResponses := make([]highlightResponse, 0, len(highlights))
	for _, item := range highlights {
		highlightResponses = append(highlightResponses, toHighlightResponse(item))
	}

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

	archiveTitle := ""
	if row.ArchiveTitle.Valid {
		archiveTitle = row.ArchiveTitle.String
	}

	byline := ""
	if row.Byline.Valid {
		byline = row.Byline.String
	}

	lang := ""
	if row.Lang.Valid {
		lang = row.Lang.String
	}

	return linkResponse{
		ID:            uuidFromPg(row.ID).String(),
		URL:           row.Url,
		Title:         title,
		SourceDomain:  sourceDomain,
		Favorite:      row.Favorite,
		CreatedAt:     row.CreatedAt.Time,
		ReadAt:        readAt,
		ArchiveTitle:  archiveTitle,
		Byline:        byline,
		Lang:          lang,
		WordCount:     int(row.WordCount),
		ExtractedText: row.ExtractedText,
		Tags:          tagResponses,
		Highlights:    highlightResponses,
	}, nil
}

func (s *Server) handleListTags(c echo.Context) error {
	ctx := c.Request().Context()
	items, err := s.queries.ListTagLinkCounts(ctx)
	if err != nil {
		s.metrics.TagListFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to list tags"})
	}

	responses := make([]tagResponse, 0, len(items))
	for _, item := range items {
		count := item.LinkCount
		responses = append(responses, tagResponse{ID: item.ID, Name: item.Name, LinkCount: &count})
	}

	s.metrics.TagListSuccess.Inc()
	return c.JSON(stdhttp.StatusOK, responses)
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

	if err == nil {
		s.metrics.TagCreateFailure.Inc()
		return c.JSON(stdhttp.StatusConflict, map[string]string{"error": "tag already exists"})
	}

	tag, err = s.queries.CreateTag(ctx, name)
	if err != nil {
		s.metrics.TagCreateFailure.Inc()
		return c.JSON(stdhttp.StatusInternalServerError, map[string]string{"error": "failed to create tag"})
	}

	s.metrics.TagCreateSuccess.Inc()
	count := int32(0)
	return c.JSON(stdhttp.StatusCreated, tagResponse{ID: tag.ID, Name: tag.Name, LinkCount: &count})
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

	var req linkTagsRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	ctx := c.Request().Context()
	responses, err := s.setLinkTags(ctx, linkID, req.TagIDs)
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

	var req linkTagsRequest
	if err := c.Bind(&req); err != nil {
		s.metrics.LinkTagMutateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": "invalid payload"})
	}

	ctx := c.Request().Context()
	responses, err := s.setLinkTags(ctx, linkID, req.TagIDs)
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

	text, note, err := validateHighlightPayload(req)
	if err != nil {
		s.metrics.HighlightCreateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	limiter := s.highlightLimiterForUser(uuidFromPg(link.UserID))
	if limiter != nil && !limiter.Allow() {
		s.metrics.HighlightRateLimited.Inc()
		return c.JSON(stdhttp.StatusTooManyRequests, map[string]string{"error": "highlight rate limit exceeded"})
	}

	noteText := pgtype.Text{}
	if note != nil {
		noteText = pgtype.Text{String: *note, Valid: true}
	}

	ctx := c.Request().Context()
	start := time.Now()
	highlight, err := s.queries.CreateHighlight(ctx, db.CreateHighlightParams{
		LinkID: link.ID,
		Text:   text,
		Note:   noteText,
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

	text, note, err := validateHighlightPayload(req)
	if err != nil {
		s.metrics.HighlightUpdateFailure.Inc()
		return c.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	noteText := pgtype.Text{}
	if note != nil {
		noteText = pgtype.Text{String: *note, Valid: true}
	}

	ctx := c.Request().Context()
	highlight, err := s.queries.UpdateHighlight(ctx, db.UpdateHighlightParams{
		Text: text,
		Note: noteText,
		ID:   uuidToPg(highlightID),
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
	highlights, err := decodeHighlights([]byte(row.Highlights))
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

func (s *Server) setLinkTags(ctx context.Context, linkID uuid.UUID, ids []int32) ([]tagResponse, error) {
	unique := make([]int32, 0, len(ids))
	seen := make(map[int32]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, apiError{Code: stdhttp.StatusBadRequest, Message: "tag ids must be positive"}
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	linkIDPG := uuidToPg(linkID)
	current, err := s.queries.ListTagsForLink(ctx, linkIDPG)
	if err != nil {
		return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to list tags"}
	}

	currentByID := make(map[int32]db.Tag, len(current))
	for _, tag := range current {
		currentByID[tag.ID] = tag
	}

	desiredTags := make(map[int32]db.Tag, len(unique))
	for _, id := range unique {
		tag, err := s.queries.GetTag(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, apiError{Code: stdhttp.StatusBadRequest, Message: "tag not found"}
			}
			return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to resolve tag"}
		}

		desiredTags[id] = tag
		if _, ok := currentByID[id]; !ok {
			if err := s.queries.AddTagToLink(ctx, db.AddTagToLinkParams{LinkID: linkIDPG, TagID: id}); err != nil {
				return nil, apiError{Code: stdhttp.StatusInternalServerError, Message: "failed to assign tag"}
			}
		}
	}

	for id := range currentByID {
		if _, ok := desiredTags[id]; !ok {
			if err := s.queries.RemoveTagFromLink(ctx, db.RemoveTagFromLinkParams{LinkID: linkIDPG, TagID: id}); err != nil {
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
	maxHighlightTextLength = 2000
	maxHighlightNoteLength = 5000
)

func validateHighlightPayload(req highlightRequest) (string, *string, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return "", nil, fmt.Errorf("text is required")
	}
	if utf8.RuneCountInString(text) > maxHighlightTextLength {
		return "", nil, fmt.Errorf("text exceeds maximum length")
	}

	var notePtr *string
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		if trimmed != "" {
			if utf8.RuneCountInString(trimmed) > maxHighlightNoteLength {
				return "", nil, fmt.Errorf("note exceeds maximum length")
			}
			notePtr = &trimmed
		}
	}

	return text, notePtr, nil
}

func toHighlightResponse(item db.Highlight) highlightResponse {
	var note *string
	if item.Annotation.Valid {
		val := item.Annotation.String
		note = &val
	}
	return highlightResponse{
		ID:        uuidFromPg(item.ID).String(),
		Text:      item.Quote,
		Note:      note,
		CreatedAt: item.CreatedAt.Time,
		UpdatedAt: item.UpdatedAt.Time,
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
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Note      *string `json:"note"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

var postgresTimestampLayouts = []string{
	"2006-01-02 15:04:05.999999-07",
	"2006-01-02 15:04:05-07",
}

func parsePostgresTimestamp(value string) (time.Time, error) {
	var lastErr error

	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	} else {
		lastErr = err
	}

	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	} else {
		lastErr = err
	}

	for _, layout := range postgresTimestampLayouts {
		t, err := time.Parse(layout, value)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}

	return time.Time{}, fmt.Errorf("parse PostgreSQL timestamp %q: %w", value, lastErr)
}

// decodeHighlights accepts highlight payloads produced by the API. The timestamp
// fields should use RFC3339 or RFC3339Nano formatting, though legacy
// PostgreSQL layouts are still supported for older clients.
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
		createdAt, err := parsePostgresTimestamp(item.CreatedAt)
		if err != nil {
			log.Printf("decodeHighlights: failed to parse created_at: payload=%+v", item)
			return nil, fmt.Errorf("failed to parse highlight created_at: %w", err)
		}
		updatedAt, err := parsePostgresTimestamp(item.UpdatedAt)
		if err != nil {
			log.Printf("decodeHighlights: failed to parse updated_at: payload=%+v", item)
			return nil, fmt.Errorf("failed to parse highlight updated_at: %w", err)
		}

		highlights = append(highlights, highlightResponse{
			ID:        item.ID,
			Text:      item.Text,
			Note:      item.Note,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
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
