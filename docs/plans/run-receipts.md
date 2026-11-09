# Run receipts: implementation plan for E1 (#241)

Status: **accepted, not built (2026-11-09).** The owner's answers to the open questions are recorded under "Decisions from the owner" below. This is the build plan for epic E1 of [`answers-you-can-check.md`](answers-you-can-check.md) (umbrella #250). That plan says *what* E1 is and why receipts come first. This one says how the first shippable slice gets built, in what order, and what it does not do yet.

## Why this one, now

- **It is the foundation of the year's direction, and none of it is built.** E3 (verifier), E5 (`slaw check`, with #308 already queued behind it), E6 (as-of) and the #7/#30 chat trust items all consume a receipt. E4 (semantics) and E7 (watches) have shipped without one, so a metric value or a watch alert still can't show what produced it.
- **The prerequisites are clear.** #193 (byte cap on artifact results) is closed. The part of #5 that matters, the result cap, is done; cancel is not needed until E5.
- **It is AI-free and benefits every surface.** It's useful to a person with no provider configured, to Claude Code over MCP (a `receipt_id` to cite), and to the in-app agent.
- **The code has a real chokepoint.** `engine.Manager.run` (`internal/engine/engine.go`) is the one run path behind every `Execute*` entry point. It already logs a statement digest, the size and the connection, and never the text.

## What a receipt is

A small JSON record of *that* a question was asked and what it touched. It is never the answer.

| Field | Notes |
|---|---|
| `id` | `r_` + the first 20 hex digits of the SHA-256 of the canonical record without `id`. A content address, not a signature. |
| `at`, `duration_ms` | Run start and duration. |
| `surface` | `query_editor`, `console` (only after `.receipts on`), `cli_query`, `render` (an A2UI component or notebook SQL cell: Go can't tell them apart until R4a, which sets `notebook_cell` or `a2ui` in its place), `artifact`, `metric`, `assertion`, `watch`, `agent_run_query`, `agent_saved_query`, `agent_preview`. |
| `actor` | `person` or `agent`. For `agent`, also `agent_session`: the MCP transport session id (SSE: the id `internal/mcp/server.go` already tracks in `sessionCeil`; stdio: one random id per process) or, in Studio's chat, the conversation id; in task mode (`preview_query` from Studio's agent task), the task's run id. Not the `agents.Handler`: one handler serves every SSE client, and chat builds a new one each turn. |
| `source` | What the person wrote: `{kind: sql\|saved_query\|metric\|artifact_query\|cell, ref}`. `ref` is a file-relative id (`queries/x.sql`, `artifacts/a#by_month`, `notebooks/n.slnb#cell-3`), never an absolute path. Empty for `render` until R4a passes a source the binding validates against the open notebook or artifact. |
| `sql` | The text as written (with `metric()` and `$name` intact). It is kept only if `engine.RecordableStatement` (the one-line `engine.Recordable`, which allows no line break or tab, with those allowed; `Recordable` is today the unexported `repl.recordable`, moved and exported in R1; it lives in `engine` because it uses `engine.TerminalText`, and `engine` already imports `secrets`) passes it, so a statement that looks like a credential is never stored (the console history's rule). Otherwise `sql_withheld: true` and only the hash. |
| `run_sql` | The text the engine executed: after `metric()` expansion, the caller's cap wrapper (`_a2ui_limit`, the grid cap) and wildcard expansion, with each expanded key list replaced by its pattern, and "expanded to N objects", so 10,000 keys never land in a receipt. That is a new `storageglob` `Rewrite.Summarize(sql) (text, n)` with no clipping (`Rewrite.Redact` is for error messages: it rewrites `LINE n:` and clips at 4000 bytes). `run_sql_hash` hashes the executed text, not this stand-in. Same `engine.RecordableStatement` rule. The drawer's **As run** view shows it. |
| `sql_hash`, `run_sql_hash`, `params_hash` | HMAC-SHA256, keyed with the same per-workspace key as `result_hmac` (distinct domain tags), of the text as written, the text as run and the full set of param names and values. Keyed so a withheld credential or a low-entropy value cannot be dictionary-attacked from a receipt: the hashes are comparable only within one workspace on one machine. The `id` stays a plain content address over the record, which holds the keyed hashes and never the raw secret. |
| `params` | Bound values, each capped at 256 bytes, with `params_hash` over the full set. Each value passes `engine.Recordable` or is withheld on its own (`"withheld"`). Stored by default; with **Keep parameter values** off (Settings > Workspace > Receipts) a receipt keeps the names and `params_hash` only, and `params_withheld: true`. |
| `connections[]` | `{name, type, read_only, status}`, `status` an enum (`connected`, `not_connected`, `error`) and never the status message, which can name a host, for each catalog the statement resolved to, plus that connection's `snapshot` (below). |
| `verdict` | `read` for policy-gated runs; `unchecked` for the person's own editor, console and CLI, which are not gated. Only reads mint in this plan, decided by the statement type DuckDB reports when it prepares the statement (`duckdb.StmtType`, as `repl`'s `Session.classify` does): `SELECT` and `EXPLAIN` mint; writes, DDL and everything else don't. |
| `rows` | `{returned, total, truncated}` as **shown**: after the caller drops its probe row (`maxRows+1`), DuckLake internal rows and the tool-result budget. |
| `columns` | Names and types only. A name can carry a value (an unaliased `SELECT 'password=x'` is named after its expression; a PIVOT names columns from row values), so each name passes `engine.Recordable` or is withheld (`{name: "", withheld: true, type}`); every name is withheld when the statement's text is, and when the names come from data (PIVOT, PIVOT_WIDER, UNPIVOT, a file reader, `FROM 'file.csv'`) only the count and types stay. |
| `result_hmac` | HMAC-SHA256 over a canonical encoding of the columns and the rows as shown, keyed with a per-workspace key in `.slaw-local/receipts/key` (0600, created on first use). The key stops anyone brute-forcing a small result from the hash. |

