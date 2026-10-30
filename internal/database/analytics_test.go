package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestGetSuburbAnalyticsMedianAndCutoff(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "analytics.db"))
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
	recent := time.Now().AddDate(0, -3, 0).Format("2006-01-02")
	old := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
	prices := []int64{100000, 200000, 300000, 400000, 130000000}
	for i, price := range prices {
		if _, err := db.Exec(`
			INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
			VALUES (?, 'PID-1', ?, ?, 'Public Registry')
		`, "S-"+string(rune('A'+i)), recent, price); err != nil {
			t.Fatalf("seed sale: %v", err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
		VALUES ('S-OLD', 'PID-1', ?, 90000000, 'Public Registry')
	`, old); err != nil {
		t.Fatalf("seed old sale: %v", err)
	}

	q := New(db)
	rows, err := q.GetSuburbAnalytics(context.Background(), GetSuburbAnalyticsParams{
		Suburb: "TESTVILLE",
		State:  "NSW",
		Cutoff: time.Now().AddDate(-1, 0, 0).Format("2006-01-02"),
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].TotalSales != 5 {
		t.Errorf("total sales = %d, want 5", rows[0].TotalSales)
	}
	if rows[0].MedianPrice != 300000 {
		t.Errorf("median = %d, want 300000", rows[0].MedianPrice)
	}
	if int64(rows[0].AvgPrice.Float64) == rows[0].MedianPrice {
		t.Errorf("average unexpectedly equals median with a large outlier")
	}
}
