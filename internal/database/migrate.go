package database

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsurePropertyColumns adds property_type and land_area to databases created
// before those columns existed. New databases already have them via schema.sql.
func EnsurePropertyColumns(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(gnaf_properties)")
	if err != nil {
		return fmt.Errorf("inspect gnaf_properties: %w", err)
	}

	hasPropertyType := false
	hasLandArea := false
	hasBasePID := false
	hasDwellingType := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read gnaf_properties columns: %w", err)
		}
		switch name {
		case "property_type":
			hasPropertyType = true
		case "land_area":
			hasLandArea = true
		case "base_pid":
			hasBasePID = true
		case "dwelling_type":
			hasDwellingType = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read gnaf_properties columns: %w", err)
	}
	rows.Close()

	if !hasPropertyType {
		if _, err := db.ExecContext(ctx, "ALTER TABLE gnaf_properties ADD COLUMN property_type TEXT"); err != nil {
			return fmt.Errorf("add property_type: %w", err)
		}
	}
	if !hasLandArea {
		if _, err := db.ExecContext(ctx, "ALTER TABLE gnaf_properties ADD COLUMN land_area REAL"); err != nil {
			return fmt.Errorf("add land_area: %w", err)
		}
	}
	if !hasBasePID {
		if _, err := db.ExecContext(ctx, "ALTER TABLE gnaf_properties ADD COLUMN base_pid TEXT"); err != nil {
			return fmt.Errorf("add base_pid: %w", err)
		}
	}
	if !hasDwellingType {
		if _, err := db.ExecContext(ctx, "ALTER TABLE gnaf_properties ADD COLUMN dwelling_type TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add dwelling_type: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_gnaf_base_pid ON gnaf_properties(base_pid)"); err != nil {
		return fmt.Errorf("add base_pid index: %w", err)
	}
	return nil
}
