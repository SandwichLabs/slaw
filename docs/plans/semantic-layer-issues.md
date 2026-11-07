# Issue drafts: the semantic layer over a lakehouse

Drafts for [`semantic-layer-lakehouse.md`](semantic-layer-lakehouse.md); the implementation plan is [`semantic-layer-lakehouse-impl.md`](semantic-layer-lakehouse-impl.md). **None of these is filed yet.** New issues have a title and labels. Gaps that existing issues already cover are drafted as comments on those issues instead.

To file a new issue: copy its body into a file, then run `gh issue create -R SandwichLabs/slaw --title "<title>" --label "<labels>" --body-file <file>`.

---

## New issues

### N1. `iceberg_catalog` connection type: S3 Tables and Iceberg REST

**Labels:** `enhancement`, `workspaces-connections`

Slaw reads an Iceberg *table* by path (`iceberg_scan`). It cannot attach an Iceberg *catalog*, and `docs/reference/connections.md` (§iceberg) lists a REST catalog as not built. S3 Tables is reached through Iceberg REST, so S3 Tables and Glue-catalogued tables are unreachable today.

**Proposal:** a new own-scope-only type, `iceberg_catalog`, that attaches as a catalog, so `s3t.namespace.table` works in the rail, joins, saved queries and `semantics/` entity `table:` fields.

- **Endpoints**
  - `s3_tables`: table-bucket ARN, SigV4.
  - (`glue` is out of scope for now: refused by `Validate` as not supported yet.)
  - `rest`: endpoint URL, with an OAuth2 client-credentials or bearer secret.
- **Credentials**
  - `s3_tables` reuses the `aws-sso` sign-in (`internal/awssso`) and the `env`/`keychain` methods of `s3`.
  - REST needs no new credential kind: `secrets.Credential` already has `Token` (bearer) and `KeyID`/`Secret` (OAuth2 client credentials), kept in the keychain and emitted as a DuckDB `TYPE iceberg` secret, never in `slaw.yaml`.
  - `s3_tables` holds no credential of its own and signs with the storage connection it requires (see the implementation plan).
