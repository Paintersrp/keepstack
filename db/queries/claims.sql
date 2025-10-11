-- name: CreateClaim :one
WITH upsert AS (
    INSERT INTO claims (link_id, user_id)
    VALUES (sqlc.arg('link_id'), sqlc.arg('user_id'))
    ON CONFLICT (link_id, user_id) DO UPDATE
        SET claimed_at = claims.claimed_at
    RETURNING id, link_id, user_id, claimed_at, xmax = 0 AS inserted
)
SELECT id, link_id, user_id, claimed_at, inserted
FROM upsert;
