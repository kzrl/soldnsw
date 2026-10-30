-- name: SearchPropertiesByAddress :many
SELECT * FROM gnaf_properties 
WHERE suburb = UPPER(@suburb) AND street_name LIKE UPPER(@street_name) || '%'
LIMIT 50;

-- name: SearchPropertiesBySuburb :many
SELECT * FROM gnaf_properties 
WHERE suburb = UPPER(@suburb)
LIMIT 50;

-- name: SearchPropertiesFlexible :many
SELECT p.* FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND (p.suburb = UPPER(@suburb) OR @suburb = '')
  AND (p.street_name LIKE @street_pattern OR @street_pattern = '%')
  AND (p.state = UPPER(@state) OR @state = '')
  AND (@postcode = '' OR p.postcode = @postcode)
  AND (
      @number_pattern = '%'
      OR UPPER(REPLACE(p.street_number, ' ', '')) LIKE @number_pattern
  )
  AND (@number_exact = '' OR @number_exact <> '')
  AND (
      @dwelling_type = ''
      OR p.dwelling_type = @dwelling_type
      OR (@dwelling_type = 'other' AND p.dwelling_type = 'unknown')
  )
ORDER BY
  CASE
    WHEN ?6 <> '' AND UPPER(REPLACE(p.street_number, ' ', '')) = ?6 THEN 0
    ELSE 1
  END,
  p.street_name,
  p.street_number
LIMIT 50;

-- name: GetSuburbSalesBySuburb :many
SELECT 
    s.sale_id,
    COALESCE(pl.canonical_pid, s.gnaf_pid) AS gnaf_pid,
    s.settlement_date,
    s.sale_price,
    s.source_type,
    p.street_number,
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode
FROM property_sales s
JOIN gnaf_properties p ON s.gnaf_pid = p.gnaf_pid
LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
ORDER BY s.settlement_date DESC, s.sale_id DESC
LIMIT @page_limit OFFSET @page_offset;

-- name: GetSuburbSalesCount :one
SELECT COUNT(*)
FROM property_sales s
JOIN gnaf_properties p ON s.gnaf_pid = p.gnaf_pid
WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state);

-- name: GetSuburbMonthlyAverages :many
SELECT
    substr(s.settlement_date, 1, 7) AS month,
    CAST(AVG(s.sale_price) AS INTEGER) AS avg_price,
    COUNT(*) AS sales_count
FROM property_sales s
JOIN gnaf_properties p ON s.gnaf_pid = p.gnaf_pid
WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
GROUP BY month
ORDER BY month ASC;

-- name: GetPropertyByPID :one
SELECT * FROM gnaf_properties 
WHERE gnaf_pid = ? LIMIT 1;

-- name: GetPropertyByBasePID :one
SELECT * FROM gnaf_properties
WHERE base_pid = ?
ORDER BY gnaf_pid
LIMIT 1;

-- name: GetSalesHistoryByPID :many
SELECT * FROM property_sales 
WHERE gnaf_pid = ? 
ORDER BY settlement_date DESC;

-- name: GetSalesHistoryByCanonical :many
SELECT s.* FROM property_sales s
JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
WHERE pl.canonical_pid = ?
ORDER BY s.settlement_date DESC, s.sale_id DESC;

-- name: GetSuburbAnalytics :many
WITH filtered AS (
    SELECT
        s.sale_price,
        ROW_NUMBER() OVER (ORDER BY s.sale_price) AS rn,
        COUNT(*) OVER () AS cnt
    FROM property_sales s
    JOIN gnaf_properties p ON s.gnaf_pid = p.gnaf_pid
    WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
      AND s.settlement_date >= @cutoff
),
med AS (
    SELECT AVG(CASE
        WHEN rn IN ((cnt + 1) / 2, (cnt + 2) / 2) THEN sale_price
    END) AS median_price
    FROM filtered
)
SELECT
    UPPER(@suburb) AS suburb,
    COUNT(*) AS total_sales,
    AVG(f.sale_price) AS avg_price,
    CAST(COALESCE(med.median_price, 0) AS INTEGER) AS median_price,
    CAST(COALESCE(MAX(f.sale_price), 0) AS INTEGER) AS max_price,
    CAST(COALESCE(MIN(f.sale_price), 0) AS INTEGER) AS min_price
FROM filtered f
CROSS JOIN med;

