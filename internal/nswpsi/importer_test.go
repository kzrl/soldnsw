package nswpsi

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestImporterUpsertPreservesCoordinates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	imp, err := NewImporter(ImportConfig{DBPath: dbPath, BatchSize: 1})
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	defer imp.Close()

	_, err = imp.db.Exec(`
		INSERT INTO gnaf_properties (
			gnaf_pid, street_number, street_name, street_type, suburb, state, postcode, latitude, longitude, lot_number, plan_number, property_type, land_area
		) VALUES ('PID-1', '1', 'OLD', 'ST', 'SUBURB', 'NSW', '2000', -33.8, 151.2, '1', 'DP1', 'RESIDENCE', 500)
	`)
	if err != nil {
		t.Fatalf("seed property: %v", err)
	}

	props := []ParsedProperty{{
		GnafPID:      "PID-1",
		StreetNumber: "1",
		StreetName:   "NEW",
		StreetType:   "ST",
		Suburb:       "SUBURB",
		State:        "NSW",
		Postcode:     "2000",
		PropertyType: "COMMERCIAL",
		LandArea:     600,
	}}
	sales := []ParsedSale{{
		SaleID:         "SALE-1",
		GnafPID:        "PID-1",
		SettlementDate: "2025-01-01",
		SalePrice:      500000,
		SourceType:     "NSW Valuer General (PSI)",
	}}
	if err := imp.insertBatch(context.Background(), props, sales); err != nil {
		t.Fatalf("insertBatch: %v", err)
	}

	var lat, lng sql.NullFloat64
	var street, propertyType string
	var landArea float64
	if err := imp.db.QueryRow(`SELECT latitude, longitude, street_name, property_type, land_area FROM gnaf_properties WHERE gnaf_pid = 'PID-1'`).Scan(&lat, &lng, &street, &propertyType, &landArea); err != nil {
		t.Fatalf("query property: %v", err)
	}
	if !lat.Valid || lat.Float64 != -33.8 || !lng.Valid || lng.Float64 != 151.2 {
		t.Errorf("import replaced coordinates: lat=%+v lng=%+v", lat, lng)
	}
	if street != "NEW" {
		t.Errorf("import did not update street name, got %q", street)
	}
	if propertyType != "COMMERCIAL" || landArea != 600 {
		t.Errorf("import did not update property type/area: %q / %v", propertyType, landArea)
	}
}
