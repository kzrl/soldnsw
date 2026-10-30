package nswpsi

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/database"
)

type ImportConfig struct {
	DataDir   string
	DBPath    string
	BatchSize int
	Filter    string
	DryRun    bool
	Verbose   bool
}

type ImportStats struct {
	FilesProcessed int64
	DATProcessed   int64
	PropsParsed    int64
	SalesParsed    int64
	PropsInserted  int64
	SalesInserted  int64
	StartTime      time.Time
	Duration       time.Duration
}

type Importer struct {
	config ImportConfig
	db     *sql.DB
	stats  ImportStats
}

func NewImporter(cfg ImportConfig) (*Importer, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 5000
	}

	var db *sql.DB
	if !cfg.DryRun {
		var err error
		db, err = sql.Open("sqlite3", cfg.DBPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}

		// Apply high-performance ingestion PRAGMAs. Foreign keys are enabled so
		// stale sale references are reported instead of silently written.
		pragmas := []string{
			"PRAGMA journal_mode=WAL;",
			"PRAGMA synchronous=OFF;",
			"PRAGMA foreign_keys=ON;",
			"PRAGMA cache_size=-64000;", // 64MB cache
			"PRAGMA temp_store=MEMORY;",
			"PRAGMA busy_timeout=10000;",
		}
		for _, p := range pragmas {
			if _, err := db.Exec(p); err != nil {
				return nil, fmt.Errorf("pragma %s error: %w", p, err)
			}
		}

		// Ensure the schema exists so the importer works on a fresh database.
		if _, err := db.Exec(database.SchemaSQL); err != nil {
			return nil, fmt.Errorf("schema setup error: %w", err)
		}
		if err := database.EnsurePropertyColumns(context.Background(), db); err != nil {
			return nil, fmt.Errorf("property schema migration error: %w", err)
		}
		if err := database.EnsureSuburbStatsSchema(context.Background(), db); err != nil {
			return nil, fmt.Errorf("stats schema migration error: %w", err)
		}
	}

	return &Importer{
		config: cfg,
		db:     db,
		stats:  ImportStats{StartTime: time.Now()},
	}, nil
}

func (imp *Importer) Close() {
	if imp.db != nil {
		// Restore normal synchronous mode before closing
		_, _ = imp.db.Exec("PRAGMA synchronous=NORMAL;")
		_ = imp.db.Close()
	}
}

