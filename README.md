# SoldNSW - NSW Property Sales & Suburb Trends

A lightweight NSW property sales and suburb trends tracker built in **Go** with **server-side rendered HTML**, styled with **Bulma CSS**, using **`sqlc`** for type-safe SQLite access. 
It uses G-NAF address records and NSW Valuer General Property Sales Information (PSI) data.

---

## Features

- **Address search**: a single autocomplete box searches full addresses, streets and suburbs. An advanced form supports street number, street, suburb, state, postcode and dwelling-type filtering. Exact matches go directly to the property page. Autocomplete uses SQLite FTS5 and excludes records without a street number.
- **Historical property pages**: full settlement history, source registry, cadastral lot/plan, property type, land area and capital-growth summary. Strata and townhouse units have individual pages, plus a building overview showing all units that share a base PSI ID.
- **Interactive map**: Leaflet.js map rendered only when latitude/longitude are available. NSW PSI imports do not contain coordinates, so those records show a clear "no coordinates" notice instead of a misleading default location.
- **Suburb market analytics**: median and average sale prices with 1-year/5-year/10-year/all-time ranges, automatic 5-year fallback when the selected window has fewer than 20 sales, maximum and minimum prices, transaction volume, monthly average price trend (Chart.js), paginated sales records, and **1-year/5-year/10-year percentage change in average sale price** in the suburb overview table.
- **Loss-making resales**: a public page per suburb listing properties whose latest recorded sale was below the immediately previous recorded sale, with 2-year/5-year/10-year/all-time ranges, dollar/percentage/date sorting, dwelling-type tabs (houses, units, townhouses, other), and nominal-loss summaries.
- **Crowdsourced sale submission**: adds sale records with date, source and price validation, CSRF protection and per-IP rate limiting.
- **NSW Valuer General PSI downloader**: discovers weekly bulk zip archives, handles Cloudflare clearance via `chromedp`, downloads concurrently with retries, and optionally extracts archives.
- **PSI importer**: parses modern and historical NSW PSI `.DAT` records, including nested zip archives, and imports them in batched SQLite transactions.
- **SQLite STRICT + WAL**: foreign keys, strict column types, indexes for the hot query paths, and pragmas applied to every pooled connection. Address records include `property_type` and `land_area` (stored in square metres). The address search index uses FTS5.
- **Embedded schema**: the schema is embedded in the binaries, so the server and importer work from any working directory.
- **Materialised analytics**: the whole-market overview is stored in a `suburb_stats` table (rebuilt on startup/import and updated after each submission), so `/suburbs` is fast even on a cold start.

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
│   └── downloader/
│       └── main.go           # NSW Valuer General bulk PSI weekly zip downloader
├── db/
│   ├── query.sql             # SQL queries for sqlc code generation
│   └── sqlc.yaml             # sqlc configuration
├── internal/
│   ├── database/
│   │   ├── schema.sql        # SQLite DDL, STRICT constraints and indexes (embedded)
│   │   ├── schema.go         # go:embed for schema.sql
│   │   ├── stats.go          # Materialised suburb summary maintenance
│   │   ├── address_search.go # FTS5 address index and autocomplete search
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
│   ├── index.html            # Search, recent sales and database stats
│   ├── property.html         # Property details, sales history and map
│   ├── suburb.html           # Suburb analytics, chart and paginated sales
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
| `make run` | Build and start the web app on `http://localhost:8080` |
| `make run-server` | Alias for `make run` |
| `make run-ingest` | Build and seed the sample G-NAF/sales dataset |
| `make run-downloader` | Build and download NSW PSI archives |
| `make run-import` | Build and import downloaded NSW PSI archives |
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

This runs `sqlc generate` first and writes `bin/server`, `bin/ingest`, `bin/downloader` and `bin/import_nsw`.

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

### 4. Import PSI data

```bash
make run-import DATA_DIR=data/nsw_weekly BATCH=5000
```

Equivalent direct invocation with extra options:

```bash
./bin/import_nsw -data data/nsw_weekly -db properties.db -batch 5000 -filter 2026 -v
```

The importer creates the schema automatically if the database is new. Property upserts preserve previously linked coordinates and cadastral details. It also rebuilds the `suburb_stats` analytics summary.

### 5. Start the server

```bash
make run
```

Open **[http://localhost:8080](http://localhost:8080)**. Override the port or database:

```bash
make run PORT=9090 DB=properties.db
```

Server options: `-port`, `-db`, `-templates`, `-static`. Environment variables `PORT` and `DB_PATH` are also supported when invoking `bin/server` directly.

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
- The unit-aware PSI import required a full re-import; unit rows now carry their own lot/plan and sales. The previous database is retained as `properties.pre-units.db` for rollback.
- Address autocomplete uses an FTS5 index rebuilt at startup when empty and after bulk imports. It intentionally excludes records without a street number because those addresses are ambiguous.
- `property_type` comes from the modern PSI purpose field where available; older PSI records may contain historical type codes (for example `TE`). `land_area` is normalized to square metres, with historical hectare/acre values converted.
- Suburb median statistics default to the last 12 months. If that window has fewer than 20 sales, the page falls back to 5 years and then all time, with a note explaining the fallback.
- The 1-year/5-year/10-year change columns compare the latest 12-month average sale price with the 12-month average ending 1, 5 or 10 years ago. A dash means there is not enough recorded sales history in one of the comparison periods.
- Loss-making resales compare the latest recorded sale with the immediately previous recorded sale. Values are nominal, same-day resales are excluded, and records are linked by property ID or identical address. Drops over 80% are flagged as "Check data" because partial titles, related-party transfers or data errors can produce extreme values.
- Uploaded/crowdsourced sales are unauthenticated. Production deployments should add authentication, moderation and durable rate limiting (for example a shared store rather than the in-memory limiter).
- The downloader's browser-clearance path is provided for the documented bulk PSI workflow. Review the source site's terms before using automated downloads.

---

## Attribution

Incorporates data sourced from the **Geocoded National Address File (G-NAF)** &copy; [Geoscape Australia](https://geoscape.com.au), licensed by the Commonwealth of Australia under the [Creative Commons Attribution 4.0 International license (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/).

NSW property sales data is sourced from the NSW Valuer General Property Sales Information (PSI) bulk data. Refer to the NSW Valuer General for the applicable licence and attribution requirements.
