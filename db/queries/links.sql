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
       l.source_domain,
       l.created_at,
       l.read_at,
       l.favorite,
       COALESCE(a.title, '') AS archive_title,
       COALESCE(a.byline, '') AS archive_byline,
       COALESCE(a.lang, '') AS lang,
       COALESCE(a.word_count, 0) AS word_count,
       COALESCE(a.extracted_text, '') AS extracted_text,
       COALESCE(tag_data.tag_ids, ARRAY[]::INTEGER[]) AS tag_ids,
       COALESCE(tag_data.tag_names, ARRAY[]::TEXT[]) AS tag_names,
       COALESCE(highlight_data.highlights, '[]'::JSON) AS highlights
FROM links l
LEFT JOIN archives a ON a.link_id = l.id
LEFT JOIN LATERAL (
    SELECT ARRAY_AGG(t.id ORDER BY t.name) AS tag_ids,
           ARRAY_AGG(t.name ORDER BY t.name) AS tag_names
    FROM link_tags lt
    JOIN tags t ON t.id = lt.tag_id
    WHERE lt.link_id = l.id
) AS tag_data ON TRUE
LEFT JOIN LATERAL (
    SELECT json_agg(
               json_build_object(
                   'id', h.id,
                   'link_id', h.link_id,
                  'text', h.quote,
                  'note', h.annotation,
                   'created_at', h.created_at,
                   'updated_at', h.updated_at
               )
               ORDER BY h.created_at DESC
           ) AS highlights
    FROM highlights h
    WHERE h.link_id = l.id
) AS highlight_data ON TRUE
WHERE l.user_id = sqlc.arg('user_id')
  AND (
    sqlc.narg('favorite') IS NULL OR l.favorite = sqlc.narg('favorite')
  )
  AND (
    sqlc.narg('query') IS NULL
    OR l.search_tsv @@ plainto_tsquery('english', sqlc.narg('query'))
    OR l.url ILIKE '%' || sqlc.narg('query') || '%'
  )
  AND (
    sqlc.narg('tag_ids') IS NULL
    OR NOT EXISTS (
        SELECT 1
        FROM unnest(sqlc.narg('tag_ids')::int4[]) AS tag_id
        WHERE NOT EXISTS (
            SELECT 1
            FROM link_tags lt
            WHERE lt.link_id = l.id
              AND lt.tag_id = tag_id
        )
    )
  )
ORDER BY l.created_at DESC
LIMIT sqlc.arg('page_limit')
OFFSET sqlc.arg('page_offset');

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
  )
  AND (
    sqlc.narg('tag_ids') IS NULL
    OR NOT EXISTS (
        SELECT 1
        FROM unnest(sqlc.narg('tag_ids')::int4[]) AS tag_id
        WHERE NOT EXISTS (
            SELECT 1
            FROM link_tags lt
            WHERE lt.link_id = l.id
              AND lt.tag_id = tag_id
        )
    )
  );

-- name: UpsertArchive :exec
INSERT INTO archives (
    link_id,
    html,
    extracted_text,
    word_count,
    lang,
    title,
    byline
) VALUES (
    sqlc.arg('link_id'),
    sqlc.narg('html'),
    sqlc.narg('extracted_text'),
    sqlc.narg('word_count'),
    sqlc.narg('lang'),
    sqlc.narg('title'),
    sqlc.narg('byline')
)
ON CONFLICT (link_id) DO UPDATE
SET html = EXCLUDED.html,
    extracted_text = EXCLUDED.extracted_text,
    word_count = EXCLUDED.word_count,
    lang = EXCLUDED.lang,
    title = EXCLUDED.title,
    byline = EXCLUDED.byline;

-- name: UpdateLinkSourceDomain :exec
UPDATE links
SET source_domain = sqlc.narg('source_domain')
WHERE id = sqlc.arg('id');

-- name: UpdateLinkTitle :exec
UPDATE links
SET title = sqlc.narg('title')
WHERE id = sqlc.arg('id');

