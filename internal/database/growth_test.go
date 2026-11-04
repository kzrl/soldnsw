package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func seedGrowthStat(t *testing.T, db *sql.DB, suburb string, sales int64, c1, c5, c10 any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO suburb_stats (
			suburb, state, property_count, sales_count, avg_price, max_price, min_price,
			change_1y, change_5y, change_10y
		) VALUES (?, 'NSW', 10, ?, 800000, 900000, 700000, ?, ?, ?)
	`, suburb, sales, c1, c5, c10); err != nil {
		t.Fatalf("seed growth stat %s: %v", suburb, err)
	}
}

func TestSuburbGrowthRankingAndSummary(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "growth.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}

	seedGrowthStat(t, db, "ALPHA", 100, 10.0, 20.0, 30.0)
	seedGrowthStat(t, db, "BETA", 50, -10.0, -5.0, 5.0)
	seedGrowthStat(t, db, "GAMMA", 10, 99.0, 50.0, 80.0)
	seedGrowthStat(t, db, "DELTA", 30, nil, nil, nil)

	q := New(db)
	top, err := q.ListSuburbGrowth(context.Background(), ListSuburbGrowthParams{
		Range: "5", Direction: "top", MinSales: 20, RowLimit: 10,
	})
	if err != nil {
		t.Fatalf("top growth: %v", err)
	}
	if len(top) != 2 || top[0].Suburb != "ALPHA" || top[1].Suburb != "BETA" {
		t.Fatalf("top growth rows = %+v, want ALPHA then BETA", top)
	}
	if top[0].ChangePct != 20 {
		t.Errorf("ALPHA 5y change = %v, want 20", top[0].ChangePct)
	}

	bottom, err := q.ListSuburbGrowth(context.Background(), ListSuburbGrowthParams{
		Range: "5", Direction: "bottom", MinSales: 20, RowLimit: 10,
	})
	if err != nil {
		t.Fatalf("bottom growth: %v", err)
	}
	if len(bottom) != 2 || bottom[0].Suburb != "BETA" || bottom[1].Suburb != "ALPHA" {
		t.Fatalf("bottom growth rows = %+v, want BETA then ALPHA", bottom)
	}

	summary, err := q.GetSuburbGrowthSummary(context.Background(), GetSuburbGrowthSummaryParams{
		Range: "5", MinSales: 20,
	})
	if err != nil {
		t.Fatalf("growth summary: %v", err)
	}
	if summary.Suburbs != 2 || summary.Rising != 1 || summary.Falling != 1 {
		t.Errorf("summary = %+v, want 2 ranked, 1 rising, 1 falling", summary)
	}
	if summary.AverageChange != 7.5 {
		t.Errorf("average change = %v, want 7.5", summary.AverageChange)
	}
}

func TestGetSuburbStats(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "suburb_stats.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seedGrowthStat(t, db, "ALPHA", 100, 10.0, 20.0, 30.0)

	stats, err := New(db).GetSuburbStats(context.Background(), GetSuburbStatsParams{
		Suburb: "alpha",
		State:  "nsw",
	})
	if err != nil {
		t.Fatalf("get suburb stats: %v", err)
	}
	if stats.Suburb != "ALPHA" || stats.SalesCount != 100 {
		t.Errorf("stats = %+v", stats)
	}
	if _, err := New(db).GetSuburbStats(context.Background(), GetSuburbStatsParams{
		Suburb: "MISSING",
		State:  "NSW",
	}); err != sql.ErrNoRows {
		t.Errorf("missing suburb error = %v, want sql.ErrNoRows", err)
	}
}
