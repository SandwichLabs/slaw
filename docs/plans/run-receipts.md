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
| `sql` | The text as written (with `metric()` and `$name` intact). It is kept only if `engine.Recordable` (today the unexported `repl.recordable`, moved and exported in R1; it lives in `engine` because it uses `engine.TerminalText`, and `engine` already imports `secrets`) passes it, so a statement that looks like a credential is never stored (the console history's rule). Otherwise `sql_withheld: true` and only the hash. |
| `run_sql` | The text the engine executed: after `metric()` expansion, the caller's cap wrapper (`_a2ui_limit`, the grid cap) and wildcard expansion, with each expanded key list replaced by its pattern, "expanded to N objects" and a hash of the list, so 10,000 keys never land in a receipt. That is a new `storageglob` `Rewrite.Summarize(sql) (text, n, listHash)` with no clipping (`Rewrite.Redact` is for error messages: it rewrites `LINE n:` and clips at 4000 bytes). `run_sql_hash` hashes the executed text, not this stand-in. Same `engine.Recordable` rule. The drawer's **As run** view shows it. |
| `sql_hash`, `run_sql_hash` | SHA-256 of the text as written and as run. |
| `params` | Bound values, each capped at 256 bytes, with `params_hash` over the full set. Each value passes `engine.Recordable` or is withheld on its own (`"withheld"`). Stored by default; with **Keep parameter values** off (Settings > Workspace > Receipts) a receipt keeps the names and `params_hash` only, and `params_withheld: true`. |
| `connections[]` | `{name, type, read_only, status}`, `status` an enum (`connected`, `not_connected`, `error`) and never the status message, which can name a host, for each catalog the statement resolved to, plus that connection's `snapshot` (below). |
| `verdict` | `read` for policy-gated runs; `unchecked` for the person's own editor, console and CLI, which are not gated. Only reads mint in this plan, decided by the statement type DuckDB reports when it prepares the statement (`duckdb.StmtType`, as `repl`'s `Session.classify` does): `SELECT` and `EXPLAIN` mint; writes, DDL and everything else don't. |
| `rows` | `{returned, total, truncated}` as **shown**: after the caller drops its probe row (`maxRows+1`), DuckLake internal rows and the tool-result budget. |
| `columns` | Names and types only. |
| `result_hmac` | HMAC-SHA256 over a canonical encoding of the columns and the rows as shown, keyed with a per-workspace key in `.slaw-local/receipts/key` (0600, created on first use). The key stops anyone brute-forcing a small result from the hash. |

Not in a receipt: any row value, a credential or anything `engine.Recordable` refuses (in the text, the run text or a parameter value), a secret name's value, a connection's DSN or status message, or an absolute path in `source.ref` or `connections[]`. SQL text is kept as the person wrote it, so a file path they typed (`read_parquet('/home/...')`) stays in `sql`: the receipt is on their own machine, beside the file that holds the same text. A redaction test enforces each line (R1 for the record, R2 for the callers).

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

### R2. Mint at the chokepoints, `receipt_id` in results

- `receipts.WithIntent`, the engine filling run facts on the context, `receipts.Finish`, and the recorder wired at every row of the chokepoint table's last column: `hub.Open`, `hub.NewAgentHandler` and Studio's fallback handler.
- `agent_session` on the context: the MCP server sets it from the transport session; Studio's chat sets the conversation id.
- `engine.Recordable` (moved from `repl`, which keeps calling it) applied by each caller to the text, the run text and each param value; `Rewrite.Summarize` for `run_sql`.
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

### R3. Connection and snapshot facts

Collected after the run, for each catalog in `connections[]`, with a 300 ms total budget. A fact that misses the budget is recorded as `not_recorded`, never retried later. A later value would describe a different moment.

| Type | Fact | Label |
|---|---|---|
| DuckLake | `ducklake_current_snapshot()` | replayable |
| `iceberg_catalog` / Iceberg | snapshot id of each table read, from `iceberg_snapshots()` | replayable |
| Delta | table version | replayable |
| Postgres | `pg_current_wal_lsn()` on Slaw's session | live only |
| DuckDB file | size + mtime | live only |
| S3/GCS/R2 files | ETag via one HEAD through the `storageglob` lister (at most 8 objects, then `+N more`) | live only |
| MotherDuck, MySQL, SQLite | status only | live only |

Collectors live in `internal/session`, next to the attach code that already knows each type. `receipts` stays a leaf that only stores them.

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
