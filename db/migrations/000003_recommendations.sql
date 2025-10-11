-- +goose Up
CREATE TABLE IF NOT EXISTS recommendations (
    link_id UUID PRIMARY KEY REFERENCES links(id) ON DELETE CASCADE,
    score INTEGER NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS recommendations;