Not in a receipt: any row value, a credential or anything `engine.Recordable` refuses (`engine.RecordableStatement` for the text and the run text, `engine.Recordable` for a parameter value), a secret name's value, a connection's DSN or status message, or an absolute path in `source.ref` or `connections[]`. SQL text is kept as the person wrote it, so a file path they typed (`read_parquet('/home/...')`) stays in `sql`: the receipt is on their own machine, beside the file that holds the same text. A redaction test enforces each line (R1 for the record, R2 for the callers).

## Design decisions

1. **Described by the caller, filled in by the engine, finished by the caller.** A caller that wants a receipt puts a `receipts.Intent` on the context of the *final* run only: surface, actor, agent session, source, original text, params and verdict. `engine.Manager.run` (and `executeWith`, which does the wildcard expansion) adds what only the engine knows: the text it executed, the catalogs it resolved, columns and duration. The caller then calls `receipts.Finish(ctx, rec, shown, truncated)` after its own trimming, which hashes the rows as shown and writes the receipt through the `receipts.Recorder` the caller got from its handler or from `hub.Open` (never from the engine). Until R4b, `Finish` applies the default policy (Keep parameter values on, 30 days / 20,000 / 64 MB). So the policy's `EXPLAIN` classification, the artifact save-time `LIMIT 0` vet, catalog listing, completion, the console's `.schema`/`.tables` and every other internal query carry no intent and never mint. Nothing new is global.
2. **The id travels on the result.** `Finish` returns the id; each caller sets it on its wire type. Every surface forwards it on its wire type (`QueryResult`, `A2UIQueryResult`, the agent's run results) rather than looking it up afterwards.
3. **Minting never fails a run.** A store that is full, unwritable or locked is logged at warn level, and the result goes out without a `receipt_id`. The drawer says "No receipt was kept for this run" and gives the reason class.
4. **Per-file store, pruned.** `.slaw-local/receipts/<aa>/<id>.json`, written atomically through `filestore`. That lets Studio, `slaw mcp serve` and `slaw receipts show` read each other's receipts without one shared index file. By default it holds at most 20,000 receipts or 64 MB, and nothing older than 30 days; all three are per-person settings (below). It is pruned on open, after a settings change and every 500 writes under `filelock`, oldest first. (The watches history, `internal/watches` `FileHistory`, is the pattern for the caps and the lock.)
5. **The agent reads only its own receipts.** A `get_receipt` tool (introspect category) returns a receipt only when `actor=agent` and `agent_session` matches the caller's MCP session or chat conversation (carried on the context, not the handler). Anything else gets the same "no receipt with that id" as a missing one, so receipts can't be used as an oracle.
6. **No schema change to any workspace file in this slice.** Notebooks stay schemaVersion 2. Inline live values (schemaVersion 3) are a later slice.

## Chokepoints (verified on main 3d2b4cd; cited by function, since lines move)

| Path | Where | Surface | Recorder wired in |
|---|---|---|---|
| Render-time and policy-gated reads | `agents.Handler.RunReadOnlySQLIn` (`internal/agents/handler_query.go`) | A2UI and notebook cells (`App.RunA2UIQueryIn`), artifacts (`RunArtifactQuery`, `RunArtifactViewQuery`, page runs; not the save-time vet), metric try-it (`studio/app_semantics.go`), assertions and watches (`workspace.ReadOnlyRunner`) | `hub.NewAgentHandler` (`internal/hub/agent.go`) and Studio's fallback handler (`studio/app_agent.go`): each builds its own `engine.Manager`, so `hub.Open` alone does not reach them |
| Agent `run_query` / `run_saved_query` | `Handler.runQuery` | MCP and in-app agent | same handlers |
| Agent `preview_query` | `internal/agents/notebook_task.go` | task mode | same handlers |
| Person's editor | `App.RunQuery` (`studio/app_queries.go`, `ExecuteCappedIn`) | Query Explorer grid | `hub.Open` |
| CLI | `cmd/query.go` | `slaw query` | `hub.Open` |
| Console | `internal/repl/exec.go` (`ExecuteOn`) | Studio console, `slaw repl` (after `.receipts on`) | `hub.Open` |

Out of scope for this plan, and listed so nobody assumes otherwise: the TUI REPL (`internal/tui`), the Table browser (`crud.Page`, which bypasses the engine), `slaw export`, and approved writes.

## Slices

Each slice is one PR (R4a and R4b are two) through the usual loop: a Sonnet implementer, an Opus adversarial review (security probes on R1, R2 and R5), CI green, then merge.

### R0. The plan and the screens (docs only)

- Done before R0: the design pass on the E1 screens of the *Answers You Can Check* canvas (the drawer, its states and entry points, and Settings > Workspace > Receipts), so R4a's drawer is built from a reviewed screen.
- This plan, linked from `docs/README.md`, `docs/design/README.md` and #241.
- The user-facing lines wait for the code that makes them true: CLAUDE.md "Key concepts" (receipts are machine-local and are not outputs; "Notebook outputs are never persisted" still holds for the folder) lands with R1, and the `docs/privacy.md` paragraph (receipts stay in `.slaw-local/receipts/`, hold no row values, and never leave the machine) lands with R2, when runs start minting.

### R1. `internal/receipts` leaf

- `engine.Recordable`: `repl.recordable` moved to `internal/engine` (not `secrets`, which `engine` imports: that would be a cycle) and exported; `repl` calls it. CLAUDE.md "Key concepts" gains the receipts line.
- Model, canonical encoding (`jsonenc`), id derivation, `Policy{KeepParams bool, MaxAge, MaxCount, MaxBytes}` with the defaults and floors (1 day, 100 receipts, 1 MB) and ceilings (365 days, 200,000, 1 GB), keyed HMAC streamed over rows, strict JSON decode (a byte cap before decode, `DisallowUnknownFields`, one value).
- `Store` interface (`Put`, `Get`, `Prune`) with `FileStore` and `Mock`. Key creation is 0600, and a key file with the wrong mode or size is refused.
- Leaf rule: it imports only stdlib, `jsonenc`, `filestore` and `filelock`, and an ast-grep rule keeps it that way.
- Tests:
  - the id is stable across runs;
  - different keys give different HMACs;
  - pruning holds the caps, and a lowered cap prunes on the next open;
  - with `KeepParams` off the record holds names and `params_hash` but no value;
  - two processes write concurrently (`filelock`);
  - the **redaction test** on the record: a withheld text keeps only its hash, a withheld param value keeps only its name, a connection `status` is an enum and never a message, and a `source.ref` that is absolute or holds `..` is refused;
  - a test that bundle export from a workspace full of receipts contains no `receipts/` entry.
- Performance budget: hashing 10,000 rows × 20 columns stays under 15 ms (benchmark in CI, not a gate).

**As built (R1, 2026-11-09).** `internal/receipts` (model, `Seal`/`Validate`/`Decode`, `ResultHasher`, `LoadKey`, `Policy`, `FileStore`, `Mock`) and `engine.Recordable` (with `engine.RecordableMax`) landed; nothing mints yet. Notes for R2:
- `Policy.Clamp` is the one bound rule (a zero limit is unset and takes its default); the store clamps on every `Prune`.
- `engine.Recordable` is the console's one-line rule (a newline or tab makes text not recordable), so it is for parameter values. `engine.RecordableStatement` is the same rule allowing `\n`, `\r\n` and tabs, for `sql` and `run_sql` as written; both still refuse other controls, C1, bidi, over 64 KB and credential-shaped text. `sql_hash` stays over the original text.
- Callers compute `params_hash`, `sql_hash` and `run_sql_hash` with `receipts.HashParams(key, ...)`, `HashText(key, ...)` and `HashRunText(key, ...)` (key from `receipts.LoadKey(root)`), `HashParams` over the full values before `Policy.ApplyParams`. `LoadKey` and `NewFileStore` take the workspace root, not the folder: `receipts.Dir` refuses a symbolic link or non-folder at `.slaw-local` or `receipts`, the key is opened with `O_NOFOLLOW` and checked on the open descriptor.
- Both `Recordable` functions now also refuse any Unicode space other than ASCII space, tab and line breaks (NBSP, U+2000 to U+200A, U+2028, U+2029, U+3000 and the like), which the redactor's ASCII `\s` would not see through. This changes the console history, but only to refuse more.
- `Seal` puts `At` in UTC; `Validate` requires it, and requires valid UTF-8 in every text field, so one record has one id. The result hash encodes maps, structs (DuckDB unions, intervals), pointers and lists by reflection, each part tagged and length-prefixed.
- `Prune` ages a receipt by its file's modification time. The result hash benchmark is about 3.5 ms for 10,000 rows x 20 columns.

### R2. Mint at the chokepoints, `receipt_id` in results

- `receipts.WithIntent`, the engine filling run facts on the context, `receipts.Finish`, and the recorder wired at every row of the chokepoint table's last column: `hub.Open`, `hub.NewAgentHandler` and Studio's fallback handler.
- `agent_session` on the context: the MCP server sets it from the transport session; Studio's chat sets the conversation id.
- `engine.Recordable` (moved from `repl`, which keeps calling it) (and `engine.RecordableStatement`) applied by each caller: `RecordableStatement` to the text and the run text, `Recordable` to each param value; `Rewrite.Summarize` for `run_sql`.
- Intents at each row of the chokepoint table, on the final run's context only.
- `run_query`, `run_saved_query` and `preview_query` results gain `receipt_id`. The tool descriptions say "cite it", and `docs/reference/mcp.md` and `agent-policy.md` document it.
- Wire types: `QueryResult.ReceiptID` and `A2UIQueryResult.ReceiptID` (fields, so the `AppApi` arity check is unaffected in R2).
- Console: off by default. `.receipts on|off` turns minting on for that console session only, and the prompt shows it while on. Nothing is remembered between sessions.
- `docs/privacy.md`: the receipts paragraph.
- Tests:
  - every chokepoint mints exactly one receipt per successful read, **including the agent and render paths** (they go through `NewAgentHandler`);
  - `rows`, `truncated` and `result_hmac` match what was shown, not the probe row or DuckLake internal rows;
  - classification, `EXPLAIN`, the artifact save-time vet, catalog queries, `.schema` and writes mint none;
  - a failing store still returns the result;
  - redaction at the callers: a credential-looking literal, a credential-looking param value and a 10,000-key wildcard;
  - SQL cannot read `.slaw-local/receipts/` or the key (agent file reads stay confined to `data/`);
  - `TestExportedAppMethodsAreThinBindings` still passes.

**As built (R2, 2026-11-09).** Every read chokepoint mints; no UI yet. Where the plan left a choice:
- **The glue.** `receipts` stays a leaf and holds the whole mechanism (`Intent`, `WithIntent`, the `Run`/`Facts` accumulator, `Recorder`, `Finish`); it cannot import `engine`, so the two text rules are handed in as `receipts.Checks{Statement, Value}` when the recorder is built by `engine.OpenRecorder(root, logger)` (key, `FileStore`, default `Policy`, `engine.RecordableStatement` and `engine.Recordable`). The `Policy` lives on the `Recorder` (`SetPolicy`, for R4b), not on the `Intent`. `Finish(ctx, rec, shown, truncated, total)` never fails a run: no recorder, no intent, a statement that is not a single select or explain, or a second call for the same run give `""`; a store failure is logged at warn and returned for a caller to ignore.
- **One recorder per open workspace.** `Workspace.Receipts()` opens it lazily (so the store is pruned once per open, not once per chat turn) and returns nil, with a warning, when the folder cannot be used (a symbolic link, say). `hub.Open` needs no wiring of its own: the editor, CLI and console read it from the opened workspace (`o.WS.Receipts()`), `hub.NewAgentHandler` attaches it with `Handler.SetReceipts`, and Studio's fallback handler does the same.
- **Engine.** `executeWith` notes the executed text (the text as handed to the engine, `$name` kept, unless the engine expanded a wildcard itself, in which case the bound, expanded text, since that is what ran), its `storageglob.Rewrite.Summarize` stand-in and the columns; `Manager.run` adds the start, the duration, the statement kind and the connections. The kind: one statement only; `SELECT`, `FROM`, `VALUES`, `TABLE` and `EXPLAIN` by their first word, everything else (`WITH` included: it can lead a `DELETE`) prepared on the connection that ran it and read as `duckdb.StmtType`; `EXPLAIN ANALYZE` is not an explain. `connections[]` is `session.ReferencedConnections`: the connection the names resolve against (the pin, or the session default, listed even for `SELECT 1`) and each one named in qualifier position; object storage, which has no catalog, is not listed (R3). Status maps `ok` to `connected`, `not-connected` to `not_connected` and every other state to `error`.
- **Result travel.** `engine.ResultSet.ReceiptID` (`json:"-"`) carries the id through `RunReadOnlySQLIn`, so `RunArtifactQuery`, the page host and the Studio wrappers forward it without a signature change; `QueryResult.ReceiptID` and `A2UIQueryResult.ReceiptID` (`receipt_id`) are the wire fields, mirrored in `env.d.ts` and `LiveQueryResult`.
- **Callers.** `RunReadOnlySQLIn` puts the intent on the final run's context after `readOnlyVerdict`, and finishes after the probe row, the DuckLake internal rows and the cap are gone; `preview_query` finishes over the sample it returns; `runQuery` over the rows after `MaxQueryRows` and the budget, and only for a statement classed a plain read. The surface of a render-time read comes from `receipts.WithOrigin` (default `render`, no source): artifact queries (`artifacts/<name>#<query>`), a metric's Try it, each subject's assertions and each watch set it; a join profile sets `Skip` and mints none (it measures a definition and answers nobody's question).
- **`agent_session`.** The MCP server puts it on the context: an SSE client's transport session id, or, on stdio, one random id per `Server`. Studio's chat panel names a turn, not a conversation, so the conversation is what the history says: a turn with no history starts one, a turn with history continues it, and closing the workspace ends it (`chatTurns.conversation`); no signature changed. A task uses its task id.
- **Console.** `.receipts on|off` is per `Session` (`settings.receipts`), off at the start and not remembered, and answers "not available here" when `Options.Receipts` is nil; the prompt shows `[receipts]`; a minted read prints a dim `receipt r_...` line. The receipt covers the rows the statement returned to the console (up to 10,000), not the lines drawn. The CLI prints nothing yet: `slaw receipts show` is R4a.
- **Tests.** `internal/receipts/recorder_test.go`, `internal/engine/receipt_test.go`, `internal/storageglob/summarize_test.go`, `internal/agents/receipts_test.go` (every agent and render path, the shown-rows checks including the result budget and the DuckLake drop, the mints-nothing cases, a failing store, redaction at the callers, SQL that cannot read the receipts folder or key), `internal/hub/receipts_test.go` (one file per chokepoint through `hub.Open` and `NewAgentHandler`), `internal/mcp/receipts_session_test.go`, `internal/repl/receipts_test.go` and `studio/receipts_test.go`. The 10,000-key wildcard is checked at the engine (`recordExecuted` over a real `storageglob.Expand`) and in `storageglob`: a statement over `s3://` keys cannot run in a unit test.

