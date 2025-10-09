-- +goose Up
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    title TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at TIMESTAMPTZ,
    favorite BOOLEAN NOT NULL DEFAULT FALSE,
    search_tsv TSVECTOR NOT NULL DEFAULT ''::TSVECTOR
);

CREATE INDEX links_user_id_idx ON links(user_id);
CREATE INDEX links_search_tsv_idx ON links USING GIN (search_tsv);

CREATE TABLE archives (
    link_id UUID PRIMARY KEY REFERENCES links(id) ON DELETE CASCADE,
    html TEXT,
    extracted_text TEXT,
    word_count INTEGER,
    lang TEXT
);

CREATE TABLE tags (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE link_tags (
    link_id UUID NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (link_id, tag_id)
);

CREATE OR REPLACE FUNCTION links_search_tsv_update() RETURNS TRIGGER AS $$
DECLARE
    body TEXT;
BEGIN
    SELECT a.extracted_text INTO body FROM archives a WHERE a.link_id = NEW.id;
    NEW.search_tsv := to_tsvector('english', coalesce(NEW.title, '') || ' ' || coalesce(body, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION archives_refresh_link_search() RETURNS TRIGGER AS $$
BEGIN
    UPDATE links
    SET search_tsv = to_tsvector('english', coalesce(title, '') || ' ' || coalesce(NEW.extracted_text, ''))
    WHERE id = NEW.link_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER links_search_tsv_update_trigger
BEFORE INSERT OR UPDATE ON links
FOR EACH ROW EXECUTE FUNCTION links_search_tsv_update();

CREATE TRIGGER archives_refresh_link_search_trigger
AFTER INSERT OR UPDATE ON archives
FOR EACH ROW EXECUTE FUNCTION archives_refresh_link_search();

-- +goose Down
DROP TRIGGER IF EXISTS archives_refresh_link_search_trigger ON archives;
DROP TRIGGER IF EXISTS links_search_tsv_update_trigger ON links;
DROP FUNCTION IF EXISTS archives_refresh_link_search();
DROP FUNCTION IF EXISTS links_search_tsv_update();
DROP TABLE IF EXISTS link_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS archives;
DROP TABLE IF EXISTS links;
DROP TABLE IF EXISTS users;
