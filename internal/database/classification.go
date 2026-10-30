package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Dwelling type values.
const (
	DwellingHouse     = "house"
	DwellingUnit      = "unit"
	DwellingTownhouse = "townhouse"
	DwellingOther     = "other"
	DwellingUnknown   = "unknown"
)

// ClassifyDwellingType applies a conservative address/type heuristic. PSI's
// RESIDENCE value is used for both houses and units, so street number and
// plan number are essential.
func ClassifyDwellingType(propertyType, streetNumber, planNumber string) string {
	pt := strings.ToUpper(strings.TrimSpace(propertyType))
	sn := strings.ToUpper(strings.TrimSpace(streetNumber))
	pn := strings.ToUpper(strings.TrimSpace(planNumber))

	switch pt {
	case "TOWNHOUSE":
		return DwellingTownhouse
	case "HOME UNIT", "UNIT", "APARTMENT", "FLAT", "VILLA":
		return DwellingUnit
	}

	if strings.Contains(sn, "/") && !strings.HasPrefix(sn, "LOT ") {
		return DwellingUnit
	}
	if strings.HasPrefix(pn, "SP") {
		return DwellingUnit
	}

	switch pt {
	case "HOUSE", "RESIDENCE":
		return DwellingHouse
	case "VACANT LAND", "COMMERCIAL", "SHOP", "OFFICE", "INDUSTRIAL",
		"SHOPPING CENTRE", "SHOPPING CENTER":
		return DwellingOther
	case "":
		return DwellingUnknown
	default:
		return DwellingUnknown
	}
}

// EnsureDwellingTypes backfills classifications for rows created before the
// dwelling_type column existed. New imports set the value directly.
func EnsureDwellingTypes(ctx context.Context, db *sql.DB) error {
	var missing int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gnaf_properties WHERE dwelling_type = ''").Scan(&missing); err != nil {
		return fmt.Errorf("count missing dwelling types: %w", err)
	}
	if missing == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		UPDATE gnaf_properties
		SET dwelling_type = CASE
			WHEN UPPER(TRIM(COALESCE(property_type, ''))) = 'TOWNHOUSE' THEN 'townhouse'
			WHEN UPPER(TRIM(COALESCE(property_type, ''))) IN ('HOME UNIT','UNIT','APARTMENT','FLAT','VILLA') THEN 'unit'
			WHEN UPPER(TRIM(COALESCE(street_number, ''))) LIKE '%/%'
			     AND UPPER(TRIM(COALESCE(street_number, ''))) NOT LIKE 'LOT %' THEN 'unit'
			WHEN UPPER(TRIM(COALESCE(plan_number, ''))) LIKE 'SP%' THEN 'unit'
			WHEN UPPER(TRIM(COALESCE(property_type, ''))) IN ('HOUSE','RESIDENCE') THEN 'house'
			WHEN UPPER(TRIM(COALESCE(property_type, ''))) IN
			     ('VACANT LAND','COMMERCIAL','SHOP','OFFICE','INDUSTRIAL','SHOPPING CENTRE','SHOPPING CENTER') THEN 'other'
			ELSE 'unknown'
		END
		WHERE dwelling_type = ''
	`)
	if err != nil {
		return fmt.Errorf("backfill dwelling types: %w", err)
	}
	return nil
}