**R2 review fixes.** Column names are redacted as above (`Facts.NamesFromData`, found by the lexer). The store never blocks a Put: the open-time and every-500th prune run in one background goroutine with a try-lock on `prune.lock` (a busy lock skips the round), on a mutex a Put never takes; `FileStore.Wait`/`Close` and `Workspace.Close` wait for it; `Recorder.SetPolicy` forwards to the store, which prunes by it. The statement kind is the caller's word when it has one (`Intent.Kind`: the policy-gated paths, the console's own parse) and otherwise a parse by `json_serialize_sql`, never a `Prepare`. Another form that names columns from data and is not covered by a parse: a table function over a file or JSON value (`json_each`, `unnest` of a struct), which names its columns by its own rules.

### R3. Connection and snapshot facts

Collected after the run, for each catalog in `connections[]`, with a 300 ms total budget. A fact that misses the budget is recorded as `not_recorded`, never retried later. A later value would describe a different moment.

| Type | Fact | Label |
|---|---|---|
| DuckLake | `ducklake_current_snapshot()` | replayable |
| `iceberg_catalog` / Iceberg | snapshot id of each table read, from `iceberg_snapshots()` | replayable |
| Delta | table version | replayable |
| Postgres | `pg_current_wal_lsn()` on Slaw's session | live only |
| DuckDB file | size + mtime | live only |
| S3/GCS/R2 files | ETag via one HEAD through the `storageglob` lister (at most 8 objects, then `+N more`); as built, size and mtime, see below | live only |
| MotherDuck, MySQL, SQLite | status only | live only |

