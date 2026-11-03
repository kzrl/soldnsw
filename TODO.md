# TODO — SoldNSW functionality and reliability improvements

This file turns the review findings into an implementation plan. Work top-down:
first fix correctness and data consistency, then reliability, then search UX,
performance, security/operations, and finally larger product features.

Status: Phases 1–4 implemented; Phases 5–7 planned.
Priority legend: **P0** = correctness/data loss, **P1** = important, **P2** = polish/optional.
Effort: **S** ≈ half day, **M** ≈ 1–3 days, **L** ≈ 3–5+ days.

---

## 0. Ground rules

- Keep schema changes additive where possible (`ALTER TABLE ... ADD COLUMN`,
  new tables/views, `CREATE INDEX IF NOT EXISTS`).
- Every schema change needs a migration path for existing databases, not just a
  fresh `schema.sql`.
- Use one source of truth for “canonical sale” data; do not add a fourth variant
  of the same aggregation query.
- Add tests with every change. Run `make check` (sqlc generate + vet + tests)
  before closing a task.
- Keep feature work and refactors in separate commits/PRs.
- Prefer SQL pagination over loading full result sets into Go.
- Record any decision that changes public data/behaviour in the README and
  `docs/plans/`.

---

## Phase 1 — Data correctness (P0) — DONE

The same sale/suburb should produce the same numbers on every page.

### 1.1 Canonical sale model — done

- [x] Chose the canonical sales rule documented below.
  - Every raw sale is mapped to `canonical_pid` via `property_links`.
  - Analytics dedupe on `(canonical_pid, settlement_date, sale_price, source_type)`.
    Raw `property_sales` rows are retained for property history/provenance.
- [x] Added a materialised `canonical_sales` table
  (`internal/database/schema.sql`, `internal/database/canonical_sales.go`).
  It contains one representative row per dedupe signature and indexes for
  suburb, postcode and property lookups.
- [x] Added `RebuildCanonicalSales`, `EnsureCanonicalSales` and
  `RefreshCanonicalSale`.
- [x] Updated the analytics-facing queries to use `canonical_sales`:
  - [x] `GetSuburbSalesBySuburb`
  - [x] `GetSuburbSalesCount`
  - [x] `GetSuburbMonthlyAverages`
  - [x] `GetSuburbAnalytics`
  - [x] `GetSuburbExtremeSales`
  - [x] `GetSuburbLossSummary`
  - [x] `GetSuburbLossTypeSummary`
  - [x] `GetSuburbLosses`
  - [x] `ListRecentSalesWithAddress`
  - [x] `GetPostcodeSummary`
  - [x] `GetPostcodeSuburbs`
  - [x] `GetTotalSalesCount`
  - [x] `internal/database/stats.go` — full and single-suburb stats
- [x] Added regression tests in
  `internal/database/canonical_sales_test.go` proving property, suburb,
  postcode, recent-sales and `suburb_stats` counts agree for duplicate PIDs.

**Acceptance:** duplicate PSI rows no longer change suburb/postcode averages,
sales counts or recent-sales duplicates; the same property has the same sales
history everywhere.

### 1.2 Deterministic canonical PID — done

- [x] Replaced the latest-sale ordering with a deterministic rule in
  `internal/database/property_links.go`:
  - prefer non-`GANSW_PSI_` PIDs,
  - then rows with a street number,
  - then rows with lot/plan,
  - then rows with coordinates,
  - then lowest PID.
- [x] Added a `schema_meta.property_links_version` marker so existing databases
  rebuild links once under the new rule.
- [x] Updated `property_links_test.go` and added migration coverage.

### 1.3 Canonicalise submissions — done

- [x] `handleSubmitAction` resolves the submitted PID to its canonical PID
  before verifying the property or inserting the sale.
- [x] New sales are inserted against the canonical PID and redirected to the
  canonical property page.
- [x] `RefreshCanonicalSale` updates the affected dedupe group.
- [x] Suburb stats are refreshed after the canonical sale update.
- [x] Added an integration test that submits against a non-canonical duplicate
  and verifies the canonical insertion, redirect and stats.
