# Plan: search the homepage by street name and postcode

Status: implemented (2026-10-30).
Author: agent

## Implementation notes

The recommended options from §7 were used:

1. Street searches show grouped matching streets, then every property on the
   selected street with pagination (50 per page).
2. Postcodes use a dedicated `/postcode/{code}` overview page.
3. Streets are searched across all of NSW and grouped by suburb/state/postcode.
4. Street grouping queries `gnaf_properties` directly, so records without a street
   number are included. The address FTS index remains unchanged and still excludes
   them.
5. Stats are computed live; no `street_stats`/`postcode_stats` table was added.
6. The existing advanced form is still present and now feeds the same grouped
   street results.

Files added/changed: `internal/database/search_query.go`, the street/postcode
queries in `db/query.sql`, `templates/postcode.html`, `templates/index.html`,
`cmd/server/main.go`, `internal/database/schema.sql` and the README/tests.

## 1. Goal

The homepage's single search box currently takes a full address (and, incidentally,
street/suburb tokens) and returns address records. Extend it so a user can type:

- a street name, e.g. `Arnold St` or `arnold st killara`
- a postcode, e.g. `2071`

and get useful, complete results: matching streets/postcodes with counts, then the
properties and sale context behind them — not a silently truncated list of 50
addresses.

Exact-address behaviour (full address → straight to `/property/{pid}`) must stay
unchanged.

Non-goals: new data sources, geocoding, changing how suburb analytics are
calculated, or redesigning the map/property pages.

## 2. Current behaviour (what the code actually does)

- `GET /` renders `templates/index.html`. The main form submits
  `GET /properties?address=…`; the advanced form submits
  `?number=&street=&suburb=&state=&postcode=&dwelling=`.
- `handlePropertiesSearch` (`cmd/server/main.go`) branches:
  - `address` non-empty → `database.SearchAddressFTS(..., 50)`. An exact normalised
    address match redirects; a single match redirects; otherwise up to 50
    `GnafProperty` rows are listed.
  - Otherwise it validates number/postcode and calls
    `SearchPropertiesFlexible` (`db/query.sql`), which filters
    `street_name LIKE '<STREET>%'`, exact postcode, suburb, state, dwelling type,
    orders by street name/number and `LIMIT 50`.
- `address_fts` (`internal/database/schema.sql`) contains one row per canonical
  `gnaf_properties` record: `NUMBER STREET TYPE SUBURB STATE POSTCODE`, tokenised
  with `unicode61`; `addressFTSQuery` ANDs all tokens as prefixes. The index
  intentionally excludes records with no street number, and the README documents
  that.
- Existing indexes: `idx_gnaf_street(street_name)`, `idx_gnaf_postcode(postcode)`,
  `idx_gnaf_suburb(suburb, street_name, postcode)`.
- `/api/address/suggest` returns only `{label, pid}` from the address FTS index.

Consequences today:

- `Arnold` returns address rows (max 50) but no street summary, no total count and
  no pagination.
- `2071` returns address rows (max 50) but no postcode overview and no way to see
  the rest.
- Streets whose properties have no street number are invisible to free-text search.
- There is no navigation from a street/postcode result to an aggregated view.
- The advanced form does support street and postcode, but it is collapsed, ANDs
  all fields, and is capped at 50.

## 3. Recommended design

Keep one box. Classify the query and return typed results; use the existing
structured filters as the canonical "street" and "postcode" inputs.

Result precedence (deterministic, table-tested):

1. **Exact address** — normalised full-address match in `address_fts` → redirect
   to `/property/{pid}` (unchanged).
2. **Street** — query resolves to a street (name plus optional type/suburb/postcode).
   Show matching streets with property counts; selecting one lists all its
   properties, paginated. Reuse `/properties?street=…&suburb=…&postcode=…` as the
   canonical URL; no new street route required initially.
3. **Postcode** — 4 digits → postcode summary (property/sales counts, median/avg
   price, latest sale) plus a suburb breakdown and paginated properties/sales.
   New `GET /postcode/{code}` page (recommended) or render inline on `/properties`
   in phase 1 and promote later.
4. **Fallback** — current FTS address list.

Classification rules (in a small, unit-tested function, not scattered in the
handler):

- normalise: trim, collapse whitespace, uppercase.
- `^\d{4}$` → postcode candidate.
- 4 digits + letters → address/street query (e.g. `2071 Arnold`).
- contains a street type token (`ST`, `RD`, `AVE`, `PL`, `DR`, `CR`, `CT`, `HWY`,
  `LN`, `PDE`, …) or matches a street in G-NAF → street query.
- query with a house number prefix (`17 …`, `1/7 …`) → address query first.
- ambiguous input runs FTS first; if no exact/single address match, use the
  street/postcode interpretation. Never silently drop tokens.

