package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const rebuildPropertyLinks = `
INSERT INTO property_links (gnaf_pid, canonical_pid, match_basis, updated_at)
WITH latest AS (
    SELECT gnaf_pid, MAX(settlement_date) AS last_sale
    FROM property_sales
    GROUP BY gnaf_pid
),
keyed AS (
    SELECT
        p.gnaf_pid,
        CASE
            WHEN p.gnaf_pid NOT LIKE 'GANSW_PSI_%' THEN 'PID|' || p.gnaf_pid
            WHEN TRIM(COALESCE(p.street_number, '')) = '' THEN 'PID|' || p.gnaf_pid
            WHEN TRIM(COALESCE(p.lot_number, '')) <> '' OR TRIM(COALESCE(p.plan_number, '')) <> ''
                THEN 'ALP|' ||
                     UPPER(REPLACE(COALESCE(p.street_number, ''), ' ', '')) || '|' ||
                     UPPER(TRIM(p.street_name)) || '|' ||
                     UPPER(TRIM(COALESCE(p.street_type, ''))) || '|' ||
                     UPPER(TRIM(p.suburb)) || '|' || p.state || '|' || p.postcode || '|' ||
                     UPPER(TRIM(COALESCE(p.lot_number, ''))) || '|' ||
                     UPPER(TRIM(COALESCE(p.plan_number, '')))
            ELSE 'AP|' ||
                 UPPER(REPLACE(COALESCE(p.street_number, ''), ' ', '')) || '|' ||
                 UPPER(TRIM(p.street_name)) || '|' ||
                 UPPER(TRIM(COALESCE(p.street_type, ''))) || '|' ||
                 UPPER(TRIM(p.suburb)) || '|' || p.state || '|' || p.postcode || '|' ||
                 substr(p.gnaf_pid, instr(substr(p.gnaf_pid, 11), '_') + 11)
        END AS match_key,
        CASE
            WHEN p.gnaf_pid NOT LIKE 'GANSW_PSI_%' THEN 'self'
            WHEN TRIM(COALESCE(p.street_number, '')) = '' THEN 'self'
            WHEN TRIM(COALESCE(p.lot_number, '')) <> '' OR TRIM(COALESCE(p.plan_number, '')) <> ''
                THEN 'address_lot_plan'
            ELSE 'address_prop_id'
        END AS match_basis,
        COALESCE(l.last_sale, '') AS last_sale
    FROM gnaf_properties p
    LEFT JOIN latest l ON l.gnaf_pid = p.gnaf_pid
),
ranked AS (
    SELECT
        gnaf_pid,
        match_basis,
        FIRST_VALUE(gnaf_pid) OVER (
            PARTITION BY match_key
            ORDER BY last_sale DESC, gnaf_pid ASC
        ) AS canonical_pid
    FROM keyed
)
SELECT
    gnaf_pid,
    canonical_pid,
    CASE WHEN gnaf_pid = canonical_pid THEN 'self' ELSE match_basis END,
    CURRENT_TIMESTAMP
FROM ranked`

// RebuildPropertyLinks rebuilds the canonical property mapping. It does not
// delete duplicate property rows: sales still reference the original rows.
func RebuildPropertyLinks(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin property link rebuild: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM property_links"); err != nil {
		return fmt.Errorf("clear property links: %w", err)
	}
	if _, err := tx.ExecContext(ctx, rebuildPropertyLinks); err != nil {
		return fmt.Errorf("rebuild property links: %w", err)
	}
	return tx.Commit()
}

// EnsurePropertyLinks builds the mapping when it is empty. The bool reports
// whether a rebuild happened.
func EnsurePropertyLinks(ctx context.Context, db *sql.DB) (bool, error) {
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM property_links").Scan(&count); err != nil {
		return false, fmt.Errorf("count property links: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if err := RebuildPropertyLinks(ctx, db); err != nil {
		return false, err
	}
	return true, nil
}

// GetCanonicalPID returns the canonical PID for a property, falling back to the
// requested PID when it is not mapped.
func GetCanonicalPID(ctx context.Context, db *sql.DB, pid string) (string, error) {
	var canonical string
	err := db.QueryRowContext(ctx, "SELECT canonical_pid FROM property_links WHERE gnaf_pid = ?", pid).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return pid, nil
	}
	return canonical, err
}
