---
name: semantic-layer
description: Model entities, metrics and joins under semantics/ — check the grain and double-sourced rows first, declare assertions, use metric() correctly, and propose definitions instead of writing files.
---

# Semantic layer

`semantics/` holds reviewed definitions (entities, metrics, joins, a
glossary) that dashboards and answers build on. A metric is a measure over a
**saved query**; a wrong saved query makes every number built on it wrong, and
`metric()` will not notice. Do these checks before you write a definition.
Field-by-field reference: `docs/reference/semantics.md`; the tool reference is
`help_search "semantics"`.

## 1. Grain first

Before a metric, state the saved query's grain ("one row per night") and
prove it. Profile the source (`SUMMARIZE <table>`, see `data-profiling`), then
count rows per intended key:

```sql
SELECT log_date, count(*) AS n FROM <the saved query's FROM> GROUP BY 1 HAVING count(*) > 1;
```

Zero rows means one row per key. Any rows mean a sum or average over that key
is wrong. Check what the key's date means too: a timestamp stamped by **start
time** puts an event that crosses midnight, or two sessions in one day, on
dates you did not expect.

Worked example: a sleep table stamped by start time had two nights sharing a
date. The saved query combined the two nights into one row per date, adding
their scores, and the metric's average came back as 106.1 on a 0-100 scale.
The grain query above found the repeated dates at once; the fix was to
collapse to one row per date in the saved query (`max_by` / a window, or pick
the main sleep), then re-check. A number outside its possible
range is a grain bug until proven otherwise.

## 2. Double-sourced rows

Two sources often record the same event (a workout from one app and a
tracker, the same night from two devices). Look for overlap on the grain key
across the source column:

```sql
SELECT log_date, count(DISTINCT source_id) AS sources, count(*) AS n
FROM <table> GROUP BY 1 HAVING count(DISTINCT source_id) > 1 LIMIT 20;
```

If the same key appears under more than one source, decide which source wins
and filter or dedup in the saved query, then re-run section 1. Say so in the
metric's `description` or `settled_by` so the choice is reviewable.

## 3. Assertions to declare

An assertion is a counted violation, run by `slaw semantics check`. The kinds
are closed:

- entity `unique` on the `id_column`, and `not_null` on the columns you rely on;
- entity `accepted_values` on a status or category column that a `CASE` or filter depends on (compared as text, case-sensitive);
- entity `range` (`min`, `max` or both) on a numeric column with a known scale (a 0 to 100 score, a price that cannot be negative);
- entity `freshness` (`column`, `max_age` as `7d` or `36h`) on the date or timestamp a dashboard depends on being current; an empty table is stale;
- entity `row_count` (`min_rows`) so an empty or truncated load is noticed;
- metric `non_negative` for a count, sum or amount; metric `not_null` when a gap would mislead;
- metric `range` (`min`, `max` or both) on the metric's value at its full dimension set, such as an average on a 0 to 100 scale;
- metric `unique`, which checks that the **saved query** has one row per dimension set (needs at least one dimension). This is what finds the fan-out and the double-sourced night of section 2: the metric's own output is grouped, so it cannot show duplicates, but the rows under it can.

In a definition, an entity assertion names its column (except `row_count`) and
a metric assertion takes none. Bounds are plain numbers (unquoted, no
exponent); `max_age` needs its unit:

```yaml
# entity
assertions:
  - {kind: unique, column: id}
  - {kind: accepted_values, column: status, values: [open, closed]}
  - {kind: range, column: score, min: 0, max: 100}
  - {kind: freshness, column: log_date, max_age: 7d}
  - {kind: row_count, min_rows: 1}
```

```yaml
# metric
dimensions: [log_date]
assertions:
  - {kind: non_negative}
  - {kind: unique}
  - {kind: range, min: 0, max: 100}
```

Declare metric `unique` only when the saved query is meant to be at the
metric's grain (one row per night, say). A query of raw rows measured by
region would rightly fail it. A failed `freshness` or `row_count` says what it
saw (`stale: latest 2026-09-30`, `too few rows: 0 rows`); the others report a
count of violating rows. They do not replace section 1: they keep the answers
you found there true as the data changes. `check` exits 1 when any check
fails, otherwise 2 when some could not run: a run where every row errors
verified nothing.

