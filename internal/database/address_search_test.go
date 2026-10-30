package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestAddressSearchFTS(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "address.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO gnaf_properties (gnaf_pid, street_number, street_name, street_type, suburb, state, postcode) VALUES
			('PID-1', '17', 'ARNOLD', 'ST', 'KILLARA', 'NSW', '2071'),
			('PID-2', '17A', 'ARNOLD', 'ST', 'KILLARA', 'NSW', '2071'),
			('PID-3', '19', 'ARNOLD', 'ST', 'KILLARA', 'NSW', '2071'),
			('PID-4', '', 'SUN VALLEY', 'RD', 'GREEN POINT', 'NSW', '2251'),
			('PID-5', '1/7', 'ARNOLD', 'ST', 'KILLARA', 'NSW', '2071');
	`); err != nil {
		t.Fatalf("seed properties: %v", err)
	}
	if err := RebuildAddressSearch(context.Background(), db); err != nil {
		t.Fatalf("rebuild address search: %v", err)
	}

	matches, err := SearchAddressFTS(context.Background(), db, "17 arnold killara", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) < 2 {
		t.Fatalf("expected 17 and 17A, got %+v", matches)
	}
	if matches[0].GnafPID != "PID-1" && matches[0].GnafPID != "PID-2" {
		t.Errorf("unexpected first match: %+v", matches[0])
	}

	unitMatches, err := SearchAddressFTS(context.Background(), db, "1/7 arnold", 10)
	if err != nil {
		t.Fatalf("unit search: %v", err)
	}
	if len(unitMatches) == 0 || unitMatches[0].GnafPID != "PID-5" {
		t.Errorf("expected unit 1/7 match, got %+v", unitMatches)
	}

	noNumberMatches, err := SearchAddressFTS(context.Background(), db, "sun valley", 10)
	if err != nil {
		t.Fatalf("no-number search: %v", err)
	}
	if len(noNumberMatches) != 0 {
		t.Errorf("properties without a street number should be excluded, got %+v", noNumberMatches)
	}
}
