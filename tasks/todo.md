# Task List: Suburbs with the Most Losses (Last 2 Years)

Plan: `tasks/plan.md`

## Task 1: Add `suburb_loss_stats` schema and full refresh

**Description:** Add the materialised two-year loss ranking table and a
`RefreshSuburbLossStats(ctx, db)` function that rebuilds it in one transaction
using the existing loss definition. Store positive loss amounts and the
`flagged_losses` count for >80% drops.

**Acceptance criteria:**
- [x] `suburb_loss_stats` exists in `schema.sql` with the documented columns and
      ranking index.
- [x] `RefreshSuburbLossStats` clears and repopulates the `2y` period.
- [x] A property whose latest sale is below the previous sale is counted once.
- [x] Same-day resales, gains, and latest sales outside the cutoff are excluded.
- [x] Resales/losses/total_loss/average_loss/largest_loss/flagged_losses match a
      hand-computed fixture.
- [x] Suburbs with resales but zero losses are stored with zero counts.

**Verification:**
- [ ] `go test -count=1 -tags sqlite_fts5 ./internal/database`
- [x] `go build -tags sqlite_fts5 ./...`

**Dependencies:** None

**Files likely touched:**
- `internal/database/schema.sql`
- `internal/database/stats.go`
- `internal/database/stats_test.go`

**Estimated scope:** Medium

## Task 2: Add single-suburb loss refresh

**Description:** Add `RefreshSuburbLossStat(ctx, db, suburb, state)` that
recomputes only the submitted suburb and upserts the `2y` row. Call it after a
sale is accepted, alongside the existing suburb/street/postcode stat refreshes.

**Acceptance criteria:**
- [x] Submitting a sale updates that suburb's loss row without rebuilding every
      suburb.
- [x] A new loss and a new gain both update the row correctly.
- [x] Re-submitting the same sale does not double count.
- [x] If no losses remain, the row is updated with zero counts rather than
      deleted.

**Verification:**
- [x] Focused database test for insert/update/upsert
- [x] Existing `/submit` handler tests still pass

**Dependencies:** Task 1

**Files likely touched:**
- `internal/database/stats.go`
- `internal/database/stats_test.go`
- `cmd/server/main.go`

**Estimated scope:** Small

## Task 3: Add ranking queries

**Description:** Add sqlc queries for the leaderboard list, total count and
summary totals. Support `losses`, `rate`, and `average_loss` sort modes, with the
20-resale threshold for rate mode.

**Acceptance criteria:**
- [x] List query returns rank inputs ordered by the requested mode.
- [x] Rate mode excludes rows with `resales < 20`.
- [x] Count and summary queries respect the same filter.
- [x] Pagination uses `LIMIT/OFFSET` and remains deterministic on ties.
- [x] Only rows with `losses > 0` appear in the ranking.

**Verification:**
- [x] Database tests cover each sort mode and pagination
- [x] `sqlc generate` produces no unexpected parameter changes

**Dependencies:** Task 1

**Files likely touched:**
- `db/query.sql`
- `internal/database/query.sql.go` (generated)
- `internal/database/stats_test.go`

**Estimated scope:** Medium

## Task 4: Add handler and desktop page

**Description:** Add `GET /suburbs/most-losses` with `sort` and `page` query
parameters, load the ranking/count/summary, and render a new
`templates/most_losses.html`. Include breadcrumbs, summary cards, sort tabs,
table, pagination, empty state and building state.

**Acceptance criteria:**
- [x] Default view ranks by loss count.
- [x] Sort tabs preserve pagination inputs and the selected state.
- [x] Each suburb links to its trends page and its `/suburbs/losses` detail with
      `range=2`.
- [x] Pagination clamps out-of-range pages.
- [x] Empty stats table shows the analytics-building notice.
- [x] No losses shows a friendly empty state.

**Verification:**
- [x] Server handler tests for default, each sort mode, page 2 and empty state
- [ ] `go test -count=1 -tags sqlite_fts5 ./cmd/server`

