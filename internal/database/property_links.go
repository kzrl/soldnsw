package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const propertyLinksVersion = "2"

const rebuildPropertyLinks = `
INSERT INTO property_links (gnaf_pid, canonical_pid, match_basis, updated_at)
WITH keyed AS (
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
        CASE WHEN p.gnaf_pid NOT LIKE 'GANSW_PSI_%' THEN 0 ELSE 1 END AS is_psi,
        CASE WHEN TRIM(COALESCE(p.street_number, '')) <> '' THEN 0 ELSE 1 END AS missing_number,
        CASE WHEN TRIM(COALESCE(p.lot_number, '')) <> '' OR TRIM(COALESCE(p.plan_number, '')) <> ''
             THEN 0 ELSE 1 END AS missing_cadastre,
        CASE WHEN p.latitude IS NOT NULL AND p.longitude IS NOT NULL THEN 0 ELSE 1 END AS missing_coordinates
    FROM gnaf_properties p
),
ranked AS (
    SELECT
        gnaf_pid,
        match_basis,
        FIRST_VALUE(gnaf_pid) OVER (
            PARTITION BY match_key
            ORDER BY
                is_psi ASC,
                missing_number ASC,
                missing_cadastre ASC,
                missing_coordinates ASC,
                gnaf_pid ASC
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_meta (key, value) VALUES ('property_links_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, propertyLinksVersion); err != nil {
		return fmt.Errorf("record property link version: %w", err)
	}
	return tx.Commit()
}

// EnsurePropertyLinks rebuilds the mapping when it is empty or was produced by
// an older canonical-selection rule. The bool reports whether a rebuild
// happened.
func EnsurePropertyLinks(ctx context.Context, db *sql.DB) (bool, error) {
	var version string
	versionErr := db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key = 'property_links_version'").Scan(&version)
	if versionErr != nil && !errors.Is(versionErr, sql.ErrNoRows) {
		return false, fmt.Errorf("read property link version: %w", versionErr)
	}
	if versionErr == nil && version == propertyLinksVersion {
		var count int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM property_links").Scan(&count); err != nil {
			return false, fmt.Errorf("count property links: %w", err)
		}
		if count > 0 {
			return false, nil
		}
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
