# The 3.3 GB of raw jsonb in profitzon-command

Read-only investigation, 2026-09-28. Nothing here has been changed. This is for
the profitzon-command application team to act on, because every lever is in the
writer, not in the platform.

## Where the space is

`profitzon-command` is 9.5 GB, and it splits:

| | Size |
|---|---|
| heap | 4089 MB |
| indexes | 2106 MB |
| **TOAST** | **3258 MB** |

TOAST is out-of-line storage for large values. Nearly all of it is two tables:

| Table | Column(s) | TOAST |
|---|---|---|
| `NovaProductMetric` | `raw`, `reimbursements` (jsonb) | 1866 MB |
| `fact_listing_health_daily_y2026m09` | `extrasJson` (jsonb) | 1334 MB |

## What is actually in there

Measured on a 2000 to 3000 row sample of `NovaProductMetric`:

| | |
|---|---|
| Average stored size of `raw` | 3286 bytes |
| Keys per row | **303** |
| Total length of the key NAMES alone, per row | **7267 bytes** |
| Key names as a share of stored size | 221% |
| Distinct whole payloads (3000 sampled) | 3000 |
| `reimbursements` average size | 70 bytes |
| Distinct `reimbursements` payloads (3000 sampled) | **1099** |

Two things follow from that, and they point in different directions.

**The key names are the payload.** Every row stores 303 key names totalling
7267 bytes, and they are identical in every row. The value is compressed down to
3286 bytes precisely because pglz is very good at that repetition, which is also
why trying a different compression codec is pointless: lz4 was measured at 0.1%
*worse* on this data, and the reason is that the win is already taken.

**Whole-payload deduplication will not help `raw`.** All 3000 sampled payloads are
distinct, so content-hashing the blob saves nothing. It *will* help
`reimbursements`, where 1099 distinct values cover 3000 rows, 63% duplicates, but
that column averages 70 bytes so the ceiling is small.

## The finding that matters

**44 of the keys in `raw` are already typed columns on the same table.** Verified
by matching key names against `pg_attribute`:

```
active_subscriptions, amazon_fees, buy_box_percentage, child_asin, clicks, cogs,
cog_value_per_unit, cost_of_goods, currency, fbm_cost, giftwrap_sales,
gross_profit, impressions, is_fbm, last_order_updated_date, marketplace_id,
marketplace_name, metric_date, net_profit, net_sales, orders, other_sales,
page_views_b2c, parent_asin, ppc_orders, ppc_sales, ppc_spend, ppc_units,
product_sales, profit_before_ads, refunded_product_sales, refunded_sales,
returned_units, returned_units_sellable, sales_deductions, seller_name,
seller_partner_id, sessions, shipping_sales, sku, ss_sales, ss_units, units,
units_unsellable
```

So the row is stored twice: once as typed columns, and again inside `raw`. The
by-cost key ranking is led by exactly these duplicates - `marketplace_name`,
`seller_name`, `org_key`, `seller_partner_id`, `marketplace_id`, `sku`,
`child_asin`, `parent_asin` are the top entries by bytes in the sample.

## Options, in order of payoff per unit of risk

1. **Stop writing the 44 keys that are already columns.** The data is not lost,
   it is one join or one column reference away. This is a change in whatever
   builds the payload, it needs no migration, and it shrinks every new row. Old
   rows shrink when rewritten, or with a one-off
   `UPDATE ... SET raw = raw - '{key1,key2,...}'::text[]` run per partition
   off-peak. Best first move.
2. **Put a retention on the payload, separate from the metrics.** The numeric
   columns are what dashboards read; `raw` looks like an audit copy of the API
   response. If it only matters for recent reconciliation, null it out beyond N
   days and keep the metrics forever. One statement per partition, and it is the
   only option that bounds this permanently rather than shrinking it once.
3. **Move `raw` out of the hot table.** A side table keyed by the same identity,
   or object storage keyed by content hash, takes the payload out of
   `NovaProductMetric`'s TOAST. Same bytes, but they stop inflating the table that
   every query and every basebackup touches, and they become independently
   archivable.
4. **Content-hash `reimbursements`.** 63% of values are duplicates. A small
   `dim_reimbursement(hash, payload)` plus a reference removes that, but the
   column is 70 bytes so keep expectations proportionate.

## Ruled out, with measurements

- **A different compression codec.** lz4 versus pglz on this data measured **0.1%
  worse**. The repetition that makes the payload look compressible is already
  being compressed.
- **Whole-blob deduplication of `raw`.** 3000 of 3000 sampled payloads are
  distinct.
- **Anything platform-side.** Postgres has no cross-row compression dictionary,
  and `attstorage` is already `x` (extended, compressed then out-of-line) on every
  one of these columns. There is no setting left to change.

## How to re-run these measurements

```sql
-- size, key count, and what the key names cost
WITH s AS (SELECT raw FROM "NovaProductMetric" WHERE raw IS NOT NULL LIMIT 2000)
SELECT count(*), round(avg(nkeys)) AS keys_per_row,
       pg_size_pretty(avg(blob)::bigint) AS avg_blob,
       pg_size_pretty(avg(keybytes)::bigint) AS avg_key_names
FROM (SELECT (SELECT count(*) FROM jsonb_object_keys(raw)) AS nkeys,
             pg_column_size(raw) AS blob,
             (SELECT sum(length(k)) FROM jsonb_object_keys(raw) k) AS keybytes
      FROM s) x;

-- which keys duplicate a real column
SELECT DISTINCT k FROM (SELECT raw FROM "NovaProductMetric" WHERE raw IS NOT NULL LIMIT 200) t,
     jsonb_object_keys(t.raw) k
WHERE lower(replace(k,'_','')) IN (
  SELECT lower(replace(attname,'_','')) FROM pg_attribute
  WHERE attrelid='public."NovaProductMetric"'::regclass AND attnum>0 AND NOT attisdropped);
```

`fact_listing_health_daily_y2026m09.extrasJson` (1334 MB) was not sampled in the
same depth. Run the same two queries against it before deciding anything; the name
suggests the same "everything we got, kept just in case" shape.
