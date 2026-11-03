package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/nswpsi"
)

func main() {
	var cfg nswpsi.ImportConfig

	flag.StringVar(&cfg.DataDir, "data", "data/nsw_weekly", "Path to directory containing NSW PSI zip/DAT files")
	flag.StringVar(&cfg.DBPath, "db", "properties.db", "Path to SQLite database file")
	flag.IntVar(&cfg.BatchSize, "batch", 5000, "Transaction batch chunk size")
	flag.StringVar(&cfg.Filter, "filter", "", "Filter filename/year substring (e.g. '2026', '2025', '20260810')")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Parse records without writing to the database")
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose output logging")
	flag.BoolVar(&cfg.FailFast, "fail-fast", false, "Stop at the first file or batch failure")
	flag.BoolVar(&cfg.RebuildOnly, "rebuild-only", false, "Skip file imports and only rebuild derived tables (links, sales, stats, search)")
	flag.Parse()

	// If ~/data is passed or expanded
	if cfg.DataDir == "~/data" {
		home, _ := os.UserHomeDir()
		cfg.DataDir = home + "/data"
	}

	fmt.Println("==================================================================")
	fmt.Println(" NSW Valuer General Bulk PSI Database Importer")
	fmt.Println("==================================================================")
	fmt.Printf(" Data Directory: %s\n", cfg.DataDir)
	fmt.Printf(" Target SQLite:  %s\n", cfg.DBPath)
	if cfg.Filter != "" {
		fmt.Printf(" Year/Filter:    '%s'\n", cfg.Filter)
	}
	if cfg.DryRun {
		fmt.Printf(" Mode:           DRY RUN (parse only)\n")
	}
	if cfg.RebuildOnly {
		fmt.Printf(" Mode:           REBUILD ONLY (no file imports)\n")
	}
	fmt.Println("------------------------------------------------------------------")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[!] Received interrupt. Stopping import gracefully...")
		cancel()
	}()

	importer, err := nswpsi.NewImporter(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize importer: %v", err)
	}
	defer importer.Close()

	stats, err := importer.Run(ctx)
	if stats == nil {
		log.Fatalf("Import failed before any files were processed: %v", err)
	}
	runErr := err
	if runErr != nil && runErr != context.Canceled {
		fmt.Printf("\n[!] Import stopped: %v\n", runErr)
	}

	durationSec := stats.Duration.Seconds()
	if durationSec <= 0 {
		durationSec = 0.001
	}
	rate := float64(stats.SalesParsed) / durationSec

	fmt.Println("\n==================================================================")
	fmt.Println(" Import Summary & Performance")
	fmt.Println("==================================================================")
	fmt.Printf(" Archives Processed: %d\n", stats.FilesProcessed)
	fmt.Printf(" DAT Files Parsed:   %d\n", stats.DATProcessed)
	fmt.Printf(" Properties Parsed:  %d (Written/Updated: %d, Failed: %d)\n", stats.PropsParsed, stats.PropsInserted, stats.PropsFailed)
	fmt.Printf(" Sales Parsed:       %d (Inserted: %d, Skipped: %d, Failed: %d)\n", stats.SalesParsed, stats.SalesInserted, stats.SalesSkipped, stats.SalesFailed)
	fmt.Printf(" Failed Files:       %d\n", stats.FailedFiles)
	fmt.Printf(" Failed Batches:     %d\n", stats.FailedBatches)
	fmt.Printf(" Elapsed Time:       %s (%.0f sales/sec)\n", stats.Duration.Round(time.Millisecond), rate)
	fmt.Println("==================================================================")

	exitCode := 0
	if runErr != nil && runErr != context.Canceled {
		exitCode = 1
	}
	if stats.HasFailures() {
		exitCode = 1
		printFailureSummary(stats)
	}

	// Display sample database summary if not dry run
	if !cfg.DryRun {
		printDatabaseOverview(cfg.DBPath)
	}
	if exitCode != 0 {
		// os.Exit skips deferred functions, so close explicitly before exiting.
		importer.Close()
		os.Exit(exitCode)
	}
}

func printFailureSummary(stats *nswpsi.ImportStats) {
	const limit = 20
	fmt.Println("\n[!] Import completed with failures:")
	if len(stats.FileFailures) > 0 {
		fmt.Println(" Failed files:")
		for i, failure := range stats.FileFailures {
			if i >= limit {
				fmt.Printf("   ... and %d more\n", len(stats.FileFailures)-limit)
				break
			}
			fmt.Printf("   - %s\n", failure)
		}
	}
	if len(stats.BatchFailures) > 0 {
		fmt.Println(" Failed batches:")
		for i, failure := range stats.BatchFailures {
			if i >= limit {
				fmt.Printf("   ... and %d more\n", len(stats.BatchFailures)-limit)
				break
			}
			fmt.Printf("   - %s\n", failure)
		}
	}
	fmt.Println(" Re-run the importer after fixing the failed inputs. The failure audit is also stored in import_runs when the database is writable.")
}

func printDatabaseOverview(dbPath string) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	var propCount, saleCount int64
	_ = db.QueryRow("SELECT COUNT(*) FROM gnaf_properties").Scan(&propCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM property_sales").Scan(&saleCount)

	fmt.Println("\n[*] Current Database State:")
	fmt.Printf(" - Total Properties in DB: %d\n", propCount)
	fmt.Printf(" - Total Settled Sales:    %d\n", saleCount)

	// Top 5 suburbs by sales count
	rows, err := db.Query(`
		SELECT p.suburb, COUNT(s.sale_id) as cnt, CAST(AVG(s.sale_price) AS INT)
		FROM gnaf_properties p
		JOIN property_sales s ON p.gnaf_pid = s.gnaf_pid
		GROUP BY p.suburb
		ORDER BY cnt DESC
		LIMIT 5
	`)
	if err == nil {
		defer rows.Close()
		fmt.Println("\n[*] Top 5 NSW Suburbs in Database:")
		fmt.Println(" Suburb               | Sales Count | Avg Price (AUD)")
		fmt.Println("----------------------|-------------|----------------")
		for rows.Next() {
			var suburb string
			var count int64
			var avgPrice int64
			if err := rows.Scan(&suburb, &count, &avgPrice); err == nil {
				fmt.Printf(" %-20s | %-11d | $%d\n", suburb, count, avgPrice)
			}
		}
	}
}
