package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestCanonicalSalesDeduplicateAndFeedAnalytics(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "canonical.db"))
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
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type) VALUES
			('S-OLD', 'GANSW_PSI_003_123', '2020-01-01', 500000, 'Public Registry'),
			('S-NEW', 'GANSW_PSI_259_123', '2022-01-01', 600000, 'Public Registry'),
			('S-DUP', 'GANSW_PSI_259_123', '2020-01-01', 500000, 'Public Registry');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := RebuildPropertyLinks(context.Background(), db); err != nil {
		t.Fatalf("rebuild links: %v", err)
	}
	canonical, err := GetCanonicalPID(context.Background(), db, "GANSW_PSI_259_123")
	if err != nil {
		t.Fatalf("canonical lookup: %v", err)
	}
	if canonical != "GANSW_PSI_003_123" {
		t.Fatalf("canonical = %q, want GANSW_PSI_003_123", canonical)
	}

	if err := RebuildCanonicalSales(context.Background(), db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}

	var canonicalCount, rawCount int64
	if err := db.QueryRow("SELECT COUNT(*) FROM canonical_sales").Scan(&canonicalCount); err != nil {
		t.Fatalf("count canonical sales: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM property_sales").Scan(&rawCount); err != nil {
		t.Fatalf("count raw sales: %v", err)
	}
	if rawCount != 3 {
		t.Fatalf("raw sales = %d, want 3", rawCount)
	}
	if canonicalCount != 2 {
		t.Fatalf("canonical sales = %d, want 2 after dedupe", canonicalCount)
	}

	q := New(db)
	analytics, err := q.GetSuburbAnalytics(context.Background(), GetSuburbAnalyticsParams{
		Suburb: "GREEN POINT",
		State:  "NSW",
		Cutoff: "1990-01-01",
	})
	if err != nil {
		t.Fatalf("suburb analytics: %v", err)
	}
	if len(analytics) != 1 || analytics[0].TotalSales != 2 {
		t.Fatalf("analytics = %+v, want 2 canonical sales", analytics)
	}

	salesCount, err := q.GetSuburbSalesCount(context.Background(), GetSuburbSalesCountParams{
		Suburb: "GREEN POINT",
		State:  "NSW",
	})
	if err != nil {
		t.Fatalf("suburb sales count: %v", err)
	}
	if salesCount != 2 {
		t.Errorf("suburb sales count = %d, want 2", salesCount)
	}

	totalSales, err := q.GetTotalSalesCount(context.Background())
	if err != nil {
		t.Fatalf("total sales: %v", err)
	}
	if totalSales != 2 {
		t.Errorf("total sales = %d, want 2", totalSales)
	}

	postcode, err := q.GetPostcodeSummary(context.Background(), "2251")
	if err != nil {
		t.Fatalf("postcode summary: %v", err)
	}
	if postcode.SalesCount != 2 {
		t.Errorf("postcode sales count = %d, want 2", postcode.SalesCount)
	}
	if postcode.PropertyCount != 1 {
		t.Errorf("postcode property count = %d, want 1 canonical property", postcode.PropertyCount)
	}

	recent, err := q.ListRecentSalesWithAddress(context.Background())
	if err != nil {
		t.Fatalf("recent sales: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent sales = %d, want 2", len(recent))
	}
	seen := make(map[string]bool)
	for _, sale := range recent {
		if seen[sale.SaleID] {
			t.Errorf("duplicate recent sale %s", sale.SaleID)
		}
		seen[sale.SaleID] = true
		if sale.GnafPid != "GANSW_PSI_003_123" {
			t.Errorf("recent sale %s points at %s, want canonical PID", sale.SaleID, sale.GnafPid)
		}
	}

	if err := RefreshSuburbStats(context.Background(), db); err != nil {
		t.Fatalf("refresh stats: %v", err)
	}
	var statsSales int64
	if err := db.QueryRow("SELECT sales_count FROM suburb_stats WHERE suburb = 'GREEN POINT' AND state = 'NSW'").Scan(&statsSales); err != nil {
		t.Fatalf("query suburb stats: %v", err)
	}
	if statsSales != 2 {
		t.Errorf("suburb_stats sales_count = %d, want 2", statsSales)
	}
}

func TestRefreshCanonicalSaleKeepsOneRepresentative(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "refresh.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO gnaf_properties (gnaf_pid, street_number, street_name, street_type, suburb, state, postcode)
		VALUES ('PID-1', '1', 'TEST', 'ST', 'TESTVILLE', 'NSW', '2000');
	`); err != nil {
		t.Fatalf("seed property: %v", err)
	}

	q := New(db)
	for i, id := range []string{"S-2", "S-1"} {
		if err := q.InsertPropertySale(context.Background(), InsertPropertySaleParams{
			SaleID:         id,
			GnafPid:        "PID-1",
			SettlementDate: "2026-01-01",
			SalePrice:      500000,
			SourceType:     "Public Registry",
		}); err != nil {
			t.Fatalf("insert sale %d: %v", i, err)
		}
		if err := RefreshCanonicalSale(context.Background(), db, id); err != nil {
			t.Fatalf("refresh canonical sale %s: %v", id, err)
		}
	}

	var count int64
	var representative string
	if err := db.QueryRow("SELECT COUNT(*), MIN(sale_id) FROM canonical_sales").Scan(&count, &representative); err != nil {
		t.Fatalf("query canonical sales: %v", err)
	}
	if count != 1 {
		t.Errorf("canonical sales = %d, want 1", count)
	}
	if representative != "S-1" {
		t.Errorf("representative = %q, want deterministic S-1", representative)
	}
}
