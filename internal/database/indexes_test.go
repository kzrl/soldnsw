package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestEnsureSearchIndexesUpgradesExistingDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "indexes.db"))
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
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	missing, err := MissingSearchIndexes(context.Background(), db)
	if err != nil {
		t.Fatalf("check missing indexes: %v", err)
	}
	if len(missing) == 0 {
		t.Fatalf("expected indexes to be missing before migration")
	}

	if err := EnsureSearchIndexes(context.Background(), db); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	missing, err = MissingSearchIndexes(context.Background(), db)
	if err != nil {
		t.Fatalf("check missing indexes after migration: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("indexes still missing: %v", missing)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM gnaf_properties").Scan(&count); err != nil {
		t.Fatalf("count properties: %v", err)
	}
	if count != 1 {
		t.Errorf("property count changed during migration: %d", count)
	}
}
