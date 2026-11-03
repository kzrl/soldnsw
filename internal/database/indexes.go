package database

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

// IndexesSQL contains the heavy indexes applied by the explicit migrate
// command. Servers should not build these synchronously during startup.
//
//go:embed indexes.sql
var IndexesSQL string

var requiredSearchIndexes = []string{
	"idx_gnaf_suburb",
	"idx_gnaf_suburb_state",
	"idx_gnaf_state",
	"idx_gnaf_street",
	"idx_gnaf_postcode",
	"idx_gnaf_base_pid",
	"idx_gnaf_street_group",
	"idx_gnaf_postcode_street",
	"idx_sales_lookup",
	"idx_sales_date",
	"idx_property_links_canonical",
	"idx_canonical_sales_dedupe",
	"idx_canonical_sales_suburb",
	"idx_canonical_sales_postcode",
	"idx_canonical_sales_property",
	"idx_canonical_sales_recent",
	"idx_import_runs_finished",
	"idx_street_stats_count",
	"idx_street_stats_location",
	"idx_postcode_stats_sales",
}

// EnsureSearchIndexes creates the heavy indexes. It is intended for the
// explicit migrate command, not for normal server startup.
func EnsureSearchIndexes(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, IndexesSQL); err != nil {
		return fmt.Errorf("create search indexes: %w", err)
	}
	return nil
}

// MissingSearchIndexes returns required index names that are absent from the
// database. The server logs these at startup and points operators at the
// migrate command.
func MissingSearchIndexes(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'index'")
	if err != nil {
		return nil, fmt.Errorf("list indexes: %w", err)
	}
	defer rows.Close()

	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []string
	for _, name := range requiredSearchIndexes {
		if !existing[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// DescribeMissingIndexes formats the missing index list for startup logs.
func DescribeMissingIndexes(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return strings.Join(missing, ", ")
}
