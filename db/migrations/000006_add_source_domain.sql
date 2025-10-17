-- +goose Up
ALTER TABLE links ADD COLUMN IF NOT EXISTS source_domain TEXT;

UPDATE links
SET source_domain = NULLIF(
    LOWER(
        split_part(
            split_part(
                split_part(
                    CASE
                        WHEN position('://' in url) > 0 THEN split_part(url, '://', 2)
                        ELSE url
                    END,
                    '/',
                    1
                ),
                '?',
                1
            ),
            '#',
            1
        )
    ),
    ''
)
WHERE url IS NOT NULL
  AND (source_domain IS NULL OR source_domain = '');

-- +goose Down
ALTER TABLE links DROP COLUMN IF EXISTS source_domain;