Alternatives considered:

- **B: expose the advanced filters only** (cheapest, no aggregation, no
  pagination). Solves discoverability but not the 50-row cap or street/postcode
  overview. Can ship as a stopgap.
- **C: explicit segmented control** (Address / Street / Postcode). Clearest UX,
  but adds mode friction to the one-box flow. Recommended fallback if usability
  testing dislikes inference.

## 4. Work breakdown

### Phase 0 — confirm and measure

- Confirm the decisions in §7.
- Capture `EXPLAIN QUERY PLAN` for current street/postcode queries on the full
  dataset (~2.9M properties per README) and record p95 latency.
- Decide whether street/postcode summaries are computed live or materialised.

### Phase 1 — SQL and data layer

Add queries to `db/query.sql` and regenerate with `make generate`:

- `SearchStreets :many` — group canonical `gnaf_properties` by
  `(street_name, street_type, suburb, state, postcode)` with
  `COUNT(*)`, optional `street_name LIKE @pattern`, `postcode`, `suburb`, `state`
  filters, ordered by exact match then count, `LIMIT @limit`.
- `ListStreetProperties :many` — exact tuple
  `street_name/street_type/suburb/state/postcode`, canonical only, `LIMIT/OFFSET`,
  ordered by street number then address. Also `CountStreetProperties :one`.
- `ListPostcodeProperties :many` + `CountPostcodeProperties :one` — canonical,
  exact postcode, `LIMIT/OFFSET`.
- `GetPostcodeSummary :one` — property count, sale count, median/average sale
  price, latest sale date. Reuse the aggregation style from
  `GetSuburbAnalytics`/`GetSuburbOverviewStats`; define the median consistently
  with suburb pages.
- Optionally `GetPostcodeSuburbBreakdown :many` — per-suburb counts/prices for the
  postcode page.

Implementation notes:

- All aggregate queries must exclude non-canonical links
  (`property_links.canonical_pid IS NULL OR = gnaf_pid`) so counts are not
  double-counted; keep that join shape consistent with existing queries.
- Do not aggregate inside the autocomplete endpoint; suggestions should only look
  up names/postcodes with a small `LIMIT`.
- Only materialise (`street_stats`, `postcode_stats`) if live queries miss the
  latency target. If materialised, rebuild alongside `suburb_stats` on
  startup/import and after submissions, and add it to the README.
- Add only indexes justified by `EXPLAIN`, likely
  `(postcode, street_name)` and `(street_name, postcode, suburb)`, using
  `CREATE INDEX IF NOT EXISTS` in `schema.sql`. Flag the first-start build cost on
  a large database in the README; consider running it via a migration rather than
  blocking startup.

### Phase 2 — typed search service

New file, e.g. `internal/database/search.go` (or `internal/search`):

- `type QueryKind string` with `address`, `street`, `postcode`, `fallback`.
- `ClassifyQuery(raw string) QueryKind` with the rules in §3.
- `SearchProperties(ctx, db, raw string, opts) (SearchResult, error)` returning
  street matches, postcode matches and address matches, so the handler does not
  repeat classification logic.
- Keep `SearchAddressFTS` and `addressFTSQuery` behaviour intact for exact
  addresses and existing tests.
- Street lookup uses the existing `idx_gnaf_street` / `idx_gnaf_suburb` prefixes.
- Optional `street_fts` FTS5 table (street + type + suburb + postcode, including
  records without a street number) if we want reliable street autocomplete and
  no-number coverage. If added: create in `schema.sql`, populate/rebuild in
  `RebuildAddressSearch`/`EnsureAddressSearch`, invoke after PSI import, and add
  `-tags sqlite_fts5` tests.

### Phase 3 — HTTP layer

- `handlePropertiesSearch`: parse inputs as now, then delegate to the typed
  search service. Preserve:
  - exact-address and single-match redirects,
  - postcode validation message,
  - number-without-street warning,
  - existing query fields on `PageData`.
- Street mode renders grouped results on `index.html` with a total count and a
  pager; selecting a street renders the paginated property list.
- Postcode mode: add `GET /postcode/{code}` → `handlePostcodeOverview` + new
  `templates/postcode.html` (recommended). Phase 1 can render the summary inline
  on `/properties` to keep the change smaller. Add the route in `cmd/server/main.go`.
- Unified suggestions: `GET /api/search/suggest?q=` returning
  `[{kind, label, value, pid?}]` (`address`, `street`, `postcode`), capped and
  without counts. Keep `/api/address/suggest` for compatibility (delegate or
  filter to addresses).
