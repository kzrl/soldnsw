# SoldNSW - NSW Property Sales & Suburb Trends

A lightweight NSW property sales and suburb trends tracker built in **Go** with **server-side rendered HTML**, styled with **Bulma CSS**, using **`sqlc`** for type-safe SQLite access. 
It uses G-NAF address records and NSW Valuer General Property Sales Information (PSI) data.

---

## Features

- **Address, street and postcode search**: a single autocomplete box accepts a full address, street name, postcode or property ID, with typed address/street/suburb/postcode suggestions. Address results, grouped streets and street/postcode property lists are paginated. Street pages show a summary (property count, sales, median/average/highest price, median land value per m²) and every property on the street. Postcode pages show a KPI overview (including median land value per m²), suburb breakdown and paginated properties. Results can be filtered by dwelling type, restricted to properties with recorded sales, and sorted by street number, latest sale or latest sale price. Database failures show a service-unavailable notice instead of a misleading empty result. Address autocomplete uses SQLite FTS5 and excludes records without a street number, while street grouping queries the address table directly and includes them.
- **Historical property pages**: full settlement history, source registry, cadastral lot/plan, property type, land area and capital-growth summary. Sales history includes a per-sale **land value ($/m²)** column, and a **comparable-sales** card lists the latest recorded sale per nearby property (same street first, then similar land area) with median price and median $/m². Strata and townhouse units have individual pages, plus a building overview showing all units that share a base PSI ID.
- **Interactive map**: Leaflet.js map rendered only when latitude/longitude are available. NSW PSI imports do not contain coordinates, so those records show a clear "no coordinates" notice instead of a misleading default location.
- **Suburb market analytics**: median and average sale prices with 1-year/5-year/10-year/all-time ranges, automatic 5-year fallback when the selected window has fewer than 20 sales, maximum and minimum prices, transaction volume, monthly average price trend (Chart.js), paginated sales records, **median land value ($/m²) for house sales with a recorded land area**, and **1-year/5-year/10-year percentage change in average sale price** in the suburb overview table.
- **Suburb growth rankings and comparison**: `/suburbs/growth` ranks the fastest- and slowest-growing suburbs by 1-year, 5-year or 10-year average price change, with a configurable minimum-sales threshold and pagination. `/compare?suburbs=A|NSW,B|NSW` puts up to four suburbs side by side with range KPIs, land value, change figures and an overlaid monthly average-price chart.
- **Loss-making resales**: a public page per suburb listing properties whose latest recorded sale was below the immediately previous recorded sale, with 2-year/5-year/10-year/all-time ranges, dollar/percentage/date sorting, dwelling-type tabs (houses, units, townhouses, other), and nominal-loss summaries.
- **Crowdsourced sale submission**: adds sale records with date, source and price validation, CSRF protection and per-IP rate limiting.
- **NSW Valuer General PSI downloader**: discovers weekly bulk zip archives, handles Cloudflare clearance via `chromedp`, downloads concurrently with retries, validates downloaded ZIPs before replacing existing files, deduplicates mirror URLs by filename, and optionally extracts archives.
- **PSI importer**: parses modern and historical NSW PSI `.DAT` records, including nested zip archives, and imports them in configurable `-batch` transactions. File, batch and row failures are counted, stored in `import_runs`, and reported with a non-zero exit status.
- **SQLite STRICT + WAL**: foreign keys, strict column types, separately managed indexes for the hot query paths, and pragmas applied to every pooled connection. Address records include `property_type` and `land_area` (stored in square metres). The address search index uses FTS5, and materialised street/postcode tables keep autocomplete fast.
- **Embedded schema**: the schema is embedded in the binaries, so the server and importer work from any working directory.
- **Materialised analytics**: the whole-market overview is stored in a `suburb_stats` table (rebuilt on startup/import and updated after each submission) and paginated at 50 suburbs per page, so `/suburbs` is fast and the long tail of suburbs is reachable instead of silently truncated.

> **Data provenance note:** the sample seed dataset includes coordinates and example G-NAF-style PIDs. Imported NSW PSI records use synthetic `GANSW_PSI_*` identifiers and have no latitude/longitude because the PSI dataset does not provide geocoding. They are shown as "NSW PSI Sales Record", not "G-NAF Verified Address".

---

## Directory Structure

