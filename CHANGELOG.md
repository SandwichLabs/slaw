# Changelog

All notable changes to Slaw are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Slaw uses [Semantic Versioning](https://semver.org/) (pre-1.0: minor versions may change behaviour).

## [Unreleased]

## [0.8.0] - 2026-11-08

Iceberg catalogs (S3 Tables and Iceberg REST) as connections, read by you and the agent like any other catalog, and lake types you can share (#20, lakehouse phase 1).

**Opening in an older Slaw:** a `slaw.yaml` or `.slaw` bundle that defines an `iceberg_catalog`, `ducklake`, `iceberg` or `delta` connection is refused by Slaw 0.7.0 and older (an unknown type, or "only you can add"). Leave those connections out of the export to share with an older Slaw.

### Added

- **Iceberg catalog connections (`iceberg_catalog`).**
  - `endpoint_type: s3_tables` (an S3 Tables bucket ARN as `warehouse`, read through the `s3` connection named in `storage`) or `rest` (any Iceberg REST catalog: `uri`, `warehouse`, `auth: oauth2 | bearer | none`). Glue is not supported yet.
  - Read-only by default. With `read_only: false`, writes pass the usual gates: the agent's tier, a writable connection and your approval.
  - Catalog-issued (vended) credentials are off: every attach sets `ACCESS_DELEGATION_MODE 'none'`, so data is read through your storage connections. A read from a bucket no storage connection covers fails with a message naming what to add.
  - `default_namespace` decides where unqualified table names resolve on a pinned catalog. With several namespaces and no default, an unqualified name fails rather than picking one.
  - If the storage connection is signed out, the catalog shows "needs <storage>" (`blocked_by` in `slaw conn list --json`), and Sign in, Fix and a failed run go to the storage connection. This applies to every connection that depends on another.
  - An S3 Tables catalog refuses a storage connection with a custom `endpoint`, since S3 Tables signs with AWS.
- **Studio's connection form** shows only the fields the chosen kind uses, picks the storage connection from a list of `s3` connections, and shows the metadata warning. The rail lists namespaces and tables by name without loading every table (at most 200 namespaces, then a "more" line); columns load when you open a table.
- **The agent reads Iceberg catalogs** on the main hub, joins with other connections included: `run_query`, `describe_table`, `introspect_schema` and `metric()` in the read-only tier, in the in-app agent and over `slaw mcp serve`. The `semantic-layer` skill has a section on tables in Iceberg catalogs.
- **The agent may call `iceberg_scan`, `iceberg_metadata`, `iceberg_snapshots` and `delta_scan`** under the same rules as `read_parquet` (`data/` and storage scopes, literal paths, the safe-option list). It never reads table metadata it could have written: a local reader path must be a table folder, and `COPY ... TO` refuses `metadata/`, `_delta_log/` and metadata file names.
- `slaw conn add` has examples for each catalog kind, and `credentials set` and the env hints name only the fields that kind signs in with.

### Changed

- **DuckLake, Iceberg and Delta connections may be defined in a committed `slaw.yaml` or a `.slaw` bundle** (they were limited to your own scopes). They carry a warning everywhere they appear: table metadata decides which files DuckDB reads, so anyone who can write it can make a query read local files, fetch any web or local-network URL, or read other storage your connections cover. Bundle import now runs each type's own validation.
- Every session that loads httpfs sets `http_timeout = 30` and `http_retries = 1` and locks them, so a hung catalog or storage request errors out instead of freezing a query or a watch check. The editor, console and REPL refuse `SET`, `RESET` and `PRAGMA` on them.
- The table browser shows a read error instead of "0 records".

## [0.7.0] - 2026-11-07

Watches: standing questions with a condition, kept as files and checked on demand from Studio or `slaw watch check` (#247, phase 0).

**Opening in an older Slaw:** a `.slaw` bundle that includes watches does not open in Slaw 0.6.4 or older (it is reported as corrupt). Untick Watches in the Export dialog, or pass selection flags to `slaw export bundle` without `--watches`, to share with an older Slaw.

### Added

- **Watches (#247, phase 0).**
  - A watch is a standing question with a condition, kept as `watches/<name>.yaml`: a saved query (with parameters) or a metric, and `rows: any|none` or an `above`, `below` or `outside` threshold.
  - Thresholds are plain decimals (at most 40 characters) and are kept, compared and displayed exactly, including integers beyond 64 bits.
  - `slaw watch list|show|check|history` lists them and checks them on demand through the agent policy's read-only path. `check` exits 0 when every watch is ok, 1 when any alert fired, and 2 when none fired but some could not be checked, or for a usage error, an interrupted check, or a workspace another process has open.
  - While Studio (or `slaw mcp serve`) has the workspace open, `slaw watch check` refuses before anything runs and records nothing (exit 2): check from Studio instead.
  - Each check is logged in `.slaw-local/watches/history.jsonl`: the outcome, the reason, the value or row count and how long it took, never result rows. An error's reason is kept as its class only, so a value a database error quoted is not stored. The history stays on this computer and is never bundled.
  - `.slaw` bundles carry `watches/<name>.yaml` entries (`slaw export bundle --watches NAME` picks them). On import, a watch whose saved query or metric the same bundle renamed is pointed at the new name, and one invalid watch refuses the whole bundle.
  - Nothing runs by itself yet: no scheduler, no notifications. See `docs/reference/watches.md`.
- **Watches in Studio (#247, phase 0).**
  - A Watches view beside Semantics lists each watch with its question, condition, last outcome and when it last fired.
  - Check all, a Check button per watch and Stop. Rows show `queued`, `checking…` and their result live; a stopped check shows as `stopped` and is never recorded, and the watches checked before it keep their results.
  - A fired watch shows its reason in an alert panel, with the first rows it read for a `rows` check (kept in memory only), and an error shows its reason in its row.
  - Each watch has a history panel, and the nav entry shows a badge counting the watches whose newest check fired. A check run by `slaw watch check` refreshes a running Studio's list and badge.
  - New watch, Edit and Delete: a form that picks a saved query (with its declared parameters) or a metric and a condition, previews the file and saves it with a conflict check; Delete asks first and refuses if the file changed. A watch file changed outside Studio appears in the view.
  - The Export dialog offers watches.
- **Watches for the agent (#247, phase 0).**
  - New tools `list_watches` and `get_watch` (read-only, every tier) and `write_watch` (creates or replaces `watches/<name>.yaml`), for the in-app agent and MCP clients alike.
  - `write_watch` is a content tool like `write_artifact`: refused under `read-only`, asks first when "Ask before changes" is on, and, when nobody can approve (headless MCP), is checked before the gate (a bad shape, or a saved query or metric that does not exist, is refused with the reason; a valid request comes back "valid and ready, not saved" with the canonical file). With an approver the card comes first, as for the other content tools, and shows the canonical file. It validates with the same decoder and encoder as Studio's New watch form, replaces only against the `rev` it read, and warns when a required query parameter has no value.
  - The agent sees when a watch was last checked and its status, never a reason or any rows, and no tool or MCP method can check a watch: the person checks in Studio or with `slaw watch check`.
  - A JSON number that arrives with more than 15 significant digits, in a threshold or in `params`, is refused, since it has already been rounded. A number sent as a JSON number may have been rounded even so, so send long numbers as strings to keep them exact.
  - The `semantic-layer` skill says how to write a watch.

## [0.6.4] - 2026-11-06

Date labels on column charts, and settings that survive a corrupt file and two Slaw processes writing at once (#333, #334).

### Changed

- **Column charts with dated columns get a date axis (#333).**
  - It applies when every column label is a date, the columns are in strictly increasing date order and the chart is not `sorted`.
  - The axis shows labels such as `Apr 7`, `Feb '25` or `Q2 '25` instead of each label cut down to fit its column ("20…").
  - Each label is the date of the column it sits under, at the step's granularity, and is never finer than the spacing between columns.
  - The first and last columns are labelled when they fit. A label that would hang past an end of the chart is aligned to that end.
  - A narrow chart falls back to labels spread evenly across its columns, and a wide chart that scrolls is labelled along its whole width.
  - Hovering a column still shows its full label. Columns that are not dates are labelled as before.
- **Settings are written under a lock shared by Studio, `slaw update` and `slaw config updates`, in 0.6.4 and later (#334).**
  - Two Studios, or Studio and those commands, no longer drop each other's changes to recent workspaces, update settings or the per-workspace MCP choice.
  - The lock is a `settings.lock` file in the config folder, kept between runs.
  - A second Studio window opened earlier no longer puts back its own theme, UI scale or motion setting when you change another one: only the setting you changed is written. If two windows change the same setting, the later one wins.
  - Changing an appearance setting no longer writes back Studio's startup copy of the settings keys it does not know about, so keys another Slaw process (for example a newer version) wrote after the window opened are kept.
  - A write that waits more than 5 seconds for the lock fails and says another Slaw process is writing.

### Fixed

- **A corrupt `settings.json` is no longer overwritten with defaults (#334).**
  - Studio starts with default settings and shows a banner saying the file could not be read.
  - The next change moves the bad file to `settings.json.corrupt-<time>` in the same folder before writing, and says where.
  - A settings file that cannot be read at all (for example because of its permissions) is not replaced, and setting changes fail until it can be read.
  - See [troubleshooting](docs/troubleshooting.md#my-settings-were-reset-to-defaults), which also covers an unsafe `settings.lock`.

## [0.6.3] - 2026-11-06

Chart axes on round numbers and real dates, several series on one line or bar chart, and event markers (#13 items 1 and 2, #320).

**Opening in an older Slaw:** a notebook or data artifact view that uses `annotations`, `zero`, a `lineChart` `series`, or `columns`/`rows` on a line or bar chart does not load in Slaw 0.6.2 or older. There the whole A2UI cell is dropped, along with any other components in it.

### Added

- **Several series on one chart (#13).**
  - `lineChart` and `barChart` can draw several series on one shared scale and axis, with a legend. They read either a long `(label, series, value)` result or a wide one (a label, then two or more numeric columns, none named `value` or `total`).
  - Inline, `lineChart` takes `series: [{name, points}]` (up to 12 series of 500 points each), and both charts take `columns` + `rows`.
  - On a line chart, a missing point breaks the line rather than reading as zero, and hover and the keyboard readout list each series' value at that label.
  - A grouped bar's hover text and accessible name list every bar in the group.
  - Past six series, a line chart draws the six with the largest total and says "+N more not shown; filter in SQL". A bar chart keeps the five largest and sums the rest into "Other".
  - A notebook query cell with no pinned view now suggests LINE or BARS for a `(label, series, value)` result when it has:
    - 2 to 6 series;
    - at least two labels;
    - no repeated pair;
    - at most 200 rows.

    BARS past 25 labels falls back to TABLE. The agent's `preview_query` suggests the same.
- **Event markers on line, area and column charts (#320).**
  - Markers come from `annotations`, as inline `rows`, their own read-only `sql` (with an optional `connection`), or a named `query` in a data artifact view.
  - Each marker has these fields:
    - `at`;
    - a one-line `label`, at most 80 characters;
    - `kind`, `solid` or `dashed`;
    - a `color` from the closed colour list.
  - At most 50 are drawn. More than 50 inline is refused, and extra rows from SQL are dropped with a note.
  - Labels are stacked into up to four rows so they do not overlap. A label with no room is left off (its rule and tooltip stay), and the chart notes how many.
  - Hover shows the full label and its `at`. The chart's text alternative lists every marker drawn.
  - The marker SQL is held to the same read-only policy as the chart's own, including in `create_dashboard`. It counts against the notebook's limit on live components. A failed marker query leaves the chart in place with a note.

### Changed

- **Readable chart axes (#13).**
  - Gridlines fall on round steps (1, 2 or 5 × 10ⁿ, usually 4 or 5 of them).
  - Line and area charts start at zero for non-negative data; `zero: false` opts out.
  - On line and area charts, a date axis is spaced by real time and never labelled finer than the data (`Apr 3`, `Apr '25`, `Q2 '25`, `2025`). Dated rows are drawn in date order.
  - Line and area charts show the value under the pointer. Left, Right, Home and End step through the points, and the values are also announced to screen readers.
  - Bar charts get a value axis and draw negative bars from the zero line.
  - Column charts get a value axis with a round top tick, and a stacked column's total now sits on its stack.
- **Chart colours are darker in two built-in themes:** light `chart.2` and `chart.3`, and signal `chart.2`. Every built-in theme's six chart colours now pass 3:1 against the page and panel background, and a test checks it. Static HTML pages use the same new colours.
- **Existing charts may now draw several series.** These now draw a wide result as one series per column, and a `(label, series, value)` result as one series per series value:
  - a notebook cell pinned to BARS or LINE;
  - a `lineChart` or `barChart` bound to `sql`.

  Before, they drew only the second column. Up to six series are drawn. A two-column `(label, value)` result draws as before; to keep one series, return two columns.

### Fixed

- A bar or line chart whose second column is text (an attribute such as `SELECT day, top_product, sales`) now plots the next numeric column instead of drawing every value as zero.

## [0.6.2] - 2026-11-05

The rest of the first agent-experience report (#321): an MCP client that keeps working across Studio restarts, proposals that reach the inbox while "Ask before changes" is on, accepting a batch of proposals at once, and `slaw artifact render` for static pages.

### Added

- **`slaw semantics accept --all [--kind K]` and Studio's "Accept all" (#321).** The CLI prints every open proposal's diff, oldest first, and asks once (default No; `--yes` skips the question, and without it the command needs a terminal). Studio's inbox lists each proposal's kind, name, file and lines added and removed before you confirm. Both accept the versions they read (the CLI the ones it printed, Studio the ones read when you pressed Accept all), never overwrite, and stop at the first proposal that is stale or has changed or been decided since you saw it, saying what was accepted before it (stale ones are flagged before you confirm; one already decided elsewhere is left out of the list). A stale proposal is still accepted one at a time with `--overwrite --rev` or Studio's "Accept anyway". No agent tool or MCP method can accept.
- **`slaw artifact render <name> [--out dir] [-p name=value] [--force]` (#321).** Draws an allowed static `view: html` page, the way Studio's page frame does, into a folder: `index.html`, the host stylesheet `_slaw/theme.css` (the built-in light theme) and the page's stylesheets, images, fonts and JSON files (not its templates). Only a page you have allowed, unchanged since, is drawn; a page with script, an A2UI view or a script view is refused. Queries run through the same read-only, policy-checked path as in Studio, and `-p` values are bound as parameters. The written page carries a content security policy (no script, no network, its own files only). The folder defaults to `./<name>-render`. Nothing is written outside it, a folder that already has files in it needs `--force` (which overwrites only the files render writes), and a failed draw writes nothing.

### Changed

- **Studio remembers the MCP server per workspace (#96).** Press Start once (Settings → Workspace → Claude and other tools) and Studio starts the server again each time it opens that workspace on this computer; Stop turns that off. The choice is kept in your own settings, never in the workspace folder or a bundle, so a cloned or pulled workspace cannot turn it on. `slaw mcp serve` from an MCP client now keeps working across Studio restarts, and its error when the server is off says where the Start button is. A server that does not restart shows why in Settings, and the workspace still opens. A read-only window does not start it.
- **An agent's `propose_definition` reaches the inbox even while "Ask before changes" is on (#321).** A proposal changes nothing in `semantics/` until a person accepts it, so the inbox is the review; this holds for headless `slaw mcp serve` too. The sandbox tier still applies (read-only refuses a proposal), as do the inbox caps. A committed `ask_before_writes: true` does not cover proposals.
- When the sandbox is Full control and "Ask before changes" is still on, Studio's assistant settings say that every change still waits for your OK and that a tool connected through `slaw mcp serve` while Studio is closed is refused, and, when the setting is yours rather than the team's, that turning it off lets changes to the workspace's own files through (#321).
- The page window's theme stylesheet moved to `internal/pagetheme`, shared with `slaw artifact render`. Studio's page frame now says a scripted page is scripted, even before it is allowed.

## [0.6.1] - 2026-11-04

Fixes from the first agent-experience report on 0.6.0 (#321, #322): glossary proposals that no longer make one another stale, clearer refusals for agents, and Studio opening artifacts with `_` in their name.

### Changed

- **A refused agent write names the setting that stopped it.** When nobody can approve a write (headless `slaw mcp serve`), the refusal now names both settings and which one blocks, for example `Sandbox: read-write ("Change rows", allows this). Ask before changes: on (this is what stopped it)`, and notes that turning the switch off does not help when the team's `slaw.yaml` requires it (#321).
- A `write_artifact`, `save_query` or `build_query` refused only for approval now returns the result columns the save-time check read (`queries: [{name, columns}]` or `columns`), so an agent can check a view's column references without a write. Over MCP, an error's structured payload is also repeated as a text block, for clients that show the model only an error's text (#321).
- Page windows' theme stylesheet also sets `--slaw-theme-type: light|dark`, so a page that sets its own `color-scheme` can still tell the mode. `docs/reference/artifacts.md` documents it and links the colour roles (#321).
- `slaw conn add --read-only` writes `read_only: true` into `slaw.yaml`, so a teammate reading the committed file can see it (#321).

### Fixed

- **Glossary terms have their own revision (#321).** Every term used to share the glossary file's revision, so after one of several term proposals was accepted the others were stale and had to be filed again one at a time. `get_definition` with a term now returns the term's `rev` (`term-…`) plus `file_rev`; a proposal filed with either is held to the term, and edits to other terms no longer make it stale. Accepting a term (`slaw semantics accept` or Studio's inbox) is pinned to that term's section, so edits to other terms since you looked are kept, not refused. A proposal already in an inbox, filed with the file's rev, is still accepted while that rev is current, but goes stale once another term changes. `slaw semantics show glossary <term>` prints one term as stored, with its revision on stderr.
- Studio opens an artifact whose name contains `_` instead of saying "That is not a valid artifact name." The view now uses the same name rule as the New dialog and the store, and a test keeps the two in step (#322).
- `docs/reference/mcp.md` said an empty workspace's `list_queries` gives `[]`; every `list_*` tool returns an object. `docs/reference/agent-policy.md` now lists `save_query`, `build_query` and `propose_definition` among the writes validated before the approval gate (#321).

## [0.6.0] - 2026-11-03

**Vibe is now Slaw**, short for SandwichLabs analysis workspace. This release also adds a compile check and three assertion kinds (and `unique` on a metric) to the semantic layer, a built-in skill for building one, and a Stop button for running queries.

**Coming from Vibe:** `vibe update` cannot move you to Slaw. Uninstall Vibe with its own installer (`curl -fsSL https://github.com/SandwichLabs/slaw/releases/download/v0.5.0/install.sh | sh -s -- --uninstall`), then install Slaw (`curl -fsSL https://sandwichlabs.dev/slaw/install.sh | sh`). Each workspace needs a few files renamed by hand, including the `-- vibe:` header of every saved query, and credentials and allow decisions are asked for again. Every step, including how to keep your settings: [Moving from Vibe to Slaw](https://github.com/SandwichLabs/slaw/blob/v0.6.0/docs/migrating-from-vibe.md).

### Added

- **A compile check for the semantic layer (#302).** `slaw semantics check` and Studio's Run assertions now start every entity and metric with a `compile` row: the definition is vetted against the real connection (the read-only policy, then `EXPLAIN`) without scanning rows. A definition that does not compile is a `fail` (exit 1) and its data checks show "not run"; a connection that cannot connect is an `error` (exit 2). `slaw semantics check --compile` runs only these rows. A statement the policy refuses is now a compile `fail` (exit 1), not an `error` (exit 2).
- **New assertion kinds (#307):** `range` (`min`/`max`, on an entity column or a metric), `freshness` (`column`, `max_age` in whole days or hours such as `7d` or `36h`, on an entity) and `row_count` (`min_rows`); `unique` now also works on a metric, checking for fan-out under its grain. `freshness` and `row_count` add a `detail` line to the report and a `detail` field to `--json`. Values are bound parameters; `min` and `max` must be plain decimals.
- **A built-in `semantic-layer` skill (#302):** grain first, double-sourced rows, the assertion YAML shape, the `metric()` grammar, and propose-don't-write. The MCP instructions point agents at it.
- A Stop button for a running query in the Query Explorer, and for Try it, Run assertions and Profile in the Semantics view. Stop interrupts DuckDB mid-statement (about 10-40 ms in testing) and the run ends as a neutral "cancelled" line, not an error. Leaving the view, or closing or switching the workspace, also cancels a running query instead of waiting for it. New `CancelRun` binding (#5, #287).
- `slaw repl` is no longer hidden: the data console (Studio's Console tab) on a real terminal, with the same engine, gates, dot commands and history. [The console reference](https://github.com/SandwichLabs/slaw/blob/v0.6.0/docs/reference/console.md) describes it (#262).
- [Example workspaces](https://github.com/SandwichLabs/slaw/blob/v0.6.0/docs/examples/) (penguins, chinook, gapminder) showing the semantic layer, kept valid by a test in `internal/docsref`. The chinook example's `slaw semantics check` fails on purpose: its 30-day `freshness` check runs on data from 2013.

### Changed

- **Vibe is now Slaw.** The app, the `slaw` command and the Go module are renamed; there is no migration. Names that carried the old one change with it: `slaw.yaml`, `.slaw-local/`, `notebooks/*.slnb`, `.slaw` bundles, `SLAW_*` environment variables, the `slaw` keychain service and config folder, the saved-query header (`-- slaw:`), the page and script-view API (`slaw.run`, `slaw.params`, `slaw.use`, `slaw.lib`, `slaw.theme`, `slaw.state`, `slaw.download`, `slaw.ask`), the page theme file `_slaw/theme.css` and `html/slaw-lib/`, `--slaw-*` theme tokens, and the hash domain tags (so existing allow decisions and keychain bindings reset). `.vibe` bundles and notebook export files from Vibe cannot be imported. Steps: [Moving from Vibe to Slaw](https://github.com/SandwichLabs/slaw/blob/v0.6.0/docs/migrating-from-vibe.md).
- New wording across the README, CLI help, About and the launcher; the built-in AI is the Slaw assistant.
- The terminal UI's REPL (`slaw studio` without `--gui`) now draws results with the console's duckbox renderer (`repl.RenderDuckbox`) instead of its own plain-text grid, so both show the same boxed table with a type row, and a result over 1,000 rows reads "1000+ rows (1000 shown)" (#262). Its table, notebook and catalog-snapshot reads now run under the context passed to `tui.WithContext` instead of `context.Background()`.
- The Wails CLI pin in CI and release is now v2.17.0, matching `go.mod`, so `wails dev` and `wails build` no longer rewrite `go.mod` (#270).
- Dependabot no longer proposes minor or major `duckdb-go` bumps (they change the DuckDB engine version); engine upgrades are hand-run. A new workflow regenerates `THIRD_PARTY_NOTICES.md` on Dependabot dependency PRs, and the CI notices check says to run `task notices` (#293). CI fails when the Wails CLI pin and `go.mod` disagree.
- The `go-sql-sprintf-ident` ast-grep rule is now in `tools/ast-grep/rules/` at warning level (a review list of `fmt.Sprintf` SQL, not a failure), and the importer's last hand-quoted sequence name uses `QuoteLiteral`.

### Fixed

- `slaw semantics check` and `profile` reach MotherDuck connections, which they used to report as "not attached"; a connection that fails to connect now shows its own error (#302). When nothing passed or failed, `check` says "Nothing was verified".
- Command help links to the docs on GitHub for the build's own release tag instead of a path that exists only in the source repo. `slaw query --help` documents the `-- slaw:` saved-query header and `--saved ID -p name=value`. `slaw semantics list` prints a file's whole error, and some YAML errors now say to quote a value inside `{...}` that contains `, ( ) : #` (#302).
- `metric()` now works in a query export (the Query Explorer's export and `slaw export --sql`) and in the terminal UI's REPL, which used to fail with DuckDB's unknown-function error. An export also gets the editor's guard it used to skip (`INSTALL`, `LOAD`, secrets are refused with the editor's message). Every person-facing run path now prepares SQL in one place, `hub.PrepareForPerson` (#287). A file export now binds `$name` placeholders as a run does, so a `$word` inside exported text must be a parameter, as in the Query Explorer.
- **Artifact queries now stop at 16 MB of result (#193).** A shared or pulled artifact with a query like `SELECT repeat('x', 1000000) FROM range(100)` could fill Studio's memory, because only rows (500) and time were capped. The size is now counted while rows are read, for level 1 artifacts, live A2UI bindings, notebook SQL cells, script views and HTML pages (a page's old check ran after all rows were read), and a result over it fails with "the result is too large". A single huge row is still built once before it is refused, and DuckDB still builds its own result first.
- Two races behind flaky tests (#227). The file watcher no longer reports a store's own save or delete as an outside change: a store announces a write before it replaces the file, and the watcher now leaves a path alone until that write has landed (and re-checks a path whose write raced its hashing). Opening the hub lock no longer reports a phantom holder while another process is only reading who holds it (it retries for up to 250 ms before reporting a real holder). A decision that cannot be saved now logs its cause (#280).
- `task build:all` builds the macOS Studio app on macOS instead of always the Linux one.

### Security

- **The agent's `create_table` and `add_column` take literal defaults only (#21, #67).** A column type could carry `DEFAULT chr(39)||chr(120)`, `DEFAULT concat(...)` or `CHECK (...)`, expressions DuckDB would evaluate on every insert. The agent's column type may now hold a type, `PRIMARY KEY`, `NOT NULL`, `UNIQUE`, `REFERENCES t(col)` and a `DEFAULT` that is a number, a single-quoted string (`'it''s'`), `TRUE`, `FALSE`, `NULL`, `CURRENT_TIMESTAMP` or `CURRENT_DATE`. `CHECK`, generated columns (`AS (...)`, `GENERATED`) and every other default are refused with a message listing what is allowed. Studio's table designer and `slaw` for people are unchanged. This applies over MCP too.
- **Datasheet edits no longer trust the column type from the database's catalog (#21).** Insert, update and delete in the datasheet cast values with `CAST(? AS <type>)`, taking the type text from the attached database. A hostile database could return a type that is not just a type. The text is now checked against a small grammar (DECIMAL(10,2), `TIMESTAMP WITH TIME ZONE`, `INTEGER[]`, `STRUCT(...)`, `MAP(...)`, `UNION(...)`, enums with quoted labels all pass), and anything else is cast as `VARCHAR`. An import that appends to or upserts into an existing table checks the same way and stops, saying why, when a column's type fails.

### Known limitations

- A metric's `measure` cannot use `CAST(... AS ...)`, `extract(... FROM ...)` or `ORDER BY` inside an aggregate, and dimensions cannot be expressions (#287).
- When another process holds the workspace and Studio falls back to read-only, Studio's runs cannot reach MotherDuck.
- Unchanged: page windows' platform requirements and the macOS page network limitation (#189).

## [0.5.0] - 2026-10-29

This release adds a **semantic layer kept as files** (#244): named metrics, entities, joins and a glossary that live in the workspace next to your queries, run anywhere SQL runs, and change only when you accept the change. See `docs/reference/semantics.md`.

### Added

- **A semantic layer as files.** A workspace can now keep:
  - `semantics/entities/<name>.yaml`: what the data is about (grain, table, id column, PII flags);
  - `semantics/metrics/<name>.yaml`: a named measure over a saved query;
  - `semantics/joins/<name>.yaml`: how two entities relate;
  - `semantics/glossary.md`.

  Files are read as strictly as an artifact manifest: size and node caps before parsing, no anchors, aliases, tags or unknown keys, and names that never need SQL quoting. A file that cannot be read is listed with its error rather than hidden, and writes compare the revision they read. `vibe semantics list [--json]` and `vibe semantics show <kind> <name>` read them. `.vibe` bundles carry them (`vibe export bundle --semantics metrics/net_revenue`), and an import that skips a clashing saved query or entity warns when a metric or join from the same bundle refers to it.
- **`metric()` in SQL.** `SELECT * FROM metric('net_revenue', by => ['region', 'month'])` runs a metric as a grouped query over its saved query. It works in the Query Explorer, `vibe query` and the console, in notebook cells, A2UI `sql` bindings and artifact queries, and in the agent's SQL tools (`run_query`, `run_saved_query`, `preview_query`, `write_cell`). `run_query` and `run_saved_query` report the metrics they used in `metrics_used`. The call is expanded before any SQL check, using only names the files declare (nothing you type inside the call reaches the SQL), so the editor's check and the agent's policy both judge the expanded statement. Inside `metric('` and `by => '`, SQL completion offers metric names and dimensions, hover describes them, and `vibe lsp snapshot` carries them. The saved query's parameters take their declared defaults. A `$name` that only a metric's saved query declares cannot be used outside `metric()`.
- **Metric measures are limited to plain functions.** Every call in a measure must be to a DuckDB scalar or aggregate function on a generated allow-list. That rules out file readers, `getenv`, `query()`, sequences, settings, `sleep_ms`, `write_log`, `setseed`, `COLUMNS`, `UNNEST` and `metric` itself. No subquery or statement can start inside parentheses (`(PIVOT ...)`, `(TABLE t)`, `(VALUES ...)` are refused), and `*` is allowed only in `count(*)`. In the Query Explorer, `vibe query` and the console, each expanded metric is also run through the agent's read-only policy, so a metric from a cloned repository cannot read files outside the workspace.
- **Agents propose definitions, and you accept them.** Three agent tools reach the semantic layer from Studio's chat and over MCP:
  - `list_definitions` and `get_definition` work in every tier, so a read-only agent can use metrics; for a metric, `get_definition` also returns its saved query's parameters.
  - `propose_definition` works in `read-write` and `full`, like `save_query`.

  A proposal is checked against its kind's rules when it is made and filed in `.vibe-local/proposals/` (on your machine only, git-ignored, never in a bundle). Nothing in `semantics/` changes until you accept it, either in Studio or with `vibe semantics proposals [--all]`, `show-proposal <id>`, `accept <id> [--overwrite --rev <rev>]` and `reject <id>`. An accept writes exactly the version you were shown. If the file changed after the agent proposed, nothing is written until you accept anyway (in Studio, **Accept anyway**; in the CLI, `--overwrite` with the `--rev` that `show-proposal` printed). No agent tool, MCP method or approval can accept, reject or write under `semantics/`.
- **Check a metric and profile a join.**
  - `vibe semantics check [entity|metric [name]] [--json]` runs a definition's assertions as read-only counts of the rows that break them:
    - for an entity, `unique`, `not_null` and `accepted_values`;
    - for a metric, `non_negative` and `not_null`.

    It reports each as `pass`, `fail` or `error` (could not run). It exits 0 when everything passes, 1 when any check fails, and 2 when nothing failed but something could not run, or when the command could not start (an unknown kind or name, a bad flag, no workspace), so a script can tell a typo from a data failure.
  - `vibe semantics profile <join>` measures a join's row counts, orphans in both directions and fan-out, and records them in the join's `measured:` block (if the join file changes while it runs, nothing is written). It warns when the data contradicts the declared cardinality.

  Every statement goes through the agent's read-only policy at its tightest tier, and values travel only as bound parameters. Neither is an agent tool or MCP method.
- **The Semantics view in Studio.** A new Semantics entry in the sidebar lists the workspace's metrics, entities, joins and glossary, plus a proposals inbox. A live badge counts the proposals waiting.
  - A metric card shows the measure, the dimensions, the saved query it reads and how it was settled. **Try it** runs the metric by the dimensions you tick, and **Run assertions** checks it.
  - An entity card shows the grain, the table and its connection, the id column, the columns flagged as personal data, and **Run assertions**.
  - A join card shows the declared cardinality next to the measured one, warns when they disagree, and has **Profile**.
  - The inbox shows each proposal's rationale, evidence and warnings, with a line diff against the current file, and **Accept** and **Reject** buttons.

  The view changes a file only when you click **Accept** (the definition) or **Profile** (the join's `measured:` block). A definition edited on disk shows on its card at once.
- **Pages and views that read a metric ask again when it changes (#282).** A `view: script` or `view: html` page whose queries call `metric()` is allowed on a hash that also covers the metric file and its saved query. If either changes, for example after a `git pull`, the page asks again instead of silently showing something different. The allow prompt, `vibe artifact view status` and Settings > Workspace > Pages list the metrics a page reads. An allowed page's queries are pinned to the revisions it was allowed over. An open page window or view closes as soon as a metric or saved query it reads changes. Pages that call no metric keep their existing decisions.
- **Saved-query parameters in Studio and the CLI (#235).** When a saved query is open in the Query Explorer, its declared `$name` parameters appear as typed inputs, filled with their defaults. `vibe query --saved ID -p name=value` runs it from the terminal. Values are bound as parameters, never pasted into the SQL. The statement must pass the same shape checks as the agent's `run_saved_query`: one statement, no placeholder inside a literal, and no parameter as the argument of `query()` or a file reader.

### Changed

- An allowed `view: script` view now runs its queries from the manifest it was allowed with, as HTML pages already did, instead of re-reading `artifact.yaml` at each run.

### Known limitations

- Query-result export (Studio and `vibe export`) and the REPL in the terminal UI (`vibe studio` without `--gui`) do not expand `metric()` yet; they fail with DuckDB's unknown-function error. In the Query Explorer, `vibe query` and the console, a metric runs with its saved query's default parameters. Try it, Run assertions and Profile have no cancel button. Measures cannot yet use `CAST(... AS ...)`, `extract(... FROM ...)` or `ORDER BY` inside an aggregate, and dimensions cannot be expressions or time grains. All of these are tracked in #287.

## [0.4.0] - 2026-10-29

### Security

- **The SQL check could be made to skip a statement.** DuckDB nests block comments (`/* a /* b */ c */`), lets `$` continue a name (`x$a$`) and allows non-ASCII letters in names, and it reads `1._5` as the number `1.` followed by the name `_5`. Vibe's checks could disagree about where a comment or a dollar-quoted string ended, so text such as `SELECT 1 AS x$a$; CREATE SECRET ...; SELECT $a$` or `/* /* */ ' */ CREATE SECRET ...` hid a statement from the check while DuckDB still ran it. The `$` and number cases affected the Query Explorer, notebooks and the agent's SQL policy (and so MCP clients and data artifacts); the nested-comment case affected the Query Explorer and notebooks. Vibe's SQL lexers now read comments, names and numbers as DuckDB does; the two that gate SQL (the editor's check and the agent's policy) are tested against DuckDB itself.
- **Statement text is no longer written to logs.** The query log line carries a short hash, the byte length and the connection. DuckDB errors are logged without the `LINE n:` echo of the statement, with quoted text blanked.
- Log and history redaction now also catches short bearer tokens, API-key headers (`X-API-Key`, `X-Auth-Token`, `X-Amz-Security-Token` and others), JWTs and common key prefixes (`sk-`, `ghp_`/`gho_`, `glpat-`, `xoxb-`/`xoxp-`, `AKIA...`).

### Added

- **A data console in the Query Explorer (#255).** A Console tab next to the results grid gives a DuckDB-CLI-style prompt over the workspace hub: statements end with `;`, results draw as boxes, Ctrl+C cancels a running query, Tab completes names from the same catalog as autocomplete, and history is kept in `.vibe-local/repl/history` (statements that look like they hold a credential are never recorded). Dot commands include `.tables`, `.schema`, `.mode`, `.timer`, `.conn`, `.read` (run a saved query) and `.save` (save the last statement as a query). The connection picker switches the console's connection at an empty prompt. The console runs Vibe's own SQL path, with the same checks as the editor; it is never a shell. Values are printed with control and bidirectional-override characters removed. Up to four consoles can be open; they close when the workspace closes. See `docs/reference/console.md`.
- **SQL autocomplete in the Query Explorer and notebook SQL cells (#256).** Names are offered as you type (Ctrl+Space opens the list on demand): connections, schemas, tables, columns (through aliases), functions and keywords, from the same completer `vibe lsp` uses. Connected connections are live; others come from the saved snapshot. Completion never connects a connection or runs your SQL.

### Changed

- The Query Explorer's editor is now CodeMirror (it was a plain text box), with the notebook cells' look and Ctrl/Cmd+Enter to run.
- Studio now writes `.vibe-local/catalog.json` for `vibe lsp` while it owns the workspace.

### Fixed

- Nested block comments (`/* a /* b */ c */`) are now read as DuckDB reads them, so a valid statement after one is no longer refused (#275). (The same mismatch could also hide a statement; see Security.)
- `vibe lsp` no longer misreads dollar-quoted strings, `E'...'` strings, nested comments or `\r` line endings, and marks a capped completion list incomplete.

## [0.3.3] - 2026-10-26

### Security

- **The agent can no longer read a DuckLake's internal metadata through PRAGMA arguments.** `PRAGMA storage_info`, `PRAGMA table_info` and their `pragma_*` table-function and `CALL` forms took any table name, so pointed at a lake's internal catalog they returned its schema and value statistics (including the start of the lake's data path), although a direct `SELECT` of it was refused. Their argument must now be one string literal naming a table, and it gets the same checks as a table reference. `pragma_storage_info('t')` is now allowed alongside `PRAGMA storage_info`.

### Added

- **Notebook cell tools:** `add_markdown_cell`, `update_cell`, `delete_cell` and `move_cell`, guarded by the notebook `rev` (delete and update are marked destructive); `get_notebook` returns full a2ui pages, connections, views and the `rev`; `preview_query` is available in chat and MCP; `add_query_cell` refuses unknown views and multiple statements.
- **Saved queries for agents.** `save_query` and `build_query` are now content tools like artifacts and notebooks (read-write can save, checked before the approval gate), vet their SQL at save (one plain read, bound on its connection; the reply lists the result columns), refuse a duplicate title instead of silently forking it, update by `id` and `rev`, and can declare `$name` parameters with types and defaults. `list_queries` returns ids and revs, and the new `run_saved_query` runs a saved query by id with bound parameters under every `run_query` gate (reads only, in every tier). `build_query` keeps a number's exact digits and compares numeric columns with numbers.
- **A built-in `a2ui` skill** gives MCP clients the A2UI component schema (`read_skill a2ui`): every type and field, which are valid in a notebook dashboard and which only in a data artifact view (controls, `pivot`, `query`, `drill`). It is generated from the validator's registries when read, so it cannot drift. The `create_dashboard`, `present_data` and `write_artifact` descriptions, the `a2ui` help topic and the MCP instructions point to it.
- **`write_artifact` catches render-time mistakes at save.** The reply lists each query's result columns (`queries: [{name, columns: [{name, type}]}]`), and a pivot, drill or chart that names a column the query does not return, a page template that passes `date` a Go layout or reads a field of a row (`.activity`), an inline `<script>`, and a `view.js` with unbalanced brackets or unclosed strings are refused with the component path, the bad name and the columns available.

### Changed

- SQL errors for the agent: a bare keyword alias (`count(*) days`) gets a hint to write `AS days`; errors show the agent's own SQL instead of the policy's `EXPLAIN` wrapper, with a cast hint when a `$param` type is ambiguous; a `?` placeholder says parameters are named (`$name`); non-string `sql` says so; the PIVOT and UNPIVOT statement forms point to the `FROM ... PIVOT (...)` forms; `pg_catalog` views get a message pointing to `information_schema`; `CREATE TEMP TABLE` under read-write explains why it needs the full tier.
- `write_artifact` reports every problem in one reply (at most 20) instead of the first, accepts `view` and `manifest` as JSON text, and keeps a numeric or boolean parameter default as a number or boolean in `artifact.yaml` and `get_artifact`. Pages already on disk are not re-checked when loaded.

### Fixed

- The note that explains an empty result now resolves a column against the tables the statement reads (catalog, schema and alias), instead of the first table with that column name in the default catalog, so it no longer lists another table's values; it stays silent when the owner is ambiguous, skips remote connections, and also explains a `count(*)` that returned 0.
- `run_query` refuses a script that runs a read before its last statement (`SELECT 1; SELECT 2`), which silently returned only the last result; write scripts are unchanged.
- `create_dashboard` now vets every `sql`-bound component like `add_query_cell` does (read-only, binds against the schema) and refuses a bad column, a write or `getenv()`, listing each failure by component path, instead of saving a dashboard that breaks when it renders.

## [0.3.2] - 2026-10-25

### Fixed

- **Editing a skill no longer corrupts its `SKILL.md`.** `read_skill` put a trust preamble in front of the skill text, and an agent that edited the text and saved it back wrote the preamble into the file, one more copy per edit. `read_skill` now returns the preamble in its own `framing` field, and `create_or_update_skill` strips any copies before saving. ([#224](https://github.com/SandwichLabs/vibe/pull/224))
- **The in-app agent no longer stops silently partway through a task.** A chat turn was capped at 8 tool calls, fewer than building an artifact takes. It now gets 20, like the notebook cell dialog, is told its budget, and if it still runs out the reply ends with "Stopped after 20 steps ... Ask me to continue." ([#223](https://github.com/SandwichLabs/vibe/pull/223))
- Agent tool results return `[]` instead of `null` for empty lists (a zero-row query, no saved queries, no warnings). `create_record` no longer returns `last_insert_id`, which DuckDB always reported as 0. ([#224](https://github.com/SandwichLabs/vibe/pull/224))
- Messages and help that pointed to Settings → Workspace → MCP or "Settings -> Start MCP Server" now name the real place, Settings → Workspace → Claude and other tools. ([#224](https://github.com/SandwichLabs/vibe/pull/224))

### Changed

- **The in-app agent's system prompt is more than a third smaller** (about 7 KB less per turn): it no longer repeats each tool's description, which the model already receives with the tool. It now tells the agent the workspace's `data/` folder path, to alias every aggregate with `AS` (bare aliases like `days`, `name` or `type` are DuckDB syntax errors), and that `run_query` parameters are `$name`. The built-in `duckdb-sql` skill says the same. ([#223](https://github.com/SandwichLabs/vibe/pull/223))
- `help_search` with an empty query returns a short index of topics; asking for a topic by name returns just that topic. The component and view lists in the help are generated from the code, and there is a new `artifacts` topic. ([#224](https://github.com/SandwichLabs/vibe/pull/224))

### Security

- Studio's markdown sanitizer, DOMPurify, is updated to 3.4.17 (a critical `SAFE_FOR_TEMPLATES` bypass, plus a high and a low advisory), and the Echo web library that Wails uses is updated to 4.16.1 (a critical WebSocket-upgrade access-control bypass and three high advisories). ([#226](https://github.com/SandwichLabs/vibe/pull/226))

## [0.3.1] - 2026-10-24

### Fixed

- **The agent can read MotherDuck tables with `ORDER BY` again.** On DuckDB 1.5.6 the MotherDuck extension plans an ordered read with `BATCH_DOWNLOAD_SINK`, which the agent's SQL policy did not know, so `run_query` refused it as "a plan operator the agent's policy does not know". It and `BATCH_UPLOAD_SINK` are now allowed alongside the sinks they stand in for. ([#207](https://github.com/SandwichLabs/vibe/pull/207))

## [0.3.0] - 2026-10-24

### Added

- **A Library for notebooks, live views and pages.** Studio's Notebooks and Artifacts entries are now one **Library**: a single list with a type column, chips (All, Notebooks, Live views, Pages, and Chart scripts once you have one), a name filter and sorting. **New** asks what kind first, with the kinds side by side and described in plain words, then asks for a title or name; pages and chart scripts start from a small working example and open as source until you allow them. **Or describe it and let the agent build it** hands your description to the agent. Home shows Library counts by type, each opening the Library on that type. Dialogs now keep Tab and Shift+Tab focus inside them.

### Changed

- **`#/notebooks` and `#/artifacts` redirect to the Library**, keeping their query, so old links work. Storage, bundles, MCP tools and the CLI are unchanged (`vibe artifact list` still lists artifacts only). The `create_dashboard` tool description now says it adds a dashboard cell to a notebook and points to `write_artifact` for a live view.
- Dependency updates: the AWS SDK for Go, `fantasy` and other Go minor/patch releases ([#191](https://github.com/SandwichLabs/vibe/pull/191)); CodeMirror and Vega-Lite 6.5 in Studio ([#188](https://github.com/SandwichLabs/vibe/pull/188)).

### Known limitations

- Unchanged from 0.2.0: page windows' platform requirements, the macOS page network limitation ([#189](https://github.com/SandwichLabs/vibe/issues/189)) and no byte cap on artifact query results ([#193](https://github.com/SandwichLabs/vibe/issues/193)).

## [0.2.0] - 2026-10-23

### Added

- **Edit queries in your own editor, and a language server.** In the terminal UI, `ctrl+o` (or `\e`) opens the input in `$VISUAL`/`$EDITOR`, and `vibe query -e` does the same from the shell; the editor runs without a shell, on a private scratch file under `.vibe-local/edit/`. `vibe lsp` is a stdio Language Server for `.sql` files in a workspace (connections, schemas, tables, views, columns through aliases, DuckDB functions, hover). It reads a names-and-types snapshot, `.vibe-local/catalog.json`, that the TUI keeps current and `vibe lsp snapshot` writes on demand, so it opens no database and never takes the lock; completion is empty until one of them has written it. Neovim plugin, Helix and eglot setup in `editors/`. See `docs/reference/editor.md`. ([#112](https://github.com/SandwichLabs/vibe/pull/112))
- **Data artifacts.** A parameterised, live view stored as files in `artifacts/<name>/` (`artifact.yaml` with typed parameters and named queries, plus an A2UI `view.a2ui.json`). Parameter controls (select, date range, number, text), a `pivot` crosstab and drill-down re-run only the queries that use a changed parameter. Studio has an Artifacts list, a view and a Source tab with conflict handling; the agent and MCP clients author with `write_artifact`, `list_artifacts` and `get_artifact` (no tool runs an artifact); `.vibe` bundles carry them; `vibe artifact list` and `vibe export bundle --artifacts` mirror the UI. A value is only ever a bound parameter, and every query is checked against the read-only SQL policy when it is saved and again on every run, so a hand-edited, pulled or bundled artifact is held to it too. See `docs/reference/artifacts.md`. ([#162](https://github.com/SandwichLabs/vibe/issues/162))
- **Custom script views for data artifacts.** An artifact can name `view.js` instead of an A2UI view (`view: script`). The code runs in a terminable Web Worker under a deny-all CSP, gets rows only through the artifact's named queries, and returns an SVG (shown as an inert image) or an A2UI page that Go validates. It shows as source until you allow it; the decision is keyed by the folder's path and a hash of its files, kept in your config directory (`views.json`), and asked again after any change. The agent can never allow a view. `vibe artifact view status|allow|deny|revoke` mirrors Studio. ([#159](https://github.com/SandwichLabs/vibe/issues/159), [#160](https://github.com/SandwichLabs/vibe/issues/160))
- **HTML pages: Claude-style artifacts in a workspace.** An artifact can be a page of your own HTML (`view: html`, files under `html/`), bound to its named queries through a closed Go `html/template` function set; data is always escaped and a page never sends SQL. Pages are checked when they are saved (no network-reaching URL, no `<link>` but a local stylesheet, no `target`) and rendered with time, work and memory budgets.
  - **Static pages** render inside Studio in a script-less, sandboxed frame.
  - **Pages with script** open in their own window, run by a helper process that has no workspace, credentials or store. On Linux it runs inside bubblewrap with no network at all (a user and network namespace and an allow-listed filesystem view). On macOS it runs in a WKWebView behind a strict CSP, a navigation policy, a content rule list and a dead proxy.
  - **Capabilities** a page may declare: `state` (small notes kept on this computer), `download` (Studio's save dialog, only after a real click or key press) and `ask` (one completion on your model provider with no tools, behind its own toggle).
  - **The allow prompt** lists the page's files, every query it can run with its connection, its capabilities in plain words (naming the provider for `ask`) and, on macOS, the network limitation below. Settings > Workspace > Pages lists decisions, the `ask` toggle and recent model use.
  - The agent and MCP clients write pages with `write_artifact` (`html_files`) and read them with `get_artifact`; no tool renders, runs or allows a page. `.vibe` bundles carry pages, never a decision or a page's state. See `docs/reference/artifacts.md`. ([#162](https://github.com/SandwichLabs/vibe/issues/162))

### Fixed

- **The agent can no longer overwrite a skill it never read.** `create_or_update_skill` without a `rev` now only creates: an existing skill is refused with a message to `read_skill` it and send the `rev` it returned, instead of falling back on the store's last-seen revision (which any skills list in the process refreshed). A stale `rev` is still a conflict. Studio's and the CLI's own saves are unchanged. ([#166](https://github.com/SandwichLabs/vibe/issues/166))
- **Language server hardening.** `vibe lsp` drains and skips an over-size message (32 MiB) instead of exiting; it refuses a catalog snapshot that is a symbolic link, not a regular file you own, or over 64 MiB (a FIFO there no longer blocks it); and `vibe lsp snapshot -c NAME` exits non-zero for an unknown connection (before the hub is opened) and, after writing the others, for a named connection that could not be connected; a local file left to Studio or the TUI is not a failure. ([#179](https://github.com/SandwichLabs/vibe/issues/179))

### Changed

- **Go 1.27.3**, for a standard-library advisory (GO-2026-6809, distrusted intermediate certificates in `crypto/x509`). ([#190](https://github.com/SandwichLabs/vibe/pull/190))
- macOS text substitutions (auto-capitalisation, autocorrect) are off in Studio's name and parameter fields.

### Known limitations

- **Page windows have platform requirements.** On Linux they need a Wayland session, bubblewrap and unprivileged user namespaces; X11-only sessions, and Ubuntu with `apparmor_restrict_unprivileged_userns`, refuse with a message. On macOS they need macOS 14 or later.
- **On macOS a page with script can use a link to open a network connection** to an address it chooses (a script-clicked link to an IP address, with or without `target=_blank`); it cannot read a reply. The allow prompt says so, and the page-window helper shares Studio's keychain identity. A network-free replacement is tracked in [#189](https://github.com/SandwichLabs/vibe/issues/189).
- **Artifact query results have no byte cap** yet (rows are capped at 500): one very large cell is read in full. HTML pages cap what a template sees at 16 MB. ([#193](https://github.com/SandwichLabs/vibe/issues/193))

## [0.1.4] - 2026-10-16

### Added

- **Set up storage without leaving the lake.** When a DuckLake, Iceberg or Delta connection points at `s3://`, `gs://`, `r2://` or `az://`, the editor names the storage connection that will read it, and if there is none offers **Set up storage**, prefilled, then brings you back to the lake. `vibe conn add` prints the matching `vibe conn add` hint. S3 presets (Amazon S3, MinIO, Backblaze B2, Floci, other S3-compatible) fill in the endpoint, URL style and TLS fields. ([#130](https://github.com/SandwichLabs/vibe/issues/130))
- **Test a lake through its storage.** Test on a lake connects its storage connection first and says which of the two failed.
- **AWS SSO sign-in for S3 (preview).** An `s3` connection can sign in with an IAM Identity Center profile (`--sign-in aws-sso:PROFILE`, or the "AWS profile (SSO)" card). Studio shows a sign-in banner with a code; `vibe conn credentials sign-in NAME` does the same in a terminal, and `aws sso login` works too (shared `~/.aws/sso/cache`). Short-lived keys are renewed before they expire, and an ended session signs the connection out rather than reading anonymously. Not yet verified against a real Identity Center. ([#131](https://github.com/SandwichLabs/vibe/issues/131))

### Changed

- **`AWS_REGION` is no longer read.** An `s3` connection outside `us-east-1` that only worked because your shell set `AWS_REGION` now fails with an AWS signing error (`AuthorizationHeaderMalformed` or a 301). Set `region` on the connection (`--region`, or the Region field).
- **Wildcards on s3, gcs and r2 are expanded by Vibe.** `*` and `**` still work in `read_parquet`, `FROM '...'` and `glob()`; `?` and `[...]` are refused in a pattern, one statement expands to at most 10,000 objects, and the pattern must start with the scope of a ready storage connection. Azure is unchanged. ([#141](https://github.com/SandwichLabs/vibe/pull/141))
- **A `TEMP MACRO` or `TEMP FUNCTION` lasts only for the run that created it.** TEMP tables and views are unaffected.
- **The query editor, `vibe query` and the TUI refuse** `INSTALL`, `LOAD`, `UPDATE EXTENSIONS`, `CREATE`/`DROP SECRET`, and `SET`/`RESET`/`PRAGMA` of `s3_url_compatibility_mode`. Install extensions with the connection's Install button or `--install`.

### Security

- **S3 reads never use your shell's AWS credentials.** DuckDB copied `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` and `AWS_REGION` into every session, so a storage URL no connection covered was read as you. Every session now has a catch-all deny secret per scheme. ([#135](https://github.com/SandwichLabs/vibe/issues/135))
- **A URL can no longer redirect a connection's signed request.** `?s3_endpoint=` and the other `?s3_*` URL parameters, which can come from an Iceberg manifest or a DuckLake catalog, overrode the secret's endpoint; the whole URL is now the object key. ([#138](https://github.com/SandwichLabs/vibe/issues/138))

### Fixed

- **A lake could attach without its storage secret** when connections were re-registered concurrently (a regression from 0.1.3). ([#137](https://github.com/SandwichLabs/vibe/pull/137))

## [0.1.3] - 2026-10-15

### Added

- **DuckLake catalogs in SQLite, and in object storage.** The DuckLake type takes a `catalog` option (`duckdb` or `sqlite`) and a catalog that is a storage URL (read-only), so a lake whose SQLite catalog and Parquet files both live in an S3-compatible store attaches as two connections, with no SQL and no credential in any file. A catalog is vetted before it is attached. ([#118](https://github.com/SandwichLabs/vibe/issues/118))
- **Storage dependencies.** A DuckLake, Iceberg or Delta connection whose URLs are in object storage connects its storage connection first, and says `needs the "NAME" connection` when that is not ready.
- **Use TLS** on S3 connections (default on), for local S3-compatible stores such as MinIO and Floci.

### Changed

- **DuckDB 1.5.6.**
- The connection form no longer writes an option's default into the YAML; a blank choice reads "Default (…)".

## [0.1.2] - 2026-10-14

### Added

- **Update check and `vibe update`.** Studio asks once whether to check GitHub for new releases (**Check weekly** or **No thanks**; development builds never ask). Settings, About, Updates has the toggle, **Check now**, the release notes (shown as plain text) and, for a Vibe installed by `install.sh`, **Update to vX**: after you confirm, it downloads that release's `install.sh`, verifies it against the release's `checksums.txt` and runs it (never `--force`), then offers **Restart now** (through the normal quit path, so unsaved-work guards run; the old Studio keeps running until you do). `vibe update [--check] [--yes]` does the same from a terminal; `vibe config updates [off|weekly]` mirrors the toggle. Nothing is checked or installed in the background without your answer; builds from source, `go install` and package managers are told to update the way they were installed. What is sent is listed in [Privacy](docs/privacy.md).
- **Motion charts in A2UI.** Seven new chart types (`areaChart`, `columnChart`, `ringChart`, `radarChart`, `sankeyChart`, `heatmap`, `streamChart`), each with inline data or a live `sql` binding, and a closed `motion` object on every chart (easing preset, speed, stagger, spring tuning), off under reduced motion. ([#117](https://github.com/SandwichLabs/vibe/pull/117))

## [0.1.1] - 2026-10-13

### Fixed

- **MotherDuck reads from the agent and over MCP.** While a MotherDuck connection was attached, every agent query was refused (`plan operator DOWNLOAD_SOURCE (L) is not on the allow-list`), including plain aggregates over `data/` files. MotherDuck marks each step of a query plan with where it runs, and the policy now ignores that marker. ([#111](https://github.com/SandwichLabs/vibe/issues/111))
- **`introspect_schema` and `describe_table` connect on first use**, like `run_query`, instead of calling a configured but idle connection "unknown". A failed connect reports its own error.
- **Clearer refusals.** A name whose first part isn't a connection (`newsroom.raw.t`) now says there is no such connection, and a four-part name (`md.newsroom.raw.t`) says a table name has at most three parts. Both explain that each MotherDuck database needs its own connection (same token); see [connections](docs/connections.md#motherduck).

### Changed

- **The `read-write` tier can write notebook cells and skills** (`add_query_cell`, `create_dashboard`, `create_or_update_skill`), so an agent that can read your data can also write up what it finds. Creating or dropping tables and saving queries still need `full`, and "Ask before changes" still applies.

## [0.1.0] - 2026-10-12

The first public release. Vibe is an agentic desktop workspace for working with data where it already lives: connections keep your data in place, a workspace is a folder of plain files, and an agent (inside Studio or your own, over MCP) works with both under strict limits.

### Added

- **Workspaces.** A workspace is a folder (optionally a git repository) with `vibe.yaml`, `notebooks/`, `queries/`, `skills/` and `data/`, plus git-ignored machine-local state in `.vibe-local/`. Files are the source of truth, writes are compare-and-write, and a watcher picks up outside changes. Found by walking up from the current directory, like git. Starter templates (`contacts`, `inventory`, `order-entry`, `training-registrations`) via `vibe workspace init --template`.
- **Connections.** Local DuckDB and SQLite files, MotherDuck, Postgres, MySQL, S3, GCS, Cloudflare R2, Azure Blob/ADLS, DuckLake, Iceberg and Delta, all attached on first use into one in-memory DuckDB hub per workspace. The connection name is the catalog alias, so sources join in one query. Read-only by default, with a visible status per connection and scopes (shared, this computer, all workspaces). A guided editor in Studio and `vibe conn add | list | test | connect | credentials | move | rm`.
- **Credentials.** Kept in the OS keychain, or read from environment variables (`env:PREFIX`), or asked each time. Write-only across the Studio bridge, MCP and the agent. A workspace `.env` is loaded only after you allow it (direnv-style; `vibe workspace env`).
- **Live notebooks.** `sql`, `markdown` and `a2ui` cells; every data cell re-runs when the notebook opens and outputs are never saved. AI-assisted cell authoring (preview a query, then write the cell).
- **Skills.** `SKILL.md` documents that tell the agent what your data means; advertised by name and loaded on demand. Built-in skills are embedded.
- **A2UI artifacts.** Validated, SQL-bound dashboards (stats, tables, bar and line charts) the agent can draw, rendered with hand-written CSS and SVG.
- **Agent.** Studio's chat agent with sandbox tiers (`read-only`, `read-write`, `full`), an SQL allow-list policy, approval cards for writes, grounding checks and result budgets. Providers: a local OpenAI-compatible server (default), Ollama, OpenAI, Anthropic, Google Gemini and Cloudflare AI Gateway.
- **MCP.** `vibe mcp serve` over stdio or loopback SSE exposes the same tool registry, with the same gating, to Claude Code, Claude Desktop, Cursor and other clients. One owner per workspace: further servers proxy to the owner, Studio's approval cards apply to proxied writes, and Studio can take over from a headless server. Studio copies ready-made Claude Code and Claude Desktop config.
- **Studio.** The Wails desktop app: launcher with recent workspaces, a connections rail, query editor with saved queries, notebooks, datasheet editing, import wizard, skills editor, agent panel, settings and four built-in themes plus your own JSON themes.
- **Command line.** `vibe workspace init | info | env`, `query`, `import`, `export`, `conn`, `config llm | llm-key`, `studio`, `mcp serve`, and `init` as an alias.
- **Terminal UI.** `vibe studio` offers a SQL prompt, a read-only table browser and a notebook list.
- **Import and export.** CSV, TSV and Excel in with full-file type inference and a pre-flight check; CSV, Parquet, JSON and Excel out; notebooks as `.vnb`.
- **Bundles.** `.vibe` files are zip bundles of notebooks, queries, skills and (optionally) sanitised connection definitions, with optional age passphrase encryption. They never hold data or credentials.
- **Install and packaging.** `install.sh` for Linux and macOS (amd64 and arm64) with checksum verification, a manifest-based upgrade and uninstall, a `--cli-only` mode and a guard against overwriting another program named `vibe`. Linux tarballs ship desktop, MIME and icon files; macOS ships an ad-hoc-signed `Vibe Studio.app`.
- **Settings, About.** Version, commit and build date, licence, third-party notices, a privacy link and a Report a bug button.
- **Documentation.** Install, quickstart, connection recipes, MCP client setup, troubleshooting, privacy and architecture guides, alongside the reference.

### Security

- The agent's SQL is an **allow-list** parsed before any `EXPLAIN` binds it. A write needs three gates: the tier allows it, the connection is writable, and you approve it. Approval is always required for a connection that is not a local file, so headless MCP refuses those writes.
- A committed `vibe.yaml` can only **tighten** the agent's limits. Looser per-person settings and sign-in methods live outside the folder, keyed by workspace id and path. A saved credential is bound to the server it was entered for and is not sent to a changed one.
- DuckLake, Iceberg and Delta (formats whose metadata names the files to read) can only be defined in your own scopes, never in a committed `vibe.yaml`.
- DuckDB runs hardened: autoinstall and autoload off, community extensions off, an app-private extension directory, and extensions downloaded only when you ask for a feature that needs one (Install, `--install`, or an Excel import or export).
- MCP over SSE binds to `127.0.0.1`, requires a fresh random bearer token, and rejects browser `Origin` headers and non-loopback hosts. The lock file that records the endpoint is same-user only.
- No telemetry. See [docs/privacy.md](docs/privacy.md).

### Known limitations

- **macOS builds are unsigned.** They are ad-hoc signed, not notarised. A browser-downloaded app needs System Settings, Privacy & Security, **Open Anyway**; the install script avoids the prompt. The macOS minimums (14 on Apple silicon, 13 on Intel) come from linked libraries and have not been run on those macOS versions. Permission prompts for Documents or Desktop may reappear after an upgrade.
- **No Windows build.**
- **Single-user.** Sharing is through shared backends and git. There is no multi-user mesh.
- **The chat agent needs a model.** A local server is the default endpoint; hosted providers need your own API key. Everything else works without a model.
- **Extensions need the network once.** DuckDB extensions are downloaded on first use of each connection type (on Install, `--install`, or the first Excel import or export) from `extensions.duckdb.org`; none ship inside the app, and there is no Remove button yet. See [troubleshooting](docs/troubleshooting.md#extension-download-blocked-or-offline) for offline use.
- **MotherDuck** cannot be attached read-only (use a read-scaling token) and is limited to one account per workspace.
- **The agent cannot read Iceberg or Delta tables** yet; you can in the SQL editor. DuckLake supports only a local DuckDB metadata catalog, and Iceberg REST catalogs are not supported.
- **Notebook export** to HTML, PDF or xlsx is not available yet.
- Older data-holding `.vibe` (SQLCipher) files are not opened.
- Linux builds target glibc 2.35 or newer and WebKitGTK 4.1; musl and older distributions are unsupported.

[Unreleased]: https://github.com/SandwichLabs/slaw/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/SandwichLabs/slaw/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/SandwichLabs/slaw/compare/v0.6.4...v0.7.0
[0.6.4]: https://github.com/SandwichLabs/slaw/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/SandwichLabs/slaw/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/SandwichLabs/slaw/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/SandwichLabs/slaw/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/SandwichLabs/slaw/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/SandwichLabs/vibe/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/SandwichLabs/vibe/compare/v0.3.3...v0.4.0
[0.3.3]: https://github.com/SandwichLabs/vibe/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/SandwichLabs/vibe/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/SandwichLabs/vibe/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/SandwichLabs/vibe/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/SandwichLabs/vibe/compare/v0.1.4...v0.2.0
[0.1.4]: https://github.com/SandwichLabs/vibe/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/SandwichLabs/vibe/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/SandwichLabs/vibe/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/SandwichLabs/vibe/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/SandwichLabs/vibe/releases/tag/v0.1.0
