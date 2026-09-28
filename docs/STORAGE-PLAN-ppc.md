# Fitting more data in less disk - ppc-profitzon-command

Measured on the live database, not estimated. Sequence follows: reindex
(free, no code change) -> partition rebuild (structural) -> normalize
(largest saving, changes the physical shape under the app).

## Done already: step 1, reindex

| Index | Before | After |
|---|---|---|
| wh_finance_event_default_pkey | 1159 MB | 488 MB |
| ..._store_id_amazon_order_id_idx | 102 MB | 38 MB |
| ..._store_id_posted_date_charge_type_idx | 101 MB | 28 MB |
| 10 partition indexes | 86 MB | 19 MB |
| **Database total** | **2500 MB** | **1619 MB** |

Cause: btree leaf density had fallen to 68% with 50% fragmentation
(327 bytes/row against ~136 freshly built). All rebuilds ran
`REINDEX INDEX CONCURRENTLY` per index name - never on the partitioned
parent, which does not support CONCURRENTLY. Zero invalid indexes remain.

Repeat when density drops again:

```sql
SELECT c.relname, (pgstatindex(c.oid)).avg_leaf_density
FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE pg_relation_size(c.oid) > 50*1024*1024;
-- below ~70% is worth a rebuild
REINDEX INDEX CONCURRENTLY <index_name>;
SELECT count(*) FROM pg_index WHERE NOT indisvalid;  -- must be 0 afterwards
```

## Step 2: rebuild the partitions - DONE (verified 2026-09-28)

No longer needed. Verified directly against the catalog: monthly partitions now
exist continuously from `wh_finance_event_202308` through
`wh_finance_event_202609`, rows are distributed across them, and
`wh_finance_event_default` holds **0 rows**. Retiring a period is now the one
statement this step existed to enable:

```sql
DROP TABLE wh_finance_event_202409;         -- or DETACH, archive, then drop
```

The plan below is kept for the record of what was done and why.

### What it was (historical)

### Original plan


3,711,028 rows covering 2024-09-03 to 2026-02-28 sit in
`wh_finance_event_default`. Monthly partitions only exist from 2026-03,
so everything older fell through to the default. The table is
partitioned in name only for that period.

Fixing it does not shrink the data by itself - it makes old months
*droppable and archivable in one statement* instead of a row-by-row
DELETE, which is what keeps growth bounded from here.

```sql
-- one per month, 2024-09 .. 2026-02
CREATE TABLE wh_finance_event_202409 PARTITION OF wh_finance_event
  FOR VALUES FROM ('2024-09-01') TO ('2024-10-01');
-- ... repeat ...

-- then move rows out of the default partition, month by month
-- (do it in batches, off-peak; each month is an independent statement)
WITH moved AS (
  DELETE FROM wh_finance_event_default
  WHERE posted_date >= '2024-09-01' AND posted_date < '2024-10-01'
  RETURNING *
)
INSERT INTO wh_finance_event SELECT * FROM moved;
```

Afterwards, retiring a period costs one statement:
`DROP TABLE wh_finance_event_202409;` (or `DETACH` then archive it
off-box first).

## Step 3: normalize the repeated text columns - the big one

Measured by rebuilding 300,000 real rows both ways:
**96 MB -> 46 MB, 52% saved**, data and indexes together.

Per-column waste on the 3.7M-row table today:

| Column | Bytes/row | Distinct values | Wasted |
|---|---|---|---|
| store_id | 25 | 2 | ~78 MB |
| sku | 17 | 43 | ~51 MB |
| charge_type | 16 | 45 | ~47 MB |
| marketplace_id | 14 | 3 | ~41 MB |
| event_type | 13 | 8 | ~37 MB |
| currency, source_origin, event_group_id | 4-7 | 1-53 | ~30 MB |

Each of these also appears inside the primary key, so every byte is
stored twice.

Shape: a lookup table per dimension, `smallint` on the fact table.

```sql
CREATE TABLE dim_store (id smallint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                        code text UNIQUE NOT NULL);
INSERT INTO dim_store(code) SELECT DISTINCT store_id FROM wh_finance_event;
-- repeat for sku, charge_type, marketplace_id, event_type, currency
```

The app then writes the smallint code, or a view restores the text
shape so existing queries keep working:

```sql
CREATE VIEW wh_finance_event_v AS
SELECT f.*, s.code AS store_code, sk.code AS sku_code
FROM wh_finance_event f
JOIN dim_store s ON s.id = f.store_id
JOIN dim_sku  sk ON sk.id = f.sku;
```

**Do this when nothing else is in flight.** The primary key is rebuilt
as part of it, and the upsert conflict target changes type - the
application's `ON CONFLICT` clause must be updated in the same release.

## Step 3: the executable version (re-measured 2026-09-28)

