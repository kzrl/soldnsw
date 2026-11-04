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

func lossTestDate(monthsAgo int) string {
	return time.Now().AddDate(0, -monthsAgo, 0).Format("2006-01-02")
}

func seedLossProperty(t *testing.T, db *sql.DB, pid, suburb string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO gnaf_properties (gnaf_pid, street_number, street_name, street_type, suburb, state, postcode)
		VALUES (?, '1', 'TEST', 'ST', ?, 'NSW', '2000')
	`, pid, suburb); err != nil {
		t.Fatalf("seed property %s: %v", pid, err)
	}
}

func seedLossSale(t *testing.T, db *sql.DB, id, pid, date string, price int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO property_sales (sale_id, gnaf_pid, settlement_date, sale_price, source_type)
		VALUES (?, ?, ?, ?, 'Public Registry')
	`, id, pid, date, price); err != nil {
		t.Fatalf("seed sale %s: %v", id, err)
	}
}

func TestRefreshSuburbLossStats(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "loss_stats.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Suburb A: one loss (100k) and one gain. Latest sales are inside 2y.
	seedLossProperty(t, db, "A1", "ALPHA")
	seedLossSale(t, db, "A1-OLD", "A1", lossTestDate(36), 500000)
	seedLossSale(t, db, "A1-NEW", "A1", lossTestDate(12), 400000)
	seedLossProperty(t, db, "A2", "ALPHA")
	seedLossSale(t, db, "A2-OLD", "A2", lossTestDate(30), 300000)
	seedLossSale(t, db, "A2-NEW", "A2", lossTestDate(6), 350000)

	// Suburb B: a >80% drop, which should be counted as a loss and flagged.
	seedLossProperty(t, db, "B1", "BRAVO")
	seedLossSale(t, db, "B1-OLD", "B1", lossTestDate(48), 1000000)
	seedLossSale(t, db, "B1-NEW", "B1", lossTestDate(6), 100000)

	// Suburb C: latest sale is older than 2 years, so it must not appear.
	seedLossProperty(t, db, "C1", "CHARLIE")
	seedLossSale(t, db, "C1-OLD", "C1", lossTestDate(60), 500000)
	seedLossSale(t, db, "C1-NEW", "C1", lossTestDate(36), 400000)

	// Same-day resale is excluded.
	seedLossProperty(t, db, "D1", "DELTA")
	sameDay := lossTestDate(6)
	seedLossSale(t, db, "D1-A", "D1", sameDay, 500000)
	seedLossSale(t, db, "D1-B", "D1", sameDay, 400000)

	// Latest sale is a gain even though an earlier pair was a loss.
	seedLossProperty(t, db, "E1", "ECHO")
	seedLossSale(t, db, "E1-OLD", "E1", lossTestDate(36), 500000)
	seedLossSale(t, db, "E1-MID", "E1", lossTestDate(24), 400000)
	seedLossSale(t, db, "E1-NEW", "E1", lossTestDate(6), 450000)

	if err := RebuildCanonicalSales(context.Background(), db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}
	if err := RefreshSuburbLossStats(context.Background(), db); err != nil {
		t.Fatalf("refresh loss stats: %v", err)
	}

	rows, err := New(db).ListSuburbLossStats(context.Background(), ListSuburbLossStatsParams{
		Sort:     "losses",
		RowLimit: 20,
	})
	if err != nil {
		t.Fatalf("list loss stats: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ranked rows = %d, want 2 (ALPHA, BRAVO): %+v", len(rows), rows)
	}
	bySuburb := map[string]ListSuburbLossStatsRow{}
	for _, row := range rows {
		bySuburb[row.Suburb] = row
	}
	alpha, ok := bySuburb["ALPHA"]
	if !ok {
		t.Fatalf("ALPHA missing from rows: %+v", rows)
	}
	if alpha.Resales != 2 || alpha.Losses != 1 {
		t.Errorf("ALPHA resales/losses = %d/%d, want 2/1", alpha.Resales, alpha.Losses)
	}
	if alpha.TotalLoss != 100000 || alpha.AverageLoss != 100000 || alpha.LargestLoss != 100000 {
		t.Errorf("ALPHA loss amounts = %d/%d/%d, want 100000 each", alpha.TotalLoss, alpha.AverageLoss, alpha.LargestLoss)
	}
	if math.Abs(alpha.AverageLossPercent-20) > 0.001 {
		t.Errorf("ALPHA average loss percent = %.2f, want 20", alpha.AverageLossPercent)
	}
	if math.Abs(alpha.LossRate-50) > 0.001 {
		t.Errorf("ALPHA loss rate = %.2f, want 50", alpha.LossRate)
	}

	bravo := bySuburb["BRAVO"]
	if bravo.Resales != 1 || bravo.Losses != 1 || bravo.TotalLoss != 900000 {
		t.Errorf("BRAVO = %+v, want 1 resale, 1 loss, 900000 total", bravo)
	}
	if bravo.FlaggedLosses != 1 {
		t.Errorf("BRAVO flagged losses = %d, want 1", bravo.FlaggedLosses)
	}
	if math.Abs(bravo.AverageLossPercent-90) > 0.001 {
		t.Errorf("BRAVO average loss percent = %.2f, want 90", bravo.AverageLossPercent)
	}

	if _, ok := bySuburb["ECHO"]; ok {
		t.Errorf("ECHO should not appear because it has no loss-making resales")
	}
	if _, ok := bySuburb["CHARLIE"]; ok {
		t.Errorf("CHARLIE should be excluded because its latest sale is outside 2 years")
	}
	if _, ok := bySuburb["DELTA"]; ok {
		t.Errorf("DELTA should be excluded because same-day resales are not losses")
	}
}

