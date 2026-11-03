package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestRebuildPropertyLinks(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "links.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO gnaf_properties (
			gnaf_pid, street_number, street_name, street_type, suburb, state, postcode, lot_number, plan_number
		) VALUES
			('GANSW_PSI_003_2981264', '14', 'BELLBOWER', 'CL', 'GREEN POINT', 'NSW', '2251', '29', 'DP1045502'),
			('GANSW_PSI_259_2981264', '14', 'BELLBOWER', 'CL', 'GREEN POINT', 'NSW', '2251', '29', 'DP1045502'),
			('GANSW_PSI_259_2981265', '14', 'BELLBOWER', 'CL', 'GREEN POINT', 'NSW', '2251', '30', 'DP1045502'),
			('GANSW_PSI_259_9999999', '', 'SUN VALLEY', 'RD', 'GREEN POINT', 'NSW', '2251', '', '');
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type) VALUES
			('S-OLD', 'GANSW_PSI_003_2981264', '2013-06-06', 680000, 'NSW Valuer General (PSI)'),
			('S-NEW', 'GANSW_PSI_259_2981264', '2021-04-26', 1225000, 'NSW Valuer General (PSI)'),
			('S-LOT30', 'GANSW_PSI_259_2981265', '2022-01-01', 1300000, 'NSW Valuer General (PSI)');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := RebuildPropertyLinks(context.Background(), db); err != nil {
		t.Fatalf("rebuild links: %v", err)
	}

	canonical, err := GetCanonicalPID(context.Background(), db, "GANSW_PSI_003_2981264")
	if err != nil {
		t.Fatalf("canonical lookup: %v", err)
	}
	if canonical != "GANSW_PSI_003_2981264" {
		t.Errorf("canonical = %q, want deterministic GANSW_PSI_003_2981264", canonical)
	}

	canonical, err = GetCanonicalPID(context.Background(), db, "GANSW_PSI_259_2981265")
	if err != nil {
		t.Fatalf("different-lot canonical lookup: %v", err)
	}
	if canonical != "GANSW_PSI_259_2981265" {
		t.Errorf("different lot was merged: %q", canonical)
	}

	canonical, err = GetCanonicalPID(context.Background(), db, "GANSW_PSI_259_9999999")
	if err != nil {
		t.Fatalf("no-number canonical lookup: %v", err)
	}
	if canonical != "GANSW_PSI_259_9999999" {
		t.Errorf("no-number property was merged: %q", canonical)
	}

	if err := RebuildCanonicalSales(context.Background(), db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}
	if err := RefreshSuburbStats(context.Background(), db); err != nil {
		t.Fatalf("refresh stats: %v", err)
	}

	q := New(db)
	properties, err := q.GetTotalPropertiesCount(context.Background())
	if err != nil {
		t.Fatalf("count properties: %v", err)
	}
	if properties != 3 {
		t.Errorf("canonical property count = %d, want 3", properties)
	}

	sales, err := q.GetSalesHistoryByCanonical(context.Background(), "GANSW_PSI_003_2981264")
	if err != nil {
		t.Fatalf("sales history: %v", err)
	}
	if len(sales) != 2 {
		t.Errorf("merged sales = %d, want 2", len(sales))
	}
}

func TestEnsurePropertyLinksRebuildsOldVersion(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "version.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO gnaf_properties (
			gnaf_pid, street_number, street_name, street_type, suburb, state, postcode, lot_number, plan_number
		) VALUES
			('GANSW_PSI_003_123', '14', 'BELLBOWER', 'CL', 'GREEN POINT', 'NSW', '2251', '29', 'DP1045502'),
			('GANSW_PSI_259_123', '14', 'BELLBOWER', 'CL', 'GREEN POINT', 'NSW', '2251', '29', 'DP1045502');
		INSERT INTO property_links (gnaf_pid, canonical_pid, match_basis) VALUES
			('GANSW_PSI_003_123', 'GANSW_PSI_259_123', 'address_lot_plan'),
			('GANSW_PSI_259_123', 'GANSW_PSI_259_123', 'self');
		INSERT INTO schema_meta (key, value) VALUES ('property_links_version', '1');
	`); err != nil {
		t.Fatalf("seed old links: %v", err)
	}

	rebuilt, err := EnsurePropertyLinks(context.Background(), db)
	if err != nil {
		t.Fatalf("ensure links: %v", err)
	}
	if !rebuilt {
		t.Fatalf("expected old link version to be rebuilt")
	}
	canonical, err := GetCanonicalPID(context.Background(), db, "GANSW_PSI_259_123")
	if err != nil {
		t.Fatalf("canonical lookup: %v", err)
	}
	if canonical != "GANSW_PSI_003_123" {
		t.Errorf("canonical = %q, want deterministic GANSW_PSI_003_123", canonical)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM schema_meta WHERE key = 'property_links_version'").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != propertyLinksVersion {
		t.Errorf("version = %q, want %q", version, propertyLinksVersion)
	}
}
