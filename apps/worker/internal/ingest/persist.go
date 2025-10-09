package ingest

import (
    "context"
    "errors"
    "fmt"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgtype"
    "github.com/jackc/pgx/v5/pgxpool"
)

// Store persists ingestion results into Postgres.
type Store struct {
    pool *pgxpool.Pool
}

// NewStore creates a Store instance.
func NewStore(pool *pgxpool.Pool) *Store {
    return &Store{pool: pool}
}

// Link represents the minimal data needed for ingestion.
type Link struct {
    ID  uuid.UUID
    URL string
}

// LookupLink retrieves a link record by identifier.
func (s *Store) LookupLink(ctx context.Context, id uuid.UUID) (Link, error) {
    row := s.pool.QueryRow(ctx, `SELECT id, url FROM links WHERE id = $1`, pgtype.UUID{Bytes: id, Valid: true})
    var link Link
    var idVal pgtype.UUID
    if err := row.Scan(&idVal, &link.URL); err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return Link{}, fmt.Errorf("link not found: %w", err)
        }
        return Link{}, fmt.Errorf("query link: %w", err)
    }
    link.ID = uuid.UUID(idVal.Bytes)
    return link, nil
}

// PersistResult writes the parsed article back to the database.
func (s *Store) PersistResult(ctx context.Context, linkID uuid.UUID, article Article, rawHTML []byte) error {
    tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        return fmt.Errorf("begin tx: %w", err)
    }
    defer tx.Rollback(ctx)

    if article.Title != "" {
        if _, err := tx.Exec(ctx, `UPDATE links SET title = $2 WHERE id = $1`, pgtype.UUID{Bytes: linkID, Valid: true}, pgtype.Text{String: article.Title, Valid: true}); err != nil {
            return fmt.Errorf("update title: %w", err)
        }
    }

    htmlContent := article.HTMLContent
    if htmlContent == "" {
        htmlContent = string(rawHTML)
    }

    if _, err := tx.Exec(ctx, `INSERT INTO archives (link_id, html, extracted_text, word_count, lang)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (link_id) DO UPDATE SET html = EXCLUDED.html, extracted_text = EXCLUDED.extracted_text, word_count = EXCLUDED.word_count, lang = EXCLUDED.lang`,
        pgtype.UUID{Bytes: linkID, Valid: true},
        pgtype.Text{String: htmlContent, Valid: true},
        pgtype.Text{String: article.TextContent, Valid: true},
        pgtype.Int4{Int32: int32(article.WordCount), Valid: true},
        pgtype.Text{String: article.Language, Valid: article.Language != ""},
    ); err != nil {
        return fmt.Errorf("upsert archive: %w", err)
    }

    if err := tx.Commit(ctx); err != nil {
        return fmt.Errorf("commit tx: %w", err)
    }
    return nil
}