Collectors live in `internal/session`, next to the attach code that already knows each type. `receipts` stays a leaf that only stores them.

**As built (R3, 2026-11-09).** Each connection in `connections[]` carries a `snapshot{fact, value, label}` (what each fact means: `docs/reference/console.md`, "Snapshot facts"). Where the plan left a choice or the build differs:
- **Where.** `session.SnapshotConnections(ctx, sess, catalog, sql)` (`internal/session/snapshot.go`, collectors in `snapshot_collect.go`) replaces `ReferencedConnections` in `engine.recordRunFacts`; `receipts` is unchanged apart from a comment. It runs only when the statement is a select or explain (what mints). The fact queries go to the pool (`Session.DB()`), never the run's pinned connection and never the engine, so they cannot move its catalog, change its result or carry an intent, and no receipt is made for them.
- **Budget.** One 300 ms deadline over all collectors, which run at once. A collector that has not answered is `not_recorded` with the value `timed out`; its answer goes to a channel nobody reads, so a late answer is dropped and nothing is retried. Any error is `not_recorded` with the value `unavailable`, never the error's text. A connection that is not attached has no `snapshot` (nil): it is not asked and not connected.
- **Object storage and table connections** attach no catalog, so R2 left them out of `connections[]`. They are now listed when a string literal of the executed text points into a storage connection's scope or at an Iceberg or Delta table connection's location (a wildcard the engine expanded is already a list of plain URLs, which is why it is the executed text that is read). A literal that a table connection claims is not also an object of its storage connection.
- **No ETag.** DuckDB 1.5 has no function that returns one (`glob` gives names, `read_blob` gives `size` and `last_modified`), and the plan's "HEAD through the lister" cannot reach it from SQL. The fact is the size and modification time of up to 8 objects (sorted by URL, so the list is stable), each from one `read_blob(url)` that selects no content (httpfs makes a HEAD), at once on the session that read them. Value: `N objects: size@unix,... (+K more)`; if that would pass 200 bytes, `N objects, digest <16 hex> (+K more)`. If any one of the objects cannot be asked, the whole fact is `not_recorded`, so a short list is never mistaken for the whole. No URL, bucket or key is in the value: only numbers and a count. The value holds nothing the "Not in a receipt" line excludes, and no bucket, key or path.
- **Iceberg tables read (judgement call).** DuckDB 1.5's `iceberg_snapshots()` takes a table *location*, not a catalog table (checked against the embedded extension: `iceberg_snapshots('cat.ns.t')` is read as a folder), and finding which tables a statement reads needs a bind that goes back to the remote catalog, which the R2 review rejected for the hot path. So an `iceberg_catalog` connection records `not_recorded` ("not available for catalog tables"), not a catalog-level guess, and the drawer should not offer replay for it. An Iceberg *table connection* (a location) is replayable: the newest snapshot id (`iceberg_snapshots(<location the statement used>) ORDER BY sequence_number DESC LIMIT 1`; the location is the literal in the executed text, so it names the metadata the run read). A table that needs a version DuckDB will not guess is `not_recorded`.
- **Delta** has no version function in the extension, so a table in a folder on this computer is the newest `NNN.json` commit in `_delta_log` (names only, no file is read); a table in object storage cannot be listed by the hub and is `not_recorded`.
- **Postgres** runs `postgres_query(<connection>, 'SELECT pg_current_wal_lsn()::text')` on the session's own attachment and records the value only when it has the shape of a WAL position. The in-memory default and types with no fact (MotherDuck, MySQL, SQLite) are `status only`, live only.
- **R3 review fixes.** Collectors read only the connection's own definition (`spec.Path`): a literal in the statement decides whether a connection is listed (a literal with a `..` segment never matches) and which objects of a storage scope are asked, and never where a collector looks. The Iceberg metadata-file form is dropped for the same reason. Delta refuses a `_delta_log` that is not a real folder by `Lstat` (a link or a FIFO is never opened). A statement that pins a version (`version`, `snapshot_from_id`, `snapshot_from_timestamp`, a DuckLake `AT (VERSION|TIMESTAMP ...)`) records `not_recorded` / "version pinned in the statement" for replayable types. The snapshot is the one current when the run finished; a commit during the run is not detected (the R4a drawer says "current when the run finished"). Only a literal after FROM or JOIN, or the first argument of a `read_*`, `*_scan` or `read_blob` call, counts as a read. At most 4 collectors run at once process-wide (a run that cannot get a slot within its budget is `timed out`); objects are capped at 8 per connection, not per run. A collector blocked in a syscall on a hung mount holds its slot until it returns.
- **Tests.** `internal/session/snapshot_test.go` (a DuckDB file without its path, status-only types, a connection not attached is skipped and not connected, Postgres and Iceberg through stand-in functions for the extensions that are not loaded, a real DuckLake that follows a commit (skipped when the extension is not installed), Delta, which literals pick which objects, the 8 + count rule against a loopback server that serves no content, a spent budget, a slow collector, the pinned connection left alone) and `internal/engine/receipt_test.go` (facts on a real run, exactly one receipt, a non-read asks for none). Not covered here: a live Postgres, a real Iceberg catalog and a real bucket (integration environments).

