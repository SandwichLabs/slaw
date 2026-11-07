# The semantic layer over a lakehouse: S3 Tables, Iceberg catalogs and DuckLake

Status: **proposal, not built (2026-11-07).** This is an overview and gap analysis. The issue drafts are in [`semantic-layer-issues.md`](semantic-layer-issues.md), and the implementation plan in [`semantic-layer-lakehouse-impl.md`](semantic-layer-lakehouse-impl.md). It builds on E4 ([`reference/semantics.md`](../reference/semantics.md), shipped) and the [answers-you-can-check](answers-you-can-check.md) plan (#250).

## What a semantic layer is

A semantic layer is **the mapping between how a business talks and what is actually in the warehouse**. It covers:
- metric formulas
- entity definitions and their grain
- synonyms
- the joins that are safe to make
- the pitfalls an experienced analyst carries in their head (units, double-sourced rows, which table is canonical)

Its job is that "what was net revenue last month?" gets **one** answer, whether a CEO, an analyst or an agent asks.

There are two generations of the idea:

| | Classic semantic layer | Agent-era "context layer" |
|---|---|---|
| Form | A metrics API or modelling language (Cube, MetricFlow, LookML, Malloy) | Markdown and small files next to the data: descriptions, glossaries, skills, `COMMENT ON` |
| Behaviour | Deterministic: a static formula behind an API | Flexible, read by a model, precise when written well |
| Weakness | Expensive to build, lags the business | The agent can ignore it; definitions drift and conflict |

Slaw already combines the two:
- **Deterministic:** reviewed YAML definitions, expanded by `metric()` into the same SQL on every surface.
- **Soft context:** descriptions, column notes, the glossary and the `semantic-layer` skill.

The gaps below are mostly about **getting that context to the agent at the right time** and **reaching the tables we actually store**.

### Why it matters for agents

Industry evidence, from MotherDuck Outpost (Nov 2026):
- On a multi-database benchmark, the best agents with **no curated semantic layer score about 70%** pass@1. Pre-processing plus a semantic layer adds about 15 points or more (Shreya Shankar, CMU).
- "Curation is better than context": the right data model matters more than a perfect context file (Jordan Tigani, MotherDuck).
- Context bound to the **data** cannot be skipped. Context bound to the **harness** (a skill) loads only when triggered (MotherDuck guides).
- The vocabulary for synonyms is best mined from real agent sessions. Canva has the agent annotate every query with `purpose` and `keywords` (Sam McFarland).

## Target stack

```
            people (Studio, CLI)        agents (Slaw assistant, Claude Code / Codex over MCP)
                      │                                   │
                      └──────────────┬────────────────────┘
                                     ▼
                 one SQL policy + metric() expansion (internal/agents, semantics/expand.go)
                                     │
             semantics/ (entities · metrics · joins · glossary)  ◄── proposals inbox ◄── agent
                                     │
                       DuckDB hub (one per workspace, read-only by default)
              ┌──────────────────────┼──────────────────────────┐
              ▼                      ▼                          ▼
   S3 Tables (Iceberg REST,    Iceberg REST catalogs       DuckLake (DuckDB/SQLite
   SigV4 via aws-sso)          (Polaris etc.; Glue later)  catalog today; Postgres next)
              └──────────────────────┴──────────────────────────┘
                                     ▼
                         S3 (storage connections + scopes)
```

The semantic layer lives in the workspace as files, the data stays in the lake, and every reader goes through one policy. Nothing about the storage changes.

## What Slaw provides today (0.7.0)

| Need | Slaw today | Where |
|---|---|---|
| Reviewed definitions in git | Entities, metrics, joins and glossary as strict YAML/markdown | `internal/semantics`, [semantics.md](../reference/semantics.md) |
| Deterministic metrics | `metric('m', by => [...])` expanded in front of every SQL path, then judged by the policy | `semantics/expand.go` |
| Agent-authored, human-approved | `propose_definition`; a person accepts, compare-and-write | `semantics/proposals` |
| Data tests on definitions | Assertions, the compile check, join profiling | `semantics/assert`, `slaw semantics check|profile` |
| Standing checks | Watches over a metric or saved query | `internal/watches` |
| Modelling guidance for agents | The `semantic-layer` built-in skill | `internal/skills/semantic-layer` |
| DuckLake | DuckDB or SQLite catalog, local or a read-only URL; snapshot metadata readable by the agent | [connections.md §ducklake](../reference/connections.md) |
| S3 identity | Storage scopes, `aws-sso` sign-in, URL hardening (#138) | `internal/awssso`, `internal/storageurl` |
| External agents | The same tools and limits over MCP | `internal/mcp` |

## Gaps

| # | Gap | Priority | Today (evidence) |
|---|---|---|---|
| G1 | Attach Iceberg catalogs: S3 Tables, REST (Glue later) | P0 | `iceberg` is a table type read by path; a REST catalog is "Not built" ([connections.md](../reference/connections.md) §iceberg) |
| G2 | Agent read path for Iceberg | P0 | `iceberg_*` readers refused for the agent in every tier (#20) |
| G3 | DuckLake with a Postgres catalog; shareable lake definitions | P1 | `catalog` is `duckdb` or `sqlite`; lake types are own-scope only, so `semantics/` table names depend on what each person named their connection |
| G4 | Catalog comments reach the agent | P1 | `describe_table` returns name, type, not-null and an always-false `pk` per column; no comments anywhere in introspection (`handler_introspect.go`, `session.ColumnInfo`) |
| G5 | Definition search, synonyms, table binding | P1 | No search tool, no synonym field, nothing injected into the prompt; definitions are not surfaced from `describe_table` |
| G6 | Metric expressiveness | P2 | `metric()` takes a name and `by` only: no parameters, time grain, or derived metrics (#287) |
| G7 | Evals and receipts | P1 | E1 (#241) and E2 (#242) are planned; no `internal/receipts` |
| G8 | Usage mining into proposals | P2 | No intent captured on agent queries |
| G9 | As-of over snapshots | P2 | E6 (#246) planned |
| G10 | Team governance: lint, owners | P2 | `owner` is free text; no conflict detection |

### Design notes per gap

- **G1, G2 belong together.** Attaching a catalog turns Iceberg tables into ordinary catalog references (`s3t.ns.orders`), which `semantics/` can name in three parts.
  - Trust model: the **definition** is trusted as DuckLake's is today (only a person defines the catalog, in their own scope). The catalog's **metadata** is not trusted by default: it is written by whoever commits to its tables, so the agent reads it confined (D1 below). The agent reads its tables as references; the raw `iceberg_scan`/`iceberg_metadata` functions stay off the allow-list.
  - Security questions from #20, and where the impl plan answers them:
    - data-file URLs a manifest names: an uncovered `s3/r2/gcs` URL fails closed at the deny secrets; a local path, an `http(s)://` URL (loopback and metadata endpoints included) and an Azure URL are only refused on the confined reader (impl L4);
    - vended credentials (S3 Tables, REST): decided **on**, set explicitly. A vended secret's scope and endpoint are the catalog's to choose and DuckDB picks the longest matching scope, so they vend into the confined reader always and into the main hub only for a trusted catalog (impl L4 note on D2/D3, settled by spike item 8).
  - Decided (direction change, 2026-11-07): agent reads of a catalog run on the main hub like any catalog, joins included, with the metadata-directed read risk stated as a warning; vended credentials are off. The earlier confined-reader design (impl plan D1–D3) is superseded.
- **G3 connection templates.** A committed file may *name* a lake (type, expected endpoint, connection name) without being trusted. Each person adopts it into their own scope. This keeps the own-scope rule and makes `table:` names in `semantics/` portable.
- **G4 + G5 are the cheapest accuracy wins.**
  - `describe_table` returns catalog comments merged with the covering entity's column notes, PII flags and metrics, each labelled with its source.
  - A capped *semantics index* (metric names and titles) goes into the system prompt and MCP `instructions`.
  - Context bound to the table, not only to a skill, is the point.
- **G6** in order:
  1. pass a saved query's declared `$params` through `metric()` (`queries.Bind` exists);
  2. a `time_dimension` with grains;
  3. composite `id_columns`;
  4. ratio metrics from other metrics;
  5. later, join-path dimensions over `consistent` profiled joins.
- **G7 is how we know any of this works.** Known-answer and must-refuse evals as files, plus `slaw eval run` (person or CI only, like `watch check`), recording whether `metric()` was used. This is the before/after number for a pilot.
- **G8** captures `purpose`/`keywords` on agent queries, stored machine-local with the receipt. `slaw semantics suggest` then files synonym and glossary **proposals**. Nothing is written without a person.

## Phases

1. **Reach the data (G1, G2).** An S3 Tables bucket attaches via SSO, and the agent runs `metric()` over it in the read-only tier over MCP. People's reads and the agent's ship in one release (D7: nothing ships after the person half alone). *Exit:* an entity on `s3t.ns.t` passes `slaw semantics check`, and the agent answers a `metric()` question on it over MCP.
2. **Context reaches the agent (G4, G5, G3 templates).** *Exit:* on the pilot domain, the agent picks the defined metric for 9 of 10 business-phrased questions without being told its name.
3. **Prove it (G7).** Pilot eval: 20 questions, about 10 metrics, one domain. Report accuracy and cost before and after the semantic layer. *Exit:* a published number.
4. **Grow it (G6, G8, G9, G10).** Metric parameters and grains, usage-mined proposals, as-of on snapshots, `slaw semantics lint`.

A DuckLake-only pilot can start today and skip phase 1.

## Not in scope

- Writing to Iceberg tables.
- A hosted or shared semantic-layer server. Definitions stay files in the workspace.
- A deterministic metrics API for other BI tools. `metric()` is a Slaw SQL feature, and an export format can come later.
- Free-text `WHERE` in `metric()`. Filters come only through declared parameters.
