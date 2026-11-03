package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// canonicalSalesSalePredicate matches all raw sale rows in the same canonical
// property/dedupe group. Keep this expression in sync with the dedupe key in
// rebuildCanonicalSales and refreshCanonicalSaleGroup.
const canonicalSalesDedupeKey = `COALESCE(pl.canonical_pid, s.gnaf_pid) || '|' ||
    s.settlement_date || '|' || CAST(s.sale_price AS TEXT) || '|' || s.source_type`

const rebuildCanonicalSales = `
INSERT INTO canonical_sales (
    sale_id, canonical_pid, suburb, state, postcode,
    settlement_date, sale_price, source_type, dedupe_key
)
WITH mapped AS (
    SELECT
        s.sale_id,
        COALESCE(pl.canonical_pid, s.gnaf_pid) AS canonical_pid,
        cp.suburb,
        cp.state,
        cp.postcode,
        s.settlement_date,
        s.sale_price,
        s.source_type,
        ` + canonicalSalesDedupeKey + ` AS dedupe_key
    FROM property_sales s
    LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
    JOIN gnaf_properties cp ON cp.gnaf_pid = COALESCE(pl.canonical_pid, s.gnaf_pid)
),
ranked AS (
    SELECT
        sale_id, canonical_pid, suburb, state, postcode,
        settlement_date, sale_price, source_type, dedupe_key,
        ROW_NUMBER() OVER (
            PARTITION BY dedupe_key
            ORDER BY sale_id ASC
        ) AS rn
    FROM mapped
)
SELECT
    sale_id, canonical_pid, suburb, state, postcode,
    settlement_date, sale_price, source_type, dedupe_key
FROM ranked
WHERE rn = 1`

const refreshCanonicalSaleGroup = `
INSERT INTO canonical_sales (
    sale_id, canonical_pid, suburb, state, postcode,
    settlement_date, sale_price, source_type, dedupe_key
)
SELECT
    s.sale_id,
    COALESCE(pl.canonical_pid, s.gnaf_pid) AS canonical_pid,
    cp.suburb,
    cp.state,
    cp.postcode,
    s.settlement_date,
    s.sale_price,
    s.source_type,
    ` + canonicalSalesDedupeKey + ` AS dedupe_key
FROM property_sales s
LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
JOIN gnaf_properties cp ON cp.gnaf_pid = COALESCE(pl.canonical_pid, s.gnaf_pid)
WHERE COALESCE(pl.canonical_pid, s.gnaf_pid) = ?1
  AND s.settlement_date = ?2
  AND s.sale_price = ?3
  AND s.source_type = ?4
ORDER BY s.sale_id ASC
LIMIT 1`

// RebuildCanonicalSales rebuilds the analytics-facing sale set. Run it after
// RebuildPropertyLinks and after bulk imports. Raw property_sales rows remain
// untouched.
func RebuildCanonicalSales(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin canonical sales rebuild: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM canonical_sales"); err != nil {
		return fmt.Errorf("clear canonical sales: %w", err)
	}
	if _, err := tx.ExecContext(ctx, rebuildCanonicalSales); err != nil {
		return fmt.Errorf("rebuild canonical sales: %w", err)
	}
	return tx.Commit()
}

// EnsureCanonicalSales populates the table on a fresh database or after an
// upgrade. Existing non-empty tables are left alone; normal writes keep them in
// sync through RefreshCanonicalSale.
func EnsureCanonicalSales(ctx context.Context, db *sql.DB) (bool, error) {
	var canonicalCount int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM canonical_sales").Scan(&canonicalCount); err != nil {
		return false, fmt.Errorf("count canonical sales: %w", err)
	}
	if canonicalCount > 0 {
		return false, nil
	}
	var saleCount int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM property_sales").Scan(&saleCount); err != nil {
		return false, fmt.Errorf("count property sales: %w", err)
	}
	if saleCount == 0 {
		return false, nil
	}
	if err := RebuildCanonicalSales(ctx, db); err != nil {
		return false, err
	}
	return true, nil
}

// RefreshCanonicalSale updates the canonical row for one inserted sale. It
// recomputes the whole dedupe group so the representative sale stays
// deterministic.
func RefreshCanonicalSale(ctx context.Context, db *sql.DB, saleID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin canonical sale refresh: %w", err)
	}
	defer tx.Rollback()

	var canonicalPID, settlementDate, sourceType string
	var salePrice int64
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(pl.canonical_pid, s.gnaf_pid), s.settlement_date, s.sale_price, s.source_type
		FROM property_sales s
		LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
		WHERE s.sale_id = ?`, saleID).Scan(&canonicalPID, &settlementDate, &salePrice, &sourceType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up sale %s: %w", saleID, err)
	}

	dedupeKey := fmt.Sprintf("%s|%s|%d|%s", canonicalPID, settlementDate, salePrice, sourceType)
	if _, err := tx.ExecContext(ctx, "DELETE FROM canonical_sales WHERE dedupe_key = ?", dedupeKey); err != nil {
		return fmt.Errorf("clear canonical sale group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, refreshCanonicalSaleGroup, canonicalPID, settlementDate, salePrice, sourceType); err != nil {
		return fmt.Errorf("refresh canonical sale group: %w", err)
	}
	return tx.Commit()
}
