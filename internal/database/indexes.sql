-- Heavy indexes are created by the explicit migrate command, not on every
-- server startup. Keep every query's required indexes here.
CREATE INDEX IF NOT EXISTS idx_gnaf_suburb ON gnaf_properties(suburb, street_name, postcode);
CREATE INDEX IF NOT EXISTS idx_gnaf_suburb_state ON gnaf_properties(suburb, state);
CREATE INDEX IF NOT EXISTS idx_gnaf_state ON gnaf_properties(state);
CREATE INDEX IF NOT EXISTS idx_gnaf_street ON gnaf_properties(street_name);
CREATE INDEX IF NOT EXISTS idx_gnaf_postcode ON gnaf_properties(postcode);
CREATE INDEX IF NOT EXISTS idx_gnaf_street_group ON gnaf_properties(street_name, street_type, suburb, state, postcode);
CREATE INDEX IF NOT EXISTS idx_gnaf_postcode_street ON gnaf_properties(postcode, street_name, street_type, suburb);
CREATE INDEX IF NOT EXISTS idx_sales_lookup ON property_sales(gnaf_pid, settlement_date DESC);
CREATE INDEX IF NOT EXISTS idx_sales_date ON property_sales(settlement_date DESC);
CREATE INDEX IF NOT EXISTS idx_property_links_canonical ON property_links(canonical_pid);
CREATE UNIQUE INDEX IF NOT EXISTS idx_canonical_sales_dedupe ON canonical_sales(dedupe_key);
CREATE INDEX IF NOT EXISTS idx_canonical_sales_suburb ON canonical_sales(suburb, state, settlement_date DESC);
CREATE INDEX IF NOT EXISTS idx_canonical_sales_postcode ON canonical_sales(postcode, settlement_date DESC);
CREATE INDEX IF NOT EXISTS idx_canonical_sales_property ON canonical_sales(canonical_pid, settlement_date DESC);
CREATE INDEX IF NOT EXISTS idx_import_runs_finished ON import_runs(status, finished_at DESC);
CREATE INDEX IF NOT EXISTS idx_street_stats_count ON street_stats(property_count DESC);
CREATE INDEX IF NOT EXISTS idx_street_stats_location ON street_stats(suburb, state, postcode);
CREATE INDEX IF NOT EXISTS idx_postcode_stats_sales ON postcode_stats(sales_count DESC);
