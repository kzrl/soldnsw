package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"soldproperty/internal/database"
)

func main() {
	dbPath := flag.String("db", "properties.db", "Path to SQLite database")
	buildStats := flag.Bool("stats", true, "Rebuild materialised suburb/street/postcode statistics")
	flag.Parse()

	db, err := sql.Open("sqlite3", dsn(*dbPath))
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA busy_timeout=10000;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			log.Fatalf("PRAGMA error: %v", err)
		}
	}

	ctx := context.Background()
	start := time.Now()
	fmt.Printf("Applying schema and migrations to %s...\n", *dbPath)
	if _, err := db.ExecContext(ctx, database.SchemaSQL); err != nil {
		log.Fatalf("Schema migration failed: %v", err)
	}
	if err := database.EnsurePropertyColumns(ctx, db); err != nil {
		log.Fatalf("Property column migration failed: %v", err)
	}
	if err := database.EnsureDwellingTypes(ctx, db); err != nil {
		log.Fatalf("Dwelling type migration failed: %v", err)
	}
	if err := database.EnsureSuburbStatsSchema(ctx, db); err != nil {
		log.Fatalf("Statistics schema migration failed: %v", err)
	}
	linksRebuilt, err := database.EnsurePropertyLinks(ctx, db)
	if err != nil {
		log.Fatalf("Property link migration failed: %v", err)
	}
	if linksRebuilt {
		fmt.Println("Canonical property links rebuilt.")
		if err := database.RebuildCanonicalSales(ctx, db); err != nil {
			log.Fatalf("Canonical sales rebuild failed: %v", err)
		}
	} else if _, err := database.EnsureCanonicalSales(ctx, db); err != nil {
		log.Fatalf("Canonical sales check failed: %v", err)
	}

	fmt.Println("Creating search indexes...")
	if err := database.EnsureSearchIndexes(ctx, db); err != nil {
		log.Fatalf("Index migration failed: %v", err)
	}
	if err := database.EnsureAddressSearch(ctx, db); err != nil {
		log.Fatalf("Address search index failed: %v", err)
	}

	if *buildStats {
		fmt.Println("Rebuilding materialised statistics...")
		if err := database.RefreshSuburbStats(ctx, db); err != nil {
			log.Fatalf("Suburb statistics rebuild failed: %v", err)
		}
		if err := database.RefreshStreetStats(ctx, db); err != nil {
			log.Fatalf("Street statistics rebuild failed: %v", err)
		}
		if err := database.RefreshPostcodeStats(ctx, db); err != nil {
			log.Fatalf("Postcode statistics rebuild failed: %v", err)
		}
	}

	if missing, err := database.MissingSearchIndexes(ctx, db); err != nil {
		log.Fatalf("Index verification failed: %v", err)
	} else if len(missing) > 0 {
		log.Fatalf("Migration finished but indexes are still missing: %s", strings.Join(missing, ", "))
	}

	fmt.Printf("Migration complete in %s.\n", time.Since(start).Round(time.Millisecond))
}

func dsn(path string) string {
	dsn := path
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_busy_timeout=10000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL"
}