- [x] Canonical links do not need a rebuild after submission because selection
  is now deterministic and independent of sale dates.

### 1.4 Recent sales and lists — done

- [x] `ListRecentSalesWithAddress` now reads from `canonical_sales`, so duplicate
  dataset records appear once.
- [x] Property pages keep raw sale history for provenance; analytics and public
  result lists use the deduplicated canonical set.
- [x] Address/street/postcode property lists already filter `property_links`.

**Remaining Phase 1 follow-up:** none required for correctness. Future work can
revisit whether same-day/same-price sales need a dealing/reference number in the
dedupe key, but that is now documented as an open decision rather than a blocker.

## Phase 2 — Import and download reliability (P0/P1) — DONE

### 2.1 Use `BatchSize` in the importer — done

- [x] Kept `ParseDATStream` for existing callers/tests.
- [x] Added chunked insertion via `ingestDAT` → `insertParsed`; each
  `BatchSize` chunk is committed in its own `insertBatch` transaction.
- [x] Batch failures are recorded and either abort (`-fail-fast`) or continue
  with the remaining chunks.
- [x] Added a test with a cancelled context proving earlier chunks remain
  committed after a later batch failure.

### 2.2 Import failure reporting and exit status — done

- [x] Extended `ImportStats` with property/sale failures, files skipped,
  failed files/batches and failure messages.
- [x] Added `-fail-fast`.
- [x] Added a final failure summary printed by `cmd/import_nsw`.
- [x] `cmd/import_nsw` exits non-zero when any file or batch failed.
- [x] Parsed vs written/inserted vs skipped/failed counts are printed.
- [x] Added failure and success tests covering `import_runs` status.

### 2.3 Downloader filename collision — done

- [x] Deduplicate targets by output filename before queueing.
- [x] Use a unique temporary path per worker/attempt.
- [x] Remove temporary files on failure/cancellation.
- [x] Validate the downloaded ZIP before replacing an existing file.
- [x] Added tests for filename dedupe, concurrent `-force` downloads, temp-file
  cleanup and invalid-ZIP rejection.

### 2.4 Download/import provenance — done

- [x] Added an `import_runs` table with file name, size, timing, status, counts,
  failure counts and error details.
- [x] The importer records one row per top-level archive/directory.
- [x] Added `GetLastSuccessfulImport`.
- [x] The shared footer shows the most recent successful/partial import date.

**Remaining Phase 2 follow-up:** download checksums were left optional; the
importer now validates ZIP integrity and records provenance, but no SHA-256
storage was added because the downloader and importer run as separate commands.

## Phase 3 — Search UX completion (P1) — DONE

### 3.1 Paginate address and street-group results — done

- [x] Added `SearchAddressFTSPage`/`CountAddressFTS` equivalents through
  `SearchAddressProperties` and `CountAddressProperties`.
- [x] Address results are filtered, sorted and paginated in SQL rather than
  fetching every FTS match.
- [x] `SearchStreetGroups` supports offset/limit and `CountStreetGroups`.
- [x] Added shared `resultFilters` and `searchPager` template blocks in
  `templates/base.html`.
- [x] Pages show total counts and “page X of Y”; out-of-range pages clamp to the
  last page.
- [x] Added tests for address page 1/2, street-group page 2 and out-of-range
  pages.

### 3.2 Suburb suggestions — done

- [x] Added `SuggestSuburbs` backed by `suburb_stats`.
- [x] Suggestions return `kind: "suburb"` and link to the suburb analytics page.
- [x] Labels show state, property count and sales count.
- [x] Suggestions are included for street/address queries so names like
  `KILLARA` no longer only fall through to address FTS.
- [x] Added suggestion tests.

### 3.3 Street-level summary — done

- [x] Added `GetStreetSummary` with property count, sales count, average,
  median, highest/lowest price and latest sale date.
- [x] Street result pages show KPI cards above the property list.
- [x] The existing suburb-trends link remains.
- [x] Added tests with duplicate PIDs and properties without sales.

### 3.4 Result filters and sorting — done

- [x] Added `dwelling`, `only_with_sales` and `sort` query parameters for address
  FTS, street, postcode and structured address results.