```text
.
├── cmd/
│   ├── server/
│   │   ├── main.go           # Web server, routes, caching, CSRF/rate limiting
│   │   └── main_test.go      # Template, search and submission regression tests
│   ├── ingest/
│   │   └── main.go           # Seed sample addresses and sales from embedded CSV
│   ├── import_nsw/
│   │   └── main.go           # Import downloaded NSW PSI zip/DAT files
│   ├── migrate/
│   │   └── main.go           # Explicit schema/index migration + stats rebuild
│   └── downloader/
│       └── main.go           # NSW Valuer General bulk PSI weekly zip downloader
├── db/
│   ├── query.sql             # SQL queries for sqlc code generation
│   ├── indexes.sql           # Heavy indexes applied by bin/migrate
│   └── sqlc.yaml             # sqlc configuration
├── internal/
│   ├── database/
│   │   ├── schema.sql        # SQLite DDL, STRICT constraints and indexes (embedded)
│   │   ├── schema.go         # go:embed for schema.sql
│   │   ├── stats.go          # Materialised suburb summary maintenance
│   │   ├── search_stats.go   # Materialised street/postcode suggestion stats
│   │   ├── indexes.go        # Index migration and verification
│   │   ├── canonical_sales.go # Canonical sale mapping and analytics source
│   │   ├── address_search.go # FTS5 address index and autocomplete search
│   │   ├── search_query.go   # Address/street/postcode query classification
│   │   ├── property_links.go # Canonical property mapping for PSI duplicates
│   │   ├── classification.go # House/unit/townhouse classification
│   │   └── ...               # Generated sqlc models/queries
│   ├── nswpsi/
│   │   ├── parser.go         # Modern and historical PSI DAT parser
│   │   ├── importer.go       # Batched, transactional importer
│   │   └── *_test.go
│   └── browser/
│       └── clearance.go      # chromedp Cloudflare clearance helper
├── templates/
│   ├── base.html             # Base layout, navbar, G-NAF attribution
│   ├── index.html            # Search, grouped street results, recent sales and stats
│   ├── postcode.html         # Postcode overview and paginated properties
│   ├── property.html         # Property details, sales history and map
│   ├── suburb.html           # Suburb analytics, chart and paginated sales
│   ├── growth.html           # Fastest/slowest growing suburb rankings
│   ├── compare.html          # Side-by-side suburb KPIs and overlaid chart
│   ├── losses.html           # Public loss-making resales page
│   ├── building.html         # Strata/building unit overview
│   └── submit.html           # Sale submission form
├── static/
│   └── css/style.css
├── Makefile                  # generate/build/run/test/import targets
├── go.mod
└── go.sum
```

---

## Prerequisites

- **Go** (see `go.mod`) with CGO enabled — `go-sqlite3` requires a C compiler.
- **make**.
- **sqlc** — required by `make generate`, `make build` and `make check`.
- **Chromium/Chrome** — only needed for the downloader's automatic Cloudflare clearance (`chromedp`). You can instead pass a `-cookie` or use `-use-browser=false`.
- **FTS5 build tag** — the make targets automatically build with `-tags sqlite_fts5`. If you build or test manually, use `go build -tags sqlite_fts5 ./...` or `go test -tags sqlite_fts5 ./...`.

Install sqlc once:

```bash
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`, or override the binary:

```bash
make build SQLC=/path/to/sqlc
```

---

## Make targets

All binaries are written to `bin/`.

| Target | Description |
|---|---|
| `make` / `make build` | Run `sqlc generate`, then build all four binaries |
| `make generate` | Run `sqlc generate` against `db/query.sql` |
| `make migrate` | Apply schema/index migrations and rebuild materialised stats |
| `make run` | Build, run migrations, and start the web app on `http://localhost:8080` |
| `make run-server` | Alias for `make run` |
| `make run-ingest` | Build and seed the sample G-NAF/sales dataset |
| `make run-downloader` | Build and download NSW PSI archives |
| `make run-import` | Build, import downloaded NSW PSI archives, then create indexes |
| `make test` | Run `go test -tags sqlite_fts5 ./...` |
| `make check` | Run `generate`, `vet` and `test` |
| `make vet` | Run `go vet ./...` |
| `make fmt` | Run `gofmt` on `cmd/` and `internal/` |
| `make tidy` | Run `go mod tidy` |
| `make clean` | Remove `bin/` |
| `make help` | List targets and current variable values |

### Variables

Override any of these after the target:

```bash
make run PORT=9090 DB=properties.db
make run-downloader DATA_DIR=data/nsw_weekly WORKERS=4
make run-import DATA_DIR=data/nsw_weekly BATCH=5000
```