### R4a. Read surfaces: `slaw receipts show`, `GetReceipt`, the drawer

- CLI: `slaw receipts show <id>` (text and `--json`) and `slaw receipts list [--surface] [--limit]`. These are the drawer's two views (CLI mirrors UI).
- Binding: `GetReceipt(id)` and `ListReceipts(filter)`, as thin bindings over `studio/receipts.go`.
- Frontend (screens: the E1 boards of the *Answers You Can Check* canvas, `docs/design/README.md`):
  - `components/EvidenceDrawer.vue` and `composables/useReceipt.ts`. The drawer shows: a one-line summary with Replayable / Live only and the policy verdict; the query with an **As written / As run** switch and its parameters; connection and snapshot; result rows, columns and fingerprint; when, by whom and from which file; the receipt id with copy and the `slaw receipts show` hint. Esc closes it and returns focus to the button that opened it;
  - its states: live only, parameter values not kept (values from the open run, else "not kept"), no receipt kept (with the reason), and opened from an id (no value, since receipts never hold one);
  - an "Evidence" affordance on the Query Explorer grid footer, notebook SQL cells, and A2UI `stat`, `table` and every chart, via `A2UIChartFrame` and `useLiveQuery`, in notebooks and in `view: a2ui` artifacts;
  - `RunA2UIQueryIn` gains a source argument (`{kind, notebook|artifact, cell|component}`), so `render` receipts get a `surface` (`notebook_cell`, `a2ui`) and a `ref`. Go validates it against the open notebook or artifact and drops a ref that doesn't match; the `AppApi` arity check and `check-arity.mjs` change with it.