**Dependencies:** Task 3

**Files likely touched:**
- `cmd/server/main.go`
- `templates/most_losses.html`
- `cmd/server/main_test.go`

**Estimated scope:** Medium

## Task 5: Add mobile cards and methodology

**Description:** Add a mobile card layout for the ranking (Bulma
`is-hidden-mobile` / `is-hidden-tablet` or equivalent), show updated timestamp,
the rate threshold note, and the loss-data methodology note matching the
per-suburb losses page.

**Acceptance criteria:**
- [x] At 390px the ranking is readable cards with no horizontal overflow.
- [x] Summary cards stack full-width on phones and go 3-up on desktop.
- [x] Methodology explains latest-vs-previous, same-day exclusion and the
      >80% "possible data issue" flag.
- [x] Rate tab explains the 20-resale threshold.

**Verification:**
- [x] Headless Chromium check at 390px and 1280px
- [ ] `go test -count=1 -tags sqlite_fts5 ./cmd/server`

**Dependencies:** Task 4

**Files likely touched:**
- `templates/most_losses.html`
- `static/css/style.css`
- `cmd/server/main_test.go`

**Estimated scope:** Small

## Task 6: Wire refresh into all stats paths

**Description:** Ensure the loss table is rebuilt wherever the other
materialised stats are rebuilt: migrate, ingest, importer, startup when empty,
background missing-stats builder. Report readiness and log the refresh duration.

**Acceptance criteria:**
- [x] `make migrate` creates/refreshes `suburb_loss_stats`.
- [x] Bulk imports and first-run ingest refresh it.
- [x] The background stats builder rebuilds it when empty.
- [x] `/readyz` includes loss stats readiness or the existing stats status
      covers it.
- [x] A failed loss refresh does not leave a partially written table.

**Verification:**
- [x] Migration test on an empty DB and an existing DB
- [x] Existing stats tests pass
- [x] Manual migrate log includes loss stats duration

**Dependencies:** Tasks 1-2

**Files likely touched:**
- `cmd/migrate/main.go`
- `cmd/ingest/main.go`
- `cmd/server/main.go`
- `internal/nswpsi/importer.go`
- `internal/database/stats.go`

**Estimated scope:** Medium

## Task 7: Add navigation and redirect

**Description:** Make the ranking discoverable. Link to it from the suburb
trends overview and the per-suburb losses page. Change `/suburbs/losses` with
no suburb to redirect to `/suburbs/most-losses` instead of the current warning.

**Acceptance criteria:**
- [x] Suburb trends page has a visible "Suburbs with the most losses" link.
- [x] Per-suburb losses page links back to the ranking.
- [x] `/suburbs/losses` with no suburb returns a 303 redirect.
- [x] Existing per-suburb losses URLs are unchanged.

**Verification:**
- [x] Server tests for redirect and both links
- [x] Manual check of both pages

**Dependencies:** Task 4

**Files likely touched:**
- `templates/suburb.html`
- `templates/losses.html`
- `cmd/server/main.go`
- `cmd/server/main_test.go`

**Estimated scope:** Small

## Task 8: Measure, document and deploy

**Description:** Run the refresh against production-sized data, record duration
and page latency, then deploy with the spot playbook and verify public pages.

**Acceptance criteria:**
- [x] Refresh duration recorded on the production DB copy.
- [x] Warm page response under 1s.
- [x] Mobile and desktop pages verified live.
- [x] Plan notes any follow-up (monthly aggregates, exclude-flagged filter).

**Verification:**
- [ ] `go test -count=1 -tags sqlite_fts5 ./...`
- [x] `spot -p deploy-soldnsw.yml -t node1.prd.springupsoftware.com`
- [x] Public `https://soldnsw.kzrl.org/suburbs/most-losses` responds 200

**Dependencies:** Tasks 1-7

**Files likely touched:**
- `tasks/plan.md`
- deployment only (no new source files)

**Estimated scope:** Small
