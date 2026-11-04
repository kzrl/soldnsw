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

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/database"
)

type ImportConfig struct {
	DataDir     string
	DBPath      string
	BatchSize   int
	Filter      string
	DryRun      bool
	Verbose     bool
	FailFast    bool
	RebuildOnly bool
}

type ImportStats struct {
	FilesProcessed int64
	DATProcessed   int64
	PropsParsed    int64
	PropsInserted  int64
	PropsFailed    int64
	SalesParsed    int64
	SalesInserted  int64
	SalesSkipped   int64
	SalesFailed    int64
	FailedFiles    int64
	FailedBatches  int64
	FileFailures   []string
	BatchFailures  []string
	StartTime      time.Time
	Duration       time.Duration
}

// HasFailures reports whether any file or batch failed.
func (s ImportStats) HasFailures() bool {
	return s.FailedFiles > 0 || s.FailedBatches > 0 ||
		len(s.FileFailures) > 0 || len(s.BatchFailures) > 0
}

type Importer struct {
	config ImportConfig
	db     *sql.DB
	stats  ImportStats

	// afterBatch is a test hook invoked after each successful chunk commit.
	afterBatch func(batch int)
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
			// Derived-table rebuilds sort millions of rows; keep temp data on
			// disk so the rebuild cannot exhaust RAM on large databases.
			"PRAGMA temp_store=FILE;",
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
	defer func() {
		imp.stats.Duration = time.Since(imp.stats.StartTime)
	}()

	var targetFiles []string
	if !imp.config.RebuildOnly {
		entries, err := os.ReadDir(imp.config.DataDir)
		if err != nil {
			return nil, fmt.Errorf("failed to read data directory: %w", err)
		}

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
	}

	if len(targetFiles) == 0 && !imp.config.RebuildOnly {
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

		before := imp.stats
		started := time.Now()
		var fileSize int64
		if info, statErr := os.Stat(target); statErr == nil {
			fileSize = info.Size()
		}

		var procErr error
		switch {
		case strings.HasSuffix(strings.ToLower(target), ".zip"):
			procErr = imp.processZipFile(ctx, target)
		case strings.HasSuffix(strings.ToLower(target), ".dat"):
			procErr = imp.processDATFile(ctx, target)
		default:
			// Directory with unzipped DAT files.
			procErr = imp.processDirectory(ctx, target)
		}

		atomic.AddInt64(&imp.stats.FilesProcessed, 1)
		if procErr != nil {
			imp.recordImportRun(ctx, baseName, fileSize, started, before, procErr.Error())
			return &imp.stats, procErr
		}
		imp.recordImportRun(ctx, baseName, fileSize, started, before, "")
	}

	if !imp.config.DryRun && imp.db != nil {
		fmt.Println("[*] Rebuilding canonical property links...")
		if err := database.RebuildPropertyLinks(ctx, imp.db); err != nil {
			fmt.Printf("[!] Property link rebuild failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding canonical sales...")
		if err := database.RebuildCanonicalSales(ctx, imp.db); err != nil {
			fmt.Printf("[!] Canonical sales rebuild failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding suburb analytics summary...")
		if err := database.RefreshSuburbStats(ctx, imp.db); err != nil {
			fmt.Printf("[!] Suburb statistics refresh failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding street suggestion statistics...")
		if err := database.RefreshStreetStats(ctx, imp.db); err != nil {
			fmt.Printf("[!] Street statistics refresh failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding postcode suggestion statistics...")
		if err := database.RefreshPostcodeStats(ctx, imp.db); err != nil {
			fmt.Printf("[!] Postcode statistics refresh failed: %v\n", err)
		}
		fmt.Println("[*] Rebuilding suburb loss rankings...")
		if err := database.RefreshSuburbLossStats(ctx, imp.db); err != nil {
			fmt.Printf("[!] Suburb loss statistics refresh failed: %v\n", err)
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
		return imp.recordFileFailure(zipPath, fmt.Errorf("zip open error: %w", err))
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
				if err := imp.recordFileFailure(f.Name, err); err != nil {
					return err
				}
				continue
			}
			nestedBytes, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				if err := imp.recordFileFailure(f.Name, err); err != nil {
					return err
				}
				continue
			}

			nestedReader, err := zip.NewReader(bytes.NewReader(nestedBytes), int64(len(nestedBytes)))
			if err != nil {
				if err := imp.recordFileFailure(f.Name, err); err != nil {
					return err
				}
				continue
			}

			for _, nf := range nestedReader.File {
				if !strings.HasSuffix(strings.ToLower(nf.Name), ".dat") {
					continue
				}
				nrc, err := nf.Open()
				if err != nil {
					if err := imp.recordFileFailure(nf.Name, err); err != nil {
						return err
					}
					continue
				}
				ingestErr := imp.ingestDAT(ctx, nf.Name, nrc)
				nrc.Close()
				if ingestErr != nil {
					return ingestErr
				}
			}
			continue
		}

		// Case 2: Direct .DAT file.
		if strings.HasSuffix(lowerName, ".dat") {
			rc, err := f.Open()
			if err != nil {
				if err := imp.recordFileFailure(f.Name, err); err != nil {
					return err
				}
				continue
			}
			ingestErr := imp.ingestDAT(ctx, f.Name, rc)
			rc.Close()
			if ingestErr != nil {
				return ingestErr
			}
		}
	}

	return nil
}

func (imp *Importer) processDATFile(ctx context.Context, datPath string) error {
	f, err := os.Open(datPath)
	if err != nil {
		return imp.recordFileFailure(datPath, err)
	}
	defer f.Close()

	return imp.ingestDAT(ctx, datPath, f)
}

func (imp *Importer) processDirectory(ctx context.Context, dirPath string) error {
	return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return imp.recordFileFailure(path, err)
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".dat") {
			return imp.processDATFile(ctx, path)
		}
		return nil
	})
}

