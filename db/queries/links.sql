-- name: CreateLink :one
INSERT INTO links (
    id,
    user_id,
    url,
    title,
    favorite
) VALUES (
    sqlc.arg('id'),
    sqlc.arg('user_id'),
    sqlc.arg('url'),
    sqlc.narg('title'),
    COALESCE(sqlc.narg('favorite'), FALSE)
)
RETURNING id, user_id, url, title, created_at, read_at, favorite;

-- name: GetLink :one
SELECT l.id,
       l.user_id,
       l.url,
       l.title,
       l.created_at,
       l.read_at,
       l.favorite
FROM links l
WHERE l.id = sqlc.arg('id');

-- name: ListLinks :many
SELECT l.id,
       l.user_id,
       l.url,
       l.title,
       l.created_at,
       l.read_at,
       l.favorite,
       COALESCE(a.extracted_text, '') AS extracted_text
FROM links l
LEFT JOIN archives a ON a.link_id = l.id
WHERE l.user_id = sqlc.arg('user_id')
  AND (
    sqlc.narg('favorite') IS NULL OR l.favorite = sqlc.narg('favorite')
  )
  AND (
    sqlc.narg('query') IS NULL
    OR l.search_tsv @@ plainto_tsquery('english', sqlc.narg('query'))
    OR l.url ILIKE '%' || sqlc.narg('query') || '%'
  )
ORDER BY l.created_at DESC
LIMIT sqlc.arg('limit')
OFFSET sqlc.arg('offset');

-- name: CountLinks :one
SELECT COUNT(*)
FROM links l
WHERE l.user_id = sqlc.arg('user_id')
  AND (
    sqlc.narg('favorite') IS NULL OR l.favorite = sqlc.narg('favorite')
  )
  AND (
    sqlc.narg('query') IS NULL
    OR l.search_tsv @@ plainto_tsquery('english', sqlc.narg('query'))
    OR l.url ILIKE '%' || sqlc.narg('query') || '%'
  );

-- name: UpsertArchive :exec
INSERT INTO archives (
    link_id,
    html,
    extracted_text,
    word_count,
    lang
) VALUES (
    sqlc.arg('link_id'),
    sqlc.narg('html'),
    sqlc.narg('extracted_text'),
    sqlc.narg('word_count'),
    sqlc.narg('lang')
)
ON CONFLICT (link_id) DO UPDATE
SET html = EXCLUDED.html,
    extracted_text = EXCLUDED.extracted_text,
    word_count = EXCLUDED.word_count,
    lang = EXCLUDED.lang;

-- name: UpdateLinkTitle :exec
UPDATE links
SET title = sqlc.narg('title')
WHERE id = sqlc.arg('id');
