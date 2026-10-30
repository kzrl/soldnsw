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
	if err != nil && err != context.Canceled {
		log.Fatalf("Import failed: %v", err)
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
	fmt.Printf(" Properties Parsed:  %d (Inserted/Updated: %d)\n", stats.PropsParsed, stats.PropsInserted)
	fmt.Printf(" Sales Parsed:       %d (Inserted: %d)\n", stats.SalesParsed, stats.SalesInserted)
	fmt.Printf(" Elapsed Time:       %s (%.0f sales/sec)\n", stats.Duration.Round(time.Millisecond), rate)
	fmt.Println("==================================================================")

	// Display sample database summary if not dry run
	if !cfg.DryRun {
		printDatabaseOverview(cfg.DBPath)
	}
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
