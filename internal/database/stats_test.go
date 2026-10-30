package database

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestRefreshSuburbStatsPriceChanges(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "stats.db"))
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
			gnaf_pid, street_number, street_name, street_type, suburb, state, postcode
		) VALUES ('PID-1', '1', 'TEST', 'ST', 'TESTVILLE', 'NSW', '2000')
	`); err != nil {
		t.Fatalf("seed property: %v", err)
	}

	seed := func(id string, monthsAgo int, price int64) {
		date := time.Now().AddDate(0, -monthsAgo, 0).Format("2006-01-02")
		if _, err := db.Exec(`
			INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
			VALUES (?, 'PID-1', ?, ?, 'Public Registry')
		`, id, date, price); err != nil {
			t.Fatalf("seed sale %s: %v", id, err)
		}
	}

	seed("S-CURRENT", 6, 600000)
	seed("S-1Y", 18, 500000)
	seed("S-5Y", 66, 400000)
	seed("S-10Y", 126, 300000)

	if err := RefreshSuburbStats(context.Background(), db); err != nil {
		t.Fatalf("refresh stats: %v", err)
	}

	var change1y, change5y, change10y sql.NullFloat64
	if err := db.QueryRow(`
		SELECT change_1y, change_5y, change_10y
		FROM suburb_stats WHERE suburb = 'TESTVILLE' AND state = 'NSW'
	`).Scan(&change1y, &change5y, &change10y); err != nil {
		t.Fatalf("query stats: %v", err)
	}

	for name, got := range map[string]struct {
		value sql.NullFloat64
		want  float64
	}{
		"1y":  {change1y, 20},
		"5y":  {change5y, 50},
		"10y": {change10y, 100},
	} {
		if !got.value.Valid {
			t.Errorf("%s change is NULL", name)
			continue
		}
		if math.Abs(got.value.Float64-got.want) > 0.01 {
			t.Errorf("%s change = %.2f, want %.2f", name, got.value.Float64, got.want)
		}
	}
}

func TestRefreshSuburbStatUpdatesPriceChanges(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "one.db"))
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
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
		VALUES ('S-1', 'PID-1', date('now', '-18 months'), 500000, 'Public Registry');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := RefreshSuburbStat(context.Background(), db, "TESTVILLE", "NSW"); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
		VALUES ('S-2', 'PID-1', date('now', '-6 months'), 600000, 'Public Registry');
	`); err != nil {
		t.Fatalf("second sale: %v", err)
	}
	if err := RefreshSuburbStat(context.Background(), db, "TESTVILLE", "NSW"); err != nil {
		t.Fatalf("update refresh: %v", err)
	}
	var change1y sql.NullFloat64
	if err := db.QueryRow(`SELECT change_1y FROM suburb_stats WHERE suburb='TESTVILLE'`).Scan(&change1y); err != nil {
		t.Fatalf("query change: %v", err)
	}
	if !change1y.Valid || math.Abs(change1y.Float64-20) > 0.01 {
		t.Errorf("change_1y = %+v, want 20", change1y)
	}
}