- [x] Supported `street`, `latest`, `price` and address-only `relevance` sorting.
- [x] Filtering/sorting is pushed into SQL where possible.
- [x] Filter state is preserved in pager links and the shared filter form.
- [x] Added tests for street/postcode/address filters and duplicate handling.

### 3.5 Property ID search — done

- [x] Added `SearchKindPropertyID`; inputs starting with `GA` and using only
  letters/digits/underscores are treated as PIDs.
- [x] Existing PIDs redirect to the property page; missing PIDs render a clean
  404 search notice.
- [x] Suggestions return `kind: "property"` with the address label.
- [x] Added parser and handler tests.

### 3.6 Search error states — done

- [x] Added `PageData.SearchError` and a branded service-unavailable state.
- [x] Address count/search, street grouping, street summary/list and postcode
  queries no longer silently render zero results on DB failure.
- [x] Error pages render with HTTP 503.
- [x] Added a closed-database regression test.

**Remaining Phase 3 follow-up:** structured suburb/number searches still use the
legacy 50-row `SearchPropertiesFlexible` limit. Filters and sorting now apply,
but that path is not paginated; it can be folded into the paginated search query
in a later cleanup.

## Phase 4 — Performance and scalability (P1) — DONE

### 4.1 Suggestion and street aggregation cost — done

- [x] Added materialised `street_stats` and `postcode_stats` tables.
- [x] Rebuild them after imports, ingest and migrations.
- [x] Update affected street/postcode rows after submissions when the tables are
  already built.
- [x] Suggestions read from the materialised tables with a live-query fallback
  while stats are being built.
- [x] Added a one-minute in-memory suggestion cache with a bounded size.
- [x] Capped suggestion/search queries at 120 characters.
- [x] Added a per-client suggestion rate limit.
- [x] Added tests for stats-backed suggestions, caching, length limiting and
  rate limiting.

**Measurement note:** the implementation removes the per-keystroke `GROUP BY`
over `gnaf_properties`, but p50/p95 timing on a full ~2.9M-row dataset has not
been measured in this workspace.

### 4.2 Losses page SQL pagination — done

- [x] Added sort/type/limit/offset parameters to `GetSuburbLosses`.
- [x] Added `CountSuburbLosses` with the same filters.
- [x] Removed in-memory filtering, sorting and slicing from the handler.
- [x] Kept summary and type-count queries consistent with the selected range.
- [x] Added pagination/sort test coverage.

### 4.3 Startup index migration — done

- [x] Moved heavy indexes to `db/indexes.sql`.
- [x] Added `cmd/migrate` and `make migrate`.
- [x] `make run` runs migrations before starting the server.
- [x] `make run-import` runs index migration (without rebuilding stats twice).
- [x] The server warns at startup when required indexes are missing.
- [x] Added an upgrade test validating indexes are created without data loss.
- [x] Documented the migration step in the README.

### 4.4 Suburb stats build lifecycle — done

- [x] Added a configurable `-stats-timeout`.
- [x] Retries failed builds with backoff.
- [x] Tracks `unknown`/`building`/`ready`/`failed`, update time and last error.
- [x] Shows build status and last update on the suburb page.
- [x] Added `GET /readyz` exposing database and stats status.
- [x] Kept the delete+insert transaction model; a future swap-table optimisation
  remains optional.

## Phase 5 — Security and operations (P1)

### 5.1 Headers and assets (P1, S)

- [ ] Move inline scripts to `/static/js/` or add a per-request CSP nonce.
- [ ] Add `Content-Security-Policy` covering scripts, styles, images and
  connections used by the app.
- [ ] Self-host Bulma, Font Awesome, Chart.js and Leaflet, or pin versions with
  SRI.
- [ ] Set `Secure` on CSRF/flash cookies when served over HTTPS.
- [ ] Add `Strict-Transport-Security` when TLS is enabled.
- [ ] Disable static directory listings.

### 5.2 Trusted proxy and rate limiting (P1, S)

- [ ] Add a trusted-proxy configuration; only honour `X-Forwarded-For` from
  trusted hops.
- [ ] Rate-limit search and suggestion endpoints separately from submissions.
- [ ] Use a shared store for rate limits if running multiple instances.
- [ ] Add tests for forwarded headers and limiter boundaries.

