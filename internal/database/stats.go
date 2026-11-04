package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const insertSuburbStats = `
INSERT INTO suburb_stats (
    suburb, state, property_count, sales_count, avg_price, max_price, min_price,
    change_1y, change_5y, change_10y, updated_at
)
WITH props AS (
    SELECT
        p.suburb,
        p.state,
        COUNT(DISTINCT COALESCE(pl.canonical_pid, p.gnaf_pid)) AS property_count
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    GROUP BY p.suburb, p.state
),
sales AS (
    SELECT
        cs.suburb,
        cs.state,
        COUNT(*) AS sales_count,
        AVG(cs.sale_price) AS avg_price,
        MAX(cs.sale_price) AS max_price,
        MIN(cs.sale_price) AS min_price,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-12 months')
                 THEN cs.sale_price END) AS current_avg,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-24 months')
                  AND cs.settlement_date < date('now', '-12 months')
                 THEN cs.sale_price END) AS avg_1y_ago,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-72 months')
                  AND cs.settlement_date < date('now', '-60 months')
                 THEN cs.sale_price END) AS avg_5y_ago,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-132 months')
                  AND cs.settlement_date < date('now', '-120 months')
                 THEN cs.sale_price END) AS avg_10y_ago
    FROM canonical_sales cs
    GROUP BY cs.suburb, cs.state
),
aggregated AS (
    SELECT
        p.suburb,
        p.state,
        p.property_count,
        COALESCE(s.sales_count, 0) AS sales_count,
        CAST(COALESCE(s.avg_price, 0) AS INTEGER) AS avg_price,
        CAST(COALESCE(s.max_price, 0) AS INTEGER) AS max_price,
        CAST(COALESCE(s.min_price, 0) AS INTEGER) AS min_price,
        CASE WHEN s.current_avg > 0 AND s.avg_1y_ago > 0
             THEN (s.current_avg - s.avg_1y_ago) * 100.0 / s.avg_1y_ago END AS change_1y,
        CASE WHEN s.current_avg > 0 AND s.avg_5y_ago > 0
             THEN (s.current_avg - s.avg_5y_ago) * 100.0 / s.avg_5y_ago END AS change_5y,
        CASE WHEN s.current_avg > 0 AND s.avg_10y_ago > 0
             THEN (s.current_avg - s.avg_10y_ago) * 100.0 / s.avg_10y_ago END AS change_10y
    FROM props p
    LEFT JOIN sales s ON s.suburb = p.suburb AND s.state = p.state
)
SELECT
    suburb,
    state,
    property_count,
    sales_count,
    avg_price,
    max_price,
    min_price,
    change_1y,
    change_5y,
    change_10y,
    CURRENT_TIMESTAMP
FROM aggregated`

