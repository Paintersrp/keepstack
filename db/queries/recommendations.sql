-- name: ListUsersWithUnread :many
SELECT DISTINCT user_id
FROM links
WHERE read_at IS NULL;

-- name: ListUnreadLinksForUser :many
SELECT
    l.id,
    l.user_id,
    l.url,
    l.title,
    l.created_at,
    l.favorite,
    a.title AS archive_title,
    a.byline,
    a.lang,
    COALESCE(a.word_count, 0) AS word_count,
    COALESCE(a.extracted_text, '') AS extracted_text
FROM links l
LEFT JOIN archives a ON a.link_id = l.id
WHERE l.user_id = $1
  AND l.read_at IS NULL;

-- name: ClearRecommendationsForUser :exec
DELETE FROM recommendations
WHERE link_id IN (
    SELECT id FROM links WHERE user_id = $1
);

-- name: UpsertRecommendation :exec
INSERT INTO recommendations (link_id, score, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT (link_id) DO UPDATE
SET score = EXCLUDED.score,
    updated_at = EXCLUDED.updated_at;

-- name: ListRecommendationsForUser :many
SELECT
    l.id,
    l.url,
    l.title,
    l.source_domain,
    l.favorite,
    l.created_at,
    l.read_at,
    a.title AS archive_title,
    a.byline,
    a.lang,
    COALESCE(a.word_count, 0) AS word_count,
    COALESCE(a.extracted_text, '') AS extracted_text,
    r.score,
    r.updated_at
FROM recommendations r
JOIN links l ON l.id = r.link_id
LEFT JOIN archives a ON a.link_id = l.id
WHERE l.user_id = $1
ORDER BY r.score DESC, r.updated_at DESC
LIMIT $2;
