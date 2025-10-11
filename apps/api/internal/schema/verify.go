package schema

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type columnSpec struct {
	name     string
	dataType string
}

// Verify ensures the database schema matches the expected structure for v0.2 features.
func Verify(ctx context.Context, pool *pgxpool.Pool) error {
	var errs []error

	archivesReady := true
	if err := ensureTable(ctx, pool, "archives"); err != nil {
		archivesReady = false
		errs = append(errs, err)
	} else if err := ensureColumns(ctx, pool, "archives", []columnSpec{
		{name: "title", dataType: "text"},
		{name: "byline", dataType: "text"},
		{name: "lang", dataType: "text"},
		{name: "word_count", dataType: "integer"},
	}); err != nil {
		errs = append(errs, err)
	}

	if archivesReady {
		if err := ensureTriggers(ctx, pool, "archives", []string{"archives_refresh_link_search_trigger"}); err != nil {
			errs = append(errs, err)
		}
	}

	linksReady := true
	if err := ensureTable(ctx, pool, "links"); err != nil {
		linksReady = false
		errs = append(errs, err)
	} else {
		if err := ensureColumns(ctx, pool, "links", []columnSpec{{name: "source_domain", dataType: "text"}}); err != nil {
			errs = append(errs, err)
		}
	}

	if linksReady {
		if err := ensureTriggers(ctx, pool, "links", []string{"links_search_tsv_update_trigger"}); err != nil {
			errs = append(errs, err)
		}
	}

	if err := ensureTable(ctx, pool, "highlights"); err != nil {
		errs = append(errs, err)
	} else if err := ensureColumns(ctx, pool, "highlights", []columnSpec{
		{name: "id", dataType: "uuid"},
		{name: "link_id", dataType: "uuid"},
		{name: "quote", dataType: "text"},
		{name: "annotation", dataType: "text"},
		{name: "created_at", dataType: "timestamp with time zone"},
		{name: "updated_at", dataType: "timestamp with time zone"},
	}); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func ensureTable(ctx context.Context, pool *pgxpool.Pool, table string) error {
	const query = `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`

	var exists bool
	if err := pool.QueryRow(ctx, query, table).Scan(&exists); err != nil {
		return fmt.Errorf("check table %q: %w", table, err)
	}
	if !exists {
		return fmt.Errorf("database schema missing table %q", table)
	}
	return nil
}

func ensureColumns(ctx context.Context, pool *pgxpool.Pool, table string, specs []columnSpec) error {
	const query = `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`

	rows, err := pool.Query(ctx, query, table)
	if err != nil {
		return fmt.Errorf("query columns for table %q: %w", table, err)
	}
	defer rows.Close()

	columns := map[string]string{}
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			return fmt.Errorf("scan column metadata for table %q: %w", table, err)
		}
		columns[name] = dataType
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate columns for table %q: %w", table, err)
	}

	var missing []string
	var mismatched []string
	for _, spec := range specs {
		dataType, ok := columns[spec.name]
		if !ok {
			missing = append(missing, spec.name)
			continue
		}
		if dataType != spec.dataType {
			mismatched = append(mismatched, fmt.Sprintf("%s (expected %s, found %s)", spec.name, spec.dataType, dataType))
		}
	}

	if len(missing) == 0 && len(mismatched) == 0 {
		return nil
	}

	sort.Strings(missing)
	sort.Strings(mismatched)

	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing columns: %s", strings.Join(missing, ", ")))
	}
	if len(mismatched) > 0 {
		parts = append(parts, fmt.Sprintf("unexpected types: %s", strings.Join(mismatched, "; ")))
	}

	return fmt.Errorf("database schema mismatch on table %q (%s)", table, strings.Join(parts, "; "))
}

func ensureTriggers(ctx context.Context, pool *pgxpool.Pool, table string, expected []string) error {
	const query = `SELECT tgname FROM pg_trigger WHERE NOT tgisinternal AND tgrelid = $1::regclass`

	rows, err := pool.Query(ctx, query, table)
	if err != nil {
		return fmt.Errorf("query triggers for table %q: %w", table, err)
	}
	defer rows.Close()

	triggers := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan trigger metadata for table %q: %w", table, err)
		}
		triggers[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate triggers for table %q: %w", table, err)
	}

	var missing []string
	for _, name := range expected {
		if _, ok := triggers[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	sort.Strings(missing)
	return fmt.Errorf("database schema missing triggers on table %q: %s", table, strings.Join(missing, ", "))
}