- Script views (`view: script`, an inert SVG) and static and scripted HTML pages get evidence in a later slice. Pages need a `slaw.receipt` API decision and a P6-style prompt line.
- Rodney QA on `:34115` is the acceptance check: open a cell, open the drawer, and see SQL, params, connection status, snapshot fact and id.

### R4b. Settings and clear

- Per person, never in `slaw.yaml` or a bundle: `workspace.SetLocalReceipts` beside `SetLocalAgent` (`internal/workspace/settings.go`), writing `ws.Local()`. It does not go through `ApplySettings`, which writes `slaw.yaml` and refuses on a stale team form. An untrusted or unreadable local file means the defaults.
- Settings > Workspace > Receipts: the toggle, the three limits, current usage and **Delete all receipts** (confirmed).
- CLI mirror: `slaw receipts settings [--keep-params on|off] [--days N] [--max-count N] [--max-mb N]` and `slaw receipts clear`.
- A change applies to new receipts; existing ones are not rewritten, only pruned (a lowered limit prunes on save).

### R5. Agent access and chat chips (the first slice of #7)

- The `get_receipt` tool with the own-session rule. Tests probe it: a receipt minted by one MCP session (SSE, same handler) requested by another is refused exactly like a missing id; a chat conversation reads its own receipts from earlier turns; a person's receipt is never returned.
- In-app chat: tool chips show `receipt_id`, row count and Open evidence. ✓ chips go on numbers in the answer that match a value in one of the turn's receipts. Matching is done in Go against the in-memory result of the turn, never against the store, which holds no values. Numbers that don't match get the dotted "not from a query" underline. The grounding check (`internal/llm/grounding.go`) is unchanged, and a test pins its behaviour.