const refreshOneSuburbStats = `
INSERT INTO suburb_stats (
    suburb, state, property_count, sales_count, avg_price, max_price, min_price,
    change_1y, change_5y, change_10y, updated_at
)
WITH props AS (
    SELECT
        p.suburb,
        p.state,
        COUNT(DISTINCT COALESCE(pl.canonical_pid, p.gnaf_pid)) AS property_count
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE p.suburb = UPPER(?1) AND p.state = UPPER(?2)
    GROUP BY p.suburb, p.state
),
sales AS (
    SELECT
        cs.suburb,
        cs.state,
        COUNT(*) AS sales_count,
        AVG(cs.sale_price) AS avg_price,
        MAX(cs.sale_price) AS max_price,
        MIN(cs.sale_price) AS min_price,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-12 months')
                 THEN cs.sale_price END) AS current_avg,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-24 months')
                  AND cs.settlement_date < date('now', '-12 months')
                 THEN cs.sale_price END) AS avg_1y_ago,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-72 months')
                  AND cs.settlement_date < date('now', '-60 months')
                 THEN cs.sale_price END) AS avg_5y_ago,
        AVG(CASE WHEN cs.settlement_date >= date('now', '-132 months')
                  AND cs.settlement_date < date('now', '-120 months')
                 THEN cs.sale_price END) AS avg_10y_ago
    FROM canonical_sales cs
    WHERE cs.suburb = UPPER(?1) AND cs.state = UPPER(?2)
    GROUP BY cs.suburb, cs.state
),
aggregated AS (
    SELECT
        p.suburb,
        p.state,
        p.property_count,
        COALESCE(s.sales_count, 0) AS sales_count,
        CAST(COALESCE(s.avg_price, 0) AS INTEGER) AS avg_price,
        CAST(COALESCE(s.max_price, 0) AS INTEGER) AS max_price,
        CAST(COALESCE(s.min_price, 0) AS INTEGER) AS min_price,
        CASE WHEN s.current_avg > 0 AND s.avg_1y_ago > 0
             THEN (s.current_avg - s.avg_1y_ago) * 100.0 / s.avg_1y_ago END AS change_1y,
        CASE WHEN s.current_avg > 0 AND s.avg_5y_ago > 0
             THEN (s.current_avg - s.avg_5y_ago) * 100.0 / s.avg_5y_ago END AS change_5y,
        CASE WHEN s.current_avg > 0 AND s.avg_10y_ago > 0
             THEN (s.current_avg - s.avg_10y_ago) * 100.0 / s.avg_10y_ago END AS change_10y
    FROM props p
    LEFT JOIN sales s ON s.suburb = p.suburb AND s.state = p.state
)
SELECT
    suburb,
    state,
    property_count,
    sales_count,
    avg_price,
    max_price,
    min_price,
    change_1y,
    change_5y,
    change_10y,
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
	if err := ensureSuburbLossStatsSchema(ctx, db); err != nil {
		return err
	}
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

// ensureSuburbLossStatsSchema adds the average-loss percentage column to an
// existing ranking table. Rows are cleared so the background builder can
// repopulate the new metric with correct values.
func ensureSuburbLossStatsSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(suburb_loss_stats)")
	if err != nil {
		return fmt.Errorf("inspect suburb_loss_stats: %w", err)
	}
	hasAverageLossPercent := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read suburb_loss_stats columns: %w", err)
		}
		if name == "average_loss_percent" {
			hasAverageLossPercent = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read suburb_loss_stats columns: %w", err)
	}
	rows.Close()
	if hasAverageLossPercent {
		return nil
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE suburb_loss_stats ADD COLUMN average_loss_percent REAL NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("add suburb_loss_stats.average_loss_percent: %w", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM suburb_loss_stats"); err != nil {
		return fmt.Errorf("clear suburb loss stats after schema upgrade: %w", err)
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

const suburbLossPeriod = "2y"

const insertSuburbLossStats = `
INSERT INTO suburb_loss_stats (
    suburb, state, period, resales, losses, total_loss, average_loss,
    average_loss_percent, largest_loss, flagged_losses, updated_at
)
WITH ordered AS (
    SELECT
        cs.canonical_pid,
        cs.suburb,
        cs.state,
        cs.sale_id,
        cs.settlement_date,
        cs.sale_price,
        LAG(cs.sale_price) OVER w AS previous_price,
        LAG(cs.settlement_date) OVER w AS previous_date,
        ROW_NUMBER() OVER (
            PARTITION BY cs.canonical_pid
            ORDER BY cs.settlement_date DESC, cs.sale_id DESC
        ) AS rn
    FROM canonical_sales cs
    WINDOW w AS (
        PARTITION BY cs.canonical_pid
        ORDER BY cs.settlement_date ASC, cs.sale_id ASC
    )
),
candidates AS (
    SELECT *
    FROM ordered
    WHERE rn = 1
      AND previous_price IS NOT NULL
      AND previous_date < settlement_date
      AND settlement_date >= ?1
),
grouped AS (
    SELECT
        suburb,
        state,
        COUNT(*) AS resales,
        COUNT(CASE WHEN sale_price < previous_price THEN 1 END) AS losses,
        CAST(COALESCE(SUM(CASE WHEN sale_price < previous_price
            THEN previous_price - sale_price ELSE 0 END), 0) AS INTEGER) AS total_loss,
        CAST(COALESCE(AVG(CASE WHEN sale_price < previous_price
            THEN previous_price - sale_price END), 0) AS INTEGER) AS average_loss,
        CAST(COALESCE(AVG(CASE WHEN sale_price < previous_price
            THEN (previous_price - sale_price) * 100.0 / NULLIF(previous_price, 0)
            END), 0) AS REAL) AS average_loss_percent,
        CAST(COALESCE(MAX(CASE WHEN sale_price < previous_price
            THEN previous_price - sale_price END), 0) AS INTEGER) AS largest_loss,
        COUNT(CASE WHEN sale_price < previous_price
            AND previous_price > 0
            AND (sale_price - previous_price) * 100.0 / previous_price < -80
            THEN 1 END) AS flagged_losses
    FROM candidates
    GROUP BY suburb, state
)
SELECT
    suburb, state, '2y', resales, losses, total_loss, average_loss,
    average_loss_percent, largest_loss, flagged_losses, CURRENT_TIMESTAMP
