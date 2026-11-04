package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newComparablesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "comparables.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func seedComparableProperty(t *testing.T, db *sql.DB, pid, street, suburb, dwelling string, landArea float64) {
	t.Helper()
	var area any
	if landArea > 0 {
		area = landArea
	}
	if _, err := db.Exec(`
		INSERT INTO gnaf_properties (
			gnaf_pid, street_number, street_name, street_type, suburb, state, postcode,
			land_area, dwelling_type
		) VALUES (?, '1', ?, 'ST', ?, 'NSW', '2000', ?, ?)
	`, pid, street, suburb, area, dwelling); err != nil {
		t.Fatalf("seed property %s: %v", pid, err)
	}
}

func seedComparableSale(t *testing.T, db *sql.DB, id, pid, date string, price int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
		VALUES (?, ?, ?, ?, 'Public Registry')
	`, id, pid, date, price); err != nil {
		t.Fatalf("seed sale %s: %v", id, err)
	}
}

func TestGetPropertyComparablesLatestSaleAndFilters(t *testing.T) {
	db := newComparablesTestDB(t)
	recent := func(monthsAgo int) string {
		return time.Now().AddDate(0, -monthsAgo, 0).Format("2006-01-02")
	}

	seedComparableProperty(t, db, "SUBJECT", "OAK", "TESTVILLE", DwellingHouse, 500)
	seedComparableProperty(t, db, "SAME", "OAK", "TESTVILLE", DwellingHouse, 520)
	seedComparableProperty(t, db, "OTHER", "PINE", "TESTVILLE", DwellingHouse, 1000)
	seedComparableProperty(t, db, "UNIT", "OAK", "TESTVILLE", DwellingUnit, 0)
	seedComparableProperty(t, db, "FAR", "OAK", "OTHERVILLE", DwellingHouse, 500)
	seedComparableProperty(t, db, "OLD", "ELM", "TESTVILLE", DwellingHouse, 500)

	seedComparableSale(t, db, "S-SUBJECT", "SUBJECT", recent(1), 800000)
	seedComparableSale(t, db, "S-SAME-OLD", "SAME", "2010-01-01", 500000)
	seedComparableSale(t, db, "S-SAME-NEW", "SAME", recent(1), 1000000)
	seedComparableSale(t, db, "S-OTHER", "OTHER", recent(2), 2000000)
	seedComparableSale(t, db, "S-UNIT", "UNIT", recent(3), 500000)
	seedComparableSale(t, db, "S-FAR", "FAR", recent(1), 900000)
	seedComparableSale(t, db, "S-OLD", "OLD", "2000-01-01", 300000)

	if err := RebuildCanonicalSales(context.Background(), db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}

	q := New(db)
	rows, err := q.GetPropertyComparables(context.Background(), GetPropertyComparablesParams{
		Pid:      "SUBJECT",
		Cutoff:   time.Now().AddDate(-2, 0, 0).Format("2006-01-02"),
		RowLimit: 8,
	})
	if err != nil {
		t.Fatalf("comparables: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (same-suburb houses only)", len(rows))
	}
	if rows[0].GnafPid != "SAME" {
		t.Errorf("first row = %s, want SAME (same street first)", rows[0].GnafPid)
	}
	if rows[0].SameStreet != 1 {
		t.Errorf("same_street = %d, want 1", rows[0].SameStreet)
	}
	if rows[0].SettlementDate == "2010-01-01" {
		t.Errorf("comparable used the older sale instead of the latest")
	}
	if rows[0].PricePerSqm != 1923 {
		t.Errorf("price_per_sqm = %d, want 1923", rows[0].PricePerSqm)
	}
	if rows[1].GnafPid != "OTHER" {
		t.Errorf("second row = %s, want OTHER", rows[1].GnafPid)
	}

	allTime, err := q.GetPropertyComparables(context.Background(), GetPropertyComparablesParams{
		Pid:      "SUBJECT",
		Cutoff:   minComparableDate,
		RowLimit: 8,
	})
	if err != nil {
		t.Fatalf("all-time comparables: %v", err)
	}
	if len(allTime) != 3 {
		t.Fatalf("all-time rows = %d, want 3", len(allTime))
	}
	foundOld := false
	for _, row := range allTime {
		if row.GnafPid == "OLD" {
			foundOld = true
		}
		if row.GnafPid == "SUBJECT" {
			t.Errorf("subject property appeared in its own comparable set")
		}
	}
	if !foundOld {
		t.Errorf("all-time comparables did not include the older sale")
	}
}

const minComparableDate = "1990-01-01"

func TestGetSuburbAnalyticsPricePerSqm(t *testing.T) {
	db := newComparablesTestDB(t)
	recent := time.Now().AddDate(0, -1, 0).Format("2006-01-02")

	seedComparableProperty(t, db, "H1", "ONE", "TESTVILLE", DwellingHouse, 500)
	seedComparableProperty(t, db, "H2", "TWO", "TESTVILLE", DwellingHouse, 1000)
	seedComparableProperty(t, db, "H3", "THREE", "TESTVILLE", DwellingHouse, 250)
	seedComparableProperty(t, db, "U1", "FOUR", "TESTVILLE", DwellingUnit, 0)

	seedComparableSale(t, db, "S-H1", "H1", recent, 500000)  // 1000/m²
	seedComparableSale(t, db, "S-H2", "H2", recent, 3000000) // 3000/m²
	seedComparableSale(t, db, "S-H3", "H3", recent, 500000)  // 2000/m²
	seedComparableSale(t, db, "S-U1", "U1", recent, 400000)

	if err := RebuildCanonicalSales(context.Background(), db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}

	rows, err := New(db).GetSuburbAnalytics(context.Background(), GetSuburbAnalyticsParams{
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
	row := rows[0]
	if row.TotalSales != 4 {
		t.Errorf("total sales = %d, want 4", row.TotalSales)
	}
	if row.MedianPrice != 500000 {
		t.Errorf("median price = %d, want 500000", row.MedianPrice)
	}
	if row.SqmSampleCount != 3 {
		t.Errorf("sqm sample count = %d, want 3 (houses with land area)", row.SqmSampleCount)
	}
	if row.MedianPricePerSqm != 2000 {
		t.Errorf("median price per sqm = %d, want 2000", row.MedianPricePerSqm)
	}
	if row.AvgPricePerSqm != 2000 {
		t.Errorf("average price per sqm = %d, want 2000", row.AvgPricePerSqm)
	}
}
