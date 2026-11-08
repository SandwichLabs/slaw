# Semantic layer: `semantics/`

The workspace can say what its data means, as reviewed files that sit in git
beside the queries and notebooks that use them. There are four kinds:

- **Entities**: a thing the data is about (a customer, an order): its grain, its table, its id column, which columns are personal data.
- **Metrics**: a named measure over a **saved query** (`net_revenue` = `sum(amount)` over the query `net_revenue_base`).
- **Joins**: how two entities relate, declared and measured.
- **The glossary**: plain markdown, one `## term` section per term.

Three runnable example workspaces, with real output, are in [`docs/examples/`](../examples/README.md).

An agent reads the modelling conventions (grain first, double-sourced rows, which assertions to declare, the `metric()` window and hand-join patterns, propose rather than write) from the built-in `semantic-layer` skill (`read_skill semantic-layer`). This page is the reference it points to.

Everything is files you can write by hand. Nothing here needs AI. `slaw semantics list` reads the files and nothing else; a metric runs when SQL calls it with [`metric()`](#using-a-metric-in-sql).

```
semantics/
├── entities/customer.yaml
├── metrics/net_revenue.yaml
├── joins/orders_customers.yaml
└── glossary.md
```

The folders are created by whatever writes the first file; `slaw workspace init` does not make empty ones (git does not keep an empty folder, and a missing `semantics/` is simply no definitions).

## Rules every yaml file shares

Each file is read as strictly as an [`artifact.yaml`](artifacts.md): by one shared decoder (`internal/strictyaml`).

- The file is at most **64 KiB** and at most **5,000** YAML nodes. The size is checked before anything is parsed.
- No control characters, no anchors or aliases, no explicit tags, no merge keys, one document, no unknown keys, no duplicate keys. Nothing is interpolated: `${ENV}` is just text.
- `version` is required and is `1`.
- `name` is required, must equal the file name without `.yaml`, and follows the **name rule**: 1 to 63 characters, `^[a-z_][a-z0-9_]{0,62}$` (lower-case letters, digits and `_`, not starting with a digit). The rule is the connection-name rule, because a name is emitted into SQL as a bare identifier or alias, and a name that never needs quoting can never carry anything else. A Windows device name (`con`, `nul`, `com1`...) is refused for the name of an entity, metric or join, since it would not be a usable file everywhere a workspace is cloned.
- Column, dimension and join-column names follow the same lower-case rule.
- **Reserved SQL words are refused** as a name, column, dimension or any part of a `table` (DuckDB's reserved keywords: `select`, `order`, `group`, `null`, `true`, `table` and so on, compared without regard to case for table parts). Names are emitted as quoted identifiers (`"region"`), so a column named like a function (`current_date`) is the column; the reserved-word rule remains as a second guard, since `GROUP BY null` would silently group by a constant. A test keeps the list equal to DuckDB's own `duckdb_keywords()`.
- At most **500** files per kind: the first 500 (by name) are listed, followed by one entry named `_truncated` whose error says how many were left out.
- A file that fails any of this is **not hidden**. It is listed with its error (`slaw semantics list`, `Error` in the store), it cannot be used, and it is never overwritten by Slaw unless you choose to overwrite it. That includes a name that is not valid (`Customer.yaml`) and a symbolic link (listed as "is a symbolic link; it is not read").
- A write is refused (as a conflict) when a file with the same name in different capitalisation exists, since on a case-insensitive filesystem it would replace that file.
- Free text fields (`description`, `owner`, `grain`, ...) have a length cap, and a control or bidirectional-override character is refused wherever it comes from, including a YAML escape such as `"\u0007"`.

Every file also has a **revision**, a hash of its bytes. A write is compare-and-write: it names the revision it read, and if the file changed on disk since, the write is refused and nothing is touched.

## Entities: `semantics/entities/<name>.yaml`

```yaml
version: 1
name: customer
description: People who bought something.   # optional, up to 2000 characters
grain: one row per customer                 # required, up to 200 characters
table: prod_pg.public.customers             # required
connection: prod_pg                         # optional
id_column: customer_id                      # required
columns:                                    # optional, up to 100
  - {name: email, pii: true, description: contact address}
owner: data team                            # optional, up to 200 characters
assertions:                                 # optional, up to 50
  - {kind: unique, column: customer_id}
  - {kind: not_null, column: created_at}
  - {kind: accepted_values, column: status, values: [active, churned]}
  - {kind: range, column: lifetime_value, min: 0}
  - {kind: freshness, column: created_at, max_age: 7d}
  - {kind: row_count, min_rows: 1}
```

| Field | Meaning |
|---|---|
| `table` | One to three dotted identifiers (`table`, `schema.table` or `catalog.schema.table`), each `^[A-Za-z_][A-Za-z0-9_]{0,62}$`. The first part of a three-part name is a connection's catalog alias. |
| `connection` | Optional connection name (the name rule) where an unqualified `table` resolves. |
| `id_column` | The column that identifies one row of this entity. |
| `columns` | Notes on columns: `name` (unique), `pii: true` to flag personal data, `description`. Optional; a column that is not listed is simply unannotated. |
| `assertions` | Checks you declare. Each names a `column`, except `row_count` (see [Assertions](#assertions)). |

## Metrics: `semantics/metrics/<name>.yaml`

```yaml
version: 1
name: net_revenue
title: Net revenue              # optional, up to 200
description: Revenue after refunds.
query: net_revenue_base         # required: a saved query id (queries/<id>.sql)
measure: sum(amount)            # required
dimensions: [region, month]     # optional, up to 20
format: currency                # optional: number | currency | percent
owner: finance
settled_by:                     # optional
  text: "A counted refunds, B excluded them. Refunds are excluded."
  ref: ""
assertions:
  - {kind: non_negative}
  - {kind: not_null}
```

| Field | Meaning |
|---|---|
| `query` | The id of a [saved query](workspaces.md) (`queries/<id>.sql`): a lower-case letter or digit first, then letters, digits, `-` and `_`. A metric is a measure over that query's rows, so the SQL, its parameters and its connection live in one place, the saved query. The file is not required to exist when the metric is read, only when it is used. |
| `measure` | One SQL aggregate expression over the saved query's columns, which is placed after `SELECT` and before `AS <name>`. It is held to a **structural** rule, not a list of bad words: at most 4 KiB, on one line; no `;`, `--` or `/*`; balanced parentheses; only plain `'strings'` with every quote closed (no `E'..'`, no `$$..$$`); **no comma outside parentheses** (so no second column); none of the words `SELECT`, `FROM`, `WITH`, `UNION`, `INTERSECT`, `EXCEPT`, `AS`, `GROUP`, `HAVING`, `ORDER`, `LIMIT`, `OFFSET`, `QUALIFY`, `WINDOW`, `PIVOT`, `PIVOT_WIDER`, `PIVOT_LONGER`, `UNPIVOT`, `TABLE`, `VALUES`, `DESCRIBE`, `SUMMARIZE`, `SHOW`, `EXPLAIN`, `PRAGMA`, `CALL`, `EXISTS` anywhere, and `WHERE` only directly inside `FILTER (...)` (its condition may open with a parenthesised expression: `FILTER (WHERE (status) = 'paid')`). After every `(` the next word may not be **any** DuckDB keyword (the full `duckdb_keywords()` list, every category), except `DISTINCT`, `CASE`, `NOT`, `NULL`, `TRUE`, `FALSE`, `INTERVAL`, `WHERE` right after `FILTER`, and a callable keyword such as `coalesce(`; so no subquery or statement (`(PIVOT ...)`, `(TABLE t)`, `(DESCRIBE ...)`, `(VALUES ...)`) can start inside a group. **Every call must be to an allowed function**: DuckDB's scalar and aggregate functions (a list generated from `duckdb_functions()` and kept equal to the linked DuckDB by a test), unqualified, less `getenv`, `query`, `query_table`, `metric`, the file readers (`read_csv`, `read_parquet`, `read_json`, `read_text`, `glob`, ...), `COLUMNS` and `UNNEST` (more than one column or row), `nextval`, `setval`, `currval`, `current_setting`, `getvariable`, `setvariable`, `sleep_ms`, `write_log`, `setseed` (side effects and state), and the secret, MotherDuck and AI functions; `FILTER`, `OVER`, `IN` and the like before `(` are not calls, and a type after `::` may take arguments (`::DECIMAL(10,2)`). No `*` except in `count(*)` (`t.*`, `sum(*)` and `* EXCLUDE (...)` are refused; `a * b` is multiplication and is fine). A metric must produce exactly one value column and change nothing. So `sum(amount)`, `sum(amount) FILTER (WHERE status = 'paid')`, `count(DISTINCT customer_id)`, `sum(a) / nullif(sum(b), 0)`, `avg(x)::DECIMAL(10,2)`, `sum(price * qty)`, `sum(coalesce(a, 0))` and `sum(CASE WHEN refunded THEN 0 ELSE amount END)` are fine; `CAST(x AS ...)` (use `::`), `extract(year FROM d)` and `string_agg(x, ',' ORDER BY y)` are not. Commas inside a call's parentheses are fine. That is all that is checked when the file is read; whether the SQL is allowed is also decided where it runs, by the SQL policy (see below). To regenerate the lists after a DuckDB upgrade: `go test ./internal/semantics -run TestGenerateLists -update-lists`. |
| `dimensions` | Columns of the saved query that a caller may group by. Lower-case column names, no duplicates. |
| `format` | How a value is shown: `number`, `currency` or `percent`. Display only. |
| `settled_by` | The disagreement that settled this definition. `text` is required (up to 2000 characters); `ref` is free text (up to 200) reserved for a verifier card or receipt id. It is stored and shown, never followed. |

## Joins: `semantics/joins/<name>.yaml`

```yaml
version: 1
name: orders_customers
left: orders                     # entity names
right: customer
on:                             # 1 to 8 column pairs
  - {left: customer_id, right: customer_id}
cardinality: many_to_one        # one_to_one | many_to_one | one_to_many | many_to_many
description: each order has one customer
measured:                       # optional: written by a profiling run you start
  at: 2026-10-29T15:30:12Z      # RFC 3339
  left_rows: 12000              # counts: whole, non-negative, plain decimal
  right_rows: 800
  max_fanout: 1                 # most right rows for one left key
  left_orphans: 12              # left rows with no right match
  right_orphans: 3
  verdict: consistent           # consistent | fan_out | orphans
```

| `cardinality` | Meaning |
|---|---|
| `one_to_one` | each left row matches at most one right row, and the reverse |
| `many_to_one` | many left rows share one right row (an order and its customer) |
| `one_to_many` | one left row has many right rows |
| `many_to_many` | neither side is unique |

| `measured.verdict` | Meaning |
|---|---|
| `consistent` | the measurement agrees with the declared cardinality |
| `fan_out` | a key has more matches than the declared cardinality allows |
| `orphans` | rows on one side have no match on the other |

The `measured` counts are read strictly: a non-negative whole number written in decimal. `1.5`, `0x10`, `1e3`, `1_000`, `12.0`, `+5` and a quoted `"12"` are refused.

`cardinality` is what you **declare**. `measured` is what a profiling run found; the run is something the person starts (see [Profiling a join](#profiling-a-join)), it is not part of reading these files, and a file with no `measured` block is complete.

## Assertions

An assertion is a check you declare; `slaw semantics check` (or Studio's Run assertions) plans and runs it. Its `kind` is closed:

| Kind | On | Takes |
|---|---|---|
| `unique` | entity | `column` |
| `unique` | metric | nothing; the metric needs at least one dimension |
| `not_null` | entity or metric | `column` on an entity, nothing on a metric |
| `accepted_values` | entity | `column` and `values` (1 to 200 values, each up to 200 characters) |
| `non_negative` | metric | nothing |
| `range` | entity or metric | `column` on an entity, nothing on a metric; `min`, `max` or both |
| `freshness` | entity | `column` and `max_age` |
| `row_count` | entity | `min_rows` (no column) |

The extra fields are strict, so a file never means something other than what it says:

- **`min`, `max`** (range) are plain decimal numbers such as `0`, `-5` or `99.5`, at most 15 digits and under 10^15 in size. A quoted `"100"`, `1e3`, `0x10`, `.inf` and `.nan` are refused, not coerced. At least one is required, and `min` may not be above `max`.
- **`max_age`** (freshness) is a whole number of days or hours: `7d`, `36h` (`^[1-9][0-9]{0,4}[dh]$`). `7` without a unit, `1w` and `0d` are refused.
- **`min_rows`** (row_count) is a whole number from 1, written in decimal.
- A field that belongs to another kind (`min` on `unique`, `max_age` on `range`, `values` on `range`) is refused.

### Running assertions

`slaw semantics check [entity|metric [name]] [--compile] [--json]` runs the declared assertions, with no argument for everything. Each entity and metric starts with a **`compile`** check (below); each assertion then becomes one read-only statement that counts the rows that break it:

| Assertion | Counts |
|---|---|
| `unique` on `col` | `count(col) - count(DISTINCT col)` over the entity's `table`: surplus rows sharing a value (NULLs are `not_null`'s business, not duplicates) |
| `not_null` on `col` | rows of the entity's `table` where `col IS NULL` |
| `accepted_values` on `col` | rows where `CAST(col AS VARCHAR)` is none of the listed values; the values are bound parameters (`$v0`, `$v1`, ...), compared as text, and NULL is not a violation |
| `non_negative` on a metric | rows where the metric is below 0 |
| `not_null` on a metric | rows where the metric is NULL |
| `range` on `col` | rows where `col < min` or `col > max` (either bound alone is fine); NULL is not a violation. The bounds are bound parameters (`$slaw_min`, `$slaw_max`) |
| `range` on a metric (a saved query that declares `$slaw_min` or `$slaw_max` is refused) | the same over the metric at its full dimension set, so a total of 106.1 on a 0 to 100 scale is found |
| `unique` on a metric | dimension sets (the metric's `dimensions` together) that **its saved query** has more than one row for. A metric is grouped, so its own result is trivially unique; this looks at the saved query's rows under it, which is where a fan-out or a double-sourced row shows. It is the one check that embeds a saved query outside a `metric()` expansion, built by the same expander with the same rules and `$name` defaults. Declare it only when the saved query is meant to be at the metric's grain; a query of order lines measured by region would, rightly, fail |
| `freshness` on `col` | 1 when the latest value of `col` is older than `max_age`, or when there is no value at all (an empty table, or all NULL), else 0. The threshold is `now` minus `max_age`, worked out in Go in UTC and bound as a timestamp (`$slaw_threshold`), so no interval text from the file reaches SQL. A `DATE`, `TIMESTAMP` or `TIMESTAMPTZ` column compares as a timestamp, a date counting from its midnight; a text or number column does not compare with a timestamp, which the compile check reports (below) |
| `row_count` | 1 when the entity's table has fewer than `min_rows` rows (bound as `$slaw_min_rows`), else 0 |

`freshness` and `row_count` return a second column, a plain-words detail: `latest 2026-09-30` or `no values`; `412 rows`. The report prints it in DETAIL (`stale: latest 2026-09-30`, `too few rows: 0 rows`; a passing check prints it as is), `--json` carries it as `detail` (present for `freshness` and `row_count` only) and Studio shows it under the status. The count is still one statement and one number: a freshness or row_count check is 0 or 1 violation. The detail is the planner's own text with a value from the data; it is shown with control characters escaped.

The compile check (below) covers every column an assertion names and the typed comparison of each `range` and `freshness` assertion (the bounds and the threshold are bound placeholder values, cast to the comparison type, `CAST($slaw_max AS DOUBLE)` and `CAST($slaw_threshold AS TIMESTAMP)`, and nothing is run), so a typo in a column, or a `range` on a text column or `freshness` on a text or number column, is a "does not compile" row with the binder's own message, and the assertions of a subject that does not compile are `not run` like the others.

### The compile check

Every entity and metric starts with a `compile` row, so a definition with no assertions still gets one. It builds one statement from the definition and **vets it instead of running it**: the agent policy's read-only verdict, whose last step is `EXPLAIN` on the real connection, which binds every identifier without reading a row. An entity's is `SELECT "id_column", "col", ... FROM <table>` (its table, every annotated column and every column an assertion names exist). A metric's is the metric at its full dimension set (none for a metric with no `dimensions:`), expanded like any `metric()` call, so the `measure` and every dimension are bound against the saved query's output columns and a `$name` parameter takes its declared default. Joins are not compiled here; `profile` binds both sides in its first statement.

`slaw semantics check --compile` runs only these rows: no rows are scanned for the result (binding can still read a file's header or sample, a parquet footer or an object listing), so it is quick enough to run after every edit, including over a MotherDuck connection. A definition that does not compile is a `fail` (fix the file; exit 1) and the detail is the database's own message, printed as `does not compile: ...`, and Studio's Run assertions shows the row as "Compiles" or "Does not compile: ...". A policy refusal (a saved query that reads outside the workspace) is also a `fail`. Only the database's Binder, Catalog and Parser errors and the policy's denials count as the definition being wrong; a connection that cannot be reached, a timeout or a failed read while binding is an `error` (exit 2), since that says nothing about the files. A metric's saved-query parameters that have no default get a type-valid placeholder for this check. When no data check could run, the report says so ("No data check could run"), even if the compile rows passed. When a subject does not compile, its own assertions are not run: they are `error` rows, `not run: the definition does not compile`, instead of one copy of the same binder message each.

A metric is checked **at its full dimension set**: it is expanded like any `metric()` call with every declared dimension, so a negative or missing value in any cell of the breakdown is found. The saved query's parameters take their declared defaults. Identifiers in the statements come only from validated names (the name rule and the table rule above) and are each quoted (`"customers"."current_date"`), so a column named like a DuckDB function such as `current_date` is the column, not today's date. A value is never part of the SQL text.

`accepted_values` compares **as text** and is case-sensitive: the column is cast to `VARCHAR` and each listed value must match what DuckDB prints. Write `1`, not `1.0`, for an integer column; `1.50` for a `DECIMAL(10,2)`; `2026-10-29 15:30:12` for a timestamp; `true` (lower case) for a boolean; and `Active` is not `active`. For example, `values: ["1", "2"]` accepts an integer column holding 1 and 2 but flags 1.0 stored in a `DOUBLE` column, and `values: [active]` flags a row holding `Active`.

Every statement runs through the agent policy's read-only path in the tightest tier, whatever the sandbox setting, the same gates a cloned artifact query gets: a definition from a cloned repository whose saved query reads files outside the workspace is refused, and a hand-written entity whose `table` is not a plain name is not even planned. A check reports one of:

| Status | Meaning |
|---|---|
| `pass` | no violating rows |
| `fail` | that many violating rows broke the assertion |
| `error` | the check could not run (a connection is down, the entity or metric file is unreadable, the saved query is missing); it says nothing about the data, and the other checks still run |

Exit status: `0` when every check passes or none is declared, `1` when any check failed, `2` for anything that stopped the run from saying whether the data is right: a check that could not run, and also a usage or open error (an unknown kind or name, a bad flag, no workspace). So a script can tell "fix the data" (1) from "fix the command or the setup" (2). `slaw semantics profile` exits 0 on success and 2 on any error, including a join file that changed while it ran. `--json` prints one object per check (`subject_kind`, `subject`, `assertion`, `column`, `status`, `violations`, `detail`, `duration_ms`, `error`, and an empty `receipt` reserved for [receipts](../plans/answers-you-can-check.md)). Names and messages are printed with control characters shown as `<U+XXXX>`. When no check could run at all, the report ends with "Nothing was verified", so a run that checked nothing is not mistaken for a clean one. The command opens the workspace read-only (no hub lock, every connection attached read-only), so it takes no lock. The one exception is MotherDuck, which cannot be attached read-only: since every statement `check` and `profile` run goes through the read-only policy, they attach a MotherDuck connection as it is defined (as Studio does for the same runs) instead of leaving it unreachable. Like any connection other than a local file, it connects on first use, and a connection that fails to connect is reported with its own error (a missing token, a refused attach). It still cannot read a local DuckDB file that Studio has attached read-write: that check ends as an `error` (the file is in use) until you close the workspace in Studio or use a copy, so run `check` beside Studio only against remote or read-only connections.

### Profiling a join

`slaw semantics profile <join> [--json]` measures how the join's two entities really relate and records it in the join file as the `measured:` block. It reads both entities' tables with a few full scans (so a large table takes a while), each statement through the same read-only policy path as the assertions:

- `left_rows` and `right_rows`: a row count of each entity's `table`.
- `left_orphans` and `right_orphans`: rows with no partner on the other side under the `on` pairs (a NULL key has no partner).
- `max_fanout`: the most right rows sharing one key that a left row has.
- The most left rows sharing one key is also measured, to judge `one_to_many` and `one_to_one`, and is reported but not stored.

The `verdict` compares this with the declared `cardinality`. `fan_out` means the declared side was supposed to be unique and is not (`many_to_one` and `one_to_one` want at most one right row per left key; `one_to_many` and `one_to_one` want at most one left row per key), so joining repeats rows and sums are inflated. `orphans` means rows that should find a partner do not (the left rows of a `many_to_one`, the right rows of a `one_to_many`, either side of a `one_to_one`; the other side having extra rows is normal). `many_to_many` is held to neither. When both apply, `fan_out` wins. A contradiction is printed as a warning and exits 0: the profile did its job. Both entities need to be reachable in one statement, so entities on different connections need a catalog-qualified `table` on at least one side.

The file is read once, before the scans, and written with that revision: if it changed while the profile ran, **nothing is written** and the command says so, so a profile never overwrites an edit. The rest of the file, comments and line endings (LF or CRLF) included, is kept as written; a file the block cannot be edited into is refused before any scan runs; only the `measured:` block is replaced or appended. Profiling is not disabled in a read-only workspace: it only reads data, and the one thing it writes is the join file's `measured:` block (so a workspace folder you cannot write to refuses it with the file error). It is your action only: no agent tool or MCP method runs it, and `get_definition` returns the `measured` block so an agent can read the result.

## The glossary: `semantics/glossary.md`

Plain markdown, up to 256 KiB, no control characters. Slaw reads it only for its terms: the level-2 headings (`## Active customer`) outside code fences, in file order. The text is otherwise yours.

## From the command line

`list` and `show` only read files; no connection is opened and no query runs. `check` and `profile` run read-only queries on your connections (see [Running assertions](#running-assertions) and [Profiling a join](#profiling-a-join)); `profile` also writes the join file. The four proposal commands read and write the [proposals inbox](#proposals-an-agent-suggests-you-decide) and, for `accept`, one file under `semantics/`.

```
slaw semantics list [--json]            kind, name, one-line summary, and any error
slaw semantics show <kind> <name>       the file exactly as stored (kind: entity | metric | join)
slaw semantics show glossary [term]     the glossary, or one term with its rev (on stderr)
slaw semantics check [entity|metric [name]] [--compile] [--json]   compile-check every definition, then run the declared assertions (exit 1: a check failed, 2: some could not run); --compile stops after the compile rows
slaw semantics profile <join> [--json]  measure a join and record it as measured: in its file
slaw semantics proposals [--all] [--json]   proposed definitions waiting for you (--all: decided ones too)
slaw semantics show-proposal <id>       who proposed it and why, and a diff of the file against the proposed one
slaw semantics accept <id> [--overwrite --rev <rev>]   write the proposed definition into semantics/
slaw semantics accept --all [--kind K] [--yes]   accept every open proposal (K: entity | metric | join | glossary), after showing each and asking once
slaw semantics reject <id>              decline it; semantics/ is not touched
```

`list` includes unreadable files with their whole error message (whitespace folded, never cut) and says on stderr how many files could not be read and how many proposals wait. When the cause is an unquoted value inside `{...}` (for example `values: (key, unit)`, which YAML splits at the comma), the error ends with a hint to quote a value that contains `,` `(` `)` `:` or `#`. `show` prints a file even when it fails validation (so you can read and fix it), then says why on stderr and exits non-zero. Control and bidirectional characters in a name, summary, error or file are shown as `<U+XXXX>`.

## Proposals: an agent suggests, you decide

An agent never writes under `semantics/`. It can only **propose** a definition, and the proposal waits for you. This is the one way an agent adds to the semantic layer, and it is the same in Studio's chat and over MCP.

| Tool | Tiers | What it does |
|---|---|---|
| `list_definitions` | every tier | The entities, metrics (with dimensions, measure and saved query), joins and glossary terms, each with its `rev`, unreadable files with their error, and `proposals: {open: n}`. A read-only agent can use metrics through this and `metric()`. |
| `get_definition` | every tier | One definition as data with its `rev`. For a metric it also returns the declared `parameters` of its saved query. For the glossary, a `name` (a term) returns that term's text and the **term's own `rev`** (`term-…`, a hash of its section; `file_rev` is the glossary file's); without a `name` it returns the term list and the file's `rev`. |
| `propose_definition` | `read-write` and `full` (a content tool, like `save_query`) | Files a proposal. Arguments: `kind` (`entity`, `metric`, `join` or `glossary`), `name`, `definition`, `rationale`, optional `evidence` and `replaces_rev`. |

`propose_definition` checks the definition **now** with the kind's own decoder and validator, so the measure rule, the reserved-word rule, the name rule and every cap apply, and an unknown field is refused. A definition that fails is refused with the reason and nothing is filed; an accepted proposal can therefore never produce a file Slaw would refuse to read. It also refuses: a `metric` whose saved query does not exist; a name that already exists unless `replaces_rev` is the `rev` the agent read (and a stale `rev`, so it cannot overwrite what it never saw); a proposal identical to one already waiting; a join with `measured` (that block is written only by a profiling run you start); a glossary text with a `## ` line of its own (it would start another term); and any call once **200** proposals are open (`accept or reject some first`). The agent is told the proposal was filed and that **nothing in `semantics/` has changed**. It is exempt from "Ask before changes": it files without an approval card, over MCP and in Studio alike, because your accept step is the approval and asking first would approve the same change twice. The tier gate (`read-only` refuses) and the inbox caps still apply.

### The inbox

A proposal is a file, `.slaw-local/proposals/<id>.yaml`, on this machine: **git-ignored and never in a `.slaw` bundle**, so a proposal is not shared and not committed until you accept it and commit the resulting file. The id is made by Slaw (`<UTC time>-<kind>-<name>-<4 hex>`, for example `20261029T153012Z-metric-net_revenue-7f3a`), never by the agent, and every command and binding checks that it has exactly that shape before using it as a file name.

```yaml
version: 1
id: 20261029T153012Z-metric-net_revenue-7f3a
kind: metric                 # entity | metric | join | glossary
name: net_revenue            # for a glossary, the term made into a name
status: open                 # open | accepted | rejected
proposed_at: 2026-10-29T15:30:12Z
proposed_by: agent           # agent | person
rationale: Refunds are excluded, per the finance thread.
evidence: [saved query net_revenue_base]
replaces_rev: ""             # the rev of the file it replaces; "" = a new file
definition:                  # the exact document that would be written (glossary: term, text)
  version: 1
  name: net_revenue
  query: net_revenue_base
  measure: sum(amount)
decided_at: ""
```

A proposal file is read as strictly as the definitions: at most **64 KiB** and **6,000** YAML nodes, no anchors, aliases, tags or merge keys, no unknown key, and the `definition` is decoded with the kind's own rules again every time the file is read (a hand-edited proposal that no longer passes is listed with its error and cannot be accepted). `rationale` is required (up to 2,000 characters), `evidence` holds up to 20 short strings. Decided proposals stay as history; Slaw keeps the newest 800 of 1,000 files and drops the oldest decided ones, never an open one.

### Accepting

Accepting is **only a person's act**: `slaw semantics accept <id>` (or `--all`) or Studio's `AcceptProposal` and `AcceptProposals`. No tool, MCP method, approval card or agent path can accept or reject a proposal, or write, replace or delete anything under `semantics/`. The agent is handed a read-only view of the store and an inbox that can only add, wrapped so neither can be turned back into the writer, and a test runs every tool in every tier against a store that fails on any write and checks that `semantics/` and the existing proposal files are byte for byte unchanged on disk afterwards.

Every accept is pinned to the version that was shown: it carries the `Rev` of what the person looked at (a token over the target file's revision and the proposal file's; for a glossary term, over that term's own revision and the proposal file's, so accepting another term does not invalidate it), and a mismatch (the target changed, or the proposal was edited, after it was shown) is refused with "changed again since you looked", before anything is written or marked. Plain `slaw semantics accept <id>` looks at the proposal right then, prints what it is about to write (the `show-proposal` text, with its `Rev`) to stderr, and writes that; `--rev <rev>` accepts the version you reviewed earlier and must match. Studio's `AcceptProposal` requires the `shown_rev` and refuses an empty one.

**Accept all.** `slaw semantics accept --all` and Studio's "Accept all" button take every open proposal (oldest first; `--kind` narrows the CLI to one kind) and run one loop, `proposals.AcceptMany`, which calls the same compare-and-write `Accept` for each. The CLI prints every proposal's `show-proposal` text (with its `Rev`) to stderr, then asks once (`[y/N]`; `--yes` skips the question, otherwise it needs a terminal, like `slaw repl`). Studio reads each proposal when you press Accept all, lists them in a confirmation (kind, name or term, the file each writes and its added and removed lines; the list scrolls and focus starts on Cancel), and sends the ids with the `shown_rev` it read. In the CLI every proposal is pinned to the version printed; in Studio, to the version read when you pressed Accept all. Anything that changes after that is refused as "changed again since you looked". A proposal that was decided elsewhere meanwhile is left out. `--kind` and `--yes` are refused without `--all`. Bulk accept never overwrites: it stops at the first proposal that is refused (a target that changed or was created since it was proposed, a version that changed again, an invalid or no longer open proposal) and leaves it and the rest open; the ones before it stay accepted. Two proposals for one file therefore accept the first and stop at the second, while proposals for different glossary terms all accept. The CLI prints a line per accepted proposal, names the one that stopped the run and why, and exits non-zero; Studio shows the same as a note above the inbox. `--all` cannot be combined with an id, `--rev` or `--overwrite`. A proposal that is stale when listed is flagged, since it stops the run; accept it one at a time with "Accept anyway" (or `--overwrite --rev`) after reading its diff.

`accept` writes the text `show-proposal` displayed, as compare-and-write against the revision the proposal was made against (`replaces_rev`). There are two exceptions to "exactly what was displayed". Accepting over a changed file, below, writes the proposed text, never a merge. For a glossary, the review is pinned to the term's section, so edits to other terms made since you looked are kept as they are in the file and the written file differs from the displayed one only there:

- the file is unchanged since: it is written, and the proposal is marked `accepted`;
- the file **changed, was created, or was deleted** since the proposal: nothing is written, and the command fails with "changed since this was proposed", showing the diff. To accept it anyway you pass `--overwrite` **and** `--rev <rev>`, the `Rev` line `show-proposal` printed for the version you reviewed (the failure message prints the exact command). `--overwrite` without `--rev` shows the proposal and refuses, so nothing can overwrite blind. If the file or the proposal changed again after you looked, the `Rev` no longer matches and even `--overwrite` is refused ("changed again since you looked"): look again;
- the file already reads exactly as proposed (a crash after the write, before the proposal was marked): nothing is written again, the proposal is only marked accepted;
- a glossary proposal appends a `## term` section, or replaces the existing section of the same term (matched without regard to case), and leaves every other term untouched. Each term has its own rev, a hash of its `## term` section (`term-` and 16 hex characters, shown by `get_definition` with a term and by `slaw semantics show glossary <term>`). A replacement carries that rev as `replaces_rev`, so proposals for several terms can be filed together and accepted in turn: changing another term does not make one stale. Accepting re-reads the file, checks the term's rev, splices the new text in and writes against the file revision just read. A proposal filed with the glossary file's rev (the older form) is still accepted while that file rev is current. A **new** term (its `replaces_rev` is empty) is never stale because some other term changed, only because that term now exists, so two new-term proposals can be accepted one after the other; a replacement is stale if its term changed ("changed"), and if the term was deleted ("the term was deleted since this was proposed"). Marking the proposal accepted compares the proposal file with the version that was shown. A glossary with a code fence that is never closed is refused (`glossary.md has an unclosed ``` fence; fix it first`), since an appended term would be swallowed by the fence. A glossary written with CRLF line endings keeps them.

`show-proposal` (and Studio's `GetProposal`) also warns about: a metric whose saved query no longer exists, a join whose `measured` block accepting would drop, and a hand-written file with comments that the canonical rewrite will lose.

`reject` marks the proposal `rejected`. A proposal that is no longer open cannot be accepted or rejected again.

Studio's bindings are `ListProposals(all)`, `GetProposal(id)` (the proposal, the current text and revision of its target, the whole proposed text, whether it is stale, the `shown_rev` pin and the warnings; the diff is drawn client-side), `AcceptProposal(id, shownRev, overwrite)` (`shownRev` is the `shown_rev` that was on screen; the write is refused if it changed, and `overwrite` needs it) and `AcceptProposals(pins)` (a list of `{id, shown_rev}`; returns the accepted proposals, the `failed_id` that stopped the run and its `error`, and never overwrites) and `RejectProposal(id)`. The Semantics view that uses them is described [below](#in-studio-the-semantics-view).

Studio's bindings for running definitions are `RunAssertions(kind, name)` (rows as `check --json` prints them; empty `kind` and `name` run everything), `ProfileJoin(name)` (the profile; a join file that changed while it ran is a conflict) and `RunMetric(name, dims)` (cancel any of the three with `CancelRun("assertions" | "profile" | "metric")`; a metric's "Try it": the metric by a subset of its dimensions, run through the read-only policy and capped at 5000 rows like the query grid, ordered by the dimensions; a result cut at the cap has `truncated: true` and `total_rows` equal to `row_count`, the true total being unknown).

## In Studio: the Semantics view

**Semantics** in the sidebar (route `/semantics`) is the person's window onto this folder. It reads and runs; it edits no file, so it has no conflict banner: a definition changes on disk (an editor, git, an accepted proposal) and its card reads the file again. A badge on the nav item counts the open proposals and stays live.

- **The list** on the left groups Metrics, Entities, Joins, the Glossary (its term count) and Proposals (an inbox row, then one row per open proposal). A file that cannot be read is listed with an `error` badge, and opens as its text with the reason, so it can be found and fixed. The selection is in the URL (`#/semantics?kind=metric&name=net_revenue`; `kind=proposal&name=<id>` for a proposal), so a reload and a link keep it.
- **A metric card** shows the title, description, owner and format, the measure, the dimensions, the saved query (a link that opens it in the Query Explorer; a missing query is flagged), how the disagreement over it was settled (`settled_by`), and its assertions. **Try it** has a checkbox per dimension and a Run button: it runs the metric by the ticked dimensions (none: the single total) and shows the rows in a grid, with a notice when the result stopped at the row cap. **Run assertions** checks the metric and lists each check, starting with its Compiles / Does not compile row, as pass, fail (with the number of violating rows) or error (the check could not run, which says nothing about the data).
- **An entity card** shows the grain, the table and its connection, the id column, the columns flagged as personal data (`pii`), the assertions (each with its bounds, window or minimum: `range on score (min 0, max 100)`, `freshness on log_date (max age 7d)`, `row_count (min 1 row)`) and **Run assertions**. A freshness or row_count row shows what it saw beneath its status.
- **A join card** shows the declared cardinality beside the measured one with its counts and when it was measured, and a warning when the data contradicts the declaration. **Profile** measures the join and writes `measured:` into its file. It is off in a read-only workspace, and a join file that changed while the profile ran is shown as a conflict ("nothing was written") rather than overwritten.
- **The glossary** is the file rendered with the app's one safe markdown renderer, with the raw file a click away.
- **The proposals inbox** lists what the agent has proposed. A proposal opens with its rationale, the evidence the agent gave, the warnings Go found, and a line diff of the current file against the proposed one (changed lines carry `+` and `-` as well as colour). **Accept** writes the version on screen (for a glossary term, that term's section; other terms keep any edits made since): it sends `shown_rev`, so a file or proposal that changed after you looked is refused ("changed again since you looked"), and the view reads the proposal again and shows the new diff (its "updated" notice does not fire when only another glossary term changed). When the file changed since the agent proposed, the view says how and offers **Accept anyway** (which also needs the version on screen) in place of Accept. **Reject** marks it rejected. Accept is off in a read-only workspace. After an accept the new definition opens.
- **Runs.** Try it, Run assertions and Profile go through the agent policy's read-only path in Go, one at a time (a second start while one is going does nothing), with a progress line, the seconds taken and a **Stop** button, since a check can run for minutes. Stop cancels the run's context, which interrupts the statement in DuckDB; the run ends as a neutral "Cancelled." line (the binding returns the fixed error `cancelled`), a stopped assertion run shows no partial results and a stopped profile writes nothing. Errors are shown as text.
- **Empty state.** With no definitions the view explains the two ways to start: write the files by hand, or ask the agent to model your data and review what it proposes.

Bindings behind the view: `ListDefinitions()` (entities, metrics, joins, glossary terms and the open-proposal count in one read) and `GetDefinition(kind, name)` (the file text as stored, its `rev`, the decoded document, and whether a metric's saved query is missing), beside the proposal and run bindings above. A change under `semantics/` or `.slaw-local/proposals/` made by another process reaches the view as a `workspace:changed-on-disk` event (kinds `semantics` and `proposal`). A proposal the in-app assistant files in this process is announced with a `semantics:proposed` event, because the watcher ignores this process's own writes.

## Sharing

The files are plain and diff-friendly, so git carries them. A [`.slaw` bundle](workspaces.md#sharing-without-git-slaw-bundles) carries them too: `semantics/entities/<name>.yaml`, `semantics/metrics/<name>.yaml`, `semantics/joins/<name>.yaml` and `semantics/glossary.md` are bundle entries, each decoded with the rules above before anything is written. Import is byte for byte, and a name that already exists is renamed (`net_revenue_2`, the name inside the file updated), replaced (against the revision just read) or skipped, like a query. When the rename is of something the same bundle refers to, the references follow: a metric whose saved query (`query:`) came in renamed from `q` to `q-2` is pointed at `q-2`, and a join whose entity came in renamed is pointed at the new entity, so an imported definition never silently uses the workspace's own namesake; when you skip (or the default skips) a clashing saved query or entity that a metric or join of the same bundle refers to, the result's warnings say the reference now points at the workspace's existing one, which may be a different query or entity; a rewritten file is re-encoded (comments are not kept) and the result's warnings say so. The glossary is one file and cannot be kept beside another: a clash defaults to skip (the dialog shows "Can't keep both"), and you can still replace it. `slaw export bundle --semantics metrics/net_revenue` picks definitions by id (`entities/<name>`, `metrics/<name>`, `joins/<name>`, `glossary`). Anything under `.slaw-local/` is never in a bundle.

## Using a metric in SQL

A metric is called by name, wherever you write SQL:

```sql
SELECT * FROM metric('net_revenue');                              -- one row, one column: net_revenue
SELECT * FROM metric('net_revenue', by => 'region');              -- one row per region
SELECT * FROM metric('net_revenue', by => ['region', 'month']);   -- grouped by both
SELECT region, net_revenue FROM metric('net_revenue', by => 'region') WHERE net_revenue > 1000 ORDER BY 2 DESC;
SELECT metric('net_revenue') / 12 AS monthly;                     -- a scalar, in a SELECT list
```

The grammar is exactly this, and nothing else:

```
metric('<name>')
metric('<name>', by => '<dimension>')
metric('<name>', by => ['<dimension>', '<dimension>', ...])
```

`<name>` and every `<dimension>` must be a plain `'quoted'` literal (not `E'..'`, not `$$..$$`, not an expression). `by` is the only named argument; there is no `where =>`, no time grain and no parameter in the call. A dimension must be one of the metric's `dimensions` (matched without regard to case). The word must be a bare `metric` followed by `(`: `"metric"(...)` and `schema.metric(...)` are left alone and fail as an unknown function. At most 20 calls in one statement.

### What it expands to

Before anything checks or runs the statement, each call is replaced by one parenthesised `SELECT` over the metric's saved query:

```sql
(SELECT "region", "month", sum(amount) AS "net_revenue" FROM (
<the saved query's SQL, trailing ';' and comments cut off, otherwise byte for byte>
) AS slaw_metric_net_revenue GROUP BY "region", "month")
```

The dimensions and the metric's alias are quoted, so a dimension called `current_date` groups by the column, not today's date. With no `by`, the dimensions and the `GROUP BY` are left out. Only the names declared in the files and the metric's `measure` are written into the SQL; **what you typed in the call never is**. The saved query must be exactly one statement beginning with `SELECT`, `WITH`, `FROM` or `VALUES`, with balanced parentheses and closed quotes, and may not itself call `metric()`. Expanding twice gives the same text (the result contains no `metric(` call), so a statement can be re-checked.

The saved query's own `$name` parameters stay in the text, bound, never written into it: a parameter you did not supply takes the default its saved query declares (relative dates such as `today-30d` resolve at the run), and a value you supply for it wins. Two metrics in one statement that declare the same `$name` differently are refused. A `$name` you write yourself outside the call that only a metric declares is refused (`$min belongs to metric net_revenue's saved query; it can't be used outside metric()`) unless you send a value for it, and a saved query open in the Query Explorer that declares the same name with another type is reported as a clash. In the Query Explorer, `slaw query` and the console you cannot supply a metric's own parameters yet (only the defaults apply); an agent's `run_query` can send them in `params`.

### Refusals

Each is a plain sentence naming the call; the first is for an unknown name and the last for a hand-edited file:

| You wrote | You are told |
|---|---|
| `metric('nope')` | `metric('nope'): there is no metric named 'nope' (metrics live in semantics/metrics/; slaw semantics list shows them)` |
| `by => 'colour'` | `metric('net_revenue'): 'colour' is not a dimension of net_revenue (dimensions: region, month)` |
| `metric(net_revenue)`, `metric()` | `metric(): the metric name must be a plain 'quoted' literal` |
| `metric('x', where => ...)`, an extra argument | `metric('x'): only by => is supported (no where, no other arguments); ...` |
| a dimension that is not a plain literal | `metric('x'): each dimension must be a plain 'quoted' literal; ...` |
| a saved query that calls `metric()` | `metric('x'): its saved query "q" uses metric(), which a metric's own query may not do` |
| a saved query that is not one read | `metric('x'): saved query "q" is not one read statement (one SELECT, WITH, FROM or VALUES)` |
| a missing saved query | `metric('x'): its saved query "q" does not exist (queries/q.sql)` |
| a `$name` the saved query does not declare | `metric('x'): its saved query "q" uses $name, which it does not declare as a parameter` |
| two metrics declaring `$name` differently | `metrics a and b both declare the parameter $name, differently (...)` |
| a saved query on another connection | `metric 'x' is defined on connection 'prod_pg'; run this statement on prod_pg` |
| more than 20 calls | `metric(): 21 metric() calls; at most 20 are allowed in one statement` |

The connection rule: a metric runs on the connection its saved query names (`connection:` in the query's header; none means the workspace default). The Query Explorer's Run-on choice, a notebook cell's connection or `slaw query -c` must be that connection. This stops one unqualified table name meaning two different tables.

### Where it works

The same expander runs in front of every place SQL is checked and run, on the policy's own lexer (`internal/sqllex`), so it cannot read a statement differently from the check that follows it:

- **The agent**, through the one tool surface: `run_query`, `run_saved_query`, `preview_query`, `write_cell`, notebook cells, A2UI `sql` bindings and artifact queries. The [SQL policy](agent-policy.md) judges the **expanded** statement, in every tier, so a metric whose saved query was hand-edited into something the policy refuses is refused, with the metric's name in front of the policy's words. `run_query` answers with `metrics_used: [{name, by, query}]` when it expanded any. Notebook cells (including the ones Studio runs for you), dashboard bindings and artifact queries are always run through this agent policy, whoever is looking, not through the editor's rules below.
- **The person's own typed SQL**: the Query Explorer and its export, `slaw query` (it says `expanded 1 metric: net_revenue` on stderr), `slaw export --sql`, the Studio console, `slaw repl` and the terminal UI's REPL (`slaw studio` without `--gui`). They share one preparation step (`hub.PrepareForPerson`): the Query Explorer, the exports, `slaw query` and the terminal UI check what you typed, expand, then check the expanded statement; the console and `slaw repl` expand first and their session checks the expanded statement. An export must also be exactly one `SELECT` (`hub.PrepareExport`). The editor's check (`usersql`) keeps its usual rules for what you typed and reads the expanded statement. Because a metric hides SQL behind a name, **each expanded metric (its saved query and measure) is also held to the agent's read-only policy**, as an artifact query is: exactly one `SELECT` by DuckDB's own parser, no file outside `data/`, no write, no secret. A cloned repository's metric that reads `/etc/passwd` is refused at run with `metric 'x' is refused: ...`, even under `slaw query --read-only`. Only the metric's part is held to the policy; your own SQL around it is not.
- **Editors**: completion and hover (see [editor.md](editor.md#metric-in-the-editor)).

**Over an Iceberg catalog.** An [`iceberg_catalog`](connections.md#iceberg_catalog) is an ordinary catalog, so an entity's `table`, a join's sides and a metric's saved query can name its tables with three parts (`cat.ns.orders`), and the agent reads them in the read-only tier like any other table, over `slaw mcp serve` too (`run_query`, `describe_table`, `introspect_schema`, `metric()`). A metric runs only on the connection its saved query names, and `run_query` has no connection argument: to call `metric()` from it, save the query on the workspace's default connection with three-part names; a saved query with `connection: cat` runs through the paths that name the connection (notebook cells, watches, `slaw semantics check`, `run_saved_query`). A check that reads a catalog's data should read a column: DuckDB answers `count(*)` from the table's metadata without opening a data file. A catalog that stalls fails after the hub's `http_timeout` (30 seconds, [agent-policy.md](agent-policy.md)) instead of hanging a run.

Not covered: a stopped assertion run does not keep the results of the checks that finished before Stop (each statement is still bounded by a 2-minute deadline and Try it by 30 seconds).