Step 2 being done changes nothing about step 3: the waste is per row, wherever
the row lives. Re-measured from `pg_stats` against today's data, roughly 1.71M
estimated rows across the partitions, parent total **741 MB**:

| Column | Distinct | Bytes/row | Heap saving |
|---|---|---|---|
| `store_id` | **1** | 26 | 40 MB |
| `sku` | 47 | 17 | 25 MB |
| `charge_type` | 58 | 16 | 23 MB |
| `marketplace_id` | 3 | 14 | 20 MB |
| `event_type` | 8 | 13 | 18 MB |
| `source_origin` | **1** | 7 | 8.5 MB |

That is about **135 MB of heap**. These columns also sit inside the primary key,
so the index saving is comparable: expect **250 to 300 MB total, roughly 35 to 40
percent** of the table. `store_id` alone is 40 MB to store one value 1.7M times.

### BLOCKED, and on what exactly

`wh_finance_event`'s primary key includes these columns, so normalizing them
changes the PK's column types. That PK is the `ON CONFLICT` arbiter for every
finance write. The application's upsert must change in the same release, or every
write starts failing the moment the migration lands.

**Do not run the migration below until the ppc app has the matching change ready
to deploy together.** The order is: deploy an app that can write either shape, run
the migration, then remove the old path.

### Migration, per dimension

Run one dimension at a time, verify, then the next. Each is independently
revertible until the column swap.

```sql
-- 1. the lookup table
CREATE TABLE dim_charge_type (
  id   smallint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code text UNIQUE NOT NULL
);
INSERT INTO dim_charge_type(code)
SELECT DISTINCT charge_type FROM wh_finance_event WHERE charge_type IS NOT NULL;

-- 2. the new column, backfilled in batches so nothing takes a long lock
ALTER TABLE wh_finance_event ADD COLUMN charge_type_id smallint;
-- repeat per partition to keep each statement short:
UPDATE wh_finance_event_202608 f SET charge_type_id = d.id
  FROM dim_charge_type d WHERE d.code = f.charge_type AND f.charge_type_id IS NULL;

-- 3. prove it before trusting it
SELECT count(*) FROM wh_finance_event WHERE charge_type IS NOT NULL AND charge_type_id IS NULL;
-- must be 0

-- 4. the view that keeps existing read queries working unchanged
CREATE OR REPLACE VIEW wh_finance_event_v AS
SELECT f.*, d.code AS charge_type_text
FROM wh_finance_event f LEFT JOIN dim_charge_type d ON d.id = f.charge_type_id;
```

The PK swap is the only step that needs the app deployed first, and it is the only
irreversible one:

```sql
-- LAST, and only with the app change live. Build the replacement first so the
-- swap is a rename rather than a window with no unique constraint.
CREATE UNIQUE INDEX CONCURRENTLY wh_finance_event_pkey_new
  ON wh_finance_event (store_id_id, charge_type_id, ...);  -- new types
-- then, per partition, ATTACH the matching leaf index and swap the constraint
```

**Do not `DROP` the old primary key before the new unique index exists and is
attached on every partition.** Arbiter probes do not increment `idx_scan`, so the
old PK reads as unused while being load-bearing for every upsert; dropping it
early turns every re-sync into duplicate rows. This is the mistake the note at the
top of `tasks.md` exists to prevent.

### Verification

- `SELECT pg_size_pretty(sum(pg_total_relation_size(c.oid))) FROM pg_class c
  JOIN pg_inherits i ON i.inhrelid=c.oid JOIN pg_class p ON p.oid=i.inhparent
  WHERE p.relname='wh_finance_event';` before and after. Expect 741 MB to roughly
  450 to 490 MB.
- Re-run one real upsert from the app against a staging copy and confirm it still
  conflicts on the intended row rather than inserting a duplicate.
- `SELECT count(*) FROM wh_finance_event;` unchanged.

## Not worth doing

- **lz4 TOAST compression.** Tested against pglz on real JSON:
  0.1% *worse*. No benefit for this data shape.
- **Replacing the composite primary key with a hash.** Tested: only
  11.6% saved, because the hash column costs in the heap what it saves
  in the index. And it must not be dropped regardless - every finance
  write uses it as its `ON CONFLICT` arbiter. Arbiter probes do not
  increment `idx_scan`, which is why it reports zero reads while being
  load-bearing.

## Capacity

There is no per-project database quota in ForgeBase - the only quota is
for file Storage. The ceiling is the server disk (38 GB, currently 58%
used with 16 GB free). To raise it:

- **Hetzner Console -> Server -> Rescale** to a plan with a larger disk,
  then `growpart /dev/sda 1 && resize2fs /dev/sda1`. Disk growth is
  one-way on Hetzner plans.
- Or attach a **Volume** (added live, no downtime) and move
  `/opt/pgforge-backups` onto it - backups are the easiest tenant to
  relocate and currently hold the WAL archive and basebackups.
