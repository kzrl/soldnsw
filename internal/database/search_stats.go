package database

import (
	"context"
	"database/sql"
	"fmt"
)

const insertStreetStats = `
INSERT INTO street_stats (
    street_name, street_type, suburb, state, postcode,
    property_count, sales_count, updated_at
)
WITH props AS MATERIALIZED (
    SELECT
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS canonical_pid,
        p.street_name, p.street_type, p.suburb, p.state, p.postcode
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid
),
sales AS (
    SELECT
        pr.street_name, pr.street_type, pr.suburb, pr.state, pr.postcode,
        COUNT(*) AS sales_count
    FROM canonical_sales cs
    JOIN props pr ON pr.canonical_pid = cs.canonical_pid
    GROUP BY pr.street_name, pr.street_type, pr.suburb, pr.state, pr.postcode
)
SELECT
    pr.street_name, pr.street_type, pr.suburb, pr.state, pr.postcode,
    COUNT(*) AS property_count,
    COALESCE(s.sales_count, 0) AS sales_count,
    CURRENT_TIMESTAMP
FROM props pr
LEFT JOIN sales s
    ON s.street_name = pr.street_name
   AND s.street_type = pr.street_type
   AND s.suburb = pr.suburb
   AND s.state = pr.state
   AND s.postcode = pr.postcode
GROUP BY pr.street_name, pr.street_type, pr.suburb, pr.state, pr.postcode`

const refreshOneStreetStats = `
INSERT INTO street_stats (
    street_name, street_type, suburb, state, postcode,
    property_count, sales_count, updated_at
)
WITH props AS (
    SELECT
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS canonical_pid,
        p.street_name, p.street_type, p.suburb, p.state, p.postcode
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
      AND p.street_name = UPPER(?1)
      AND p.street_type = UPPER(?2)
      AND p.suburb = UPPER(?3)
      AND p.state = UPPER(?4)
      AND p.postcode = ?5
),
sales AS (
    SELECT COUNT(*) AS sales_count
    FROM canonical_sales cs
    JOIN props pr ON pr.canonical_pid = cs.canonical_pid
)
SELECT
    UPPER(?1), UPPER(?2), UPPER(?3), UPPER(?4), ?5,
    COUNT(*) AS property_count,
    COALESCE((SELECT sales_count FROM sales), 0) AS sales_count,
    CURRENT_TIMESTAMP
FROM props
WHERE true
  AND EXISTS (SELECT 1 FROM street_stats)
ON CONFLICT(street_name, street_type, suburb, state, postcode) DO UPDATE SET
    property_count = excluded.property_count,
    sales_count = excluded.sales_count,
    updated_at = CURRENT_TIMESTAMP`

const insertPostcodeStats = `
INSERT INTO postcode_stats (
    postcode, property_count, sales_count, suburb_count, sample_suburb, updated_at
)
WITH props AS (
    SELECT
        p.postcode,
        COUNT(*) AS property_count,
        COUNT(DISTINCT p.suburb || '|' || p.state) AS suburb_count,
        MIN(p.suburb) AS sample_suburb
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid
    GROUP BY p.postcode
),
sales AS (
    SELECT cs.postcode, COUNT(*) AS sales_count
    FROM canonical_sales cs
    GROUP BY cs.postcode
)
SELECT
    p.postcode,
    p.property_count,
    COALESCE(s.sales_count, 0) AS sales_count,
    p.suburb_count,
    p.sample_suburb,
    CURRENT_TIMESTAMP
FROM props p
LEFT JOIN sales s ON s.postcode = p.postcode`

const refreshOnePostcodeStats = `
INSERT INTO postcode_stats (
    postcode, property_count, sales_count, suburb_count, sample_suburb, updated_at
)
WITH props AS (
    SELECT
        p.postcode,
        COUNT(*) AS property_count,
        COUNT(DISTINCT p.suburb || '|' || p.state) AS suburb_count,
        MIN(p.suburb) AS sample_suburb
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
      AND p.postcode = ?1
    GROUP BY p.postcode
),
sales AS (
    SELECT COUNT(*) AS sales_count
    FROM canonical_sales cs
    WHERE cs.postcode = ?1
)
SELECT
    ?1,
    p.property_count,
    COALESCE((SELECT sales_count FROM sales), 0),
    p.suburb_count,
    p.sample_suburb,
    CURRENT_TIMESTAMP
FROM props p
WHERE true
  AND EXISTS (SELECT 1 FROM postcode_stats)
ON CONFLICT(postcode) DO UPDATE SET
    property_count = excluded.property_count,
    sales_count = excluded.sales_count,
    suburb_count = excluded.suburb_count,
    sample_suburb = excluded.sample_suburb,
    updated_at = CURRENT_TIMESTAMP`

// RefreshStreetStats rebuilds the materialised street suggestion table.
func RefreshStreetStats(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin street stats refresh: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM street_stats"); err != nil {
		return fmt.Errorf("clear street stats: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insertStreetStats); err != nil {
		return fmt.Errorf("rebuild street stats: %w", err)
	}
	return tx.Commit()
}

// RefreshStreetStat updates one street after a sale is recorded.
func RefreshStreetStat(ctx context.Context, db *sql.DB, streetName, streetType, suburb, state, postcode string) error {
	if _, err := db.ExecContext(ctx, refreshOneStreetStats, streetName, streetType, suburb, state, postcode); err != nil {
		return fmt.Errorf("refresh street stat: %w", err)
	}
	return nil
}

// RefreshPostcodeStats rebuilds the materialised postcode suggestion table.
func RefreshPostcodeStats(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin postcode stats refresh: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM postcode_stats"); err != nil {
		return fmt.Errorf("clear postcode stats: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insertPostcodeStats); err != nil {
		return fmt.Errorf("rebuild postcode stats: %w", err)
	}
	return tx.Commit()
}

// RefreshPostcodeStat updates one postcode after a sale is recorded.
func RefreshPostcodeStat(ctx context.Context, db *sql.DB, postcode string) error {
	if _, err := db.ExecContext(ctx, refreshOnePostcodeStats, postcode); err != nil {
		return fmt.Errorf("refresh postcode stat %s: %w", postcode, err)
	}
	return nil
}

// EnsureStreetStats builds the table when it is empty.
func EnsureStreetStats(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM street_stats").Scan(&count); err != nil {
		return false, fmt.Errorf("count street stats: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if err := RefreshStreetStats(ctx, db); err != nil {
		return false, err
	}
	return true, nil
}

// EnsurePostcodeStats builds the table when it is empty.
func EnsurePostcodeStats(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM postcode_stats").Scan(&count); err != nil {
		return false, fmt.Errorf("count postcode stats: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if err := RefreshPostcodeStats(ctx, db); err != nil {
		return false, err
	}
	return true, nil
}