// ingestDAT parses one DAT stream, records parse statistics, and writes it in
// BatchSize-sized transactions.
func (imp *Importer) ingestDAT(ctx context.Context, source string, r io.Reader) error {
	props, sales, err := ParseDATStream(r)
	if err != nil {
		return imp.recordFileFailure(source, err)
	}
	if len(props) == 0 && len(sales) == 0 {
		return nil
	}

	atomic.AddInt64(&imp.stats.DATProcessed, 1)
	atomic.AddInt64(&imp.stats.PropsParsed, int64(len(props)))
	atomic.AddInt64(&imp.stats.SalesParsed, int64(len(sales)))

	if imp.config.DryRun {
		return nil
	}
	return imp.insertParsed(ctx, source, props, sales)
}

// insertParsed splits parsed rows into BatchSize chunks. Each chunk is its own
// transaction; failures are recorded and either abort (FailFast) or continue
// with the remaining chunks.
func (imp *Importer) insertParsed(ctx context.Context, source string, props []ParsedProperty, sales []ParsedSale) error {
	total := len(props)
	if len(sales) > total {
		total = len(sales)
	}
	batchSize := imp.config.BatchSize
	if batchSize <= 0 {
		batchSize = 5000
	}

	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}

		propEnd := end
		if propEnd > len(props) {
			propEnd = len(props)
		}
		saleEnd := end
		if saleEnd > len(sales) {
			saleEnd = len(sales)
		}
		propStart := start
		if propStart > len(props) {
			propStart = len(props)
		}
		saleStart := start
		if saleStart > len(sales) {
			saleStart = len(sales)
		}

		batchNumber := start/batchSize + 1
		if err := imp.insertBatch(ctx, props[propStart:propEnd], sales[saleStart:saleEnd]); err != nil {
			if recordErr := imp.recordBatchFailure(source, err); recordErr != nil {
				return recordErr
			}
			continue
		}
		if imp.afterBatch != nil {
			imp.afterBatch(batchNumber)
		}
	}
	return nil
}

// recordFileFailure records a file-level failure and returns the error only
// when FailFast is enabled.
func (imp *Importer) recordFileFailure(source string, err error) error {
	atomic.AddInt64(&imp.stats.FailedFiles, 1)
	message := fmt.Sprintf("%s: %v", source, err)
	imp.stats.FileFailures = append(imp.stats.FileFailures, message)
	fmt.Printf("[!] %s\n", message)
	if imp.config.FailFast {
		return err
	}
	return nil
}