// Run scans data directory and processes all archives
func (imp *Importer) Run(ctx context.Context) (*ImportStats, error) {
	entries, err := os.ReadDir(imp.config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read data directory: %w", err)
	}

	var targetFiles []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".zip") && !strings.HasSuffix(strings.ToLower(name), ".dat") && !e.IsDir() {
			continue
		}

		if imp.config.Filter != "" && !strings.Contains(name, imp.config.Filter) {
			continue
		}

		targetFiles = append(targetFiles, filepath.Join(imp.config.DataDir, name))
	}

	sort.Strings(targetFiles)

	fmt.Printf("\n[*] Found %d dataset archives/directories matching filter '%s'\n", len(targetFiles), imp.config.Filter)
	if len(targetFiles) == 0 {
		return &imp.stats, nil
	}

	for idx, target := range targetFiles {
		select {
		case <-ctx.Done():
			return &imp.stats, ctx.Err()
		default:
		}

		baseName := filepath.Base(target)
		fmt.Printf("[%d/%d] Processing %s ...\n", idx+1, len(targetFiles), baseName)

		if strings.HasSuffix(strings.ToLower(target), ".zip") {
			if err := imp.processZipFile(ctx, target); err != nil {
				fmt.Printf("[!] Error processing %s: %v\n", baseName, err)
			}
		} else if strings.HasSuffix(strings.ToLower(target), ".dat") {
			if err := imp.processDATFile(ctx, target); err != nil {
				fmt.Printf("[!] Error processing %s: %v\n", baseName, err)
			}
		} else {
			// Directory with unzipped DAT files
			if err := imp.processDirectory(ctx, target); err != nil {
				fmt.Printf("[!] Error processing dir %s: %v\n", baseName, err)
			}
		}

		atomic.AddInt64(&imp.stats.FilesProcessed, 1)
	}

	if !imp.config.DryRun && imp.db != nil {
		fmt.Println("[*] Rebuilding canonical property links...")
		if err := database.RebuildPropertyLinks(ctx, imp.db); err != nil {
			fmt.Printf("[!] Property link rebuild failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding suburb analytics summary...")
		if err := database.RefreshSuburbStats(ctx, imp.db); err != nil {
			fmt.Printf("[!] Suburb statistics refresh failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding address search index...")
		if err := database.RebuildAddressSearch(ctx, imp.db); err != nil {
			fmt.Printf("[!] Address search rebuild failed: %v\n", err)
		}
	}

	imp.stats.Duration = time.Since(imp.stats.StartTime)
	return &imp.stats, nil
}

func (imp *Importer) processZipFile(ctx context.Context, zipPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("zip open error: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		lowerName := strings.ToLower(f.Name)

		// Case 1: Nested .zip file (common in 2015-2025 yearly archives e.g. 20240101.zip)
		if strings.HasSuffix(lowerName, ".zip") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			nestedBytes, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}

			nestedReader, err := zip.NewReader(bytes.NewReader(nestedBytes), int64(len(nestedBytes)))
			if err != nil {
				continue
			}

			for _, nf := range nestedReader.File {
				if strings.HasSuffix(strings.ToLower(nf.Name), ".dat") {
					nrc, err := nf.Open()
					if err != nil {
						continue
					}
					props, sales, err := ParseDATStream(nrc)
					nrc.Close()
					if err == nil && (len(props) > 0 || len(sales) > 0) {
						atomic.AddInt64(&imp.stats.DATProcessed, 1)
						atomic.AddInt64(&imp.stats.PropsParsed, int64(len(props)))
						atomic.AddInt64(&imp.stats.SalesParsed, int64(len(sales)))
						if !imp.config.DryRun {
							if err := imp.insertBatch(ctx, props, sales); err != nil {
								fmt.Printf("[!] Batch insert error in %s: %v\n", nf.Name, err)
							}
						}
					}
				}
			}
			continue
		}

		// Case 2: Direct .DAT file
		if strings.HasSuffix(lowerName, ".dat") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			props, sales, err := ParseDATStream(rc)
			rc.Close()
			if err == nil && (len(props) > 0 || len(sales) > 0) {
				atomic.AddInt64(&imp.stats.DATProcessed, 1)
				atomic.AddInt64(&imp.stats.PropsParsed, int64(len(props)))
				atomic.AddInt64(&imp.stats.SalesParsed, int64(len(sales)))
				if !imp.config.DryRun {
					if err := imp.insertBatch(ctx, props, sales); err != nil {
						fmt.Printf("[!] Batch insert error in %s: %v\n", f.Name, err)
					}
				}
			}
		}
	}

	return nil
}

func (imp *Importer) processDATFile(ctx context.Context, datPath string) error {
	f, err := os.Open(datPath)
	if err != nil {
		return err
	}
	defer f.Close()

	props, sales, err := ParseDATStream(f)
	if err != nil {
		return err
	}

	atomic.AddInt64(&imp.stats.DATProcessed, 1)
	atomic.AddInt64(&imp.stats.PropsParsed, int64(len(props)))
	atomic.AddInt64(&imp.stats.SalesParsed, int64(len(sales)))

	if !imp.config.DryRun {
		return imp.insertBatch(ctx, props, sales)
	}
	return nil
}

func (imp *Importer) processDirectory(ctx context.Context, dirPath string) error {
	return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".dat") {
			return imp.processDATFile(ctx, path)
		}
		return nil
	})
}

