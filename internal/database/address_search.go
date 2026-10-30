package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// AddressMatch is a single autocomplete/search result.
type AddressMatch struct {
	Address string `json:"label"`
	GnafPID string `json:"pid"`
}

// RebuildAddressSearch rebuilds the FTS5 address index used by autocomplete.
// Records without a street number are intentionally excluded.
func RebuildAddressSearch(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin address search rebuild: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM address_fts"); err != nil {
		return fmt.Errorf("clear address search: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO address_fts (address, gnaf_pid)
		SELECT
			UPPER(TRIM(
				COALESCE(p.street_number, '') || ' ' ||
				p.street_name || ' ' ||
				COALESCE(p.street_type, '') || ' ' ||
				p.suburb || ' ' || p.state || ' ' || p.postcode
			)),
			p.gnaf_pid
		FROM gnaf_properties p
		LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
		WHERE p.street_number IS NOT NULL AND TRIM(p.street_number) <> ''
		  AND (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
	`); err != nil {
		return fmt.Errorf("populate address search: %w", err)
	}
	return tx.Commit()
}

// EnsureAddressSearch populates the address index when it is empty.
func EnsureAddressSearch(ctx context.Context, db *sql.DB) error {
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM address_fts").Scan(&count); err != nil {
		return fmt.Errorf("count address search: %w", err)
	}
	if count > 0 {
		return nil
	}
	return RebuildAddressSearch(ctx, db)
}

// SearchAddressFTS returns FTS matches ordered by relevance.
func SearchAddressFTS(ctx context.Context, db *sql.DB, query string, limit int) ([]AddressMatch, error) {
	fts := addressFTSQuery(query)
	if fts == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `
		SELECT address, gnaf_pid
		FROM address_fts
		WHERE address_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, fts, limit)
	if err != nil {
		return nil, fmt.Errorf("address search: %w", err)
	}
	defer rows.Close()

	var matches []AddressMatch
	for rows.Next() {
		var match AddressMatch
		if err := rows.Scan(&match.Address, &match.GnafPID); err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	return matches, rows.Err()
}

// addressFTSQuery converts free text into a safe FTS5 prefix query.
func addressFTSQuery(query string) string {
	query = strings.ToUpper(strings.TrimSpace(query))
	query = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r), r == ' ', r == '/':
			return r
		default:
			return ' '
		}
	}, query)

	tokens := strings.Fields(strings.ReplaceAll(query, "/", " "))
	if len(tokens) == 0 {
		return ""
	}
	if len(tokens) > 8 {
		tokens = tokens[:8]
	}
	for i := range tokens {
		tokens[i] += "*"
	}
	return strings.Join(tokens, " ")
}
