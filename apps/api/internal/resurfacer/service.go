package resurfacer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/keepstack/apps/api/internal/db"
)

// Service recalculates resurfacing recommendations for unread links.
type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	now     func() time.Time
}

// New constructs a Service using the provided connection pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:    pool,
		queries: db.New(pool),
		now:     time.Now,
	}
}

// WithNow overrides the time source. Intended for tests.
func (s *Service) WithNow(now func() time.Time) {
	s.now = now
}

// Rebuild recalculates the recommendation set for all users with unread links.
func (s *Service) Rebuild(ctx context.Context, limit int) (int, error) {
	userIDs, err := s.queries.ListUsersWithUnread(ctx)
	if err != nil {
		return 0, fmt.Errorf("list users: %w", err)
	}

	total := 0
	for _, rawUserID := range userIDs {
		if !rawUserID.Valid {
			continue
		}
		userID := uuid.UUID(rawUserID.Bytes)
		count, err := s.rebuildForUser(ctx, userID, limit)
		if err != nil {
			return total, fmt.Errorf("rebuild user %s: %w", userID, err)
		}
		total += count
	}

	return total, nil
}

func (s *Service) rebuildForUser(ctx context.Context, userID uuid.UUID, limit int) (int, error) {
	rows, err := s.queries.ListUnreadLinksForUser(ctx, uuidToPg(userID))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, err
		}
		return 0, fmt.Errorf("list unread links: %w", err)
	}

	if len(rows) == 0 {
		if err := s.clearExisting(ctx, userID); err != nil {
			return 0, err
		}
		return 0, nil
	}

	now := s.now().UTC()
	candidates := make([]candidate, 0, len(rows))
	for _, row := range rows {
		linkID := uuid.UUID(row.ID.Bytes)
		createdAt := row.CreatedAt.Time
		score := scoreLink(now, createdAt, row.Favorite, int(row.WordCount))
		candidates = append(candidates, candidate{linkID: linkID, score: score, createdAt: createdAt})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].createdAt.Before(candidates[j].createdAt)
		}
		return candidates[i].score > candidates[j].score
	})

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := s.queries.WithTx(tx)
	if err := qtx.ClearRecommendationsForUser(ctx, uuidToPg(userID)); err != nil {
		return 0, fmt.Errorf("clear recommendations: %w", err)
	}

	updatedAt := pgtype.Timestamptz{Time: now, Valid: true}
	for _, candidate := range candidates {
		if err := qtx.UpsertRecommendation(ctx, db.UpsertRecommendationParams{
			LinkID:    uuidToPg(candidate.linkID),
			Score:     int32(candidate.score),
			UpdatedAt: updatedAt,
		}); err != nil {
			return 0, fmt.Errorf("upsert recommendation: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}

	return len(candidates), nil
}

func (s *Service) clearExisting(ctx context.Context, userID uuid.UUID) error {
	return s.queries.ClearRecommendationsForUser(ctx, uuidToPg(userID))
}

type candidate struct {
	linkID    uuid.UUID
	score     int
	createdAt time.Time
}

func scoreLink(now, created time.Time, favorite bool, wordCount int) int {
	if now.Before(created) {
		now = created
	}
	daysUnread := int(now.Sub(created).Hours() / 24)
	if daysUnread < 0 {
		daysUnread = 0
	}
	if daysUnread > 30 {
		daysUnread = 30
	}

	score := daysUnread
	if favorite {
		score += 10
	}

	switch {
	case wordCount >= 2500:
		score += 3
	case wordCount >= 1500:
		score += 2
	case wordCount >= 800:
		score += 1
	}

	return score
}

func uuidToPg(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
