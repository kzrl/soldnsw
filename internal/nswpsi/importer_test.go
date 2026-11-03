package nswpsi

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

func TestInsertParsedCommitsBatchChunks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "batch.db")
	imp, err := NewImporter(ImportConfig{DBPath: dbPath, BatchSize: 2})
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	defer imp.Close()

	var props []ParsedProperty
	var sales []ParsedSale
	for i := 1; i <= 5; i++ {
		pid := "PID-" + string(rune('A'+i))
		props = append(props, ParsedProperty{
			GnafPID:      pid,
			StreetNumber: "1",
			StreetName:   "TEST",
			StreetType:   "ST",
			Suburb:       "TESTVILLE",
			State:        "NSW",
			Postcode:     "2000",
		})
		sales = append(sales, ParsedSale{
			SaleID:         "SALE-" + string(rune('A'+i)),
			GnafPID:        pid,
			SettlementDate: "2025-01-01",
			SalePrice:      500000,
			SourceType:     "NSW Valuer General (PSI)",
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	imp.afterBatch = func(batch int) {
		if batch == 1 {
			cancel()
		}
	}

	if err := imp.insertParsed(ctx, "batch-test.dat", props, sales); err != nil {
		t.Fatalf("insertParsed should continue after non-fail-fast batch errors: %v", err)
	}
	if imp.stats.FailedBatches == 0 {
		t.Errorf("expected at least one failed batch after cancellation")
	}
	if imp.stats.PropsInserted != 2 || imp.stats.SalesInserted != 2 {
		t.Errorf("inserted props/sales = %d/%d, want first batch only (2/2)", imp.stats.PropsInserted, imp.stats.SalesInserted)
	}
	var propCount, saleCount int
	if err := imp.db.QueryRow("SELECT COUNT(*) FROM gnaf_properties").Scan(&propCount); err != nil {
		t.Fatalf("count properties: %v", err)
	}
	if err := imp.db.QueryRow("SELECT COUNT(*) FROM property_sales").Scan(&saleCount); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if propCount != 2 || saleCount != 2 {
		t.Errorf("committed rows = %d properties/%d sales, want 2/2", propCount, saleCount)
	}
}

func TestImporterRecordsFailuresInImportRuns(t *testing.T) {
	for _, failFast := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "fail-fast"}[failFast], func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dataDir, "bad.zip"), []byte("not a zip"), 0644); err != nil {
				t.Fatalf("write bad zip: %v", err)
			}
			dbPath := filepath.Join(t.TempDir(), "runs.db")
			imp, err := NewImporter(ImportConfig{
				DataDir:   dataDir,
				DBPath:    dbPath,
				BatchSize: 1,
				FailFast:  failFast,
			})
			if err != nil {
				t.Fatalf("NewImporter: %v", err)
			}
			defer imp.Close()

			stats, runErr := imp.Run(context.Background())
			if failFast {
				if runErr == nil {
					t.Fatalf("expected fail-fast error")
				}
			} else if runErr != nil {
				t.Fatalf("unexpected continue-mode error: %v", runErr)
			}
			if !stats.HasFailures() || stats.FailedFiles != 1 {
				t.Fatalf("failure stats = %+v, want one failed file", stats)
			}

			var status, message string
			if err := imp.db.QueryRow(`
				SELECT status, error_message FROM import_runs
				ORDER BY finished_at DESC, run_id DESC LIMIT 1
			`).Scan(&status, &message); err != nil {
				t.Fatalf("query import_runs: %v", err)
			}
			if status != "failed" {
				t.Errorf("status = %q, want failed", status)
			}
			if !strings.Contains(message, "zip open error") {
				t.Errorf("error message = %q, want zip open error", message)
			}
		})
	}
}

func TestImporterRecordsSuccessfulRun(t *testing.T) {
	dataDir := t.TempDir()
	dat := "B;001;123;1;20250101 01:00;;;14;BELLBOWER CL;GREEN POINT;2251;610.5;M;20241201;20250101;500000;SP3;R;RESIDENCE;;SAV;;;AW123;\n"
	if err := os.WriteFile(filepath.Join(dataDir, "valid.dat"), []byte(dat), 0644); err != nil {
		t.Fatalf("write dat: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "success.db")
	imp, err := NewImporter(ImportConfig{DataDir: dataDir, DBPath: dbPath, BatchSize: 1})
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	defer imp.Close()

	stats, err := imp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.HasFailures() {
		t.Fatalf("unexpected failures: %+v", stats.FileFailures)
	}

	var status string
	var propsWritten, salesWritten int64
	if err := imp.db.QueryRow(`
		SELECT status, properties_written, sales_written
		FROM import_runs ORDER BY finished_at DESC, run_id DESC LIMIT 1
	`).Scan(&status, &propsWritten, &salesWritten); err != nil {
		t.Fatalf("query import_runs: %v", err)
	}
	if status != "success" {
		t.Errorf("status = %q, want success", status)
	}
	if propsWritten != 1 || salesWritten != 1 {
		t.Errorf("written props/sales = %d/%d, want 1/1", propsWritten, salesWritten)
	}
}
