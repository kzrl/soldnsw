-- name: SearchPropertiesByAddress :many
SELECT * FROM gnaf_properties
WHERE suburb = UPPER(@suburb) AND street_name LIKE UPPER(@street_name) || '%'
LIMIT 50;

-- name: SearchPropertiesBySuburb :many
SELECT * FROM gnaf_properties
WHERE suburb = UPPER(@suburb)
LIMIT 50;

-- name: SearchPropertiesFlexible :many
WITH latest AS (
    SELECT canonical_pid, settlement_date AS latest_sale_date, sale_price AS latest_sale_price,
           ROW_NUMBER() OVER (
               PARTITION BY canonical_pid
               ORDER BY settlement_date DESC, sale_id DESC
           ) AS rn
    FROM canonical_sales
)
SELECT p.* FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
LEFT JOIN latest l ON l.canonical_pid = p.gnaf_pid AND l.rn = 1
CROSS JOIN (
    SELECT CAST(@sort AS TEXT) AS sort_key, CAST(@number_exact AS TEXT) AS number_exact_key
) sort_params
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND (p.suburb = UPPER(@suburb) OR @suburb = '')
  AND (p.street_name LIKE @street_pattern OR @street_pattern = '%')
  AND (p.state = UPPER(@state) OR @state = '')
  AND (@postcode = '' OR p.postcode = @postcode)
  AND (
      @number_pattern = '%'
      OR UPPER(REPLACE(p.street_number, ' ', '')) LIKE @number_pattern
  )
  AND (
      @dwelling_type = ''
      OR p.dwelling_type = @dwelling_type
      OR (@dwelling_type = 'other' AND p.dwelling_type = 'unknown')
  )
  AND (CAST(@only_with_sales AS INTEGER) = 0 OR l.canonical_pid IS NOT NULL)
  AND (CAST(@sort AS TEXT) = '' OR TRUE)
  AND (CAST(@number_exact AS TEXT) = '' OR TRUE)
ORDER BY
  CASE
    WHEN sort_params.number_exact_key <> '' AND UPPER(REPLACE(p.street_number, ' ', '')) = sort_params.number_exact_key THEN 0
    ELSE 1
  END,
  CASE WHEN sort_params.sort_key = 'latest' THEN l.latest_sale_date END DESC,
  CASE WHEN sort_params.sort_key = 'price' THEN l.latest_sale_price END DESC,
  p.street_name,
  p.street_number
LIMIT 50;

-- name: GetSuburbSalesBySuburb :many
SELECT
    cs.sale_id,
    cs.canonical_pid AS gnaf_pid,
    cs.settlement_date,
    cs.sale_price,
    cs.source_type,
    p.street_number,
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode
FROM canonical_sales cs
JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
ORDER BY cs.settlement_date DESC, cs.sale_id DESC
LIMIT @page_limit OFFSET @page_offset;

-- name: GetSuburbSalesCount :one
SELECT COUNT(*)
FROM canonical_sales
WHERE suburb = UPPER(@suburb) AND state = UPPER(@state);

-- name: GetSuburbMonthlyAverages :many
SELECT
    substr(cs.settlement_date, 1, 7) AS month,
    CAST(AVG(cs.sale_price) AS INTEGER) AS avg_price,
    COUNT(*) AS sales_count
FROM canonical_sales cs
WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
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
        cs.sale_price,
        ROW_NUMBER() OVER (ORDER BY cs.sale_price) AS rn,
        COUNT(*) OVER () AS cnt
    FROM canonical_sales cs
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
      AND cs.settlement_date >= @cutoff
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
    cs.sale_id,
    cs.canonical_pid AS gnaf_pid,
    cs.settlement_date,
    cs.sale_price,
    cs.source_type,
    p.street_number,
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode
FROM canonical_sales cs
JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
ORDER BY cs.settlement_date DESC, cs.sale_id DESC
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
SELECT COUNT(*) FROM canonical_sales;

-- name: GetTopSuburbs :many
SELECT suburb, state, sales_count
FROM suburb_stats
WHERE sales_count > 0
ORDER BY sales_count DESC, suburb ASC
LIMIT ?;

