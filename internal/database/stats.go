package database

import (
	"context"
	"database/sql"
	"fmt"
)

const insertSuburbStats = `
INSERT INTO suburb_stats (
    suburb, state, property_count, sales_count, avg_price, max_price, min_price,
    change_1y, change_5y, change_10y, updated_at
)
WITH aggregated AS (
    SELECT
        p.suburb,
        p.state,
        COUNT(DISTINCT COALESCE(pl.canonical_pid, p.gnaf_pid)) AS property_count,
        COUNT(s.sale_id) AS sales_count,
        AVG(s.sale_price) AS avg_price,
        MAX(s.sale_price) AS max_price,
        MIN(s.sale_price) AS min_price,
        AVG(CASE WHEN s.settlement_date >= date('now', '-12 months') THEN s.sale_price END) AS current_avg,
        AVG(CASE WHEN s.settlement_date >= date('now', '-24 months')
                  AND s.settlement_date < date('now', '-12 months') THEN s.sale_price END) AS avg_1y_ago,
        AVG(CASE WHEN s.settlement_date >= date('now', '-72 months')
                  AND s.settlement_date < date('now', '-60 months') THEN s.sale_price END) AS avg_5y_ago,
        AVG(CASE WHEN s.settlement_date >= date('now', '-132 months')
                  AND s.settlement_date < date('now', '-120 months') THEN s.sale_price END) AS avg_10y_ago
    FROM gnaf_properties p
    LEFT JOIN property_sales s ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    GROUP BY p.suburb, p.state
)
SELECT
    suburb,
    state,
    property_count,
    sales_count,
    CAST(COALESCE(avg_price, 0) AS INTEGER),
    CAST(COALESCE(max_price, 0) AS INTEGER),
    CAST(COALESCE(min_price, 0) AS INTEGER),
    CASE WHEN current_avg > 0 AND avg_1y_ago > 0
         THEN (current_avg - avg_1y_ago) * 100.0 / avg_1y_ago END,
    CASE WHEN current_avg > 0 AND avg_5y_ago > 0
         THEN (current_avg - avg_5y_ago) * 100.0 / avg_5y_ago END,
    CASE WHEN current_avg > 0 AND avg_10y_ago > 0
         THEN (current_avg - avg_10y_ago) * 100.0 / avg_10y_ago END,
    CURRENT_TIMESTAMP
FROM aggregated`

const refreshOneSuburbStats = `
INSERT INTO suburb_stats (
    suburb, state, property_count, sales_count, avg_price, max_price, min_price,
    change_1y, change_5y, change_10y, updated_at
)
WITH aggregated AS (
    SELECT
        p.suburb,
        p.state,
        COUNT(DISTINCT COALESCE(pl.canonical_pid, p.gnaf_pid)) AS property_count,
        COUNT(s.sale_id) AS sales_count,
        AVG(s.sale_price) AS avg_price,
        MAX(s.sale_price) AS max_price,
        MIN(s.sale_price) AS min_price,
        AVG(CASE WHEN s.settlement_date >= date('now', '-12 months') THEN s.sale_price END) AS current_avg,
        AVG(CASE WHEN s.settlement_date >= date('now', '-24 months')
                  AND s.settlement_date < date('now', '-12 months') THEN s.sale_price END) AS avg_1y_ago,
        AVG(CASE WHEN s.settlement_date >= date('now', '-72 months')
                  AND s.settlement_date < date('now', '-60 months') THEN s.sale_price END) AS avg_5y_ago,
        AVG(CASE WHEN s.settlement_date >= date('now', '-132 months')
                  AND s.settlement_date < date('now', '-120 months') THEN s.sale_price END) AS avg_10y_ago
    FROM gnaf_properties p
    LEFT JOIN property_sales s ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE p.suburb = UPPER(?1) AND p.state = UPPER(?2)
    GROUP BY p.suburb, p.state
)
SELECT
    suburb,
    state,
    property_count,
    sales_count,
    CAST(COALESCE(avg_price, 0) AS INTEGER),
    CAST(COALESCE(max_price, 0) AS INTEGER),
    CAST(COALESCE(min_price, 0) AS INTEGER),
    CASE WHEN current_avg > 0 AND avg_1y_ago > 0
         THEN (current_avg - avg_1y_ago) * 100.0 / avg_1y_ago END,
    CASE WHEN current_avg > 0 AND avg_5y_ago > 0
         THEN (current_avg - avg_5y_ago) * 100.0 / avg_5y_ago END,
    CASE WHEN current_avg > 0 AND avg_10y_ago > 0
         THEN (current_avg - avg_10y_ago) * 100.0 / avg_10y_ago END,
    CURRENT_TIMESTAMP
FROM aggregated
WHERE true
ON CONFLICT(suburb, state) DO UPDATE SET
    property_count = excluded.property_count,
    sales_count = excluded.sales_count,
    avg_price = excluded.avg_price,
    max_price = excluded.max_price,
    min_price = excluded.min_price,
    change_1y = excluded.change_1y,
    change_5y = excluded.change_5y,
    change_10y = excluded.change_10y,
    updated_at = CURRENT_TIMESTAMP`

// EnsureSuburbStatsSchema adds the price-change columns to an existing
// materialised stats table. If the table predates those columns it is dropped
// and recreated; the derived stats are rebuilt in the background afterwards.
func EnsureSuburbStatsSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(suburb_stats)")
	if err != nil {
		return fmt.Errorf("inspect suburb_stats: %w", err)
	}
	has1y, has5y, has10y := false, false, false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read suburb_stats columns: %w", err)
		}
		switch name {
		case "change_1y":
			has1y = true
		case "change_5y":
			has5y = true
		case "change_10y":
			has10y = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read suburb_stats columns: %w", err)
	}
	rows.Close()
	if has1y && has5y && has10y {
		return nil
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS suburb_stats"); err != nil {
		return fmt.Errorf("drop old suburb_stats: %w", err)
	}
	if _, err := db.ExecContext(ctx, SchemaSQL); err != nil {
		return fmt.Errorf("recreate suburb_stats: %w", err)
	}
	return nil
}

// RefreshSuburbStats rebuilds the whole materialised suburb summary. It is
// expensive on the full PSI dataset, so callers should run it in the
// background or at the end of a bulk import.
func RefreshSuburbStats(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin suburb stats refresh: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM suburb_stats"); err != nil {
		return fmt.Errorf("clear suburb stats: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insertSuburbStats); err != nil {
		return fmt.Errorf("rebuild suburb stats: %w", err)
	}
	return tx.Commit()
}

// RefreshSuburbStat updates one suburb after a new sale is recorded.
func RefreshSuburbStat(ctx context.Context, db *sql.DB, suburb, state string) error {
	if _, err := db.ExecContext(ctx, refreshOneSuburbStats, suburb, state); err != nil {
		return fmt.Errorf("refresh suburb stat %s/%s: %w", suburb, state, err)
	}
	return nil
}

// CountSuburbs returns the number of materialised suburb/state pairs.
func CountSuburbs(ctx context.Context, db *sql.DB) (int64, error) {
	var count int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM suburb_stats").Scan(&count)
	return count, err
}

// SuburbStateByName returns the state with the most recorded sales for a
// suburb. It is used when a direct URL supplies only a suburb name.
func SuburbStateByName(ctx context.Context, db *sql.DB, suburb string) (string, error) {
	var state string
	err := db.QueryRowContext(ctx, `
		SELECT state FROM suburb_stats
		WHERE suburb = UPPER(?)
		ORDER BY sales_count DESC
		LIMIT 1`, suburb).Scan(&state)
	return state, err
}
