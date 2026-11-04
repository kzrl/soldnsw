# Implementation Plan: Suburbs with the Most Losses (Last 2 Years)

## Overview

Add a leaderboard page that ranks suburbs by loss-making resales over the last
two years. A loss-making resale is the existing definition used by
`/suburbs/losses`: a property's latest recorded sale in the period is below its
immediately preceding recorded sale, with same-day resales excluded.

The page should answer "where are the most losses happening?" at a glance and
link into the existing per-suburb losses detail. It must stay fast on the full
NSW dataset (~6.7M canonical sales), so the ranking is materialised rather than
computed per request.

## Architecture Decisions

- **Route:** `GET /suburbs/most-losses`. A request to `/suburbs/losses` without
  a suburb redirects here instead of showing the current "choose a suburb"
  warning.
- **Materialised ranking:** store one row per suburb/state in a new
  `suburb_loss_stats` table for the `2y` period. A page request then reads a few
  thousand rows instead of running window functions over the whole sale set.
- **Consistent loss definition:** reuse the logic in
  `GetSuburbLossSummary`/`GetSuburbLossTypeSummary` — `LAG` over sales per
  canonical property, `ROW_NUMBER` to pick the latest sale, then require the
  latest sale date to fall inside the period.
- **Ranking modes:** default by loss count; optional "Highest loss rate" and
  "Largest average loss" tabs. Rate ranking requires at least 20 resales so a
  1-resale suburb cannot own the top spot. Average loss is the mean percentage
  drop from the previous sale price, not a dollar total.
- **Amounts are stored as positive magnitudes.** Existing loss SQL returns
  negative values; the materialised table stores `total_loss`, `average_loss`
  and `largest_loss` as positive numbers so sorting and display are simpler.
- **Data-quality signal:** store a `flagged_losses` count for resales more than
  80% below the previous price, matching the "Check data" flag on the losses
  page. The first version displays the count in the methodology note rather
  than excluding it, so the ranking stays consistent with the detail page.
- **Fixed 2-year snapshot:** the table is rebuilt when stats are rebuilt
  (import, migrate, startup when empty). `updated_at` is shown on the page. The
  sliding window can lag the current date by the refresh interval; if we later
  want 1/5/10-year leaderboards or a daily-exact window, switch to monthly
  suburb aggregates and sum the relevant months.

## Data Model

```sql
CREATE TABLE IF NOT EXISTS suburb_loss_stats (
    suburb          TEXT NOT NULL,
    state           TEXT NOT NULL,
    period          TEXT NOT NULL,          -- '2y' for now
    resales         INTEGER NOT NULL,       -- latest-sale resales in the period
    losses          INTEGER NOT NULL,       -- resales below previous price
    total_loss      INTEGER NOT NULL,       -- positive AUD magnitude
    average_loss    INTEGER NOT NULL,
    average_loss_percent REAL NOT NULL,
    largest_loss    INTEGER NOT NULL,
    flagged_losses  INTEGER NOT NULL,       -- >80% drop, same flag as losses page
    updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (suburb, state, period)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_suburb_loss_stats_rank
    ON suburb_loss_stats(period, losses DESC, total_loss DESC);
```

Rows with `losses = 0` are retained so the summary cards can use one table and
so `RefreshSuburbLossStat` can keep a zero row instead of deleting.

### Refresh query shape

```sql
WITH ordered AS (
    SELECT
        cs.canonical_pid, cs.suburb, cs.state, cs.sale_id,
        cs.settlement_date, cs.sale_price,
        LAG(cs.sale_price) OVER w AS previous_price,
        LAG(cs.settlement_date) OVER w AS previous_date,
        ROW_NUMBER() OVER (
            PARTITION BY cs.canonical_pid
            ORDER BY cs.settlement_date DESC, cs.sale_id DESC
        ) AS rn
    FROM canonical_sales cs
    WINDOW w AS (
        PARTITION BY cs.canonical_pid
        ORDER BY cs.settlement_date ASC, cs.sale_id ASC
    )
),
losses AS (
    SELECT *
    FROM ordered
    WHERE rn = 1
      AND previous_price IS NOT NULL
      AND previous_date < settlement_date
      AND settlement_date >= @cutoff
)
SELECT suburb, state, ...
FROM losses
WHERE sale_price < previous_price
GROUP BY suburb, state;
```

