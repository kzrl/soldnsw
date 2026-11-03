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

// AddressPropertySearchOptions controls paginated address FTS result pages.
type AddressPropertySearchOptions struct {
	Query         string
	DwellingType  string
	OnlyWithSales bool
	Sort          string
	Limit         int
	Offset        int
}

// SearchAddressProperties returns property rows for an FTS address query with
// filtering, sorting and pagination applied in SQL.
func SearchAddressProperties(ctx context.Context, db *sql.DB, opts AddressPropertySearchOptions) ([]GnafProperty, error) {
	fts := addressFTSQuery(opts.Query)
	if fts == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	where, args := addressPropertyWhere(opts, fts)
	order := addressPropertyOrder(opts.Sort)
	query := `
		SELECT
			p.gnaf_pid, p.street_number, p.street_name, p.street_type, p.suburb, p.state, p.postcode,
			p.latitude, p.longitude, p.lot_number, p.plan_number, p.property_type, p.land_area,
			p.base_pid, p.dwelling_type
		FROM address_fts f
		JOIN gnaf_properties p ON p.gnaf_pid = f.gnaf_pid
		LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
		WHERE ` + where + `
		ORDER BY ` + order + `
		LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("address property search: %w", err)
	}
	defer rows.Close()

	var properties []GnafProperty
	for rows.Next() {
		var p GnafProperty
		if err := rows.Scan(
			&p.GnafPid, &p.StreetNumber, &p.StreetName, &p.StreetType, &p.Suburb, &p.State, &p.Postcode,
			&p.Latitude, &p.Longitude, &p.LotNumber, &p.PlanNumber, &p.PropertyType, &p.LandArea,
			&p.BasePid, &p.DwellingType,
		); err != nil {
			return nil, err
		}
		properties = append(properties, p)
	}
	return properties, rows.Err()
}

// CountAddressProperties counts the full filtered result set for an address
// query without loading the rows.
func CountAddressProperties(ctx context.Context, db *sql.DB, opts AddressPropertySearchOptions) (int64, error) {
	fts := addressFTSQuery(opts.Query)
	if fts == "" {
		return 0, nil
	}
	where, args := addressPropertyWhere(opts, fts)
	query := `
		SELECT COUNT(*)
		FROM address_fts f
		JOIN gnaf_properties p ON p.gnaf_pid = f.gnaf_pid
		LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
		WHERE ` + where
	var count int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count address properties: %w", err)
	}
	return count, nil
}

func addressPropertyWhere(opts AddressPropertySearchOptions, fts string) (string, []any) {
	conditions := []string{
		"f.address_fts MATCH ?",
		"(pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)",
	}
	args := []any{fts}
	if opts.DwellingType != "" {
		if opts.DwellingType == "other" {
			conditions = append(conditions, "(p.dwelling_type = 'other' OR p.dwelling_type = 'unknown')")
		} else {
			conditions = append(conditions, "p.dwelling_type = ?")
			args = append(args, opts.DwellingType)
		}
	}
	if opts.OnlyWithSales {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM canonical_sales cs WHERE cs.canonical_pid = p.gnaf_pid
		)`)
	}
	return strings.Join(conditions, " AND "), args
}

func addressPropertyOrder(sort string) string {
	switch sort {
	case "latest":
		return `COALESCE((
			SELECT cs.settlement_date FROM canonical_sales cs
			WHERE cs.canonical_pid = p.gnaf_pid
			ORDER BY cs.settlement_date DESC, cs.sale_id DESC LIMIT 1
		), '') DESC, p.street_name, p.street_number`
	case "price":
		return `COALESCE((
			SELECT cs.sale_price FROM canonical_sales cs
			WHERE cs.canonical_pid = p.gnaf_pid
			ORDER BY cs.settlement_date DESC, cs.sale_id DESC LIMIT 1
		), 0) DESC, p.street_name, p.street_number`
	case "street":
		return `p.street_name, p.suburb, CAST(REPLACE(p.street_number, ' ', '') AS INTEGER), p.street_number`
	default:
		return "f.rank"
	}
}