-- name: GetSuburbLossSummary :one
WITH base AS (
    SELECT
        cs.canonical_pid AS property_key,
        cs.canonical_pid AS gnaf_pid,
        p.street_number,
        p.street_name,
        p.street_type,
        p.postcode,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type,
        cs.sale_id,
        cs.settlement_date,
        cs.sale_price,
        cs.source_type
    FROM canonical_sales cs
    JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
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
        cs.canonical_pid AS property_key,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type,
        cs.sale_id,
        cs.settlement_date,
        cs.sale_price
    FROM canonical_sales cs
    JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
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
        cs.canonical_pid AS property_key,
        cs.canonical_pid AS gnaf_pid,
        cs.suburb,
        cs.state,
        p.street_number,
        p.street_name,
        p.street_type,
        p.postcode,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type,
        cs.sale_id,
        cs.settlement_date,
        cs.sale_price,
        cs.source_type
    FROM canonical_sales cs
    JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
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
), losses AS (
    SELECT
        gnaf_pid,
        suburb,
        state,
        street_number,
        street_name,
        street_type,
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
)
SELECT
    l.gnaf_pid,
    l.suburb,
    l.state,
    l.street_number,
    l.street_name,
    l.street_type,
    l.postcode,
    l.dwelling_type,
    l.sale_id,
    l.settlement_date,
    l.sale_price,
    l.previous_date,
    l.previous_price,
    l.loss_amount,
    l.loss_percent,
    l.holding_days
FROM losses l
CROSS JOIN (
    SELECT CAST(@sort AS TEXT) AS sort_key, CAST(@dwelling_type AS TEXT) AS dwelling_filter
) params
WHERE params.dwelling_filter = ''
   OR l.dwelling_type = params.dwelling_filter
   OR (params.dwelling_filter = 'other' AND l.dwelling_type = 'unknown')
ORDER BY
    CASE WHEN params.sort_key = 'percent' THEN l.loss_percent END ASC,
    CASE WHEN params.sort_key = 'date' THEN l.settlement_date END DESC,
    l.loss_amount ASC,
    l.settlement_date DESC
LIMIT @row_limit OFFSET @row_offset;