-- name: UpdateLinkFavorite :one
WITH updated AS (
    UPDATE links AS l
    SET favorite = sqlc.arg('favorite')
    WHERE l.id = sqlc.arg('id')
    RETURNING l.id,
              l.user_id,
              l.url,
              l.title,
              l.source_domain,
              l.created_at,
              l.read_at,
              l.favorite
)
SELECT u.id,
       u.user_id,
       u.url,
       u.title,
       u.source_domain,
       u.created_at,
       u.read_at,
       u.favorite,
       COALESCE(a.title, '') AS archive_title,
       COALESCE(a.byline, '') AS archive_byline,
       COALESCE(a.lang, '') AS lang,
       COALESCE(a.word_count, 0) AS word_count,
       COALESCE(a.extracted_text, '') AS extracted_text,
       COALESCE(tag_data.tag_ids, ARRAY[]::INTEGER[]) AS tag_ids,
       COALESCE(tag_data.tag_names, ARRAY[]::TEXT[]) AS tag_names,
       COALESCE(highlight_data.highlights, '[]'::JSON) AS highlights
FROM updated u
LEFT JOIN archives a ON a.link_id = u.id
LEFT JOIN LATERAL (
    SELECT ARRAY_AGG(t.id ORDER BY t.name) AS tag_ids,
           ARRAY_AGG(t.name ORDER BY t.name) AS tag_names
    FROM link_tags lt
    JOIN tags t ON t.id = lt.tag_id
    WHERE lt.link_id = u.id
) AS tag_data ON TRUE
LEFT JOIN LATERAL (
    SELECT json_agg(
               json_build_object(
                   'id', h.id,
                   'link_id', h.link_id,
                   'text', h.quote,
                   'note', h.annotation,
                   'created_at', h.created_at,
                   'updated_at', h.updated_at
               )
               ORDER BY h.created_at DESC
           ) AS highlights
    FROM highlights h
    WHERE h.link_id = u.id
) AS highlight_data ON TRUE;

-- name: CreateTag :one
INSERT INTO tags (name)
VALUES (sqlc.arg('name'))
RETURNING id, name;

-- name: DeleteTag :exec
DELETE FROM tags
WHERE id = sqlc.arg('id');

-- name: ListTags :many
SELECT id, name
FROM tags
ORDER BY name;

-- name: ListTagLinkCounts :many
SELECT t.id,
       t.name,
       COUNT(lt.link_id)::INT AS link_count
FROM tags t
LEFT JOIN link_tags lt ON lt.tag_id = t.id
GROUP BY t.id, t.name
ORDER BY t.name;

-- name: GetTag :one
SELECT id, name
FROM tags
WHERE id = sqlc.arg('id');

-- name: GetTagByName :one
SELECT id, name
FROM tags
WHERE name = sqlc.arg('name');

-- name: AddTagToLink :exec
INSERT INTO link_tags (link_id, tag_id)
VALUES (sqlc.arg('link_id'), sqlc.arg('tag_id'))
ON CONFLICT DO NOTHING;

-- name: RemoveTagFromLink :exec
DELETE FROM link_tags
WHERE link_id = sqlc.arg('link_id')
  AND tag_id = sqlc.arg('tag_id');

-- name: ListTagsForLink :many
SELECT t.id, t.name
FROM tags t
JOIN link_tags lt ON lt.tag_id = t.id
WHERE lt.link_id = sqlc.arg('link_id')
ORDER BY t.name;

-- name: CreateHighlight :one
INSERT INTO highlights (
    id,
    link_id,
    quote,
    annotation
)
VALUES (
    COALESCE(sqlc.narg('id'), gen_random_uuid()),
    sqlc.arg('link_id'),
    sqlc.arg('text'),
    sqlc.narg('note')
)
RETURNING id, link_id, quote, annotation, created_at, updated_at;

-- name: UpdateHighlight :one
UPDATE highlights
SET quote = sqlc.arg('text'),
    annotation = sqlc.narg('note'),
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING id, link_id, quote, annotation, created_at, updated_at;

-- name: DeleteHighlight :exec
DELETE FROM highlights
WHERE id = sqlc.arg('id');

-- name: ListHighlightsByLink :many
SELECT id, link_id, quote, annotation, created_at, updated_at
FROM highlights
WHERE link_id = sqlc.arg('link_id')
ORDER BY created_at DESC;
