# Data artifacts

A **data artifact** is a computation plus a view, stored as plain files in the workspace. It stores questions (SQL and parameters), never answers: every query runs when the artifact opens and again when a parameter changes.

```
artifacts/<name>/
├── artifact.yaml        title, typed parameters with defaults, NAMED queries over them
└── view.a2ui.json       a declarative A2UI view (closed schema, validated)    (view: a2ui, the default)
    or view.js           a custom view, run in a worker after you allow it     (view: script)
    or html/             an HTML page (templates, CSS, JS, assets)             (view: html; static pages render in Studio)
```

An artifact has exactly one view, the one its `view:` names (a file, or for `view: html` the `html/` folder). Both existing, or the wrong one for the kind, makes the folder broken (it lists with an error). There is no fallback A2UI page for a script view: before you allow it, the artifact opens as source. See [Custom views](#custom-views-view-script).

The folder name is the artifact's name. This page describes the file formats and the view rules; the plan and its decisions are in [`../plans/data-artifacts.md`](../plans/data-artifacts.md).

## The manifest: `artifact.yaml`

```yaml
version: 1
title: Regional sales
description: Revenue by region, with drill-down to customers.
parameters:
  - name: from
    type: date
    default: today-90d
    label: From
  - name: to
    type: date
    default: today
  - name: region
    type: enum
    options_query: regions
    default: ""
queries:
  - name: regions
    connection: prod_pg
    sql: SELECT DISTINCT region FROM sales.orders ORDER BY 1
  - name: by_region
    connection: prod_pg
    sql: |
      SELECT region, sum(amount) AS revenue
      FROM sales.orders
      WHERE order_date BETWEEN $from AND $to
      GROUP BY ALL ORDER BY revenue DESC
view: a2ui
```

The file is decoded strictly: an unknown key is an error, and there is no `${ENV}` interpolation. Parameter and query names start with a letter, then letters, digits and underscores (at most 64 characters), and are unique without regard to case. A query takes up to 64 KB of SQL and an optional `connection`. Every `$name` in a query must be a declared parameter; which parameters a query uses is derived from its SQL. A value is bound as a parameter and never becomes SQL text.

### Parameter types

| Type | Value | Constraints |
|---|---|---|
| `text` | string | `max_length` (default and cap 1,000) |
| `number` | number | `min`, `max` |
| `integer` | whole number within +/-2^53 | `min`, `max` |
| `date` | `YYYY-MM-DD` | none; default may be relative (below) |
| `boolean` | `true` or `false` | none |
| `enum` | string | exactly one of `options` (up to 200) or `options_query` (a query whose first column supplies the options; it may not use the parameter it feeds) |

Every parameter has a `default`, so an artifact opens without input. In a query, write `$from::DATE` where a date is used in a function or arithmetic; a plain comparison (`BETWEEN $from AND $to`) needs no cast.

### Date defaults

A `date` default is a literal `YYYY-MM-DD` or one of a closed set, resolved on the local clock each time the artifact runs: `today`, `today-<n>d` (1 to 3660 days back), `start-of-month`, `start-of-year`.

### Caps

`artifact.yaml` 256 KB, `view.a2ui.json` 1 MB, `view.js` 1 MB (valid UTF-8, not empty, and none of the characters that can make the code differ from what is shown: control characters other than tab, newline and carriage return, DEL, C1 controls, the bidi controls U+202A-202E and U+2066-2069, the line and paragraph separators U+2028/2029 (JavaScript ends a line comment at them), U+200E/200F, U+061C, the soft hyphen U+00AD, the zero-width characters U+200B-200D, U+2060 and U+FEFF, and U+FFF9-FFFC; `artifact.yaml` refuses the same controls, bidi characters and separators), 20 queries, 50 parameters, 200 view components.

## The view: `view.a2ui.json`

The view is an [A2UI](notebooks.md#a2ui-component-reference) page, `{ title, components[] }`, validated against its manifest. The schema is closed (unknown fields are errors) and no view field is a color, style or class: labels and column names are shown as text only. Errors are JSON-path-prefixed, as for any A2UI page.

- **Data comes only from named queries.** A data-bound component (`stat`, `table`, and the charts) takes `query: "<name>"` instead of `sql`. `sql` and `connection` are refused in a view (the query holds the connection). `query` must name a query of the manifest. Inline data (`rows`, `bars`, `points`) is still allowed.
- `refresh` (a timer re-run of a `sql` binding) is refused on a query-bound component: a view's queries re-run when a parameter they use changes.
- **Event markers.** A `lineChart`, `areaChart` or `columnChart` may carry `annotations` (labelled vertical rules for releases, changes, incidents): inline `{rows: [{at, label, kind, color}]}`, or `{query: "<name>"}` naming a manifest query that returns `(at, label[, kind, color])`. `annotations.sql` and `annotations.connection` are refused in a view like every other `sql`. The query must return at least two columns (checked with the result columns when the artifact is saved). At most 50 markers. See [`notebooks.md`](notebooks.md#event-markers-annotations).
- **The view-only types** (listed in the [component reference](notebooks.md#artifact-view-components)) are refused in notebooks, chat and `create_dashboard`.
- Every other component type behaves as in [`notebooks.md`](notebooks.md).

### Controls

Each control names its parameter; the manifest holds the semantics (type, options, default).

| Control | Fields | Parameter type |
|---|---|---|
| `select` | `param`, `label` | `enum` (options static or from `options_query`) or `boolean` |
| `dateRange` | `start`, `end`, `label` | both `date` |
| `numberInput` | `param`, `label` | `number` or `integer` |
| `textInput` | `param`, `label` | `text` |

A control whose parameter is not declared, or is of another type, is a validation error. Changing a control re-runs only the queries that use its parameter.

### `pivot`

`{query, rows, columns, value, label?, drill?}` lays out a **long-form** result (one row per row-key and column-key, already aggregated with `GROUP BY` in the query) as a crosstab: `rows` (1 to 8 result column names) are the row headers, `columns` names the column whose values become the column headers, `value` the column shown in each cell. The three roles must use different columns. The component never aggregates: totals come from the SQL (for example `GROUP BY ROLLUP`) and are shown as returned. Two result rows for the same cell is a render error that says to group by those columns. Column names are checked against the result at render, since they are only known after a run. A null key reads `(blank)` and an empty-string key `(empty)`; rows and columns keep the order of first appearance in the result, which the query's `ORDER BY` controls. The grid is a real `<table>` (a column-group header, column headers, row headers); every header and cell is plain text, and a long header is clipped and carries its full text as a tooltip. A truncated result (500 rows) is labelled above the grid.

### Drill-down

`drill: {param, column}` on `table`, `barChart`, `columnChart` and `pivot`. The component must be bound with `query` (a `drill` on inline data is a validation error): clicking a row sets `param` (any declared parameter) to that row's `column` value, validated like any other value, and re-runs the queries that use it. `column` is checked against the result at render: a column that is not there shows an error and turns drill off for that component.

The clicked row's value is offered to the parameter exactly as a control's value is: it must pass the parameter's own rules (a value outside a static `enum`, a number past `max`, a NULL, all refused). A refused value shows the parameter's inline error (and a note under the component), changes nothing and runs nothing. A number is read as text for a `text` or `enum` parameter, and a timestamp as its date for a `date` parameter. The parameter's control shows the drilled value and its **Reset**. The row whose value is the parameter's current value is marked (`aria-current`; on a pivot, the row header when the drill column is a row key); nothing is marked when every row matches. A component keeps showing its previous result while its query re-runs, so keyboard focus stays where it was.

Keyboard: a drillable table row, bar, column or pivot cell is reached with Tab (one stop per component) and the arrow keys, and **Enter** or **Space** drills. On a `pivot`, drill is on the cells (a cell is one result row); on a `table`, the first cell of each row holds the button.

## In Studio

**Library** in the sidebar lists every notebook and artifact together (`#/library`), with a type column and chips: All, Notebooks, Live views (A2UI artifacts), Pages (HTML artifacts) and, once one exists, Chart scripts (`view: script`). A folder that cannot be read lists with its error (under **All**). Opening an artifact (`#/library/artifact/<name>`) shows the view with its controls. Each named query runs once for every component bound to it, and a changed parameter re-runs only the queries that use it. A bad value shows an inline error and runs nothing; **Reset** restores a parameter's default. Results are capped at 500 rows (the view says so when a query hit the cap). A broken artifact opens to its errors, and the list and view update when either file changes on disk (an editor, git, an agent over MCP).

**New** (on the Library) first asks what kind: Notebook, Live view or Page side by side, with Chart script under **Advanced** (`#/library?new=<type>` preselects one; Home's New notebook does). A Page has a *starts with a script* box. Step 2 asks for a title (notebook) or a name (an artifact) and shows what it creates; a row at the bottom, **Or describe it and let the agent build it**, hands the chosen type to the agent panel as an unsent request naming `write_artifact` (for a notebook it opens the AI cell dialog instead). For an artifact, New creates `artifacts/<name>/` from a small working example (two date parameters, one query, a date-range control and a stat); a name already in use is refused. The `NewArtifact(name, kind)` binding that does it (the old `#/artifacts` and `#/notebooks` paths redirect to the Library) knows four starters, each sharing that manifest body: `a2ui` (the live view above, also the empty kind), `html` (a static page: `html/index.html.tmpl` with the dates and a table over the `span` query, plus `html/assets/style.css`, which uses the theme's `--slaw-*` roles in the page window and falls back to system colours in a static page's frame), `html-script` (the same page plus `html/app.js`, which calls `slaw.run` again and updates a cell; no capabilities) and `script` (a `view.js` that draws a one-bar SVG). Any other kind is refused with that list. A starter holds no network address and passes the same save-time checks as any other file; the page and script starters start undecided, so they open as source with **Allow** like any other (nothing is allowed for you). The name is 1 to 64 lowercase letters, digits, `-` or `_`.

The **Source** tab edits the two files as text in a code editor (**Save**, or Ctrl/Cmd+S). It shows the files as they are on disk, so a broken artifact can be opened there and fixed. Save writes both files exactly as typed (comments and layout in `artifact.yaml` are kept) and sends the one revision over both. What happens depends on what is wrong:

- **A structural error** (a bad manifest, a view that does not match it, a file over its cap) refuses the save and says why; nothing is written.
- **A query the agent's read policy refuses** (a write statement, a parameter used as a file path, an unknown connection) does not refuse the save: the artifact is saved, and the policy's reason for each query is listed as a warning. The run-time gate is the same either way, and a refused run shows only a fixed message, so this list is where the reason is. A person must be able to save while a source is offline.
- **A stale revision** (either file changed on disk since it was opened, for example by an agent's `write_artifact`) is a conflict and writes nothing. The conflict banner offers **Reload from disk**, **Keep mine** (replaces only the revision the banner was raised against; a file that changed again is a fresh conflict) and **Save mine as a copy** (`<name>-copy`, then `<name>-copy-2` and so on; never over an existing artifact). An outside change to untouched text is followed quietly. If the artifact was deleted on disk, **Keep mine** writes it back.

The two files are written one after the other (the view, then the manifest), each atomically, and a save refused midway puts the old view back. A crash between the two leaves a pair that fails validation: it lists with its error and does not run, and the Source tab opens it for fixing.

Leaving the Source tab with unsaved text asks first. In a warning, the workspace's absolute path is shown as `<workspace>`. **Keep mine** needs the revision the banner was raised against; without one (and with the artifact present) it is a conflict, and the banner fetches it again. **Save mine as a copy** skips a `-copy` name that is taken or unusable (a link, a plain file) and uses the next.

**Delete** (on the Source tab, after a confirmation) removes both files and the folder, but only if they are still at the revision the tab shows: an artifact edited meanwhile is a conflict and stays.

## Authoring with the agent

The agent (Studio chat or an MCP client) writes artifacts with `write_artifact` and reads them with `list_artifacts` and `get_artifact` (see [mcp.md](mcp.md#data-artifact-tools) and [agent-policy.md](agent-policy.md#data-artifacts)); the built-in `data-artifacts` skill (`read_skill`) teaches the format. Every query is held to the agent's read policy when it is saved and again on every run, so an artifact written by hand, by git or by a bundle is held to it too. The tools never return query results.

**Save-time feedback.** `write_artifact` checks everything it can before it writes, in one pass, and returns every finding together (numbered, at most 20 and then "and N more"; a request that fails any check writes nothing). In a workspace that asks before the agent writes, the same checks run before the approval gate (the dry run), so a request that only lacks approval has already passed them. The checks:

- **Manifest** (`artifacts.ValidateAll`: every header, parameter, query and `options_query` problem, not only the first), the **view** against the manifest, and the **SQL policy** per query.
- **Result columns.** For every query that passes the policy, the columns it returns are read by binding it under the policy that just passed it, with its parameters at their defaults, wrapped as `SELECT * FROM (...\n) LIMIT 0` (the newline keeps a trailing `--` comment from swallowing the parenthesis; the probe has the same 30-second limit as a preview): the engine plans the statement and returns the column list, and no row is read. The reply carries `queries: [{name, columns: [{name, type}]}]` (DuckDB type names). `get_artifact` does not return columns (reading them binds the queries, which can attach a connection, and `get_artifact` is a read-only introspection tool); save again to see them.
- **Column references** (`a2ui.CheckViewColumns`, via `artifacts.CheckViewShape`, A2UI views only). A pivot's `rows`, `columns` and `value`, and any `drill.column`, must name a column of the component's query. A chart reads its columns by position, so a data-bound component needs a minimum column count (a `lineChart` or `barChart` with several series reads `(label, series, value)` or a numeric column per series, see [`notebooks.md`](notebooks.md#several-series-on-a-line-or-bar-chart)): `barChart`, `lineChart`, `areaChart`, `columnChart`, `radarChart`, `ringChart` 2; `stat`, `streamChart` 1; `heatmap`, `sankeyChart` 3. An error names the component path (`components[1].rows[0]`), the bad name and the columns available. A script view's page is only known when it runs, so it is not checked.
- **Pages** (`artifacts.VetPageForWrite`, run for fresh content: the agent's `write_artifact` and Studio's New. The store itself, bundle import, conflict resolution and loading a stored page run `VetPage` without these rules, so a page already on disk or in a bundle still lists, loads, imports and can be repaired): a literal `date` layout must be `iso`, `short`, `long` or `month`; a field read on a query row (`{{ .name }}` inside `range (query "q").Rows`, or on a variable holding a row) is refused because a row is an array (`{{ index . 0 }}`), as is a field other than `Rows`, `Columns` or `Truncated` on a result. The check follows `query`, `.Rows`, `index`, `range`, `with` and variables bound to them, and says nothing about a value it cannot trace. An inline `<script>` (no `src`, and no data `type` such as `application/json`) is refused: the page's CSP has no inline script, so it could never run.
- **Script views** (`artifacts.CheckScriptSyntax`, on the agent's write only): unbalanced or mismatched `()`, `[]`, `{}`, an unterminated string, template literal, regular expression or `/* */` comment, or no `draw` at all. This is a structural scan, not a parser: no pure-Go JavaScript parser is a dependency, and one that lags the webview's syntax would refuse valid scripts.
- **Forms.** `view` and `manifest` accept an object or its JSON text. A parameter default keeps its type in `artifact.yaml` (`default: 0` for an integer, `true` for a boolean; a number that would be written with an exponent, or a text that reads as another type, stays quoted) and in `get_artifact`; a file with every default quoted, as older versions wrote them, still loads.

## Running an artifact

A run belongs to the person's UI (Studio's `RunArtifactQuery` binding); no agent tool and no MCP method runs one. The steps, in order:

1. The artifact is loaded from the store, so what runs is what is on disk.
2. `artifacts.Bind` turns the query name and the raw values into the manifest's SQL text, unchanged, plus bound parameters. A value of the wrong shape (a date that is not one, a number past `max`, a value outside an `enum`, a name that is no parameter) is refused here.
3. The save-time checks repeat on the statement and its connection (one statement, every `$name` a parameter token, no parameter as the argument of `query(...)`, `query_table(...)` or a file reader), and the read-only SQL policy classifies the statement with the actual values. A policy refusal shows the fixed "live bindings are read-only" text, and its reason goes to the debug log and to the Source tab's warnings. The exceptions are reasons that hold in every tier: an empty statement, or one DuckDB cannot parse or bind (a misspelt column), shows that reason itself. A lexical, binding or connection refusal shows its own short reason (never the SQL).
4. The statement runs read-only in every sandbox tier (a tier changes what the agent may *write*, not what an artifact may), at most 30 seconds, and returns at most 500 rows and 16 MB of data. The size is counted while rows are read (text and bytes by length, other values a fixed cost, lists and maps by their elements), and the read stops at the first row that takes the result over it, with "the result is too large". The same cap applies to a live A2UI binding, a notebook SQL cell, a script view's `slaw.run` and a page's queries. It bounds what is kept across rows; one enormous row (`repeat('x', 100000000)`) is still built once before it is refused, and DuckDB builds its own result first (limited by its memory limit).

Because step 3 runs every time, a manifest edited by hand, pulled from git or imported from a bundle is held to the same policy as one the agent saved. Nothing is trusted because it "was vetted once".

## Sharing

- **Git.** `artifacts/` is ordinary workspace content; commit it with the rest.
- **Bundles.** A `.slaw` bundle carries artifacts as two entries each (`artifacts/<name>/artifact.yaml` and its view file, `view.a2ui.json` or `view.js`). An allow never travels. Import checks structure only; the SQL is vetted when it runs ([workspaces.md](workspaces.md#sharing-without-git-slaw-bundles)).
- **CLI.** `slaw artifact list [--json]` mirrors the Library's artifact kinds (live views, pages and chart scripts; not notebooks) (name, title, view kind, error; never SQL). `slaw artifact view status|allow|deny|revoke <name>` decides on a script view. `slaw export bundle --artifacts a,b` exports. There is no CLI run or write.

## Credentials

An artifact holds no credential. A query names a connection; the connection's sign-in is resolved by the session as for any query. A connection password or `.env` value never appears in an artifact error, in `get_artifact` or `list_artifacts`, in a log line or in a bundle (an end-to-end test audits each, `internal/hub/artifacts_e2e_test.go`; it covers credentialed connections that fail before attaching, since no network or database extension is available to a plain `go test`).

## Custom views: `view: script`

A manifest that says `view: script` draws itself with `view.js` instead of an A2UI page, for charts the A2UI components cannot draw. The spike ([plan](../plans/data-artifacts.md#spike-custom-view-isolation-run-2026-10-17)) ruled out an iframe (a sandboxed frame can reach Wails' binding IPC, [#174](https://github.com/SandwichLabs/slaw/issues/174)) and chose a dedicated, terminable Web Worker: it receives rows only through `run(queryName, params)` and returns either an SVG document string (shown as an inert image) or an A2UI page (validated by Go first). A `view.html` is not a thing: a bundle that holds one is refused as an unknown entry.

**Status.** Built: trust (below), the view host and the bridge ([The view host](#the-view-host-and-the-worker-contract)).

**Nothing runs until you allow it.** The code in `view.js` is written by someone else or by the Slaw assistant, so the artifact opens as **source**: a read-only code view of `view.js` and of `artifact.yaml`, with **Allow** and **Deny** and the line "This view runs code written by someone else or by the Slaw assistant. It cannot reach the network or your files, and can only run this artifact's queries."

- **What an allow covers.** Allow is keyed by the artifact folder's canonical absolute path (symlinks resolved; on macOS and Windows spelled as the file system spells it) plus the `ViewHash`: a sha256 over a version-tagged (`slaw-view-hash/1`), length-prefixed list, entry 0 `artifact.yaml`, then each view file as (path, bytes) sorted by path. Any change to `artifact.yaml` (a query's SQL or connection included) or to `view.js` ends the allow: the view says it **changed since you allowed it** and asks again. Bytes moved from one file to the other change the hash. A clone of the folder at another path, or a copy under another name, is undecided.
- **Metrics it reads (#282).** When a query's SQL calls `metric('name')` (see [the semantic layer](semantics.md)), the metric's file and its saved query are not part of `view.js` or `artifact.yaml`, so a `git pull` could change what an allowed view reads without asking. The key is therefore `artifacts.DepsHash` (tag `slaw-deps-hash/1`): a sha256 over the `ViewHash` and, sorted, the revision of each called metric file and of the saved query it names (`missing` for one that does not exist, so the day it appears the view asks again). It applies **only to a view whose queries call `metric()`**: a view that calls none keeps its plain `ViewHash`, so every existing decision stays valid. The prompt (and `slaw artifact view status`) lists the metrics it reads with their saved queries and says so, and Settings > Workspace > Pages shows them beside the decision. Hosts re-derive the plain hash from the bytes they hold (`ArtifactViewStatus.ContentHash`) and check the decision against the folded one. For a scripted page or script view every query counts (any can be run); for a static page only the queries its templates use. The allow also records the hash of the view's own files, so when only a metric moved the prompt and Settings say "a metric it reads changed" instead of "the view changed" (decisions made before this note existed have none and behave exactly as before). **A run reads only what was allowed:** the view's and page's queries run with the allowed metric and saved-query revisions pinned (`semantics.WithPins`); the expander refuses any other ("a metric this page reads changed ... allow the page again"), whatever a verdict cache says. And Studio re-checks every open page window and view grant when a `semantics/` or `queries/` file changes on disk and after its own accept, save and profile, closing the ones no longer allowed and telling the trust screen. The SQL policy still vets the expanded statement at every run: this is about your consent, not access.
- **What you saw is what you allow.** Allow sends the hash of the bytes on screen, and Go refuses it if the files differ by then. If the files change while you are reading (a reload with a different hash while Allow is armed), Studio keeps showing what you were reading, says "This view changed while you were reading it", and keeps Allow off until you click "Show the new version". The read-only editors draw any remaining control or invisible character as a visible marker, and the CLI prints them as `<U+XXXX>`. The view host serves the bytes it hashed (held in memory under the view's token), never a second read.
- **Deny** holds for that folder until revoked, across edits.
- **Where decisions live.** In `views.json` beside `dotenv.json` in your own config directory (the same JSON format as `dotenv.json`). Never in the workspace folder, never in a bundle, never in git. **Settings > Workspace > Custom views** lists them with **Revoke**.
- **Agent edits ask again.** The agent can write a script view (`write_artifact`'s `view_source`) but cannot allow one: there is no tool, MCP method or other path that allows, denies or revokes. Every edit, by anyone, asks again.

**CLI.** `slaw artifact view status|allow|deny|revoke <name>` mirrors the prompt and Settings (as `slaw workspace env` does for `.env`). `status` prints the state, the hash and the source. `allow` shows the source and asks on a terminal; without one it needs `--hash <hash from status> --yes` (you read the source) and is refused if the files changed since. That is scriptable by design: a process that can run your shell can also edit `views.json` directly, so an agent with a shell is outside this feature's threat model. What the feature stops is the agent's tools, MCP and a pulled file allowing a view; it does not stop a program running as you. `slaw artifact list` shows the kind (`script`).

**Sharing.** A script view travels in a bundle as `artifacts/<name>/artifact.yaml` plus `artifacts/<name>/view.js` (the view file must be the one the manifest names), and arrives undecided on the other machine.

**Writing one.** The `data-artifacts` skill (what the agent reads) and [the contract below](#the-view-host-and-the-worker-contract) describe it: define `draw`, call `slaw.run(queryName, params)` for rows, return an SVG string or an A2UI page.

## The view host and the worker contract

An allowed view runs in a dedicated Web Worker, never an iframe. Studio's Go side serves it, a Vue host (`CustomView`, `useViewBridge`) talks to it, and nothing else reaches it.

### What Studio shows

The artifact opens to the view: a control for each manifest parameter (the level-1 controls: `select` for an enum or boolean, `numberInput`, `textInput`, and a `dateRange` for each pair of date parameters, a lone date alone), a **Reset**, and what the view drew. The view draws on open and again after every parameter change (debounced, the latest request wins; a result that arrives for an older request is dropped), when the theme changes and when the width changes. The source stays one click away under **View source**. If the artifact changes on disk, or the decision stops being *allowed* (revoked, denied, edited), the Worker is terminated and the screen returns to the trust prompt. That happens after the file watcher's short debounce; until then the query gate below (every run re-checks the view is still allowed at the hash it was served at) refuses anything from the old Worker. A view that fails to load, or whose draw fails, shows the message the script gave (as text, at most 300 characters); a bridge error is always one of the fixed texts below.

### The contract (what `view.js` sees)

`view.js` is a classic script run by `importScripts` in a Worker. There is no DOM, no `window`, no network, no `indexedDB`, `caches`, `BroadcastChannel`, `localStorage`, `navigator.storage` or `navigator.locks` (a Worker shares Studio's origin, so these are removed before the view loads), no `eval` or `new Function` (the CSP has no `'unsafe-eval'`), no nested worker, and `self.webkit` is undefined. The script's world is `slaw` and one function it defines:

```js
// Define a global draw(); Studio calls it with the current state.
function draw({ params, theme, width, height }) {
  return slaw.run('by_region', { from: params.from, to: params.to })
    .then((res) => ({ svg: '<svg xmlns="http://www.w3.org/2000/svg" ...>...</svg>' }))
}
```

| | |
|---|---|
| `draw({params, theme, width, height})` | Global. Returns, or resolves to, `{svg: string}` or `{a2ui: page}`. `params` is every parameter's current typed value; `width` is the space available in pixels; `height` is 400. A draw over 30 seconds stops the view. |
| `slaw.run(queryName, params)` | Promise of `{columns, rows, truncated}`. Runs one of this artifact's named queries through `RunArtifactViewQuery(token, …)`, which is level 1's `RunArtifactQuery` behind the view's own grant (the artifact is the token's, and the view must still be allowed at the served hash when the query starts and when it returns): the SQL is the manifest's, the values are bound parameters Go re-validates, at most 500 rows and 30 seconds. `params` is a flat object (at most 32 keys named like parameters; values are a string up to 1,000 characters, a finite number, a boolean or null; at most 8 KB of JSON). Rejects with a fixed text. |
| `slaw.lib(name)` | Loads a pinned library by name, from a closed list (below), and returns its global. Synchronous. Any other name throws. |
| `slaw.theme` | Plain strings: `chart.1` to `chart.6` (and `chart`, the six as an array), `fg`, `fg.muted`, `bg`, `bg.alt`, `surface`, `border`, `accent` (hex colours), and `type` (`light` or `dark`). Also passed to `draw` as `theme`. |

Output:

- **`{svg}`**: an SVG document string, at most 2 MB. Studio shows it only as `<img src="blob:…">` with type `image/svg+xml` (revoked when replaced or when the view closes), never in the page's DOM. An SVG image runs no script and loads nothing, so it cannot reach out even if it tries; fonts inside it are the system's (give text a `font-family`), and the page's CSS variables do not apply (use `slaw.theme`).
- **`{a2ui: page}`**: an A2UI page object (the Worker hands it to Studio as JSON text, at most 1 MB), sent to Go and validated with the same context-aware schema as `view.a2ui.json` (`ValidateView` with the manifest's parameters and queries), then rendered by the level-1 renderer with the artifact runtime, so `query`-bound components, controls, `pivot` and `drill` work, and the queries behind them run through the usual lane. An invalid page shows the validation error.

### Libraries

Pinned exact versions, vendored by the frontend build into `dist/slaw-view/lib/` and served from the Studio binary (no CDN), all permissive licences (see `THIRD_PARTY_NOTICES.md`):

| `slaw.lib(...)` | Package | Leaves |
|---|---|---|
| `d3-array`, `d3-format`, `d3-time-format`, `d3-scale`, `d3-shape`, `d3-geo` | d3-array 3.2.4, d3-format 3.1.2, d3-time-format 4.1.0, d3-scale 4.0.2, d3-shape 3.2.0, d3-geo 3.1.1 (their UMD builds, with d3-color, d3-interpolate, d3-time and d3-path as dependencies) | `d3` |
| `vega` | vega 6.4.0, with `vega-interpreter` 2.3.2 bundled as `vega.expressionInterpreter` | `vega` |
| `vega-lite` | vega-lite 6.4.3 (loads `vega` too) | `vegaLite` |

Vega and vega-lite run in the Worker with no DOM and no eval: use the interpreter, and `toSVG()`.

```js
async function draw(ctx) {
  const vl = slaw.lib('vega-lite'), vega = slaw.lib('vega')
  const res = await slaw.run('sales', {})
  const spec = { /* a vega-lite spec with data.values from res.rows */ }
  const view = new vega.View(vega.parse(vl.compile(spec).spec, null, { ast: true }),
    { expr: vega.expressionInterpreter, renderer: 'none' })
  return { svg: await view.toSVG() }
}
```

### Limits and the watchdog

- **Queries:** at most 4 in flight and 60 in any rolling minute; a call over either gets a fixed error and runs nothing. A message that is not exactly `{t: 'run', id, query, params}` with a query named like a manifest query, or that carries anything else (an `sql` field, another artifact's name), is refused the same way; an unknown message type is ignored. A Worker that sends over 100 messages or 16 MB of payload in a second is stopped. A Worker answers each draw once: a repeated or late result is ignored.
- **Fixed error texts** (what `slaw.run` rejects with, never a path, an SQL error or the policy's reason): "That request was not valid.", "Too many queries are running at once.", "Too many queries in the last minute.", "The query could not be run."
- **Heartbeat:** the Worker beats every 250 ms. Studio terminates it after 1 second of silence (4 seconds while it starts; 5 seconds while it parses a library, once per library file; and it must be ready within 15 seconds), or after a draw over 30 seconds, and shows **This view stopped responding** with **Reload**. A script that blocks its thread for over a second (a long synchronous loop, a very large vega chart) is stopped; aggregate in SQL and keep drawing work small. Termination does not touch Studio's UI thread. The watchdog guards liveness, not CPU: a busy loop that yields (async) keeps beating and is stopped only by the 30 second draw limit.
- **Output:** an SVG over 2 MB or an A2UI page over 1 MB is refused with a message.

### How it is served and confined

Go serves the Worker through the Wails asset server's middleware under `/slaw-view/`, with nothing else reachable:

| Path | Serves |
|---|---|
| `/slaw-view/<token>/worker.js` | The host-owned bootstrap (embedded in the binary; it sets up the channel, the heartbeat and `slaw`, then `importScripts` the view). |
| `/slaw-view/<token>/view.js` | The artifact's `view.js`, the exact bytes that were hashed. |
| `/slaw-view/lib/<file>.js` | One pinned library. |

A **token** is 32 random bytes, issued by `OpenArtifactView(name, origin)` only for a view that is *allowed right now*, and bound to that artifact, its `ViewHash` and the open workspace for this Studio process. The view's bytes are read once when the token is issued, hashed together with `artifact.yaml`, compared with the allowed hash and held in memory; those held bytes are what `view.js` serves. Each request for a token's files also re-checks that the decision still allows exactly that hash: a changed, revoked or denied view gets 404 and the token is dropped (it never serves the new bytes, and never a mix). A new token for an artifact replaces its older ones; at most 32 tokens are held. A token for one artifact cannot fetch another's files.

Every response under `/slaw-view/`, errors included, carries `Content-Security-Policy`, `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`. The Worker's own response (its `worker.js`) carries:

```
default-src 'none'; script-src <origin>/slaw-view/<token>/view.js <origin>/slaw-view/lib/;
connect-src 'none'; worker-src 'none'; img-src 'none'; base-uri 'none'; form-action 'none'
```

`<origin>` is the page's own origin, which the page passes in (`location.protocol//location.host`: `wails://wails.localhost:34115` in `task studio:dev`, `wails://wails` in a built app on Linux and macOS, `http://wails.localhost` on Windows); Go accepts only a plain `http`, `https` or `wails` origin and puts nothing else in a header. A path source needs a host, so `'self'` could not be narrowed to these paths. The spike showed the Worker's own response CSP is what holds `connect-src 'none'`: `fetch`, XHR, WebSocket, EventSource, `importScripts` of anything but those paths, and nested workers are all blocked. No path ends in `/` or `index.html` (Wails would inject its runtime into it), and a request under `/slaw-view/` that would match that injection is answered 400, never 404 (Wails would swap a 404 for its own page). There is no directory listing, a path with `..`, `//` or a backslash is 400, nothing from the workspace is reachable except through a token, and only `GET` and `HEAD` are accepted.

The Worker holds one `MessagePort`, handed over in its first message and kept in the bootstrap's closure; every later message uses it, never a broadcast. Studio's page owns the artifact name for every `run`.

## HTML pages: `view: html` (static pages render in Studio)

> **Status.** Built end to end. The file set, the manifest block, the save-time vet, the trust hash and prompt, the renderer, the static host, the scripted host (the page window), the capabilities `state`, `download` and `ask`, the agent tool, bundle entries and `slaw artifact view` all exist. A *static* page, once allowed, renders inside Studio ([Rendering and the static host](#rendering-and-the-static-host)); a *scripted* page opens in its own native window ([Scripted pages: the page window](#scripted-pages-the-page-window)) with the [capabilities](#capabilities-of-a-scripted-page). The agent can write a page and read its files back but can never allow, run or render one ([Agents, bundles and the CLI](#agents-bundles-and-the-cli)). Plan: [`../plans/html-artifacts.md`](../plans/html-artifacts.md).

An HTML page is a Go `html/template` page of your own markup, with optional CSS, JS and assets, whose only data source is the manifest's named queries. Like every artifact it stores questions, never answers.

```
artifacts/<name>/
├── artifact.yaml
└── html/
    ├── index.html.tmpl     the entry template (html.entry); other *.tmpl files are partials
    ├── app.js              optional; any .js/.mjs file makes the page "scripted"
    └── assets/…            style.css, images, fonts, data; at most assets/<folder>/<file>
```

```yaml
view: html
html:
  entry: index.html.tmpl        # default; a .tmpl directly under html/
  window: {width: 1200, height: 800}   # scripted pages only; 200 to 4000
  capabilities:                 # scripted pages only; the closed set below
    state: {scope: local}       # scope is "local" (the only one built)
    download: {}
    ask: {}
```

The block is strict like the rest of the manifest: `html:` only goes with `view: html`, an unknown key or capability is refused, and **capabilities and a window need script** (a static page declares none, so the manifest never promises something the page's host does not offer).

### Capabilities of a scripted page

Each capability is declared in the manifest (so it is in the `PageHash` and in the person's allow decision), granted by Studio when the window opens, and reached through `slaw.use(name)`, which resolves `null` for anything not declared. Each one maps to a Studio method behind the pipe: none is an agent tool, none is reachable over MCP, and the artifact is the window's (Studio pinned it at open), never something a message names. A call to a capability the window was not granted is "That is not available." and runs nothing. The same limits as `slaw.run` apply (4 in flight, 60 a minute). Every error is a fixed text.

**`state`** (scope `local`, the only one built). A small JSON store for the page's own state.

```js
const state = await slaw.use("state")
await state.set("pinned", { cohort: "EU" })     // any JSON value
await state.get("pinned")                       // the value, or null
await state.list("filters/")                    // keys with that prefix, sorted
await state.delete("pinned")
const off = state.onChange((e) => {})           // { key, deleted, value } when another window of this artifact wrote
```

The file is `.slaw-local/artifacts/<name>/state.json`: git-ignored, machine-local, never in the artifact folder, a bundle or a commit. It is keyed by the artifact's name (from Studio, never from the page), so one artifact's state is not reachable from another's, and it survives an edit and a re-allow. At most 1 MB in all (keys and values), 64 KB a value, 200 bytes a key (no control or format characters), 2,000 keys. Writes are held in memory and flushed after 300 ms, when the window closes and when the workspace closes, with a compare-and-write: a store that finds the file changed by another merges its own changes onto it. A file that is not valid is refused and left alone, and so is a state path that goes through a symbolic link (a link committed under `.slaw-local/` could otherwise aim the state at a tracked folder). The file is written without HTML escaping, so `<`, `>` and `&` take one byte, as the caps count them. **The contents are untrusted input to the page**: another version of the page wrote them, so a page puts a value in the document with `textContent`, never `innerHTML`.

**`download`** (`save({ filename, data, type })`). `type` is `csv`, `json`, `svg`, `png`, `pdf` or `text` (or the MIME name); `data` is a string, an `ArrayBuffer`, a typed array or a `Blob`; at most 20 MB. Studio opens its native save dialog with the (cleaned) name filled in; the person picks where, and cancelling resolves `{ saved: false }` with nothing written. Studio writes only the path they chose, and only if the page is still the one they allowed when the dialog closes. The name has no path parts, control, bidi or reserved characters and gets the extension of the type; `png` and `pdf` data must start with the file's signature. **What gates a save.** A page can post a save straight to the message handler, so the check cannot live in the page's API. The helper's native window code records the person's physical input (a click, a touch, or a key press that is not Escape or a held key's repeat) before the page sees it, outside the web process, and the helper forwards a `download` call to Studio only if there was one in the last five seconds; one input pays for one save, and without one the call is refused with "Saving a file needs a click or a key press first." (`navigator.userActivation` is not used: it stays true for seconds after the host's own replies.) The injected API keeps a courtesy check of its own, which only saves a round trip. Studio adds a throttle per artifact: no new dialog for 10 seconds after the person cancels one, and at most 3 a minute. Beyond that the save dialog is the real gate, and Studio writes only the path the person chose. A saved SVG may contain script: it is the person's file, and opening it is their choice.

**`ask`** (`ask(prompt, { system })` resolves `{ text }`, or `null` when no model is set up). One completion on the provider in Settings > App, through `internal/llm` with **no tools**: the request carries none, and neither the agent's tool registry nor task mode is reachable from it. At most 10 a minute per artifact, a 32 KB prompt (8 KB of system text), and 4,000 output tokens. It has its **own toggle**, in Settings > Workspace > Pages ("Let it ask the model"): on when the page is allowed, revocable separately, off again after an edit changes the page until it is allowed again, and kept in your own config directory (`pageask.json`), not in the workspace. This is the one capability that can send text, which may include query results, to a remote provider. No key, endpoint or provider detail reaches the page: a failure is "The model could not answer." Each use is added to the agent activity log (`ListAgentActivity`, the `agent:activity` event and Settings > Workspace > Pages, "Recent model use by pages") with the page's name, the sizes and the outcome, never the text.

**Not offered:** connectors or MCP, writes of any kind, network, files outside `state`, the agent's tools, the person's identity.

### The file set

A folder that breaks any rule below lists with its error and does not run; nothing is repaired.

- Extensions: `.tmpl` (only directly under `html/`), `.js`, `.mjs` (directly under `html/` or in `assets/`), and `.css`, `.svg`, `.png`, `.jpg`, `.webp`, `.woff2`, `.json` (in `assets/` only, so nothing hashed is unreachable). Names use letters, digits, `.`, `_`, `-`; no dotfile (`.DS_Store` and `Thumbs.db` are skipped: not hashed, not served, not an error), no symbolic link (files are opened without following one), no duplicate path (case-insensitive). A stale `html/` folder beside a manifest that is not `view: html` is ignored until the artifact becomes a page; then its files are replaced or removed with the rest.
- Layout: files directly in `html/`, in `html/assets/`, or in `html/assets/<folder>/`; no other folder. `_slaw` and `slaw-lib` are reserved for the host.
- Caps: 200 files, 5 MB in all, 1 MB per file, 256 KB per template. Text files are UTF-8 with no control, bidi or invisible characters; images and fonts must start with their format's magic bytes; `.json` must be one JSON value; an SVG must be inert: a script, an event handler, a `javascript:` URL, a DOCTYPE or entity, a processing instruction other than the XML declaration, a `foreignObject`, `set`/`animate`, a link or `url()` that is not a `#fragment` or a literal `assets/` path are all refused.
- One `Rev` covers `artifact.yaml` and every file (path and bytes, in path order), so a change to any of them is a conflict for a stale writer. A change on disk under `html/**` is reported by the file watcher as a change to the artifact.

### Static or scripted

The kind is **derived**, never declared: a page is *scripted* if `html/` holds any `.js`/`.mjs` file, a template holds a `<script src>` element (an inline `<script>` is refused when a page is written, see below; also in `<svg>`, or inside an `{{if}}` branch or a partial), or any element has an `on*` attribute. Otherwise it is *static*. Templates are read as markup with every `{{ ... }}` replaced by a marker (both branches of `{{if}}` and `{{range}}` stay visible), using the HTML tokenizer `html/template` is built on. A `{{template}}` call is replaced by the callee's own markup **at the call site**, because a partial runs inside its caller's context (it may close the caller's `<style>`, or open an `<svg>`); a `{{range}}` body is read twice, since it may repeat; if `<svg>` or `<math>` appears anywhere, the whole page is read as foreign content from the first byte. A call cycle, or a page whose expansion (all templates together) is over 5000 calls or 2 MB, is refused, and a template the entry never reaches is read on its own. A template that cannot be read that way is refused: an action inside a tag name or between a tag's attributes (so a tag cannot be spelled by data or split across partials), an action right after `<`, a template that ends inside a tag, and any HTML comment (use `{{/* */}}`; `html/template` strips comments, which can join the text around one into a tag). `<noscript>`, `<xmp>`, `<noembed>` and `<noframes>` are refused: the tokenizer reads them as text, but a page without script shows their content as markup. Inside `<svg>`/`<math>` no element's content is treated as raw text and `<style>` text has its entities decoded, as in a browser. A template action may not fill an SVG paint attribute (`fill`, `stroke`, `filter`, `mask`, `clip-path`, `marker-*`, `cursor`, ...), and any attribute that holds `url(` gets the CSS rules below.

### The template functions

The function map is closed, and one leaf package (`internal/artifacts/pagefuncs`) defines it for both the save-time vet and the renderer:

| Function | Returns |
|---|---|
| `query "name"` | `{Columns, Rows, Truncated}` of that manifest query |
| `param "name"` | the bound value of a parameter |
| `params` | the declared parameters, for building controls |
| `link "name" value …` | a URL to this page with those parameters overridden |
| `number`, `percent`, `date`, `bytes`, `truncate` | formatted text (`date` takes `iso`, `short`, `long` or `month`, never a Go layout) |
| `scaleLinear`, `scaleBand`, `pathLine` | numbers and path text, to draw SVG without script |
| `json v` | the value as plain data, for a `<script type="application/json">` block |

**No function returns `template.HTML`, `JS`, `CSS`, `URL`, `HTMLAttr`, `JSStr` or `Srcset`** (nor a type holding one): data is always escaped by context, and a lint test walks every function's result types to keep it so. The builtin `call` is refused, and so is chaining a function result (`{{ call.X }}`). The functions also normalise what an environment hands them (a named string type, or a trusted type hidden in a row, becomes a plain string).

### The save-time vet

Saving a page (the store's `Put`, and every read of one) runs the vet; a failure is the error the author sees.

- Every template parses against the closed function map with `missingkey=error`, and `html/template`'s context escaper is run over every template, so an escaping error is found at save, not at render. The vet **never executes a page**: each template is run through a synthetic root whose first action fails, which is after the escaper has done its work and before anything of the page runs (so `{{ range 3000000000 }}` or a doubling chain of calls costs nothing). The renderer has no such luck and bounds a render itself (see below): a context cannot interrupt an empty `range`.
- `query`, `param` and `link` take **quoted literal names** that the manifest declares (a computed, piped or variable name is refused); `{{template "x"}}` must name a file or a `{{define}}`, and a template name may be defined only once. The queries and parameters a page uses are derived from this (`PageInfo.Queries`, `.Params`).
- **URL attributes are an allow-list.** `href`, `src`, `srcset`, `poster`, `action`, `formaction`, `xlink:href` (and a few older ones) must be a `link` call, a `#fragment`, or a literal relative `assets/` path; a `<script src>` may also name a top-level `.js` of the page. No absolute URL, no templated value beside other text, and no other action. No `target`, `ping`, `download` or `srcdoc` attribute; `<link>` only as `rel="stylesheet"` with a literal `assets/` href; no `<meta http-equiv>`, `<base>`, `<iframe>`, `<frame>`, `<object>`, `<embed>`. CSS (a `style` attribute, a `<style>` element, a `.css` file) may load only `url(assets/…)` or `url(#…)`: no backslash escape, `@import`, or `image-set()`-style function, and no action inside `url()`. This closes the hint and click paths that open a connection under every CSP layer found in the spike.

### Trust hash

`artifacts.PageHash` is what an allow will be keyed by: a SHA-256 over `artifact.yaml`, the derived kind, the declared capabilities, and every file as path, bytes and role, sorted by path, every field length-prefixed (tag `slaw-page-hash/1`). A renamed, added, removed or moved file, a byte moved across a boundary, a role change, or a switch between static and scripted all change it. The stores return it as `Page.Hash` with every read of a page. `ViewHash` (for `view: script`) is unchanged. A page whose queries call `metric()` is allowed on `DepsHash` over this hash and the metric files and saved queries it reads (see [Custom views](#custom-views-view-script)); a page that calls none is allowed on `PageHash` itself, exactly as before.

### Rendering and the static host

**`slaw artifact render <name>`** (CLI, people only) draws an allowed *static* page the way Studio's page frame does and writes it to a folder, for a browser or for publishing. It mirrors the frame and adds nothing agent-only (no MCP method or tool renders a page).

- **Which pages.** Only `view: html` pages with no script. A scripted page (it opens only in a Studio page window), an A2UI view and a script view are each refused with a message naming why.
- **Trust.** The page must be allowed as it is now: the same decision Studio uses (the folder's path plus `PageHash`, with the metric and saved-query revisions it reads, in `views.json`). A page never allowed, denied, or changed since is refused with the `slaw artifact view status|allow` hint. What renders is rebuilt from the bytes the allowed hash covers.
- **Queries.** Through `artifacts.RenderHTMLPage` with its work budget, 10 MB output cap and 30 s deadline, and the person's read-only, policy-gated path: the workspace opens read-only without the hub lock (`hub.OpenOptions.PolicyGatedRuns`, as `slaw semantics check` does), and every query runs through the agent policy's read-only runner in the tightest tier, so the SQL policy is applied at render time as in Studio, the 500-row cap holds, and the allowed metric pins apply. A failed draw writes nothing; the error text is the renderer's fixed sentence (add `--verbose` for the reason in the log).
- **Parameters.** `-p name=value` (repeatable) is read as a page request's query string is: a declared name, once, of its type, then bound by `artifacts.Bind` like every artifact query. A value never becomes SQL text.
- **Output.** `--out` defaults to `./<name>-render`. It writes `index.html` (the rendered page, with a Content-Security-Policy `<meta>` and then `<link rel="stylesheet" href="_slaw/theme.css">` inserted at the very start of the document, after a doctype, so the HTML parser puts them in the head before anything the page wrote), `_slaw/theme.css` and the page's static files at their `html/` paths (stylesheets, images, fonts, JSON; never a template). The theme is the built-in light theme (the roles of `internal/pagetheme`, which Studio's page window shares for the stylesheet itself); there is no window to take roles from. A folder that already has files is refused unless `--force`, which overwrites only the files it writes. Every target is checked before the first write (a local path, no link or file where a folder must be, no names that differ only in case), the writes go through `os.Root` on the folder so nothing can land outside it, and a symbolic link at a file's own path is replaced, never followed.
- **Protection.** Studio's frame is held by response headers (a script-less CSP) and a `sandbox=""` attribute; a folder opened from disk has neither. What protects the exported files is the save-time vet, the render-time checks of the rendered markup (no script, no handler, no foreign URL), and this `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; form-action 'none'; base-uri 'none'">`, which the host inserts after those checks at the start of the document (it does not search for a `<head>`, so markup that imitates one cannot move it out of effect): no script, no network, only the folder's own files, no form posts.
- **Limits.** The page is drawn once with the given values. A link or form back to the page itself (`page?...`) does nothing from a file.

`artifacts.RenderHTMLPage(ctx, run, artifact, values, w)` executes the entry template with its partials against the closed function map. Its `query` runs the manifest's named query **lazily and once per render**, through a `PageRunner` the host provides; Studio's runs the query of the manifest the page was hashed with (`RunManifestQuery`, the same path as `RunArtifactQuery` but over the held manifest, so an edit on disk after the allow is never run), so every page query is vetted SQL at every run, goes through `artifacts.Bind`, is read-only in every tier, capped at 500 rows and 30 seconds, and a failure is a fixed text (never the policy's reason or a path). The template cannot name a connection, write SQL or read a file.

**The renderer bounds itself**, because nothing interrupts a template that loops without calling a function:

- Parsed templates are rewritten before they run so every `range` is charged what it will iterate (a slice or map's length, an integer's value) and every `{{template}}` call one step, against one budget of 2,000,000 per render. `{{ range 3000000000 }}{{ end }}`, nested loops, and a call chain that doubles at every level end in the "took too long" block, with no goroutine left behind. The deadline and a cancel (a revoke, a new token, a workspace switch) are checked before every action in every list, so inside a loop body a render overruns by at most one action.
- Templates may not call themselves, directly or through others: the vet refuses it at save and the renderer again (a call depth bound by the number of templates).
- **Strings are budgeted too.** The builtins that build strings from their arguments (`printf`, `print`, `println`, `js`, `html`, `urlquery`) are replaced, inside a render, by versions that estimate their result from their arguments (the format, every width, each verb repeating the longest argument, six bytes per input byte for the escapers) and refuse *before* allocating if it cannot fit; every string a page function returns (`number`, `percent`, `date`, `bytes`, `truncate`, `pathLine`, `link`, `json`) is charged the same way. Arguments that are not strings (a row set, a row, a map) are sized by walking what they hold, so one large cell counts in full each time it is named. One budget of 64 MB covers a render, and one query result is capped at 16 MB of cell text (over it the query fails with the fixed block; the runner has already read the rows, so this bounds what the template sees, not the read), so a page cannot hold a gigabyte in variables where neither the output cap nor the deadline would see it; going over is the "too large" block. The names and behaviour are the builtins' own (the vet, which never executes a page, uses the plain map).
- A render is limited to 30 s, and the output to 10 MB. A failed render writes nothing: the host sends a small fixed error document instead (a failed query names the query, a parameter error says so, never more).
- The rendered markup is read again with the vet's rules (no script or frame element, no `on*` handler, no `target`/`ping`/`download`, no `<meta http-equiv>`, a `<link>` only as a local stylesheet, a URL attribute only this page's own route, a `#fragment` or `assets/…`) as defence in depth; a scripted page's own script elements are the one allowed difference.

Values come **only from the query string** of the page request. Each key must be a declared parameter, once, and read as its parameter's type; Bind refuses anything out of range or of the wrong shape on every query. `link "name" value …` writes `page?name=value&…` (relative to the page's route, keeping the token path; the current values stay, the named ones are overridden; a name or value the parameter would refuse is a template error).

The host is `studio/viewserver.go` and `viewserver_page.go`, under the same `/slaw-view/` middleware as script views:

| Route | Serves |
|---|---|
| `/slaw-view/<token>/page?<params>` | the rendered page |
| `/slaw-view/<token>/assets/<path>` | one file of `html/assets/` (by exact path, extensions of the closed list; never a template, never a query) |

- **The token is per (Studio process, artifact)** and is issued by `OpenArtifactPage` only for a static page that is allowed right now. The artifact is the token's, never the path's: no path under one token reaches another artifact. A new token for an artifact replaces its older ones, and a revoke or deny drops them at once.
- **Serve what was hashed.** The files are read once, hashed (`PageHash`) and compared with the allowed hash when the token is issued; the page renders those held bytes. The decision is re-checked (the same workspace, allowed, same hash) on use, at most every half second per page token, and again after a render, so a render that overlapped an edit or a revoke is not delivered.
- **Response headers**, on every response under the page routes, errors included: `Content-Security-Policy: sandbox; default-src 'none'; script-src 'none'; style-src <a> 'unsafe-inline'; img-src <a> data:; font-src <a>; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'` (`<a>` is `<origin>/slaw-view/<token>/assets/`; an error without a page of its own gets `sandbox; ` plus the deny policy), `X-Content-Type-Options: nosniff`, `Cache-Control: no-store`. Sub-resources load only from `assets/`, which runs no query, so an `<img>` or a CSS `url()` pointing at `/page` is refused by the CSP.
- **Limits.** At most 4 renders in flight and 60 a minute per token (429 over). The webview sends no `Sec-Fetch-*` header on these requests (measured in WebKitGTK and WKWebView), so the route also refuses a request whose header says it is an image, style, script, font or `fetch` (403) when one is present. A path that ends in `/` or `index.html` is 400 (Wails would inject its runtime), `GET` and `HEAD` only, a query string over 8 KB is refused.
- **Studio shows the page in `<iframe sandbox="">`** (no `allow-scripts`, `allow-same-origin`, `allow-forms`, `allow-popups` or `allow-top-navigation`), with the artifact's level-1 controls above it; a change to a control reloads the frame from the new query string. The frame's bottom edge can be dragged to resize it (a script-less page cannot report its own height). The page links to itself only with `link`: there are no forms.
- **Studio's own page narrows frame navigation** with a one-directive meta CSP, `frame-src <origin>/slaw-view/` (candidate a1 of the base plan). Without it a click on an outside link inside the frame would navigate the frame, and a link to `/` would load Studio's own page into it; with it both load nothing. It is **emitted into `index.html` at build time for the window's origin** (`studio/frontend/scripts/frame-csp.mjs`): `wails://wails` in a built app on Linux and macOS, `http://wails.localhost` on Windows (`SLAW_STUDIO_ORIGIN` overrides it for a cross build), and `wails://wails.localhost:34115` in `task studio:dev`. The dev build also names `http://localhost:34115` (the same server in a browser, for Rodney layout QA); that origin is only ever emitted by the dev server, never by a build. Before it opens a page the frontend checks that the meta is there and names this window's origin, and shows a plain refusal otherwise.
- **Allowing.** The page shows as its files (the manifest and every file under `html/`, read-only, with role and size) until the person allows it, keyed by the artifact folder's path plus `PageHash` in `views.json`, exactly like a script view. Any edit, by anyone, ends the allow and the frame is torn down. The agent has no tool or MCP method to allow, deny or revoke. `slaw artifact view status|allow|deny|revoke <name>` covers pages too (it prints the file list and every text file).

An agent-written or pulled page is untrusted markup under a hard CSP; the data in it is escaped by context. The allow prompt, the agent tool and the bundle entries are described below.

### Scripted pages: the page window

A scripted page never runs inside Studio's window. **Open page** (under the artifact's controls, once the page is allowed) opens it in a native window that Slaw owns, in a **helper process**: the Studio binary started as `slaw-studio --view-host`, which never starts Wails and is given no workspace path, connection, credential or store. The window and the helper are Studio's alone to open: no tool, MCP method or page can.

**What Studio sends.** After it renders the entry document for the controls' values, Studio sends the helper, over a dedicated pair of pipes (file descriptors 3 and 4, marked close-on-exec in the helper at once so WebKit's own processes never inherit them; JSON lines, `internal/pagehost`): the page's files as they were hashed (no templates), the host's theme stylesheet, the vendored libraries the page names, and the document. The helper serves them from memory under `slaw-page://<name>/`; it has no other source, so a path that was not sent does not exist.

- Every response carries `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; worker-src blob:; frame-src 'none'; form-action 'none'; base-uri 'none'`, `Content-Type`, `nosniff` and `no-store`. There is no `'unsafe-eval'` and no inline script: data reaches script through a `<script type="application/json">` block written with `{{ json … }}`. A library that needs `eval` (some vega builds) uses its CSP-safe build.
- `_slaw/theme.css` holds the person's theme roles as `--slaw-*` custom properties (built in Go from a validated list; the host links it first in the document's `<head>`). It also sets `color-scheme` and a `--slaw-theme-type` property, each `light` or `dark`; a page stylesheet that sets its own `color-scheme` replaces the first, so read the mode from the property (`getComputedStyle(document.documentElement).getPropertyValue('--slaw-theme-type').trim()`, or a `style(--slaw-theme-type: dark)` container query). The role names are listed under [Colour roles](themes.md#colour-roles) in themes.md. `slaw-lib/<file>.js` serves the pinned libraries Studio vendors for script views (d3 modules, vega, vega-lite, the vega interpreter): name one in `<script src="slaw-lib/d3-scale.js">`. A name that is not vendored is a 404 for the page.
- **The page-side API**, injected before the page runs: `slaw.run(query, values)` resolves `{columns, rows, truncated}` for one of the manifest's named queries (the same gates as every artifact query: vetted SQL at every run, Bind, read-only, 500 rows, 30 s; a value is only ever a bound parameter). `slaw.params()` returns the declared parameters with their defaults, static options and the value the page was drawn with. `slaw.use(name)` resolves a capability object for a name the manifest declared and Studio granted, and `null` for every other name; a page must render without them. Errors are fixed texts ("That query could not run.", "This page is asking for data too quickly."), never a path or the policy's reason. A query the page's templates do not use but the manifest has is allowed and logged.
- **Navigation.** The window loads the first page and follows `link` URLs (`?region=EU`): Studio renders the document again for the new query string. Every other navigation is refused, including `about:blank`, `blob:`, `data:`, `javascript:` and `file:` links, redirects, new windows, downloads, and every permission request (camera, microphone, location, notifications, clipboard, full screen). Dialogs (`alert`, `confirm`, `prompt`) do nothing. There is no context menu and no inspector. `localStorage`, `sessionStorage` and IndexedDB do not exist, and nothing survives the window (an ephemeral store); a page keeps state only through the `state` capability.
- **The window cannot pass for Studio.** Its native title is always `<artifact title> — Slaw page` and never follows `document.title`; it has no menu beyond Close; it is never full screen. **Slaw never asks you for a password, key or any credential in a page window. If a page does, close it.**
- **Limits.** At most 4 windows open. A window may have 4 requests in flight and start 60 a minute; a page that posts more than 400 messages a second, or any line that breaks the protocol (a field naming an artifact, an unknown type, a line over 64 KB), has its window killed. The helper accepts at most 96 MB of files from Studio, and a document of 10 MB.
- **The window ends** on a missed heartbeat (the page posts one every 500 ms; 4 s without one, or 25 s to the first, kills the helper and its whole process tree: "This page stopped responding"), a crash of the web process, a change to any file of the artifact or to the decision on disk ("This page changed"; Reopen asks again and Go checks the allowed hash), a workspace switch or close, Studio quitting, or the person closing it. Studio killing the helper takes the web process with it on both platforms (a process group, and on Linux the helper's pid namespace); the helper also dies with Studio (the pipe closing, and `PR_SET_PDEATHSIG` on Linux).

**Linux: the network gate.** The helper is only ever started inside bubblewrap (`/usr/bin/bwrap`), with new user, net, pid, ipc, uts and cgroup namespaces, the real uid mapped to itself, no capabilities, a new session, and an **allow-listed filesystem view**: `/usr` read-only, a generated `nsswitch.conf` of `hosts: files` and an empty `resolv.conf`, a tmpfs `/run` with only the Wayland socket bound back in, a tmpfs `/tmp` and home, `/dev` (with `/dev/dri`), `/proc`, `/sys`, a few read-only parts of `/etc` (linker cache, fonts, time zone), and the Studio binary itself. A network namespace alone is not enough: systemd-resolved's socket, avahi and the system bus all answered from inside one in the spike. There is no system bus, resolver, nscd or session bus in this view to find. The environment passed is `XDG_RUNTIME_DIR` (and `WAYLAND_DISPLAY` when it is not `wayland-0`); nothing else of yours (no `AWS_*`, token, password, `.env` overlay, `HOME` or `DISPLAY`). WebKit's own web-process sandbox is switched on (`webkit_web_context_set_sandbox_enabled`, which Wails never does) and runs nested inside. If any of this cannot be set up, **there is no page window** and Studio says so, with these fixed reasons: "this system cannot isolate a page window (it needs bubblewrap and unprivileged user namespaces)" (a missing `bwrap`, or user namespaces refused, as under Ubuntu's `apparmor_restrict_unprivileged_userns`), "a page window needs a Wayland session" (an X11-only session is refused: the X server's abstract socket does not cross a network namespace and its cookie file would have to be handed over), "page windows are not available in this build" (Studio built without the `webkit2_41` tag, or on Windows). The helper itself refuses to open a window if it finds itself outside the gate (a network interface other than loopback, or a `HOME` other than the gate's).

**macOS: four filtering layers and one accepted leak.** WebKit's Networking process is started by launchd, so no namespace applies, and `sandbox-exec` is deprecated and not used. The helper sets up, before the first load: the strict CSP above; the navigation policy; a `WKContentRuleList` that blocks every URL but `slaw-page:`, compiled first, with the first load waiting for it; and a non-persistent `WKWebsiteDataStore` whose `proxyConfigurations` point at a loopback port the helper bound itself and never listens on, so no other process can be the proxy and nothing answers there (on macOS 26.6.2 a connect to that bound, non-listening port hangs about 7.8 s and then fails with a timeout, so a host-name load waits that long instead of failing at once; a 125 s hold saw no failover to a direct connection, which `nw_proxy_config_set_failover_allowed(false)` is there to guarantee), and host names are not resolved locally. WebRTC and media devices are off through private preferences (settings, not a layer). Page windows need **macOS 14**; on 13 and older they are refused. **The leak:** a link to an IP address that page script clicks (`a.click()`), with or without `target=_blank` (the check refuses `target` in a template, but script can add it later), opens a TCP connection to the address and port the page chose, because IP addresses bypass the proxy and WebKit preconnects before the rules or the policy apply. Measured on macOS 26.6.2: over plain HTTP one empty connection per click; over `https://` to an IP a TLS ClientHello (no SNI, as the target is an IP) and four connections per click; the reach includes loopback, the LAN and a non-private address (a TEST-NET address was reached). The page cannot read a reply, but the choice of address, port and timing carries a few bytes per click, the click needs no gesture and can repeat, and the address can be on the person's LAN or loopback. The allow screen says so ("On macOS a scripted page can use a link to open a network connection to an address it chooses, including a computer on your network or this one, though it cannot read a reply."). A network-free replacement is tracked in [#189](https://github.com/SandwichLabs/slaw/issues/189).

**The page window itself (both platforms).** Nothing can be dropped on the page: the view takes no drag types, so a dropped file gives the page no event and no name, size, type or time. On macOS, Cmd-W and Cmd-Q in the page window both close that window and nothing else. The helper runs with `/` as its working directory. On macOS the helper keeps its compiled rule list in `slaw-rules-<pid>` in the temporary directory, which Studio removes after it reaps the helper, and which a starting helper sweeps for dead pids in Studio's temporary directory (Studio passes its own `$TMPDIR`; with none, the helper uses its per-user temporary folder and nothing is swept or removed by path). A helper that cannot make its rule list or its proxy socket shows "This page window could not be set up on this Mac." instead of "This page stopped responding".

**Shared keychain identity (macOS, #189).** Keychain access is granted per application, and the helper is the same executable as Studio, so an escape from the web process into the helper could read Slaw's keychain items without a prompt. A separate helper executable with its own identifier would close it and waits on the developer account; until then it is part of the accepted macOS limitation. (On Linux the Secret Service has no per-binary access control, so the OS user is the boundary either way.)

### The allow prompt

Nothing under `html/` renders or runs until the person allows it. The prompt (Studio's artifact screen, or `slaw artifact view status|allow`) shows, before the Allow button:

- **The kind and what it can reach.** The prompt text is part of the status read that carries the hash on screen (`ArtifactViewState`), never a second read, and **Allow stays off until it has loaded**. A *static* page has no script and shows in a frame that cannot reach the network, files or other pages. A *scripted* page has script and runs in its own window, with no file, credential or other-page access, and no usable network (except as noted for macOS).
- **The queries it can run**, each with the connection it runs on (`the workspace database` when none is named). For a *static* page this is the list its templates derive. For a *scripted* page it is **every query in the manifest**, because `slaw.run` can reach any of them (the templates' own are marked, the rest are "script only"); the prompt says a script is not limited to the ones its templates name. `slaw.run` itself is unchanged: a query outside the template list is allowed and logged.
- **The declared capabilities**, scripted pages only, each with a plain line: `state` (notes kept on this computer only), `download` (a save dialog you confirm, after a click or key press) and `ask` ("This page can send text, which may include query results, to <provider>", naming the usable provider and model; with none usable it says the page will use whatever provider is set up later, since the toggle stays on).
- **The macOS network disclosure**, on macOS for a scripted page, word for word: "On macOS a scripted page can use a link to open a network connection to an address it chooses, including a computer on your network or this one, though it cannot read a reply. Every other load, lookup and request is blocked, but this one gap exists until the page window can be isolated further." (see below, #189).
- **The credential warning**, always: Slaw never asks for a password, key or token inside a page; a page that does is trying to steal it, so deny it.
- **The metrics it reads**, when its queries call `metric()`: each metric with its saved query, and the line that their files are part of what you allow, so a change to one asks again (#282).
- **The files**: every file under `html/` and the manifest with role and size, each text file read-only in the editor. A binary asset shows only its size; the hash covers it.

Allow is keyed by the folder's path and `PageHash`, so any edit, by anyone, asks again. `ask` is on at allow time (the agent already sends query results to the same provider, and the prompt names it) and can be switched off separately in **Settings > Workspace > Pages**, which lists every page and script-view decision with its kind and declared capabilities, a Revoke button, the per-page "Let it ask the model" checkbox, and **Recent model use by pages** (page name, time, bytes sent and received, outcome; never the text). The same log is written at info level ("agent activity"). Decisions live in `views.json` and `pageask.json` in your config directory, never in the folder or a bundle.

### Agents, bundles and the CLI

- **Agent tool.** `write_artifact` takes `html_files: {path: text}` with a `view: html` manifest (text files only; binary assets arrive by git or a bundle, and a binary asset already stored is kept when the write carries the `rev`). The files are checked against the rules above and vetted per the template rules at save, and again by the store. A file `get_artifact` returned without its text (`text_omitted: true`, over the cap, never a prefix) is kept with `{"keep": true}` and the `rev`. `get_artifact` returns a page's derived kind, entry, the queries, parameters and capabilities it uses, and each file's path, role, size and text. A write is a compare-and-write on the page's `Rev`. **No tool or MCP method renders, runs or allows a page**; an agent edit changes the hash, so the person is asked again. Over MCP it is the same tool.
- **Bundles.** A page travels as `artifacts/<name>/artifact.yaml` plus one entry per file, `artifacts/<name>/html/<path>`, under the file-layout rules; the whole set is vetted while the bundle opens, before anything is written, with the usual 10 MB per entry and 50 MB total. A decision never travels (an imported page is undecided) and neither does `.slaw-local/` state.
- **CLI.** `slaw artifact list` shows `html (static)` or `html (scripted)`. `slaw artifact view status|allow|deny|revoke <name>` covers pages: `status` prints the kind, the queries with their connections, the declared capabilities with the same plain lines, the files and every text file. No command opens a page or runs one with script. `slaw artifact render <name> [--out DIR] [-p name=value ...] [--force]` draws a **static** page to files (below).