FROM grouped`

const refreshOneSuburbLossStats = `
INSERT INTO suburb_loss_stats (
    suburb, state, period, resales, losses, total_loss, average_loss,
    average_loss_percent, largest_loss, flagged_losses, updated_at
)
WITH ordered AS (
    SELECT
        cs.canonical_pid,
        cs.suburb,
        cs.state,
        cs.sale_id,
        cs.settlement_date,
        cs.sale_price,
        LAG(cs.sale_price) OVER w AS previous_price,
        LAG(cs.settlement_date) OVER w AS previous_date,
        ROW_NUMBER() OVER (
            PARTITION BY cs.canonical_pid
            ORDER BY cs.settlement_date DESC, cs.sale_id DESC
        ) AS rn
    FROM canonical_sales cs
    WHERE cs.suburb = UPPER(?1) AND cs.state = UPPER(?2)
    WINDOW w AS (
        PARTITION BY cs.canonical_pid
        ORDER BY cs.settlement_date ASC, cs.sale_id ASC
    )
),
candidates AS (
    SELECT *
    FROM ordered
    WHERE rn = 1
      AND previous_price IS NOT NULL
      AND previous_date < settlement_date
      AND settlement_date >= ?3
),
aggregated AS (
    SELECT
        COUNT(*) AS resales,
        COUNT(CASE WHEN sale_price < previous_price THEN 1 END) AS losses,
        CAST(COALESCE(SUM(CASE WHEN sale_price < previous_price
            THEN previous_price - sale_price ELSE 0 END), 0) AS INTEGER) AS total_loss,
        CAST(COALESCE(AVG(CASE WHEN sale_price < previous_price
            THEN previous_price - sale_price END), 0) AS INTEGER) AS average_loss,
        CAST(COALESCE(AVG(CASE WHEN sale_price < previous_price
            THEN (previous_price - sale_price) * 100.0 / NULLIF(previous_price, 0)
            END), 0) AS REAL) AS average_loss_percent,
        CAST(COALESCE(MAX(CASE WHEN sale_price < previous_price
            THEN previous_price - sale_price END), 0) AS INTEGER) AS largest_loss,
        COUNT(CASE WHEN sale_price < previous_price
            AND previous_price > 0
            AND (sale_price - previous_price) * 100.0 / previous_price < -80
            THEN 1 END) AS flagged_losses
    FROM candidates
)
SELECT
    UPPER(?1), UPPER(?2), '2y', resales, losses, total_loss, average_loss,
    average_loss_percent, largest_loss, flagged_losses, CURRENT_TIMESTAMP
FROM aggregated
WHERE true
ON CONFLICT(suburb, state, period) DO UPDATE SET
    resales = excluded.resales,
    losses = excluded.losses,
    total_loss = excluded.total_loss,
    average_loss = excluded.average_loss,
    average_loss_percent = excluded.average_loss_percent,
    largest_loss = excluded.largest_loss,
    flagged_losses = excluded.flagged_losses,
    updated_at = CURRENT_TIMESTAMP`

// RefreshSuburbStat updates one suburb after a new sale is recorded.
func RefreshSuburbStat(ctx context.Context, db *sql.DB, suburb, state string) error {
	if _, err := db.ExecContext(ctx, refreshOneSuburbStats, suburb, state); err != nil {
		return fmt.Errorf("refresh suburb stat %s/%s: %w", suburb, state, err)
	}
	return nil
}

// RefreshSuburbLossStats rebuilds the two-year loss ranking table. It runs a
// window over the whole canonical sale set, so callers should use it in the
// background or at the end of a bulk import.
func RefreshSuburbLossStats(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin suburb loss stats refresh: %w", err)
	}
	defer tx.Rollback()

	cutoff := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
	if _, err := tx.ExecContext(ctx, "DELETE FROM suburb_loss_stats WHERE period = ?", suburbLossPeriod); err != nil {
		return fmt.Errorf("clear suburb loss stats: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insertSuburbLossStats, cutoff); err != nil {
		return fmt.Errorf("rebuild suburb loss stats: %w", err)
	}
	return tx.Commit()
}

// RefreshSuburbLossStat updates one suburb's ranking row after a new sale.
func RefreshSuburbLossStat(ctx context.Context, db *sql.DB, suburb, state string) error {
	cutoff := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
	if _, err := db.ExecContext(ctx, refreshOneSuburbLossStats, suburb, state, cutoff); err != nil {
		return fmt.Errorf("refresh suburb loss stat %s/%s: %w", suburb, state, err)
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
