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

CREATE INDEX IF NOT EXISTS idx_gnaf_suburb ON gnaf_properties(suburb, street_name, postcode);
CREATE INDEX IF NOT EXISTS idx_gnaf_suburb_state ON gnaf_properties(suburb, state);
CREATE INDEX IF NOT EXISTS idx_gnaf_state ON gnaf_properties(state);
CREATE INDEX IF NOT EXISTS idx_gnaf_street ON gnaf_properties(street_name);
CREATE INDEX IF NOT EXISTS idx_gnaf_postcode ON gnaf_properties(postcode);
CREATE INDEX IF NOT EXISTS idx_sales_lookup ON property_sales(gnaf_pid, settlement_date DESC);
CREATE INDEX IF NOT EXISTS idx_sales_date ON property_sales(settlement_date DESC);

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

CREATE INDEX IF NOT EXISTS idx_property_links_canonical
ON property_links(canonical_pid);