-- name: InsertGnafProperty :exec
INSERT INTO gnaf_properties (
    gnaf_pid, street_number, street_name, street_type, suburb, state, postcode, latitude, longitude, lot_number, plan_number, property_type, land_area, base_pid, dwelling_type
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(gnaf_pid) DO UPDATE SET
    street_number = excluded.street_number,
    street_name = excluded.street_name,
    street_type = excluded.street_type,
    suburb = excluded.suburb,
    state = excluded.state,
    postcode = excluded.postcode,
    latitude = COALESCE(excluded.latitude, gnaf_properties.latitude),
    longitude = COALESCE(excluded.longitude, gnaf_properties.longitude),
    lot_number = COALESCE(excluded.lot_number, gnaf_properties.lot_number),
    plan_number = COALESCE(excluded.plan_number, gnaf_properties.plan_number),
    property_type = COALESCE(excluded.property_type, gnaf_properties.property_type),
    land_area = COALESCE(excluded.land_area, gnaf_properties.land_area),
    base_pid = COALESCE(excluded.base_pid, gnaf_properties.base_pid),
    dwelling_type = CASE WHEN excluded.dwelling_type <> '' THEN excluded.dwelling_type ELSE gnaf_properties.dwelling_type END;

-- name: InsertPropertySale :exec
INSERT INTO property_sales (
    sale_id, gnaf_pid, settlement_date, sale_price, source_type
) VALUES (?, ?, ?, ?, ?);

-- name: ListRecentSalesWithAddress :many
SELECT 
    s.sale_id,
    COALESCE(pl.canonical_pid, s.gnaf_pid) AS gnaf_pid,
    s.settlement_date,
    s.sale_price,
    s.source_type,
    p.street_number,
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode
FROM property_sales s
JOIN gnaf_properties p ON s.gnaf_pid = p.gnaf_pid
LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
ORDER BY s.settlement_date DESC
LIMIT 25;

-- name: GetDistinctSuburbs :many
SELECT suburb, state, MIN(postcode) AS postcode
FROM gnaf_properties
GROUP BY suburb, state
ORDER BY suburb ASC, state ASC;

-- name: GetSuburbOverviewStats :many
SELECT suburb, state, property_count, sales_count, avg_price, max_price, min_price,
       change_1y, change_5y, change_10y
FROM suburb_stats
ORDER BY sales_count DESC, suburb ASC;

-- name: GetTotalPropertiesCount :one
SELECT CAST(COALESCE(SUM(property_count), 0) AS INTEGER) FROM suburb_stats;

-- name: GetTotalSalesCount :one
SELECT COUNT(*) FROM property_sales;

-- name: GetTopSuburbs :many
SELECT suburb, state, sales_count
FROM suburb_stats
WHERE sales_count > 0
ORDER BY sales_count DESC, suburb ASC
LIMIT ?;

-- name: GetSuburbLossSummary :one
WITH base AS (
    SELECT
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS property_key,
        p.suburb,
        p.state,
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS gnaf_pid,
        p.street_number,
        p.street_name,
        p.street_type,
        p.postcode,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type,
        s.sale_id,
        s.settlement_date,
        s.sale_price,
        s.source_type
    FROM property_sales s
    JOIN gnaf_properties p ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
    WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
), ordered AS (
    SELECT
        *,
        LAG(sale_price) OVER w AS previous_price,
        LAG(settlement_date) OVER w AS previous_date,
        ROW_NUMBER() OVER (
            PARTITION BY property_key
            ORDER BY settlement_date DESC, sale_id DESC
        ) AS rn
    FROM base
    WINDOW w AS (
        PARTITION BY property_key
        ORDER BY settlement_date ASC, sale_id ASC
    )
)
SELECT
    COUNT(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND settlement_date >= @cutoff
        THEN 1
    END) AS resales,
    COUNT(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN 1
    END) AS losses,
    COALESCE(SUM(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN sale_price - previous_price
        ELSE 0
    END), 0) AS total_loss,
    CAST(COALESCE(AVG(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN sale_price - previous_price
    END), 0) AS INTEGER) AS average_loss,
    COALESCE(MIN(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN sale_price - previous_price
    END), 0) AS largest_loss
FROM ordered;

-- name: GetSuburbLossTypeSummary :many
WITH base AS (
    SELECT
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS property_key,
        p.suburb,
        p.state,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type,
        s.sale_id,
        s.settlement_date,
        s.sale_price
    FROM property_sales s
    JOIN gnaf_properties p ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
    WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
), ordered AS (
    SELECT
        *,
        LAG(sale_price) OVER w AS previous_price,
        LAG(settlement_date) OVER w AS previous_date,
        ROW_NUMBER() OVER (
            PARTITION BY property_key
            ORDER BY settlement_date DESC, sale_id DESC
        ) AS rn
    FROM base
    WINDOW w AS (
        PARTITION BY property_key
        ORDER BY settlement_date ASC, sale_id ASC
    )
)
SELECT
    dwelling_type,
    COUNT(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND settlement_date >= @cutoff
        THEN 1
    END) AS resales,
    COUNT(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN 1
    END) AS losses,
    COALESCE(SUM(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN sale_price - previous_price ELSE 0
    END), 0) AS total_loss,
    CAST(COALESCE(AVG(CASE
        WHEN rn = 1 AND previous_price IS NOT NULL AND previous_date < settlement_date
             AND sale_price < previous_price AND settlement_date >= @cutoff
        THEN sale_price - previous_price
    END), 0) AS INTEGER) AS average_loss
FROM ordered
GROUP BY dwelling_type;

-- name: GetSuburbLosses :many
WITH base AS (
    SELECT
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS property_key,
        p.suburb,
        p.state,
        COALESCE(pl.canonical_pid, p.gnaf_pid) AS gnaf_pid,
        p.street_number,
        p.street_name,
        p.street_type,
        p.postcode,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type,
        s.sale_id,
        s.settlement_date,
        s.sale_price,
        s.source_type
    FROM property_sales s
    JOIN gnaf_properties p ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
    WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
), ordered AS (
    SELECT
        *,
        LAG(sale_price) OVER w AS previous_price,
        LAG(settlement_date) OVER w AS previous_date,
        ROW_NUMBER() OVER (
            PARTITION BY property_key
            ORDER BY settlement_date DESC, sale_id DESC
        ) AS rn
    FROM base
    WINDOW w AS (
        PARTITION BY property_key
        ORDER BY settlement_date ASC, sale_id ASC
    )
)
SELECT
    gnaf_pid,
    street_number,
    street_name,
    street_type,
    suburb,
    state,
    postcode,
    dwelling_type,
    sale_id,
    settlement_date,
    sale_price,
    COALESCE(previous_date, '') AS previous_date,
    COALESCE(previous_price, 0) AS previous_price,
    sale_price - COALESCE(previous_price, 0) AS loss_amount,
    CAST((sale_price - COALESCE(previous_price, 0)) * 100.0 / COALESCE(previous_price, 1) AS REAL) AS loss_percent,
    CAST(julianday(settlement_date) - julianday(previous_date) AS INTEGER) AS holding_days
FROM ordered
WHERE rn = 1
    AND previous_price IS NOT NULL
    AND previous_date < settlement_date
    AND sale_price < previous_price
    AND settlement_date >= @cutoff
ORDER BY sale_price - COALESCE(previous_price, 0) ASC;

-- name: GetSuburbExtremeSales :many
SELECT kind, sale_id, gnaf_pid, settlement_date, sale_price, street_number, street_name, street_type
FROM (
    SELECT 'max' AS kind, s.sale_id, s.gnaf_pid, s.settlement_date, s.sale_price,
           p.street_number, p.street_name, p.street_type
    FROM property_sales s
    JOIN gnaf_properties p ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
    WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
      AND s.settlement_date >= @cutoff
    ORDER BY s.sale_price DESC, s.settlement_date DESC, s.sale_id DESC
    LIMIT 1
)
UNION ALL
SELECT kind, sale_id, gnaf_pid, settlement_date, sale_price, street_number, street_name, street_type
FROM (
    SELECT 'min' AS kind, s.sale_id, s.gnaf_pid, s.settlement_date, s.sale_price,
           p.street_number, p.street_name, p.street_type
    FROM property_sales s
    JOIN gnaf_properties p ON p.gnaf_pid = s.gnaf_pid
    LEFT JOIN property_links pl ON pl.gnaf_pid = s.gnaf_pid
    WHERE p.suburb = UPPER(@suburb) AND p.state = UPPER(@state)
      AND s.settlement_date >= @cutoff
    ORDER BY s.sale_price ASC, s.settlement_date DESC, s.sale_id DESC
    LIMIT 1
);

-- name: CountPropertiesByBasePID :one
SELECT COUNT(*) FROM gnaf_properties WHERE base_pid = ?;

-- name: GetBuildingUnitsByBasePID :many
SELECT
    p.gnaf_pid,
    p.street_number,
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode,
    p.lot_number,
    p.plan_number,
    p.property_type,
    p.land_area,
    COALESCE((SELECT s.settlement_date
       FROM property_sales s
      WHERE s.gnaf_pid = p.gnaf_pid
      ORDER BY s.settlement_date DESC, s.sale_id DESC
      LIMIT 1), '') AS latest_sale_date,
    COALESCE((SELECT s.sale_price
       FROM property_sales s
      WHERE s.gnaf_pid = p.gnaf_pid
      ORDER BY s.settlement_date DESC, s.sale_id DESC
      LIMIT 1), 0) AS latest_sale_price,
    (SELECT COUNT(*) FROM property_sales s2 WHERE s2.gnaf_pid = p.gnaf_pid) AS sales_count
FROM gnaf_properties p
WHERE p.base_pid = ?
ORDER BY p.street_number, p.gnaf_pid;