### 5.3 Health and observability (P1, S)

- [ ] Add `GET /healthz` (process alive).
- [ ] Add `GET /readyz` (DB reachable, schema/indexes/stats ready).
- [ ] Log method, path, status, duration and response size.
- [ ] Add counters/timers for search modes, DB errors and slow queries.
- [ ] Expose build version/commit for deployments.

### 5.4 URL escaping (P2, S)

- [ ] Replace raw `{{.Suburb}}` / `{{.State}}` interpolation in `href` with
  `urlquery` across templates.
- [ ] Add template tests for names with spaces, apostrophes and `&`.

---

## Phase 6 — Product improvements (P2)

### 6.1 Postcode trends (P2, M)

- [ ] Generalise `GetSuburbMonthlyAverages` to support postcode aggregation.
- [ ] Add a monthly chart and range selector to `postcode.html`.
- [ ] Add median/change metrics comparable to suburb pages.

### 6.2 Submission provenance and moderation (P1 for production, L)

- [ ] Add a `submissions` table (PID, sale data, source URL, IP, timestamp,
  status).
- [ ] Keep pending/rejected submissions out of public analytics.
- [ ] Add duplicate detection/unique constraints.
- [ ] Add a simple moderation/admin interface.
- [ ] Show provenance badges on property pages.

### 6.3 Geocoding / G-NAF linking (P2, L)

- [ ] Add a G-NAF address linker that populates latitude/longitude and
  G-NAF PIDs for PSI records.
- [ ] Make the property map useful for imported PSI data.
- [ ] Document the source/licensing implications.

### 6.4 Comparable-sales and nearby properties (P2, L)

- [ ] Add same-street/nearby recent sales to property pages.
- [ ] Add a “recently sold nearby” section once coordinates are available.

---

## Phase 7 — Test and verification work (ongoing)

- [ ] Unit tests for canonical sale dedupe and deterministic canonical PIDs.
- [ ] Integration tests through the real mux for `/properties`, `/postcode/{code}`,
  `/api/search/suggest`, `/submit`.
- [ ] Fuzz `ParseSearchQuery` with long/mixed/punctuated inputs.
- [ ] Test importer chunking, partial failure exit status and resume.
- [ ] Test downloader duplicate filenames and concurrent `-force`.
- [ ] Test migrations/index creation on a pre-existing database.
- [ ] Add query-plan assertions or benchmarks for street/postcode/loss queries.
- [ ] Add a performance smoke test on a large synthetic dataset.

Definition of done for each task:

- [ ] Code, migration and tests merged.
- [ ] `make check` passes.
- [ ] Relevant README/docs updated.
- [ ] Behaviour verified on a large dataset or documented as not measured.
- [ ] No new silent error paths introduced.

---

## Suggested delivery order

1. [x] **Canonical sales model + canonical PID determinism** (Phase 1)
2. [x] **Importer batching + exit status, downloader collision fix** (Phase 2)
3. [x] **Search pagination, suburb suggestions, search error states** (Phase 3)
4. [x] **Materialised suggestions, losses SQL pagination, index migration,
   stats lifecycle** (Phase 4)
5. **CSP/assets/cookies, proxy-aware rate limits, health/observability**
   (Phase 5)
6. **Postcode trends, provenance/moderation, geocoding** (Phase 6)

---

## Open decisions

1. Canonical sale dedupe key: the implementation uses
   `(canonical_pid, settlement_date, sale_price, source_type)`. Revisit only if a
   dealing/reference number is added to the source data.
2. Keep duplicate sale rows for provenance, or collapse them for public display?
3. Materialised `canonical_sales`/`street_stats`/`postcode_stats` tables, or live
   queries with indexes and caching?
4. Exact street/postcode page sizes and whether grouped street matches need
   pagination or a “show all” limit.
5. Whether crowdsourced sales are hidden by default until moderation is
   available.
6. Trusted-proxy deployment shape (direct, nginx, Cloudflare, etc.) for rate
   limiting and client IPs.
7. Whether geocoding is in scope for this product or a separate data pipeline.