// recordBatchFailure records an insert-batch failure and returns the error only
// when FailFast is enabled.
func (imp *Importer) recordBatchFailure(source string, err error) error {
	atomic.AddInt64(&imp.stats.FailedBatches, 1)
	message := fmt.Sprintf("%s: %v", source, err)
	imp.stats.BatchFailures = append(imp.stats.BatchFailures, message)
	fmt.Printf("[!] Batch insert error in %s: %v\n", source, err)
	if imp.config.FailFast {
		return err
	}
	return nil
}

// recordImportRun stores one row per top-level archive/directory. It is a
// best-effort audit trail; a failure to write the row does not abort the import.
func (imp *Importer) recordImportRun(ctx context.Context, fileName string, fileSize int64, started time.Time, before ImportStats, procErr string) {
	if imp.config.DryRun || imp.db == nil {
		return
	}
	delta := statsDelta(before, imp.stats)
	fileErrors := imp.stats.FileFailures
	if len(before.FileFailures) <= len(fileErrors) {
		fileErrors = fileErrors[len(before.FileFailures):]
	}
	batchErrors := imp.stats.BatchFailures
	if len(before.BatchFailures) <= len(batchErrors) {
		batchErrors = batchErrors[len(before.BatchFailures):]
	}
	errorParts := append([]string{}, fileErrors...)
	errorParts = append(errorParts, batchErrors...)
	if procErr != "" {
		errorParts = append(errorParts, procErr)
	}
	errorMessage := strings.Join(errorParts, "; ")

	status := "success"
	if delta.FailedFiles > 0 || delta.FailedBatches > 0 || errorMessage != "" {
		status = "partial"
		if delta.PropsInserted == 0 && delta.SalesInserted == 0 {
			status = "failed"
		}
	}

	_, err := imp.db.ExecContext(ctx, `
		INSERT INTO import_runs (
			run_id, file_name, file_size, started_at, finished_at, status,
			properties_parsed, properties_written, sales_parsed, sales_written,
			sales_skipped, failed_files, failed_batches, error_message
		) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(),
		fileName,
		fileSize,
		started.UTC().Format("2006-01-02 15:04:05"),
		status,
		delta.PropsParsed,
		delta.PropsInserted,
		delta.SalesParsed,
		delta.SalesInserted,
		delta.SalesSkipped,
		delta.FailedFiles,
		delta.FailedBatches,
		errorMessage,
	)
	if err != nil {
		fmt.Printf("[!] Could not record import run for %s: %v\n", fileName, err)
	}
}

func statsDelta(before, after ImportStats) ImportStats {
	return ImportStats{
		FilesProcessed: after.FilesProcessed - before.FilesProcessed,
		DATProcessed:   after.DATProcessed - before.DATProcessed,
		PropsParsed:    after.PropsParsed - before.PropsParsed,
		PropsInserted:  after.PropsInserted - before.PropsInserted,
		PropsFailed:    after.PropsFailed - before.PropsFailed,
		SalesParsed:    after.SalesParsed - before.SalesParsed,
		SalesInserted:  after.SalesInserted - before.SalesInserted,
		SalesSkipped:   after.SalesSkipped - before.SalesSkipped,
		SalesFailed:    after.SalesFailed - before.SalesFailed,
		FailedFiles:    after.FailedFiles - before.FailedFiles,
		FailedBatches:  after.FailedBatches - before.FailedBatches,
	}
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
			atomic.AddInt64(&imp.stats.PropsFailed, 1)
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
			atomic.AddInt64(&imp.stats.SalesFailed, 1)
			if imp.config.Verbose {
				fmt.Printf("[!] Sale %s skipped: %v\n", s.SaleID, err)
			}
			continue
		}
		if n, err := res.RowsAffected(); err == nil {
			if n > 0 {
				atomic.AddInt64(&imp.stats.SalesInserted, 1)
			} else {
				atomic.AddInt64(&imp.stats.SalesSkipped, 1)
			}
		}
	}

	return tx.Commit()
}