This is a full-table window over `canonical_sales`. It belongs in the stats
rebuild path, not in a request handler. Measure it against a copy of the
production database before choosing the background-build timeout.

## Page Design

```
Home > Suburbs > Suburbs with the most losses

Suburbs with the most losses
Latest recorded sale below the previous recorded sale, last 2 years.
Analytics updated 3 Nov 2026

[ 1,234 loss-making resales ] [ 20.0% average loss ] [ 812 suburbs affected ]

Most losses | Highest loss rate | Largest average loss
Rate rankings include suburbs with at least 20 resales.

[table on tablet/desktop]                     [cards on mobile]
Rank  Suburb          Losses  Resales  Rate   Total loss  Actions
1     SYDNEY, NSW     384     2,889    13.3%  $866.8M     View losses / Trends
...
```

Table columns: Rank, Suburb, State, Losses, Resales, Loss rate, Average loss %,
Largest loss, Actions. Suburb links to `/suburbs`, "View losses"
links to `/suburbs/losses?suburb=…&state=…&range=2&sort=amount`.

Mobile: one card per suburb showing suburb/state, `losses of resales (rate)`,
total loss, and two compact actions. Do not make a 10-column table scroll on a
phone.

States:

- Stats table empty/building: info notification, same wording as the suburb
  page's "analytics being prepared" state.
- No losses in the period: friendly empty state with link to all suburbs.
- Rate tab with too few qualifying suburbs: note the 20-resale threshold.

## Open Questions

1. Should the leaderboard default to **count**, **rate**, or **total loss**?
   Plan assumes count, with tabs for the other two.
2. Are suspected data errors (>80% drops) acceptable in the main ranking? Plan
   assumes yes for consistency, with a visible count and methodology note; a
   later "Exclude flagged" filter is the follow-up.
3. Is a weekly/import-time snapshot of "last 2 years" acceptable, or is an
   exact rolling window required? Plan assumes snapshot + `updated_at`.

## Task List

### Phase 1: Data foundation

- [x] Task 1: Add `suburb_loss_stats` schema and full refresh
- [x] Task 2: Add single-suburb refresh for new sale submissions
- [x] Task 3: Add sqlc queries for ranking, count and summary

**Checkpoint: Data foundation**
- [x] Refresh produces correct resales/losses/total/rate/flagged values for a
      seeded multi-suburb fixture
- [x] Tests pass: `go test -count=1 -tags sqlite_fts5 ./internal/database`

### Phase 2: Page slice

- [x] Task 4: Add `/suburbs/most-losses` handler and desktop table page
- [x] Task 5: Add mobile card layout, summary cards and methodology note
- [x] Task 6: Wire refresh into migrate/ingest/importer/startup and readyz

**Checkpoint: Page slice**
- [x] Page ranks seeded suburbs correctly by count/rate/total
- [x] Pagination and empty/building states work
- [x] Mobile renders cards without horizontal overflow
- [x] Tests pass: `go test -count=1 -tags sqlite_fts5 ./...`

### Phase 3: Discoverability and delivery

- [x] Task 7: Add links from suburb trends and per-suburb losses, and redirect
      `/suburbs/losses` with no suburb to the ranking
- [x] Task 8: Measure refresh/page performance on production-sized data and
      deploy

**Checkpoint: Complete**
- [x] Production page returns in well under 1s warm
- [x] Refresh duration recorded and within the stats build budget
- [x] Deployed and verified on mobile and desktop

## Implementation Results

- Full materialised refresh measured on the production database: **18.75s**
  for the window/aggregate and **~18s** for the server background build.
- Production table contains **3,555 suburb/state rows**, **16,693**
  loss-making resales and **$6,769,070,058** total nominal loss.
- Warm `/suburbs/most-losses` response: **~5ms** locally, well under the 1s
  target.
- Deployed via the spot playbook; mobile (390px) and desktop (1280px) verified.