-- name: CountSuburbLosses :one
WITH base AS (
    SELECT
        cs.canonical_pid AS property_key,
        cs.settlement_date,
        cs.sale_price,
        cs.sale_id,
        CAST(COALESCE(NULLIF(p.dwelling_type, ''), 'unknown') AS TEXT) AS dwelling_type
    FROM canonical_sales cs
    JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
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
SELECT COUNT(*)
FROM ordered
WHERE rn = 1
    AND previous_price IS NOT NULL
    AND previous_date < settlement_date
    AND sale_price < previous_price
    AND settlement_date >= @cutoff
    AND (CAST(@dwelling_type AS TEXT) = ''
         OR dwelling_type = CAST(@dwelling_type AS TEXT)
         OR (CAST(@dwelling_type AS TEXT) = 'other' AND dwelling_type = 'unknown'));

-- name: GetSuburbExtremeSales :many
SELECT kind, sale_id, gnaf_pid, settlement_date, sale_price, street_number, street_name, street_type
FROM (
    SELECT 'max' AS kind, cs.sale_id, cs.canonical_pid AS gnaf_pid, cs.settlement_date, cs.sale_price,
           p.street_number, p.street_name, p.street_type
    FROM canonical_sales cs
    JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
      AND cs.settlement_date >= @cutoff
    ORDER BY cs.sale_price DESC, cs.settlement_date DESC, cs.sale_id DESC
    LIMIT 1
)
UNION ALL
SELECT kind, sale_id, gnaf_pid, settlement_date, sale_price, street_number, street_name, street_type
FROM (
    SELECT 'min' AS kind, cs.sale_id, cs.canonical_pid AS gnaf_pid, cs.settlement_date, cs.sale_price,
           p.street_number, p.street_name, p.street_type
    FROM canonical_sales cs
    JOIN gnaf_properties p ON p.gnaf_pid = cs.canonical_pid
    WHERE cs.suburb = UPPER(@suburb) AND cs.state = UPPER(@state)
      AND cs.settlement_date >= @cutoff
    ORDER BY cs.sale_price ASC, cs.settlement_date DESC, cs.sale_id DESC
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

-- name: SearchStreetGroups :many
SELECT
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode,
    COUNT(*) AS property_count
FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.street_name LIKE @street_pattern
  AND (CAST(@exact_street AS TEXT) = '' OR TRUE)
  AND (CAST(@street_type AS TEXT) = '' OR p.street_type = UPPER(CAST(@street_type AS TEXT)))
  AND (CAST(@suburb AS TEXT) = '' OR p.suburb = UPPER(CAST(@suburb AS TEXT)))
  AND (CAST(@state AS TEXT) = '' OR p.state = UPPER(CAST(@state AS TEXT)))
  AND (CAST(@postcode AS TEXT) = '' OR p.postcode = CAST(@postcode AS TEXT))
GROUP BY p.street_name, p.street_type, p.suburb, p.state, p.postcode
ORDER BY
  CASE WHEN p.street_name = UPPER(CAST(@exact_street AS TEXT)) AND CAST(@exact_street AS TEXT) <> '' THEN 0 ELSE 1 END,
  property_count DESC,
  p.street_name ASC,
  p.suburb ASC,
  p.postcode ASC
LIMIT @row_limit OFFSET @row_offset;

-- name: CountStreetGroups :one
SELECT COUNT(*) AS group_count FROM (
    SELECT p.street_name, p.street_type, p.suburb, p.state, p.postcode
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
      AND p.street_name LIKE @street_pattern
      AND (CAST(@street_type AS TEXT) = '' OR p.street_type = UPPER(CAST(@street_type AS TEXT)))
      AND (CAST(@suburb AS TEXT) = '' OR p.suburb = UPPER(CAST(@suburb AS TEXT)))
      AND (CAST(@state AS TEXT) = '' OR p.state = UPPER(CAST(@state AS TEXT)))
      AND (CAST(@postcode AS TEXT) = '' OR p.postcode = CAST(@postcode AS TEXT))
    GROUP BY p.street_name, p.street_type, p.suburb, p.state, p.postcode
);

-- name: CountStreetProperties :one
SELECT COUNT(*)
FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.street_name = UPPER(@street_name)
  AND p.street_type = UPPER(@street_type)
  AND p.suburb = UPPER(@suburb)
  AND p.state = UPPER(@state)
  AND p.postcode = @postcode
  AND (CAST(@dwelling_type AS TEXT) = ''
       OR p.dwelling_type = CAST(@dwelling_type AS TEXT)
       OR (CAST(@dwelling_type AS TEXT) = 'other' AND p.dwelling_type = 'unknown'))
  AND (CAST(@only_with_sales AS INTEGER) = 0
       OR EXISTS (SELECT 1 FROM canonical_sales cs WHERE cs.canonical_pid = p.gnaf_pid));

-- name: ListStreetProperties :many
WITH latest AS (
    SELECT canonical_pid, settlement_date AS latest_sale_date, sale_price AS latest_sale_price,
           ROW_NUMBER() OVER (
               PARTITION BY canonical_pid
               ORDER BY settlement_date DESC, sale_id DESC
           ) AS rn
    FROM canonical_sales
)
SELECT p.* FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
LEFT JOIN latest l ON l.canonical_pid = p.gnaf_pid AND l.rn = 1
CROSS JOIN (SELECT CAST(@sort AS TEXT) AS sort_key) sort_params
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.street_name = UPPER(@street_name)
  AND p.street_type = UPPER(@street_type)
  AND p.suburb = UPPER(@suburb)
  AND p.state = UPPER(@state)
  AND p.postcode = @postcode
  AND (CAST(@dwelling_type AS TEXT) = ''
       OR p.dwelling_type = CAST(@dwelling_type AS TEXT)
       OR (CAST(@dwelling_type AS TEXT) = 'other' AND p.dwelling_type = 'unknown'))
  AND (CAST(@only_with_sales AS INTEGER) = 0 OR l.canonical_pid IS NOT NULL)
  AND (CAST(@sort AS TEXT) = '' OR TRUE)
ORDER BY
  CASE WHEN sort_params.sort_key = 'latest' THEN l.latest_sale_date END DESC,
  CASE WHEN sort_params.sort_key = 'price' THEN l.latest_sale_price END DESC,
  CASE WHEN p.street_number IS NULL OR TRIM(p.street_number) = '' THEN 1 ELSE 0 END,
  CAST(REPLACE(p.street_number, ' ', '') AS INTEGER),
  p.street_number,
  p.gnaf_pid
LIMIT @row_limit OFFSET @row_offset;

-- name: CountPostcodeProperties :one
SELECT COUNT(*)
FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.postcode = @postcode
  AND (CAST(@dwelling_type AS TEXT) = ''
       OR p.dwelling_type = CAST(@dwelling_type AS TEXT)
       OR (CAST(@dwelling_type AS TEXT) = 'other' AND p.dwelling_type = 'unknown'))
  AND (CAST(@only_with_sales AS INTEGER) = 0
       OR EXISTS (SELECT 1 FROM canonical_sales cs WHERE cs.canonical_pid = p.gnaf_pid));

-- name: ListPostcodeProperties :many
WITH latest AS (
    SELECT canonical_pid, settlement_date AS latest_sale_date, sale_price AS latest_sale_price,
           ROW_NUMBER() OVER (
               PARTITION BY canonical_pid
               ORDER BY settlement_date DESC, sale_id DESC
           ) AS rn
    FROM canonical_sales
)
SELECT p.* FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
LEFT JOIN latest l ON l.canonical_pid = p.gnaf_pid AND l.rn = 1
CROSS JOIN (SELECT CAST(@sort AS TEXT) AS sort_key) sort_params
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.postcode = @postcode
  AND (CAST(@dwelling_type AS TEXT) = ''
       OR p.dwelling_type = CAST(@dwelling_type AS TEXT)
       OR (CAST(@dwelling_type AS TEXT) = 'other' AND p.dwelling_type = 'unknown'))
  AND (CAST(@only_with_sales AS INTEGER) = 0 OR l.canonical_pid IS NOT NULL)
  AND (CAST(@sort AS TEXT) = '' OR TRUE)
ORDER BY
  CASE WHEN sort_params.sort_key = 'latest' THEN l.latest_sale_date END DESC,
  CASE WHEN sort_params.sort_key = 'price' THEN l.latest_sale_price END DESC,
  p.suburb ASC,
  p.street_name ASC,
  CASE WHEN p.street_number IS NULL OR TRIM(p.street_number) = '' THEN 1 ELSE 0 END,
  CAST(REPLACE(p.street_number, ' ', '') AS INTEGER),
  p.street_number,
  p.gnaf_pid
LIMIT @row_limit OFFSET @row_offset;

-- name: GetPostcodeSummary :one
WITH props AS (
    SELECT p.gnaf_pid, p.suburb, p.state
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE p.postcode = @postcode
      AND (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
),
sales AS (
    SELECT cs.sale_price, cs.settlement_date
    FROM canonical_sales cs
    WHERE cs.postcode = @postcode
),
filtered AS (
    SELECT
        sale_price,
        ROW_NUMBER() OVER (ORDER BY sale_price) AS rn,
        COUNT(*) OVER () AS cnt
    FROM sales
),
med AS (
    SELECT AVG(CASE
        WHEN rn IN ((cnt + 1) / 2, (cnt + 2) / 2) THEN sale_price
    END) AS median_price
    FROM filtered
)
SELECT
    (SELECT COUNT(*) FROM props) AS property_count,
    (SELECT COUNT(*) FROM sales) AS sales_count,
    CAST(COALESCE((SELECT AVG(sale_price) FROM sales), 0) AS INTEGER) AS avg_price,
    CAST(COALESCE(med.median_price, 0) AS INTEGER) AS median_price,
    CAST(COALESCE((SELECT MAX(sale_price) FROM sales), 0) AS INTEGER) AS max_price,
    CAST(COALESCE((SELECT MIN(sale_price) FROM sales), 0) AS INTEGER) AS min_price,
    CAST(COALESCE((SELECT MAX(settlement_date) FROM sales), '') AS TEXT) AS latest_sale_date,
    (SELECT COUNT(*) FROM (SELECT DISTINCT suburb, state FROM props)) AS suburb_count
FROM med;

-- name: GetPostcodeSuburbs :many
WITH props AS (
    SELECT p.gnaf_pid, p.suburb, p.state
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE p.postcode = @postcode
      AND (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
),
sales AS (
    SELECT cs.canonical_pid, cs.sale_price
    FROM canonical_sales cs
    WHERE cs.postcode = @postcode
)
SELECT
    pr.suburb,
    pr.state,
    COUNT(DISTINCT pr.gnaf_pid) AS property_count,
    COUNT(s.sale_price) AS sales_count,
    CAST(COALESCE(AVG(s.sale_price), 0) AS INTEGER) AS avg_price
FROM props pr
LEFT JOIN sales s ON s.canonical_pid = pr.gnaf_pid
GROUP BY pr.suburb, pr.state
ORDER BY property_count DESC, pr.suburb ASC, pr.state ASC;

-- name: GetStreetSummary :one
WITH props AS (
    SELECT p.gnaf_pid
    FROM gnaf_properties p
    LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
    WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
      AND p.street_name = UPPER(@street_name)
      AND p.street_type = UPPER(@street_type)
      AND p.suburb = UPPER(@suburb)
      AND p.state = UPPER(@state)
      AND p.postcode = @postcode
),
sales AS (
    SELECT cs.sale_price, cs.settlement_date
    FROM canonical_sales cs
    JOIN props pr ON pr.gnaf_pid = cs.canonical_pid
),
filtered AS (
    SELECT
        sale_price,
        ROW_NUMBER() OVER (ORDER BY sale_price) AS rn,
        COUNT(*) OVER () AS cnt
    FROM sales
),
med AS (
    SELECT AVG(CASE
        WHEN rn IN ((cnt + 1) / 2, (cnt + 2) / 2) THEN sale_price
    END) AS median_price
    FROM filtered
)
SELECT
    (SELECT COUNT(*) FROM props) AS property_count,
    (SELECT COUNT(*) FROM sales) AS sales_count,
    CAST(COALESCE((SELECT AVG(sale_price) FROM sales), 0) AS INTEGER) AS avg_price,
    CAST(COALESCE(med.median_price, 0) AS INTEGER) AS median_price,
    CAST(COALESCE((SELECT MAX(sale_price) FROM sales), 0) AS INTEGER) AS max_price,
    CAST(COALESCE((SELECT MIN(sale_price) FROM sales), 0) AS INTEGER) AS min_price,
    CAST(COALESCE((SELECT MAX(settlement_date) FROM sales), '') AS TEXT) AS latest_sale_date
FROM med;

-- name: SuggestSuburbs :many
SELECT suburb, state, property_count, sales_count
FROM suburb_stats
WHERE suburb LIKE @suburb_pattern
  AND (CAST(@exact_suburb AS TEXT) = '' OR TRUE)
ORDER BY
  CASE WHEN suburb = UPPER(CAST(@exact_suburb AS TEXT)) THEN 0 ELSE 1 END,
  sales_count DESC,
  suburb ASC,
  state ASC
LIMIT @row_limit;

-- name: SuggestStreetGroups :many
SELECT
    p.street_name,
    p.street_type,
    p.suburb,
    p.state,
    p.postcode,
    COUNT(*) AS property_count
FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.street_name LIKE @street_pattern
GROUP BY p.street_name, p.street_type, p.suburb, p.state, p.postcode
ORDER BY property_count DESC, p.street_name ASC, p.suburb ASC, p.postcode ASC
LIMIT @row_limit;

-- name: SuggestPostcodes :many
SELECT
    p.postcode,
    COUNT(*) AS property_count,
    MIN(p.suburb) AS sample_suburb
FROM gnaf_properties p
LEFT JOIN property_links pl ON pl.gnaf_pid = p.gnaf_pid
WHERE (pl.canonical_pid IS NULL OR pl.canonical_pid = p.gnaf_pid)
  AND p.postcode LIKE @postcode_pattern
GROUP BY p.postcode
ORDER BY p.postcode ASC
LIMIT @row_limit;

-- name: GetLastSuccessfulImport :one
SELECT substr(finished_at, 1, 10) AS finished_date
FROM import_runs
WHERE status IN ('success', 'partial')
ORDER BY finished_at DESC
LIMIT 1;

-- name: SuggestStreetStats :many
SELECT street_name, street_type, suburb, state, postcode, property_count
FROM street_stats
WHERE street_name LIKE @street_pattern
  AND (CAST(@exact_street AS TEXT) = '' OR TRUE)
ORDER BY
  CASE WHEN street_name = UPPER(CAST(@exact_street AS TEXT)) THEN 0 ELSE 1 END,
  property_count DESC,
  street_name ASC,
  suburb ASC,
  postcode ASC
LIMIT @row_limit;

-- name: SuggestPostcodeStats :many
SELECT postcode, property_count, sample_suburb
FROM postcode_stats
WHERE postcode LIKE @postcode_pattern
  AND (CAST(@exact_postcode AS TEXT) = '' OR TRUE)
ORDER BY
  CASE WHEN postcode = CAST(@exact_postcode AS TEXT) THEN 0 ELSE 1 END,
  postcode ASC
LIMIT @row_limit;