Each entity and metric gets a `compile` row first: its table and columns, or
its measure and dimensions against the saved query, are bound on the real
connection without scanning rows. `slaw semantics check --compile` runs only
those, fast enough after every edit; run it before the full `check`.

## 4. Using `metric()`

```sql
SELECT * FROM metric('avg_sleep_score', by => 'log_date');
SELECT * FROM metric('revenue', by => ['region', 'month']);
```

The grammar is only `metric('name')`, `metric('name', by => 'dim')` or
`by => ['dim', ...]`, with plain quoted literals. There is no `where =>`, time
grain or parameter in the call, and `by` takes only the metric's declared
`dimensions`. The result column is named after the metric. A metric runs on
its saved query's connection, so the statement must run on that connection
(`slaw query -c <conn>`, or the cell's connection).

**A window: filter outside.** Put one `WHERE` on the expansion:

```sql
SELECT * FROM metric('avg_sleep_score', by => 'log_date')
WHERE log_date >= current_date - 30 ORDER BY log_date;
```

It can only filter a dimension or the metric value. If the window belongs in
the data, give the saved query a `$since` parameter with a default such as
`today-30d`. The Query Explorer, `slaw query` and the console run it with
that default; through `run_query` you pass a value in `params`.

**A derived number across two metrics: join by hand.** There is no derived
metric. Join the two expansions on their shared dimensions (same connection):

```sql
SELECT d, i.calories_in - o.calories_out AS balance
FROM metric('calories_in', by => 'd') i
JOIN metric('calories_out', by => 'd') o USING (d);
```

An inner join drops a day either side lacks; use `FULL OUTER JOIN` and say so
if the gap matters.

## 5. Propose, don't write

Agents never write under `semantics/`. File a definition with
`propose_definition` (`kind`, `name`, `definition`, `rationale`, and
`evidence`: the queries or values you checked, for example the grain result).
It is validated now and waits in the person's inbox without an approval card, even when "Ask before changes" is on (accepting it is the person's approval); **nothing changes and
`metric()` will not find it until they accept it** (`slaw semantics proposals`,
`accept`, `reject`, or Studio). Save the query first (`save_query`), since a
metric's saved query must exist. To change a definition send the `rev` from
`get_definition` as `replaces_rev` (for a glossary term, the term's own rev: proposals for
different terms can be filed together). Do not use a proposed definition in
answers until accepted. Use `list_definitions` first: reuse a name that
exists rather than proposing a near-duplicate.

From a shell: `slaw semantics list`, `show <kind> <name>`, `check`, `profile
<join>`. Run `check` after the person accepts, and report what passed and what
could not run.

## 6. Watches

A watch is a standing question with a condition (`watches/<name>.yaml`):
write one with `write_watch` over a saved query or a metric that exists
(`list_watches` and `get_watch` read them; send the `rev` to replace one). You
cannot check a watch: the person checks it in Studio's Watches view or with
`slaw watch check`, so tell them it is written and not yet checked.

## 7. Tables in Iceberg catalogs

An `iceberg_catalog` connection is an ordinary catalog (`list_connections`
shows it with `catalog: true`). Name its tables with **three parts**,
`catalog.namespace.table` (`cat.ns.orders`), so a saved query and the metric
over it do not depend on which connection is the default. A connection
pinned to a catalog may set `default_namespace`, which lets a saved query on
that connection say `orders` (without it, `orders` resolves only when the
catalog has a single namespace). Joins with other connections' tables are fine: it is one
DuckDB.

When you check data, read a column (`SELECT max(id) ...`, `sum(amount)`),
not `count(*)`: DuckDB answers `count(*)` from the table's metadata without
opening a data file, so it proves nothing about the data. A metric over a
catalog runs only on the connection its saved query names, so call `metric()`
from `run_query` with its `connection` argument (`connection: "cat"`,
a name from `list_connections`; the same gates apply). The alternative is a saved
query on the default connection with three-part names, which needs no argument. Listing a
catalog (`introspect_schema`) shows table names only; `describe_table` on one
table fetches its columns, and a slow catalog fails after the hub's HTTP
timeout instead of hanging.