Notes for R5 from the R2 review:
1. Go mints the chat conversation id and returns it on `AgentChatResult`, and accepts back only ids it issued in this workspace (today the id is derived from the history length).
2. The AI cell dialog keeps one session across its follow-ups (today each `taskId` is its own session).
3. `get_receipt` never returns `agent_session`.
4. Document that a new SSE session after a reconnect loses access to the earlier session's receipts.
5. The snapshot recorded is the one current when the run finished; a commit during the run is not detected.

### Later slices (separate plans when reached)

- inline live values (schemaVersion 3);
- "Why this number" (a single-level GROUP BY rewrite);
- Profile this run;
- evidence-carrying exports (#1, #6);
- page evidence;
- #30 (receipts for external agents' grounding).

## Exit for this plan (R0–R5)

- Every query-rendered value in the Query Explorer, notebook cells and A2UI components (in notebooks and `view: a2ui` artifacts) opens to a receipt.
- `run_query`, `run_saved_query` and `preview_query` return `receipt_id` over MCP.
- Tests prove four things:
  - no receipt reaches the folder or a bundle;
  - the redaction test passes;
  - the agent can't read another session's receipts;
  - grounding is unchanged.

## Risks

- **The cost of hashing on large grids.** It is bounded by the existing row caps (`queryGridRowCap`, `a2uiQueryRowCap`). The R1 benchmark watches it.
- **The cost of snapshot facts on remote sources.** There is the R3 budget, and Postgres gets one cheap function call. It is skipped entirely when the connection is not attached yet.
- **Store growth.** The console is opt-in per session, and the caps are configurable with floors so a typo can't disable pruning.
- **A false sense of proof.** "Live only" is printed on every non-replayable fact, and nothing reads "verified" until E3.

## Decisions from the owner (2026-11-09)

1. **Drawer.** Evidence opens in a right-hand drawer, like the Watches alert panel. Esc closes it and returns focus to the value. The E1 screens get a design pass before R0 (see R0).
2. **Parameter values are stored by default**, capped at 256 bytes each, with a per-person toggle (**Keep parameter values**) that keeps only names and `params_hash`.
3. **Console receipts are off by default.** `.receipts on` turns them on for one console session.
4. **Retention is 30 days / 20,000 receipts / 64 MB by default**, and configurable per person in Settings > Workspace > Receipts and `slaw receipts settings`.