- Pagination: follow the existing `page`, `ShowingFrom`, `ShowingTo`,
  `TotalPages`, `HasPrev`, `HasNext` conventions used by `handleSuburbAnalytics`.

### Phase 4 — homepage UI

In `templates/index.html`:

- Update the label/placeholder/help text to say address, street or postcode.
- Add a few example chips (`Arnold St Killara`, `2071`) under the box.
- Consider opening the advanced `<details>` by default, or renaming it to make
  street/postcode filtering discoverable.
- Suggestions list: typed icon/badge and secondary text (suburb/postcode for
  streets; suburb count for postcodes). Keep the existing keyboard model
  (`ArrowUp/Down`, `Enter`, `Escape`, `aria-activedescendant`) and click
  navigation. Use `role="listbox"`/`role="option"` correctly if grouping is added.
- Results panel: street header (`ARNOLD ST, KILLARA` + count + pager) or postcode
  KPI cards + suburb breakdown + pager. Reuse the suburb pager markup where
  possible.
- Empty state: distinguish "no address", "no street", "no postcode" and suggest
  the advanced filters.

### Phase 5 — tests and docs

- `internal/database`: classification table tests; street aggregation (including
  duplicates via `property_links`, records without street numbers); postcode
  summary; pagination and ordering; typed suggest results.
- `cmd/server/main_test.go`: street query does not redirect when multiple matches
  and includes all results; postcode query renders summary/pager; exact-address
  redirect unchanged; single street match still redirects (`TestStreetOnlySearch`);
  invalid postcode warning; pagination; suggest JSON shapes.
- Template test additions in the existing `TestTemplatesRender`.
- Update README: features, search behaviour, new route, FTS index behaviour,
  materialised stats (if any), and test expectations.
- Run `make check` (sqlc generate + vet + tests with `sqlite_fts5`).

### Phase 6 — rollout and verification

- No destructive migration; indexes use `IF NOT EXISTS`; FTS rebuild stays
  startup-only when empty (plus any new index).
- Manual flows on the seeded DB and, if available, a full-size DB:
  full address, street only, street + suburb, postcode, postcode + street,
  ambiguous names, no-match, pagination, browser back/forward, autocomplete
  keyboard only, mobile layout.
- Measure p95 for street/postcode queries; confirm `EXPLAIN` uses indexes.
- Update the README and close the plan.

## 5. Acceptance criteria

- Entering a full address still redirects to the property page.
- Entering a street name returns all matching streets with counts (not a truncated
  address list) and one click lists every canonical property on that street,
  paginated.
- Entering a postcode shows a postcode summary and paginated properties (or a
  dedicated postcode page).
- Autocomplete labels each suggestion's type and supports keyboard selection.
- No silent truncation: totals and pagination are visible when results exceed a
  page.
- `make check` passes; new tests cover classification, aggregation, handlers,
  rendering, and the unchanged exact-address behaviour.
- Query latency and startup time on the full dataset are measured and recorded;
  no unbounded scans on the suggest path.

## 6. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Free-text index excludes no-street-number records | Document, or add a street-specific index in phase 2 |
| Aggregating 2.9M rows per search is slow | Measure; only aggregate on submit, not on keystrokes; materialise if needed |
| Canonical-link `LEFT JOIN` makes counts expensive | Reuse the existing canonical pattern; benchmark; consider a canonical flag |
| New index build blocks startup on a large DB | `IF NOT EXISTS`, background/migration path, README note |
| Same street name across many suburbs | Return grouped streets; require suburb/postcode to disambiguate; never guess a single property unless exact |
| Postcode-only search spans multiple states | Show states in grouped results; postcode page lists all states/suburbs |
| `street_number` is TEXT, so ordering is lexicographic | Keep current ordering or sort numerically with a tested rule; don't change silently |
| `/api/address/suggest` consumers | Keep endpoint and response fields; add the typed endpoint alongside |
| Scope creep into SEO/analytics pages | Postcode page is optional; street results reuse `/properties` |

## 7. Decisions to confirm

1. **Street results**: grouped list of matching streets (recommended) or immediately
   all properties on the street? What page size — keep 50 or use the suburb page
   size (25)?
2. **Postcode results**: dedicated `/postcode/{code}` analytics page (recommended)
   or filter inside `/properties`?
3. **Street scope**: show matching streets across all of NSW (recommended, grouped
   by suburb/postcode) or require suburb/postcode before searching?
4. **No-street-number properties**: should they appear in street results? This
   requires a street-specific index and a README/FTS behaviour change.
5. **Stats strategy**: live aggregation first (recommended for phase 1) or
   materialised `street_stats`/`postcode_stats` from day one?
6. **Stopgap**: is promoting/opening the existing advanced filters acceptable as an
   interim release before the full grouped search?