func TestSuburbLossStatQueries(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "loss_queries.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO suburb_loss_stats
			(suburb, state, period, resales, losses, total_loss, average_loss, average_loss_percent, largest_loss, flagged_losses)
		VALUES
			('ALPHA', 'NSW', '2y', 100, 40, 4000000, 100000, 25, 250000, 1),
			('BRAVO', 'NSW', '2y', 3, 1, 900000, 900000, 60, 900000, 0),
			('CHARLIE', 'NSW', '2y', 80, 20, 800000, 40000, 15, 100000, 0),
			('DELTA', 'NSW', '2y', 10, 0, 0, 0, 0, 0, 0)
	`); err != nil {
		t.Fatalf("seed loss stats: %v", err)
	}

	q := New(db)
	ctx := context.Background()

	byCount, err := q.ListSuburbLossStats(ctx, ListSuburbLossStatsParams{Sort: "losses", RowLimit: 10})
	if err != nil {
		t.Fatalf("list by losses: %v", err)
	}
	if len(byCount) != 3 || byCount[0].Suburb != "ALPHA" || byCount[1].Suburb != "CHARLIE" {
		t.Errorf("losses ordering = %+v, want ALPHA then CHARLIE", byCount)
	}

	byRate, err := q.ListSuburbLossStats(ctx, ListSuburbLossStatsParams{Sort: "rate", MinResales: 20, RowLimit: 10})
	if err != nil {
		t.Fatalf("list by rate: %v", err)
	}
	if len(byRate) != 2 || byRate[0].Suburb != "ALPHA" {
		t.Errorf("rate ordering with >=20 resales = %+v, want ALPHA first and BRAVO excluded", byRate)
	}

	rateCount, err := q.CountSuburbLossStats(ctx, CountSuburbLossStatsParams{Sort: "rate", MinResales: 20})
	if err != nil {
		t.Fatalf("count by rate: %v", err)
	}
	if rateCount != 2 {
		t.Errorf("rate row count = %d, want 2", rateCount)
	}

	byAverage, err := q.ListSuburbLossStats(ctx, ListSuburbLossStatsParams{Sort: "average_loss", RowLimit: 10})
	if err != nil {
		t.Fatalf("list by average loss: %v", err)
	}
	if len(byAverage) != 3 || byAverage[0].Suburb != "BRAVO" || byAverage[1].Suburb != "ALPHA" {
		t.Errorf("average loss ordering = %+v, want BRAVO then ALPHA", byAverage)
	}

	summary, err := q.GetSuburbLossStatsSummary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.SuburbsAffected != 3 || summary.Losses != 61 || summary.TotalLoss != 5700000 || summary.FlaggedLosses != 1 {
		t.Errorf("summary = %+v, want 3 suburbs, 61 losses, 5700000 total, 1 flagged", summary)
	}
	if math.Abs(summary.AverageLossPercent-(1360.0/61.0)) > 0.001 {
		t.Errorf("summary average loss percent = %.3f, want %.3f", summary.AverageLossPercent, 1360.0/61.0)
	}

	averageCount, err := q.CountSuburbLossStats(ctx, CountSuburbLossStatsParams{Sort: "average_loss", MinLosses: 10})
	if err != nil {
		t.Fatalf("count average loss rows: %v", err)
	}
	if averageCount != 2 {
		t.Errorf("average loss row count with >=10 losses = %d, want 2", averageCount)
	}
}

func TestRefreshSuburbLossStat(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "one_loss_stat.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}

	seedLossProperty(t, db, "A1", "ALPHA")
	seedLossSale(t, db, "A1-OLD", "A1", lossTestDate(30), 500000)
	seedLossSale(t, db, "A1-NEW", "A1", lossTestDate(6), 400000)
	seedLossProperty(t, db, "B1", "BRAVO")
	seedLossSale(t, db, "B1-OLD", "B1", lossTestDate(30), 300000)
	seedLossSale(t, db, "B1-NEW", "B1", lossTestDate(6), 350000)

	ctx := context.Background()
	if err := RebuildCanonicalSales(ctx, db); err != nil {
		t.Fatalf("rebuild canonical sales: %v", err)
	}
	if err := RefreshSuburbLossStat(ctx, db, "ALPHA", "NSW"); err != nil {
		t.Fatalf("refresh ALPHA: %v", err)
	}
	if err := RefreshSuburbLossStat(ctx, db, "BRAVO", "NSW"); err != nil {
		t.Fatalf("refresh BRAVO: %v", err)
	}
	// Refreshing again must upsert, not double count.
	if err := RefreshSuburbLossStat(ctx, db, "ALPHA", "NSW"); err != nil {
		t.Fatalf("refresh ALPHA again: %v", err)
	}

	var resales, losses, total int64
	if err := db.QueryRow(`
		SELECT resales, losses, total_loss FROM suburb_loss_stats
		WHERE suburb = 'ALPHA' AND state = 'NSW' AND period = '2y'
	`).Scan(&resales, &losses, &total); err != nil {
		t.Fatalf("read ALPHA row: %v", err)
	}
	if resales != 1 || losses != 1 || total != 100000 {
		t.Errorf("ALPHA row = %d/%d/%d, want 1/1/100000", resales, losses, total)
	}

	// A suburb with no losses must still get a zero row, not be deleted.
	if err := db.QueryRow(`
		SELECT resales, losses FROM suburb_loss_stats
		WHERE suburb = 'BRAVO' AND state = 'NSW' AND period = '2y'
	`).Scan(&resales, &losses); err != nil {
		t.Fatalf("read BRAVO row: %v", err)
	}
	if resales != 1 || losses != 0 {
		t.Errorf("BRAVO row = %d/%d, want 1/0", resales, losses)
	}
}

func TestEnsureSuburbStatsSchemaAddsLossPercent(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "loss_schema.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(SchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	// Simulate a database created before average_loss_percent existed.
	if _, err := db.Exec(`
		DROP TABLE suburb_loss_stats;
		CREATE TABLE suburb_loss_stats (
			suburb TEXT NOT NULL,
			state TEXT NOT NULL,
			period TEXT NOT NULL,
			resales INTEGER NOT NULL DEFAULT 0,
			losses INTEGER NOT NULL DEFAULT 0,
			total_loss INTEGER NOT NULL DEFAULT 0,
			average_loss INTEGER NOT NULL DEFAULT 0,
			largest_loss INTEGER NOT NULL DEFAULT 0,
			flagged_losses INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (suburb, state, period)
		) STRICT;
		INSERT INTO suburb_loss_stats (suburb, state, period, losses) VALUES ('OLD', 'NSW', '2y', 1);
	`); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}
	if err := EnsureSuburbStatsSchema(context.Background(), db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	var hasColumn bool
	rows, err := db.Query("PRAGMA table_info(suburb_loss_stats)")
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatalf("read table info: %v", err)
		}
		if name == "average_loss_percent" {
			hasColumn = true
		}
	}
	rows.Close()
	if !hasColumn {
		t.Fatalf("average_loss_percent column was not added")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM suburb_loss_stats").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Errorf("rows after migration = %d, want 0 so the table is rebuilt", count)
	}
}
