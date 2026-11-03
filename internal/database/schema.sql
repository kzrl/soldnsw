CREATE TABLE IF NOT EXISTS gnaf_properties (
    gnaf_pid TEXT PRIMARY KEY,
    street_number TEXT,
    street_name TEXT NOT NULL,
    street_type TEXT NOT NULL,
    suburb TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('NSW','VIC','QLD','SA','WA','TAS','NT','ACT')),
    postcode TEXT NOT NULL CHECK(length(postcode) = 4),
    latitude REAL,
    longitude REAL,
    lot_number TEXT,
    plan_number TEXT,
    property_type TEXT,
    land_area REAL,
    base_pid TEXT,
    dwelling_type TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE IF NOT EXISTS property_sales (
    sale_id TEXT PRIMARY KEY,
    gnaf_pid TEXT NOT NULL,
    settlement_date TEXT NOT NULL CHECK(settlement_date LIKE '____-__-__'),
    sale_price INTEGER NOT NULL CHECK(sale_price > 10000),
    source_type TEXT NOT NULL DEFAULT 'Public Registry',
    created_at TEXT DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (gnaf_pid) REFERENCES gnaf_properties(gnaf_pid) ON DELETE CASCADE
) STRICT;


CREATE TABLE IF NOT EXISTS suburb_stats (
    suburb TEXT NOT NULL,
    state TEXT NOT NULL,
    property_count INTEGER NOT NULL,
    sales_count INTEGER NOT NULL,
    avg_price INTEGER NOT NULL,
    max_price INTEGER NOT NULL,
    min_price INTEGER NOT NULL,
    change_1y REAL,
    change_5y REAL,
    change_10y REAL,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (suburb, state)
) STRICT;

-- street_stats and postcode_stats are materialised for fast autocomplete.
CREATE TABLE IF NOT EXISTS street_stats (
    street_name TEXT NOT NULL,
    street_type TEXT NOT NULL,
    suburb TEXT NOT NULL,
    state TEXT NOT NULL,
    postcode TEXT NOT NULL,
    property_count INTEGER NOT NULL DEFAULT 0,
    sales_count INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (street_name, street_type, suburb, state, postcode)
) STRICT;

CREATE TABLE IF NOT EXISTS postcode_stats (
    postcode TEXT PRIMARY KEY,
    property_count INTEGER NOT NULL DEFAULT 0,
    sales_count INTEGER NOT NULL DEFAULT 0,
    suburb_count INTEGER NOT NULL DEFAULT 0,
    sample_suburb TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT;

CREATE VIRTUAL TABLE IF NOT EXISTS address_fts USING fts5(
    address,
    gnaf_pid UNINDEXED,
    tokenize = 'unicode61'
);

CREATE TABLE IF NOT EXISTS property_links (
    gnaf_pid TEXT PRIMARY KEY,
    canonical_pid TEXT NOT NULL,
    match_basis TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (gnaf_pid) REFERENCES gnaf_properties(gnaf_pid) ON DELETE CASCADE,
    FOREIGN KEY (canonical_pid) REFERENCES gnaf_properties(gnaf_pid) ON DELETE CASCADE
) STRICT;


CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- canonical_sales is the analytics-facing sale set. Raw property_sales rows
-- are retained for property history and provenance; this table maps each sale
-- to its canonical property and keeps one representative row per duplicate
-- sale signature.
CREATE TABLE IF NOT EXISTS canonical_sales (
    sale_id TEXT PRIMARY KEY,
    canonical_pid TEXT NOT NULL,
    suburb TEXT NOT NULL,
    state TEXT NOT NULL,
    postcode TEXT NOT NULL,
    settlement_date TEXT NOT NULL,
    sale_price INTEGER NOT NULL,
    source_type TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    FOREIGN KEY (sale_id) REFERENCES property_sales(sale_id) ON DELETE CASCADE,
    FOREIGN KEY (canonical_pid) REFERENCES gnaf_properties(gnaf_pid) ON DELETE CASCADE
) STRICT;





-- import_runs is an audit trail written by the bulk importer. status is one of
-- success, partial or failed.
CREATE TABLE IF NOT EXISTS import_runs (
    run_id TEXT PRIMARY KEY,
    file_name TEXT NOT NULL,
    file_size INTEGER NOT NULL DEFAULT 0,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL,
    properties_parsed INTEGER NOT NULL DEFAULT 0,
    properties_written INTEGER NOT NULL DEFAULT 0,
    sales_parsed INTEGER NOT NULL DEFAULT 0,
    sales_written INTEGER NOT NULL DEFAULT 0,
    sales_skipped INTEGER NOT NULL DEFAULT 0,
    failed_files INTEGER NOT NULL DEFAULT 0,
    failed_batches INTEGER NOT NULL DEFAULT 0,
    error_message TEXT NOT NULL DEFAULT ''
) STRICT;