| Variable | Default | Used by |
|---|---|---|
| `GO` | `go` | all Go commands |
| `SQLC` | `sqlc` | `generate` |
| `TAGS` | `sqlite_fts5` | build, test and vet |
| `BIN_DIR` | `bin` | all build targets |
| `DB` | `properties.db` | `run`, `run-ingest`, `run-import` |
| `PORT` | `8080` | `run` |
| `TEMPLATES` | `templates` | `run` |
| `STATIC` | `static` | `run` |
| `DATA_DIR` | `data/nsw_weekly` | `run-downloader`, `run-import` |
| `WORKERS` | `4` | `run-downloader` |
| `BATCH` | `5000` | `run-import` |

`make build` always runs `make generate` first, so the sqlc output in `internal/database/` stays in sync with `db/query.sql` and `internal/database/schema.sql`.

---

## Quickstart

### 1. Build

```bash
make build
```

This runs `sqlc generate` first and writes `bin/server`, `bin/ingest`, `bin/downloader`, `bin/import_nsw` and `bin/migrate`.

### 2. Seed sample data (optional)

```bash
make run-ingest
```

For custom CSV files, call the binary directly:

```bash
./bin/ingest -db properties.db -gnaf path/to/gnaf.csv -sales path/to/sales.csv
```

### 3. Download NSW Valuer General weekly bulk data (optional)

```bash
make run-downloader WORKERS=4
```

Equivalent direct invocation with extra options:

```bash
./bin/downloader -out data/nsw_weekly -workers 4 -dry-run -filter 2026 -extract
```

Useful options:

- `-dry-run`: list discovered weekly `.zip` URLs without downloading.
- `-filter 2026`: only download files matching a year or keyword.
- `-extract`: unpack downloaded archives automatically.
- `-cookie "..."`: supply a session cookie if browser clearance is not desired.
- `-use-browser=false`: skip `chromedp` and use direct HTTP requests.
- `-browser /usr/bin/chromium`: explicitly select the Chrome/Chromium binary for `chromedp`.

### 4. Import PSI data

```bash
make run-import DATA_DIR=data/nsw_weekly BATCH=5000
```

Equivalent direct invocation with extra options:

```bash
./bin/import_nsw -data data/nsw_weekly -db properties.db -batch 5000 -filter 2026 -v
```

For a new database, create the heavy indexes after the import:

```bash
./bin/migrate -db properties.db -stats=false
```

`make run-import` does this automatically. `make run` also runs the full migrate
command before starting the server.

Useful importer options:

- `-batch`: number of parsed property/sale rows per transaction.
- `-fail-fast`: stop at the first file or batch failure instead of continuing.
- `-dry-run`: parse and count records without writing.
- `-v`: print skipped individual properties/sales.

The importer records each top-level archive/directory in the `import_runs` table with file size, timing, counts, status and error details. The server footer shows the most recent successful or partial import date.

The importer creates the schema automatically if the database is new. Property upserts preserve previously linked coordinates and cadastral details. It also rebuilds the `suburb_stats` analytics summary.

### 5. Start the server

```bash
make run
```