- **Endpoint rule (in the spirit of #138):**
  - the catalog endpoint is fixed by the definition;
  - data files are read with the catalog's vended credentials where vending is allowed (below); any `s3/r2/gcs` location no secret covers falls through to the deny secrets;
  - a local, `http(s)://` or Azure location is refused on the confined reader; on the main hub it is a trusted catalog's documented residual risk.
- **Vended credentials: on**, set explicitly (`ACCESS_DELEGATION_MODE 'vended_credentials'`), never left to DuckDB's default. A vended secret's scope and endpoint are chosen by the catalog and DuckDB picks the longest matching scope, so the spike measures whether one can outrank the deny secrets (whose scope is the bare `s3`) or a storage connection's. Until shown otherwise, vending is on for the confined reader always and on the main hub only for a trusted catalog; an untrusted catalog attached on the main hub for the person uses `'none'` (impl plan, L4 note).
- **Trust toggle:** `trust_writers` (default false). An untrusted catalog is read by the agent only on the confined lake reader (#20). A trusted one is read on the main hub and can be joined with other data, with the residual risk shown in the form (local files, `http(s)` requests from this machine, Azure and any object under the person's other storage scopes, and the catalog's own vended endpoint). The toggle trusts the catalog's operator as well as its writers.
- **Read-only:** writes stay out of scope.
- **Release:** ships together with the agent read path (#20), not before it.
- **Extensions:** `iceberg` + `avro` + `httpfs` (+ `aws`), through the existing consent flow and the *Extension needed* status.
- **Studio:** a connection form with the storage line (#130 pattern) and Test (list namespaces, then read one table's schema).

**Acceptance**
- Attach a real S3 Tables bucket via SSO. `SELECT` from `s3t.ns.t` works in the Query Explorer and `slaw query`.
- The tables appear in the rail and in `list_connections` / `introspect_schema`.
- An entity with `table: s3t.ns.t` passes `slaw semantics check`.
- A committed `iceberg_catalog` in `slaw.yaml` is refused like `ducklake`.
- Tests: a REST catalog fixture (e.g. a local REST catalog container behind the integration tag), endpoint and scope refusals, and the dependency on the storage connection.

Related: #20, #263. Plan: `docs/plans/semantic-layer-lakehouse.md` (G1).

---

### N2. DuckLake with a Postgres metadata catalog

**Labels:** `enhancement`, `workspaces-connections`

A DuckLake's `catalog` option is `duckdb` or `sqlite` today. A multi-writer production lake usually keeps its catalog in Postgres, which `docs/plans/ducklake-catalogs.md` left out of scope.

**Proposal:** `catalog: postgres`, naming an existing `postgres` connection of the workspace for the credential, and attaching `ducklake:postgres:<dsn>` with `DATA_PATH` inside a storage scope.

- Read-only by default.
- Writes follow the three-gate rule; approval is always asked for a lake.
- The `__ducklake_metadata_*` hiding and the agent policy apply unchanged.

**Acceptance:** a lake on a Postgres catalog (integration fixture) attaches read-only, and its tables are readable by a person and by the agent in the read-only tier. The credential is never in the lake definition, and Test reports which dependency failed.

Plan: G3.

---

### N3. Connection templates: name a lake in `slaw.yaml` without trusting it

**Labels:** `enhancement`, `workspaces-connections`

`ducklake`, `iceberg` (and N1's `iceberg_catalog`) are own-scope only: a committed definition is refused, because crafted metadata can read local files. That rule is right, but it means every teammate defines the lake by hand. `semantics/` entity `table:` fields (`lake.main.orders`) then break whenever someone picks a different connection name.

**Proposal:** a committed `connection_templates:` block in `slaw.yaml` naming the connection, type and expected endpoint or catalog location. It carries no trust and nothing attaches from it.

- Studio shows "This workspace expects a DuckLake called `lake`: **Set up**", which prefills the own-scope form with the name fixed.
- `slaw conn adopt <name>` does the same from the CLI.
- `slaw semantics check` reports a definition whose connection is a template not yet adopted, naming the setup step.

**Acceptance:** clone a workspace with a template and `semantics/` on `lake.*`. Adopt it in one step, then `check` passes. A template never attaches, and its fields are validated like a definition's.

Plan: G3.

---

### N4. `describe_table` returns catalog comments and the semantic layer's notes

**Labels:** `enhancement`, `agent-experience`

`describe_table` returns, per column, name, type, not-null and a `pk` flag that is always false (`internal/agents/handler_introspect.go`; `session.ColumnInfo` in `internal/session/types.go` has no comment field). The following never reach the agent:
- table and column comments (`COMMENT ON` in DuckDB/DuckLake, Iceberg schema field `doc`, Glue comments);
- the entity covering the table: its grain, id column, `pii` flags, column descriptions, and the metrics built on it.

Comments are the cheapest context there is, and they travel with the data.

**Proposal**
- Add `comment` to `ColumnInfo` and a table `comment` to `TableDescription`, read from `duckdb_columns()` / `duckdb_tables()` (and Iceberg metadata through N1's attach).
- When an entity's `table` resolves to the described table, add `semantics: {entity, grain, id_column, columns: [{name, description, pii}], metrics: [names]}`.
- Label each field's source (`catalog` vs `semantics`). Cap the lengths.
- The same shape goes to `introspect_schema` (summarised) and the MCP tool.
- Optional follow-up: `slaw semantics import-comments` files entity column-note **proposals** from catalog comments.

**Acceptance:** with `COMMENT ON COLUMN orders.amount IS 'cents'` and an entity on `orders`, `describe_table orders` returns both, labelled. Responses stay within the existing size caps, and the tests cover a table with no entity, no comments, and comments containing control characters (refused or escaped).

Plan: G4. Related: #221.

---

### N5. `search_definitions`, synonyms, and glossary references

**Labels:** `enhancement`, `agent-experience`

An agent has to call `list_definitions` (up to 500 per kind) and guess which metric matches a business phrase. There is no search, no synonym field, and glossary terms are not linked to the entities or metrics they mean. Canva's experience at Outpost: "search is life". Insiders say `MRR`, outsiders say "is revenue up?".

**Proposal**
- `synonyms: []` (up to 20 short strings, strict-decoded, capped) on entities, metrics and metric dimensions.
- `refs: [kind/name]` in a glossary term, as a small fenced YAML block under the `## term` heading, or a front-matter list. Decide in the design.
- A new tool, `search_definitions(q, kinds?, limit?)`, available in every tier. It does lexical ranking over names, titles, descriptions, synonyms and glossary text, and returns `{kind, name, title, score, why}`. Hybrid or vector ranking is a later option.
- `propose_definition` accepts `synonyms` like any other field.

**Acceptance:** with `net_revenue` carrying the synonym `sales after refunds`, a search for "sales after refunds last quarter" ranks it first. Search is read-only in every tier and appears in `help_search` and MCP.

Plan: G5.

---

### N6. Put the semantic layer in front of the agent: prompt index and table hints

**Labels:** `enhancement`, `agent-experience`, `byo-agent`

Nothing from `semantics/` is in the system prompt: `internal/llm/prompt.go` only names the Meaning tools and the prompt body (`runner.go`) mentions `propose_definition`. MCP `instructions` (`internal/hub/instructions.go`) say to `read_skill semantic-layer` before working on `semantics/`, but carry no definition names. An agent that goes straight to `run_query` bypasses the layer.

**Proposal**
- **Semantics index.** A capped list of metric names and titles (and entity names) in the in-app system prompt and in MCP `instructions`, refreshed when `semantics/` changes. It is cut at N entries, with "use search_definitions for more".
- **Advisory hint.** When `run_query` aggregates over a table that an entity covers and a metric on that entity exists, add a `hint` to the response ("`net_revenue` is defined on this table; consider metric()"). The hint is never a refusal.
- **Measure** with N7's evals: the share of business questions answered through `metric()`, with and without the index.

**Acceptance:** an eval run shows the change. The index respects the prompt-size budget. A workspace with no `semantics/` produces no index text.

Plan: G5.

---

### N7. Ratio and derived metrics

**Labels:** `enhancement`

A metric's own saved query may not call `metric()` (`semantics/expand.go`), so a ratio metric such as `net_revenue / orders` has to be written by hand as a new measure over a joined saved query.

**Proposal:** a metric may declare `ratio: {numerator: <metric>, denominator: <metric>}` instead of `query` + `measure`. It expands to the two metrics' sub-selects at the same `by` dimensions, joined on those dimensions, with `nullif(denominator, 0)`.

- Both metrics must declare every requested dimension.
- Cycles and depth greater than 1 are refused.
- The policy judges the expanded statement.

**Acceptance:** `metric('aov', by => 'month')` equals a hand-written ratio on the Chinook example. Assertions run on it, and the cycle and missing-dimension cases are refused with clear text.

Plan: G6. The time grain and `$param` pass-through parts are in the #287 comment below.

---

### N8. `slaw eval`: known-answer and must-refuse evals as files

**Labels:** `enhancement`, `answers-you-can-check`

This is a narrow slice of E2 (#242), sized so a team can measure its semantic layer. Today watches and assertions check data, but nothing checks whether an **agent** answers business questions correctly or uses the defined metrics.

**Proposal**
- `evals/<name>.yaml` holds:
  - `question` (text);
  - `expect`: one of `{metric, by?, value, tolerance}`, `{saved_query, params?}` to compare result sets, or `{refuse: true}` / `{need_more_info: true}`;
  - optional `must_use_metric: <name>`.
- `slaw eval run [name...]` is for people and CI only, like `watch check`. It drives the configured agent headless in the read-only tier and records:
  - pass/fail;
  - the tools called;
  - `metrics_used`;
  - tokens and duration.

  Results go to `.slaw-local/evals/history.jsonl` (machine-local, never bundled). Exit codes follow `watch check`.
- No agent tool can run or read eval answers. The expected values stay out of the agent's reach.

**Acceptance:** a 5-question eval on the Chinook example runs headless and reports pass rate and metric use. A must-refuse case passes when the agent declines, and the results file holds no rows.

Plan: G7. Part of #242.

---

### N9. Usage mining: capture query intent and propose synonyms and terms

**Labels:** `enhancement`, `agent-experience`

The best source of business vocabulary is what people ask agents (Canva's `purpose` + `keywords` comments). Slaw keeps no record of intent.

**Proposal**
- Optional `purpose` (up to 200 characters) and `keywords` (up to 10) arguments on `run_query`, `preview_query` and `run_saved_query`. The tool descriptions ask the agent to fill them.
- They are stored machine-local with the run (with the E1 receipt when it lands; until then, in a capped `.slaw-local/intent.jsonl`). They are never bundled.
- `slaw semantics suggest` (person only) groups keywords by the tables and metrics they hit. It files **proposals**: synonyms on existing metrics, new glossary terms, and candidate entities for tables that are queried often but not covered. Every proposal goes through the inbox.

**Acceptance:** after a scripted session, `suggest` files at least one synonym proposal for a metric that was queried under a different name. Nothing is written under `semantics/`, and the intent log is capped and pruned.

Plan: G8. Related: #241.

---

### N10. `slaw semantics lint` and owner routing

**Labels:** `enhancement`

As `semantics/` grows across teams, definitions conflict. Examples:
- two metrics with the same measure over overlapping saved queries;
- a synonym claimed by two metrics;
- an entity whose table no longer resolves;
- a metric with no owner.

**Proposal**
- `slaw semantics lint [--json]` reads files and the catalog snapshot, and runs no query. Its findings, each warn or error:
  - duplicate or conflicting measures;
  - synonym collisions;
  - a missing `owner`;
  - unresolved tables;
  - joins never profiled, or not `consistent`;
  - glossary `refs` to missing definitions.

  Exit codes are CI-friendly.
- An optional `semantics/OWNERS` file (CODEOWNERS syntax) maps paths to owners. `lint` checks that each `owner:` matches it, and the same file can feed GitHub CODEOWNERS.

**Acceptance:** fixture workspaces trigger each finding. Lint runs with no connection, and its exit status follows `semantics check`.

Plan: G10.

---

## Comments on existing issues

### On #20: Agent reads of Iceberg/Delta tables via a confined path

> The semantic layer over a lakehouse (`docs/plans/semantic-layer-lakehouse.md`, G2) needs the agent to read Iceberg. Proposal for the Iceberg half: once N1 (`iceberg_catalog`) attaches a catalog that **only a person can define** (own scope, like `ducklake`), the agent reads its tables as ordinary catalog references (`s3t.ns.t`). The raw `iceberg_scan` / `iceberg_metadata` / `iceberg_snapshots` stay off the allow-list. That matches the DuckLake trust model already shipped.
>
> A catalog's **metadata** is written by anyone who can commit to its tables, and a crafted manifest can name a local file, an `http(s)://` URL (a loopback service, the cloud metadata endpoint) or an Azure URL. So by default the agent's statements over an `iceberg_catalog` run on a confined **lake reader**: a second in-process DuckDB with `disabled_filesystems = 'LocalFileSystem'` set and locked, no `azure` extension, holding only those catalogs and the `s3` connections they sign with. The policy's `EXPLAIN` runs there too, which covers bind-time fetches. Such statements cannot join other connections, and on the main hub a reference to such a catalog, directly or through a view, is refused for the agent.
>
> A person can set `trust_writers: true` on a catalog to read it on the main hub instead (joins allowed), accepting the documented residual risk. A person's `semantics check` and `watch check` follow the same routing. Vended credentials are on; because a vended secret's scope and endpoint are the catalog's to choose, they are accepted on the lake reader always and on the main hub only for a trusted catalog.
>
> Design and acceptance: `docs/plans/semantic-layer-lakehouse-impl.md` (L4, L5). Snapshot functions for G9 go on the lake reader's allow-list only.
>
> **Acceptance:** `metric()` over an Iceberg-backed saved query works for the agent in the read-only tier over MCP.

### On #287: Semantics follow-ups

> Two items from the lakehouse semantic layer plan (G6) that fit here:
> 1. **`$param` pass-through in `metric()`:** `metric('net_revenue', by => 'region', params => {start: '2026-01-01'})`, bound through `queries.Bind`, never as SQL text, and only the saved query's declared parameters. This gives filtered metrics without a free-text `where =>`.
> 2. **Time grain:** `time_dimension: created_at` and `grains: [day, week, month, quarter]` on a metric, so `by => 'month'` means `date_trunc('month', created_at)` defined once. This settles the "which week?" class of disagreement in the file, not in each query.
>
> Composite `id_columns` (§8b) is also needed for lake tables keyed on (tenant, id).

### On #242: Epic E2 (VibeBench)

> N8 (`slaw eval`) in `docs/plans/semantic-layer-issues.md` proposes a narrow, file-based slice of E2 to ship first: known-answer and must-refuse evals per workspace, run headless by a person or CI, recording `metrics_used`. It lets a team measure its own semantic layer before the full benchmark exists.

### On #246: Epic E6 (as-of and snapshot diff)

> The lakehouse plan (G9) needs `as_of` on `metric()` and `run_saved_query` for Iceberg (via N1's catalog attach) as well as DuckLake, recorded in the receipt, so a reported number can be reproduced at the snapshot it was read from.