// insertBatch inserts properties and sales within a single atomic SQLite transaction
func (imp *Importer) insertBatch(ctx context.Context, props []ParsedProperty, sales []ParsedSale) error {
	if len(props) == 0 && len(sales) == 0 {
		return nil
	}

	tx, err := imp.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx error: %w", err)
	}
	defer tx.Rollback()

	// Prepared statement for properties. ON CONFLICT preserves latitude,
	// longitude and cadastral details that a G-NAF linker has already filled
	// in, instead of wiping them via INSERT OR REPLACE.
	stmtProp, err := tx.PrepareContext(ctx, `
		INSERT INTO gnaf_properties (
			gnaf_pid, street_number, street_name, street_type, suburb, state, postcode, latitude, longitude,
			lot_number, plan_number, property_type, land_area, base_pid, dwelling_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gnaf_pid) DO UPDATE SET
			street_number = excluded.street_number,
			street_name = excluded.street_name,
			street_type = excluded.street_type,
			suburb = excluded.suburb,
			state = excluded.state,
			postcode = excluded.postcode,
			latitude = COALESCE(excluded.latitude, gnaf_properties.latitude),
			longitude = COALESCE(excluded.longitude, gnaf_properties.longitude),
			lot_number = COALESCE(excluded.lot_number, gnaf_properties.lot_number),
			plan_number = COALESCE(excluded.plan_number, gnaf_properties.plan_number),
			property_type = COALESCE(excluded.property_type, gnaf_properties.property_type),
			land_area = COALESCE(excluded.land_area, gnaf_properties.land_area),
			base_pid = COALESCE(excluded.base_pid, gnaf_properties.base_pid),
			dwelling_type = CASE WHEN excluded.dwelling_type <> '' THEN excluded.dwelling_type ELSE gnaf_properties.dwelling_type END
	`)
	if err != nil {
		return fmt.Errorf("prepare prop stmt error: %w", err)
	}
	defer stmtProp.Close()

	for _, p := range props {
		var lotVal, planVal, typeVal sql.NullString
		if p.LotNumber != "" {
			lotVal = sql.NullString{String: p.LotNumber, Valid: true}
		}
		if p.PlanNumber != "" {
			planVal = sql.NullString{String: p.PlanNumber, Valid: true}
		}
		if p.PropertyType != "" {
			typeVal = sql.NullString{String: p.PropertyType, Valid: true}
		}
		var areaVal sql.NullFloat64
		if p.LandArea > 0 {
			areaVal = sql.NullFloat64{Float64: p.LandArea, Valid: true}
		}
		baseVal := sql.NullString{String: p.BaseGnafPID, Valid: p.BaseGnafPID != ""}
		dwellingType := database.ClassifyDwellingType(p.PropertyType, p.StreetNumber, p.PlanNumber)

		res, err := stmtProp.ExecContext(ctx,
			p.GnafPID,
			p.StreetNumber,
			p.StreetName,
			p.StreetType,
			p.Suburb,
			p.State,
			p.Postcode,
			nil, // latitude (to be populated by a G-NAF linker)
			nil, // longitude (to be populated by a G-NAF linker)
			lotVal,
			planVal,
			typeVal,
			areaVal,
			baseVal,
			dwellingType,
		)
		if err != nil {
			if imp.config.Verbose {
				fmt.Printf("[!] Property %s skipped: %v\n", p.GnafPID, err)
			}
			continue
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			atomic.AddInt64(&imp.stats.PropsInserted, 1)
		}
	}

	// Prepared statement for sales
	stmtSale, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO property_sales (
			sale_id, gnaf_pid, settlement_date, sale_price, source_type
		) VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare sale stmt error: %w", err)
	}
	defer stmtSale.Close()

	for _, s := range sales {
		res, err := stmtSale.ExecContext(ctx,
			s.SaleID,
			s.GnafPID,
			s.SettlementDate,
			s.SalePrice,
			s.SourceType,
		)
		if err != nil {
			if imp.config.Verbose {
				fmt.Printf("[!] Sale %s skipped: %v\n", s.SaleID, err)
			}
			continue
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			atomic.AddInt64(&imp.stats.SalesInserted, 1)
		}
	}

	return tx.Commit()
}