Open **[http://localhost:8080](http://localhost:8080)**. Override the port or database:

```bash
make run PORT=9090 DB=properties.db
```

Server options: `-port`, `-db`, `-templates`, `-static`, `-stats-timeout`, `-trusted-proxies`. Environment variables `PORT`, `DB_PATH` and `TRUSTED_PROXIES` are also supported when invoking `bin/server` directly. Set `-trusted-proxies` to the reverse-proxy addresses (for example `127.0.0.1,::1` behind Caddy) so client IPs for rate limiting come from `X-Forwarded-For`; leave it empty when the server is exposed directly. `GET /healthz` reports process liveness and `GET /readyz` reports database/materialised-stats readiness.

### 6. Run tests

```bash
make test
# or run generation + vet + tests together:
make check
```

---

## Regenerating sqlc code

Run the generator through make:

```bash
make generate
```

`make build` and `make check` run this automatically. The sqlc configuration reads `db/query.sql` and the embedded schema at `internal/database/schema.sql`. If you need to run it manually:

```bash
cd db
sqlc generate
```

---

## Data notes

- Analytics queries are expensive on the full NSW PSI dataset (millions of properties and sales). The service applies indexes for recent sales, state/suburb/street filters and date ordering, and stores the whole-market overview in `suburb_stats`. On a fresh database the table is built in the background on first startup; the page shows a short "calculating" notice until it is ready.
- Old building-level PSI URLs redirect to a building overview listing the units that share that base ID.
- Dwelling types are classified from property type, street number and plan number. `RESIDENCE` is used for both houses and units, so street/plan evidence takes priority. Records without enough evidence are `unknown` and appear under “Other / unknown”.
- PSI property IDs are unit-aware. Strata and townhouse records that share a building-level PSI ID receive suffixes such as `_U3`, `_U305`, while `base_pid` preserves the original building ID. This prevents all units in a building from being merged into one property.
- Canonical property links merge PSI records that represent the same address and lot/plan but appear under different district codes. Old property URLs redirect to the canonical PID, and sale histories are merged. Original property rows and sales are retained.
- Canonical PID selection is deterministic: it prefers non-PSI records, then records with more address/cadastral/coordinate data, then the lowest PID. It no longer depends on the latest sale date, so new submissions do not silently move the canonical property.
- Analytics read from a `canonical_sales` table. It maps every raw sale to its canonical property and keeps one representative row per duplicate sale signature `(canonical_pid, settlement_date, sale_price, source_type)`. Raw `property_sales` rows remain the source of truth for property history and provenance. The table is rebuilt after bulk imports and after canonical-link migrations, and updated per submission.
- Crowdsourced sales are recorded against the canonical property, so duplicate PSI rows do not fragment sale history or suburb/postcode statistics.
- The unit-aware PSI import required a full re-import; unit rows now carry their own lot/plan and sales. The previous database is retained as `properties.pre-units.db` for rollback.
- Address autocomplete uses an FTS5 index rebuilt at startup when empty and after bulk imports. It intentionally excludes records without a street number because those addresses are ambiguous. Street searches do not use that index, so streets containing only unnumbered records still appear in grouped street results. Suggestions also use materialised `street_stats`/`postcode_stats`, with a live-query fallback while the tables are being built. Suggestion prefix results are cached for one minute and rate-limited per client. Queries are capped at 120 characters. The suggestion endpoint is `GET /api/search/suggest?q=…`; the older `GET /api/address/suggest` endpoint remains for address-only clients. Postcode overviews are available at `GET /postcode/{code}`.
- Heavy indexes live in `db/indexes.sql` and are applied by `bin/migrate`; the server only warns at startup if they are missing. The materialised suburb/street/postcode tables are built in the background with retries and a configurable `-stats-timeout`. Their build status is shown on the suburb page and available from `GET /readyz`.
- Only configured trusted proxies may set `X-Forwarded-For`; the server skips trusted hops from the right and uses the first untrusted address. Access logs record method, path, status, response size and duration.
- Loss-making resale pages sort, filter and paginate in SQL instead of loading every loss into memory.
- `property_type` comes from the modern PSI purpose field where available; older PSI records may contain historical type codes (for example `TE`). `land_area` is normalized to square metres, with historical hectare/acre values converted.
- Suburb median statistics default to the last 12 months. If that window has fewer than 20 sales, the page falls back to 5 years and then all time, with a note explaining the fallback.
- The 1-year/5-year/10-year change columns compare the latest 12-month average sale price with the 12-month average ending 1, 5 or 10 years ago. A dash means there is not enough recorded sales history in one of the comparison periods.
- Loss-making resales compare the latest recorded sale with the immediately previous recorded sale. Values are nominal, same-day resales are excluded, and records are linked by property ID or identical address. Drops over 80% are flagged as "Check data" because partial titles, related-party transfers or data errors can produce extreme values.
- Uploaded/crowdsourced sales are unauthenticated. Production deployments should add authentication, moderation and durable rate limiting (for example a shared store rather than the in-memory limiter).
- The downloader's browser-clearance path is provided for the documented bulk PSI workflow. Review the source site's terms before using automated downloads.
- Import failures are recorded per input file in `import_runs`; use `-fail-fast` when a partial import should abort immediately. The importer exits non-zero when any file or batch failed.

---

## Attribution

Incorporates data sourced from the **Geocoded National Address File (G-NAF)** &copy; [Geoscape Australia](https://geoscape.com.au), licensed by the Commonwealth of Australia under the [Creative Commons Attribution 4.0 International license (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/).

NSW property sales data is sourced from the NSW Valuer General Property Sales Information (PSI) bulk data. Refer to the NSW Valuer General for the applicable licence and attribution requirements.
