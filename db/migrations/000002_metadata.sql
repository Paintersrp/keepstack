-- +goose Up
ALTER TABLE archives
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS byline TEXT,
    ADD COLUMN IF NOT EXISTS lang TEXT,
    ADD COLUMN IF NOT EXISTS word_count INTEGER;

ALTER TABLE links
    ADD COLUMN IF NOT EXISTS source_domain TEXT;

CREATE TABLE IF NOT EXISTS highlights (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id UUID NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    quote TEXT NOT NULL,
    annotation TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS highlights_link_id_idx ON highlights(link_id);

DROP TRIGGER IF EXISTS archives_refresh_link_search_trigger ON archives;
DROP TRIGGER IF EXISTS links_search_tsv_update_trigger ON links;
DROP FUNCTION IF EXISTS archives_refresh_link_search();
DROP FUNCTION IF EXISTS links_search_tsv_update();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION links_search_tsv_update() RETURNS TRIGGER AS $$
DECLARE
    archive_title TEXT;
    archive_byline TEXT;
    archive_body TEXT;
BEGIN
    SELECT a.title, a.byline, a.extracted_text
    INTO archive_title, archive_byline, archive_body
    FROM archives a
    WHERE a.link_id = NEW.id;

    NEW.search_tsv := to_tsvector(
        'english',
        coalesce(NEW.title::text, '') || ' ' ||
        coalesce(archive_title, '') || ' ' ||
        coalesce(archive_byline, '') || ' ' ||
        coalesce(archive_body, '')
    );

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION archives_refresh_link_search() RETURNS TRIGGER AS $$
DECLARE
    link_title TEXT;
BEGIN
    SELECT l.title INTO link_title FROM links l WHERE l.id = NEW.link_id;

    UPDATE links
    SET search_tsv = to_tsvector(
        'english',
        coalesce(link_title, '') || ' ' ||
        coalesce(NEW.title, '') || ' ' ||
        coalesce(NEW.byline, '') || ' ' ||
        coalesce(NEW.extracted_text, '')
    )
    WHERE id = NEW.link_id;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

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

DROP INDEX IF EXISTS highlights_link_id_idx;
DROP TABLE IF EXISTS highlights;

ALTER TABLE links DROP COLUMN IF EXISTS source_domain;
ALTER TABLE archives DROP COLUMN IF EXISTS title;
ALTER TABLE archives DROP COLUMN IF EXISTS byline;
ALTER TABLE archives DROP COLUMN IF EXISTS lang;
ALTER TABLE archives DROP COLUMN IF EXISTS word_count;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION links_search_tsv_update() RETURNS TRIGGER AS $$
DECLARE
    body TEXT;
BEGIN
    SELECT a.extracted_text INTO body FROM archives a WHERE a.link_id = NEW.id;
    NEW.search_tsv := to_tsvector('english', coalesce(NEW.title, '') || ' ' || coalesce(body, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION archives_refresh_link_search() RETURNS TRIGGER AS $$
BEGIN
    UPDATE links
    SET search_tsv = to_tsvector('english', coalesce(title, '') || ' ' || coalesce(NEW.extracted_text, ''))
    WHERE id = NEW.link_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER links_search_tsv_update_trigger
BEFORE INSERT OR UPDATE ON links
FOR EACH ROW EXECUTE FUNCTION links_search_tsv_update();

CREATE TRIGGER archives_refresh_link_search_trigger
AFTER INSERT OR UPDATE ON archives
FOR EACH ROW EXECUTE FUNCTION archives_refresh_link_search();
