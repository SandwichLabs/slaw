# Slaw direction — an agentic workspace for the data you already have

**Date:** 2026-10-06 (delivered 2026-10-06; see §9)
**Status:** **Delivered.** Epic vibe-app#105 (issues vibe-app#106–vibe-app#119) is merged: the code matches this document, apart from the departures listed in §9. This remains the product north star.
- **Next:** [`plans/answers-you-can-check.md`](plans/answers-you-can-check.md) (epic #250, 2026-10-26) builds on this direction: every number Slaw shows comes with its evidence. See §10.
- **Supersedes** the 2026-09-22 direction (Vibe as a "single-file" data workspace), whose primitives mostly carry forward. That version is in git history.
- **Plan:** [`plans/workspaces-and-connections.md`](plans/workspaces-and-connections.md) (implemented; its "as built" summary lists the amendments and open follow-ups).
- **Tracking:** epic vibe-app#105, issues vibe-app#106–vibe-app#119. What is left (packaging and signing in #4, and the follow-ups named in §9) is listed there.
- **Earlier work this builds on:** vibe-app#62–vibe-app#70 (shipped in PR vibe-app#71) and vibe-app#93 part 1 (PR vibe-app#104).
- **Issue numbers:** the numbers in this document up to #119 are from the archived `SandwichLabs/vibe-app`; read them as `vibe-app#N` (the diagram in §5 uses them bare). The packaging epic (#4), the launch issues (#36–#51), #118 in the DuckLake bullet of §9 and everything after it are in this repo.

---

## 1. The change in one paragraph

Slaw is an **agentic desktop workspace for working with data where it already lives**. It has the shape of DataGrip or DBeaver, with an AI agent, live notebooks, skills and A2UI artifacts built in, and it exposes all of it to external agents over MCP.
- **Connections:** data stays resident in its connections: local DuckDB files, MotherDuck, Postgres, SQLite, S3/GCS/R2, and the files and formats DuckDB's extensions can read. Each connection is attached into one DuckDB hub, so any source can be joined with any other.
- **Workspace:** a folder you open like an Obsidian vault and can switch at any time. Its notebooks, saved queries, skills and connection definitions are plain files, so a workspace can be a **git repository shared with a team**.
- **Credentials:** they live in the **OS keychain** or environment variables, never in the folder.
- **`.slaw` file:** it is an **exchange format** (a bundle) for notebooks, queries, skills and connection definitions. It no longer holds data, and the older data-holding `.vibe` container is not opened or imported at all (§9).

## 2. Why

- **What users asked for.** Early feedback was consistent. A file that *holds* the data is less useful than a tool that *reaches* the data people already have, especially shared backends like MotherDuck, where the data is already shared and versioned. The single-file container made every team workflow a copy.
- **Why the old container caused trouble.** It caused most of the trust problems the 2026-09-26 persona panel found (vibe-app#93): a temp plaintext copy, Save-or-lose semantics, last-writer-wins between Studio and MCP, and no way to share notebooks without sharing data. PR vibe-app#104 patched those. This direction removes their cause.
- **Prior art.** `../duck-tape` proved the hub shape (attach every source into one DuckDB, connection name = catalog alias, read-only by default). Its unbuilt PRD has the right ideas for project discovery, env interpolation and schema caching. Its secret handling (plaintext DSNs, logged) is what we will not do.

## 3. The primitives

Eight nouns. Everything in the product is one of these.

### 3.1 Workspace — vibe-app#109, vibe-app#111
- **What it is:** a folder with a `slaw.yaml` at the root. That file holds the id, name, default connection, agent settings and shared connection definitions.
- **What's in it:** `notebooks/*.slnb`, `queries/*.sql`, `skills/<name>/SKILL.md` and `data/`, which holds local files and the default `data/local.duckdb`.
- **Machine-local state:** lives in `.slaw-local/`, which git ignores.
- **Discovery:** found by walking up from the current directory, the way git finds `.git`.
- **Studio:** opens workspaces from a launcher with a recent-workspaces list, and switches between them from the shell header.
- **Files are the source of truth.** Writes are compare-and-write, and a watcher picks up changes from git, editors, Claude Code and other processes.
- **Git-friendly.** Files serialise in a stable, diff-friendly form. Outputs are never persisted.

### 3.2 Connections — vibe-app#108, vibe-app#115
- **Definition and naming.** A named, typed definition attached into the session's in-memory DuckDB hub. The name is the catalog alias in SQL (`prod_pg.public.orders`).
- **Types.** `duckdb` file, `postgres`, `mysql`, `motherduck`, `sqlite`, `s3`/`gcs`/`r2`, `azure`, `ducklake` and `iceberg`/`delta` (vibe-app#115, vibe-app#119). The metadata-driven formats (`ducklake`, `iceberg`, `delta`) may be defined in any scope, with a warning where a shared one is shown or imported (§9).
- **Scopes:** user (every workspace), shared (committed in `slaw.yaml`) and local (this machine).
- **Behaviour.** Connections are read-only by default and only connect when first used. Each has a visible status: connected, error, needs credentials, extension missing, or locked by another process.
- **MotherDuck:** one account per workspace. A MotherDuck connection cannot be attached read-only (DuckDB does not support `READ_ONLY` on `md:`); use a read-scaling token for read-only access.

### 3.3 Credentials — vibe-app#114
- **Where they live:** the OS keychain, or `env:` references for CI, headless use and development. There is no plaintext fallback.
- **Write-only across every boundary.** Studio's UI, MCP and the agent can set or clear a credential, never read it.
- **What's covered:** per-person values (username, password, tokens, keys) live with the credential, never in committed config.
- **Kept out of logs and errors.** Secrets are redacted from logs and errors.
- **The LLM API key** moves here too.

### 3.4 Tables
- **Where they live:** tables stay in their sources. DuckDB is the single engine and queries them in place.
- **Editing.** Datasheet editing, CRUD, import (CSV, xlsx, Parquet, JSON via extensions) and export work against any **writable** connection. The default target is the workspace's local DuckDB.
- **No save step.** Writes are the source's own transactions, so connection data has nothing to Save or Revert.

### 3.5 Notebooks — vibe-app#66
Unchanged in spirit.
- **Format:** JSON documents of `sql` / `markdown` / `a2ui` cells, one file per notebook.
- **Running:** outputs are never persisted, and every data cell re-runs on open.
- **Connections (format v2, vibe-app#108):** a SQL cell or A2UI binding may name a `connection`, and otherwise uses the workspace default.
- **Agent authoring** uses task mode: `preview_query` before a terminal `write_cell` or `need_more_info`.

### 3.6 Skills — vibe-app#62
- **Format:** `SKILL.md` documents. Built-ins are embedded, and workspace skills live in `skills/<name>/SKILL.md`.
- **Compatibility:** they use the same format external agents such as Claude Code read.
- **Loading:** the agent sees each skill's name and description and loads the body on demand.
- **Table-bound skills:** gate on a catalog-qualified table, checked lazily so opening a workspace never connects to a remote source.

### 3.7 Artifacts (A2UI) — vibe-app#67
Unchanged: `{ title, components[] }`, a closed color enum, unknown fields rejected, inline data or a live `sql` binding, Go validates and Vue renders. Bindings gain an optional `connection`. The component set has since grown to twenty types (the KINETIC charts).

**Data artifacts (#162).** A parameterised, live view stored as files, `artifacts/<name>/artifact.yaml` (typed parameters, *named* queries) plus `view.a2ui.json`. It stores questions, never answers; a value is bound as a parameter and never becomes SQL text. The A2UI schema gains parameter controls, `pivot` and drill-down for views only (`a2ui.ValidateView`). The agent authors with `write_artifact`, one tool surface for Studio and MCP. See [`reference/artifacts.md`](reference/artifacts.md) and [`plans/data-artifacts.md`](plans/data-artifacts.md).

### 3.8 Agent + MCP — vibe-app#63, vibe-app#64, vibe-app#70, vibe-app#79, vibe-app#116
- **One tool surface.** A single registry in `internal/agents` is used by the in-app runner and exposed over MCP with the same gating.
- **Default: reads freely, writes need permission.** A write needs all three of:
  - the sandbox tier allows it;
  - the connection is writable;
  - the user approves it. The vibe-app#79 approval gate is forced on for any connection that isn't local.
- **SQL policy is an allow-list.** The agent can never attach databases, touch secrets or settings, install extensions, use raw-SQL passthroughs, or read files or URLs outside the workspace's data and the configured storage scopes.
- **MCP.** `slaw mcp serve` discovers the workspace from the current directory. When Studio already has the workspace open, it hands requests to Studio's server (vibe-app#112), because DuckDB allows only one writer per local file.

### Import / Export — vibe-app#118
- **`.slaw` bundle (v2):** a zip of notebooks, queries, skills and connection definitions (never credentials). It is unencrypted by default, with optional passphrase encryption ([age](https://age-encryption.org)). `slaw export bundle`, `slaw import <file.slaw>`, and the workspace menu in Studio; opening a `.slaw` file with Slaw starts the import.
- **Legacy `.vibe` files (v1, SQLCipher + data):** **not supported** (vibe-app#110 was dropped). They get a plain "not supported" message.
- **Notebook export** to HTML/PDF/xlsx is still vibe-app#73.

## 4. What stays, what goes, what changes

| | |
|---|---|
| **Stays** | DuckDB as the single engine; notebooks, skills, A2UI, task mode; one tool surface; MCP; guardrails; the vibe-app#79 approval gate; the vibe-app#80 holder-naming lock (reused for hub ownership); import wizard (vibe-app#82); datasheet editing; SchoolHouse theming; TUI as a SQL REPL |
| **Goes** (gone) | The SQLCipher container and `internal/container`; the plaintext temp DuckDB copy; Save / Don't save / Revert for table data (vibe-app#81, vibe-app#98); per-file passphrases; `verify`, `compact`, `rekey`, `dump`, `passphrase`; `.vibe` as the thing Studio opens |
| **Changes** | Container → workspace folder. One DuckDB → a hub of connections. `_vibe_*` rows → files. Plaintext LLM key → keychain. Agent `full` deny-list → allow-list. `Unlock` → launcher + switcher. `vibe mcp serve <file>` → `slaw mcp serve [--workspace]`. `.vibe` → `.slaw` exchange bundle. |

## 5. Sequencing

The owner chose to build workspaces first.

```
Phase 0 ─ #106 design pass   #107 spike
              │                 │
Slice 1 ─ #108 session hub ─► #109 workspace + stores ─► (#110 legacy import: dropped) ─► #111 Studio launcher/shell
                     │                    └──────────► #112 hub lock + mcp serve     #113 CLI (closes #72)
Slice 2 ─ #114 secrets ─► #115 connections ─► #116 agent SQL policy ─► #117 Studio connections UI + `slaw conn`
Slice 3 ─ #118 .slaw bundle   #119 more types   file-open wiring (done)   #4 packaging & signing (open)
```

- **Slice 1** shipped a usable workspace with local DuckDB only: git-shared notebooks, agent and MCP working.
- **Slice 2** adds remote sources and the keychain.
- **Slice 3** is exchange and breadth. Done, apart from packaging.

## 6. Design principles

- **Data stays where it lives.** Slaw reaches data; it doesn't copy it into its own format.
- **The workspace is the app.** A folder of plain files you can version, diff, review and share.
- **Credentials in the keychain.** Never in the folder, the bundle, the logs, or the agent's context.
- **Read-only until asked.** Connections default to read-only, and agent writes need approval.
- **One tool surface.** The built-in assistant and external MCP clients see the same tools with the same gating.
- **AI as collaborator, not dependency.** Everything works without AI; notebooks and SQL are first-class by hand.
- **Live, not frozen.** Notebooks and artifacts store questions (SQL), not answers.
- **Progressive disclosure.** Skills advertise a line and load on demand; connections connect on first use.

## 7. Non-goals

- Bound forms, App Mode, macros, a data-entry persona (still gone, see vibe-app#68).
- A Python execution plane. Revisited only through the compute decision spike (#248) and an owner decision recorded in §9. Scheduled refresh is now planned as standing questions (#247), read-only and enabled per person.
- A plaintext-file fallback for credentials.
- A git client: commit, push and merge stay in the user's git tool. Studio shows branch and dirty state only.
- Multi-user / Tailscale mesh. Still deferred; shared backends plus git cover the sharing need for now.
- Windows, for this epic (macOS + Linux per #4).
- A terminal or server-side A2UI renderer.

## 8. Prior art and references

- Earlier internal prototypes (a DuckDB hub with ATTACH, schema discovery and secret-handling lessons; a notebook, A2UI, skills and task-mode prototype). Not part of this repository.
- A three-persona UX panel review (2026-09-26): the trust findings this direction answers. Its write-up is not part of the public repository.
- `docs/archive/ms-access-parity-plan.md` and related: the Access-parity roadmap, superseded 2026-09-22.

## 9. As delivered: what changed from the plan

Everything above shipped. These are the places where the result differs from what was first written, in the order they matter.

- **No legacy import (scope change, vibe-app#110 closed).** The plan made the legacy `.vibe` importer permanent. It was dropped by an owner decision recorded on the epic (2026-10-06), which also let the container code go. Studio, the CLI and MCP never open a v1 file, `internal/container` and the container commands (`info`, `verify`, `compact`, `rekey`, `dump`, `passphrase`) are gone (the names stay registered as hidden stubs that point to `slaw workspace init`), and a v1 or SQLCipher `.vibe` is told "not supported". `MigratePages` went with it, so old pages are not migrated.
- **The agent's SQL policy is an allow-list, parsed first (vibe-app#116).** The syntactic check happens before any `EXPLAIN` binds the statement. Reads are the readers the policy names, over `data/` and the scope of a ready storage connection; writes need three gates (tier, writable connection, person's approval), and the approval is always required for anything that is not a local file, so headless MCP refuses those writes. See [`reference/agent-policy.md`](reference/agent-policy.md).
- **Metadata-driven formats are open, with a warning (changed 2026-11-07; was own-scope only, vibe-app#119).** DuckLake, Iceberg and Delta read whatever files their own metadata names, so a crafted table in a cloned repository could read any file on the machine. The first decision refused to let them into a committed `slaw.yaml`, a bundle or the agent. Zac replaced it: "Let's not restrict iceberg catalogs. This tool should be able to run the same queries DuckDB itself can. Just warn where necessary." Only "read-only by default" stays. They can now be defined in any scope, `iceberg_scan`, `iceberg_metadata`, `iceberg_snapshots` and `delta_scan` are agent readers under the same path, URL and reader-count rules as `read_parquet`, and the warning is shown on the connection form, in `slaw conn add|move|list` and on bundle import. Every credential rule stands.
- **Trust moved out of the folder.** A workspace arrives with whatever its authors committed, so a committed `slaw.yaml` can only *tighten* the agent's limits, its `credentials:` line is only a UI hint, `${VAR}` interpolation and absolute paths are refused in it, and per-person settings (looser agent policy, sign-in method) live outside the folder, keyed by workspace id and the folder's real path.
- **MotherDuck cannot be read-only.** `READ_ONLY` is not supported on `md:` attaches, so the type defaults to `read_only: false` and rejects `true`; read-only comes from a read-scaling token.
- **DuckLake catalogs: SQLite, and in object storage (#118, PRs #119-#124 and the end-to-end step).** The DuckLake type takes a `catalog` option (`duckdb` or `sqlite`) and a catalog that is a storage URL, read-only, vetted before attach (a crafted catalog's views and rows could otherwise read any file), with the lake connecting its storage connection first and saying `needs the "floci" connection: ...` when that is not ready. It needed DuckDB 1.5.6 (the bump, #120). The case it was built for, a SQLite catalog and Parquet in a no-TLS path-style S3-compatible store, is exercised end to end over the CLI, the agent tool surface, `slaw mcp serve` and Studio ([`plans/ducklake-catalogs.md`](plans/ducklake-catalogs.md)). Postgres metadata catalogs and an advanced-attach escape hatch stay deferred.
- **Bundles use age (vibe-app#118).** The optional passphrase envelope is an [age](https://age-encryption.org) scrypt recipient, not a bespoke format.
- **Storage setup from the lake form, and AWS SSO sign-in for S3 (#130, #131).** A lake, Iceberg or Delta form shows whether a storage connection covers its URL, offers **Set up storage** (with S3-compatible presets) and Test tries the storage connection first. An S3 connection can sign in with an `aws-sso:PROFILE` from `~/.aws/config`: Slaw resolves the profile in Go (not DuckDB's `credential_chain`), keeps the keys in memory and the DuckDB temporary secret, renews them before they end, fails closed (never anonymous) when the session ends, and runs the device sign-in natively from Studio and `slaw conn credentials sign-in`; `slaw mcp serve` only reports "needs sign-in". The SSO token lives only in the AWS CLI's shared cache. S3, GCS and R2 wildcards are expanded by Slaw in a private listing session, because the URL-parameter hardening (#138) turns DuckDB's own globs off. Plan: [`plans/storage-setup-and-sso.md`](plans/storage-setup-and-sso.md).
- **Data artifacts, level 1 (epic #162, steps 1-6b and 10; PRs #164-#172).** Delivered as planned: the `internal/artifacts` store with one revision over both files, `Bind`, the context-aware view validator, `write_artifact` / `list_artifacts` / `get_artifact`, Studio's list, view, controls, pivot, drill-down and Source tab with conflicts, `.slaw` bundle entries and `slaw artifact list`. Every query is vetted at save and again at every run, in every tier, so a file edited by hand, git or a bundle is held to the same policy. No tool runs an artifact for the agent. Plan: [`plans/data-artifacts.md`](plans/data-artifacts.md).
- **The Library (plan [`plans/library.md`](plans/library.md), steps L1-L5, 0.3.0).** Studio's Notebooks and Artifacts rail entries became one **Library** with a type picker for New (notebook, live view, page, chart script) and an agent hand-off; a UI change only, with storage, bundles, MCP and the CLI unchanged.
- **Data artifacts, level 2 (custom views): not built.** The step 7 spike (Linux, 2026-10-17) dropped the iframe design: a sandboxed frame can still reach the Wails binding IPC, because Wails authorises a message by the top view's URI (filed as [#174](https://github.com/SandwichLabs/slaw/issues/174)), and an iframe cannot be stopped when it loops. A terminable worker returning an SVG or a validated A2UI page passed every row on Linux and, in the macOS run (2026-10-18, [#158](https://github.com/SandwichLabs/slaw/issues/158)), on macOS too, so the outcome is a reshape to worker views. Building it (plan steps 8-9) waits on the owner's sign-off.
- **HTML pages (epic #162, steps P1-P6; PRs #186, #192, #194, #195 and the P5/P6 stack).** Built as planned, with the departures recorded in [`plans/html-artifacts.md`](plans/html-artifacts.md) (*As built*). `view: html` adds a Go `html/template` page (`artifacts/<name>/html/`) whose only data source is the manifest's named queries. A static page renders in Studio in a script-less frame; a scripted page opens in its own window in a helper process with no workspace, credential or store (Linux: bubblewrap network gate; macOS: filtering layers with one accepted, disclosed leak, #189, and a shared keychain identity). Scripted pages may declare `state` (local only), `download` (save dialog behind a physical input) and `ask` (one tool-less completion). Trust is the `.env` pattern extended to a file set (`PageHash` in `views.json`); the allow prompt lists the files, the queries and their connections, each capability in plain words and, on macOS, the network disclosure. The agent writes pages with `write_artifact`'s `html_files` and reads them back, but no tool or MCP method renders, runs or allows one, and every edit asks again. Bundles carry the files, never a decision or state. `slaw artifact view` covers pages. The mac gate still needs a run on real Macs (the owner's QA checklist), and a network-free replacement for the macOS leak is #189.
- **Packaging ships unsigned; signing is still open.** Launch arguments, macOS `OnFileOpen`, the `Info.plist` document types and the Linux `.desktop` and MIME files are in the repo ([`packaging.md`](packaging.md) §1a), and v0.1.0 onward ship as ad-hoc-signed tarballs with `install.sh`. Signing, notarisation, the offline-extension decision and the signed-build keychain check remain in epic #4 (macOS human checks in #81).

### Open-source launch decisions (2026-10-11, epic #51)

Recorded under #36 and #39; the audit and the plan are in [`plans/open-source-launch.md`](plans/open-source-launch.md).

- **Name:** kept **Vibe** and `vibe` at the time. Because Mistral Vibe also installs a `vibe` command, `install.sh` carried a collision guard (#47) (it refuses to overwrite another vendor's binary). The product was renamed **Slaw** in v0.6.0 ([`plans/rename-to-slaw.md`](plans/rename-to-slaw.md); first renamed Assay, which never shipped: [`plans/rename-to-assay.md`](plans/rename-to-assay.md)); the guard stays, now for any other `slaw`.
- **Repo and module:** `SandwichLabs/slaw`, Go module `github.com/SandwichLabs/slaw` (until v0.6.0: `SandwichLabs/vibe`). Public since v0.1.0 (the go-public step, #50).
- **Licence:** Apache-2.0.
- **History:** the public repo is a filtered copy of `vibe-app` `main`. `landing/`, `docs/reviews/ux-panel-2026-09-26/` and `docs/archive/*.txt` were dropped from all history, the 88 MB binary was never on `main`, and a work-email author was mailmapped.
- **Reporting:** GitHub only, no email address. Vulnerabilities go through GitHub private vulnerability reporting; everything else through Issues.
- **Contributions:** DCO sign-off (`git commit -s`), no CLA.
- **Old repo:** `SandwichLabs/vibe-app` is archived (read-only, pointer in its README). All development happens in `SandwichLabs/slaw`; `vibe-app#N` references in the docs are history.
- **Telemetry:** none.
- **v0.1.0 shape:** unsigned, ad-hoc-signed tarballs installed by `install.sh`; signing, notarisation and Homebrew stay with the packaging plan ([`packaging.md`](packaging.md)).
- **Pruned for the public:** the ast-grep rules moved to `tools/ast-grep/` and `RODNEY.md` to `docs/contributing/rodney.md`.

### Open follow-ups

- #131: not yet checked against a real IAM Identity Center (needs a real AWS account); the fakes and MinIO STS cover everything else.
- #126 (a workspace with a MotherDuck connection cannot open offline), #128 (the Tables view lists only the default schema), #127 (the rail's Fix on a dependency error opens the lake, not the storage connection), #129 (smaller DuckLake catalog follow-ups) and #142 (Floci as the S3 test double instead of MinIO).
- No CI job runs the `-tags integration` tests (MinIO, the SSO journey), and macOS is unverified for the SSO sign-in.
- #20: agent reads of Iceberg and Delta through a confined hub.
- MotherDuck has only been exercised against the spike and with fakes; a pass with a real token is outstanding.
- Iceberg REST catalogs; DuckLake with a Postgres metadata catalog (a DuckDB or SQLite catalog, on this computer or read-only in object storage, is supported).
- Removing a downloaded extension from Studio (there is Install but no Remove).
- #4: signing and notarisation, CI and release, extension bundling, keychain on signed builds.
- #1: notebook export to HTML, PDF and xlsx. vibe-app#56 (Studio Home) is superseded by the launcher.

## 10. Next: answers you can check (2026-10-26)

The direction for the next twelve months is [`plans/answers-you-can-check.md`](plans/answers-you-can-check.md) (umbrella epic #250, epics #241-#249). It keeps every primitive above and adds evidence to them:

- **Run receipts (#241):** every query run records what it touched. Receipts live machine-local in `.slaw-local/receipts/` and never go into the folder, a bundle or git. Every rendered value opens to its evidence.
- **SlawBench (#242):** measures agent accuracy before anything is claimed.
- **The verifier (#243):** an independent second derivation. A disagreement becomes a question to the person.
- **Semantic layer (#244):** reviewed files under `semantics/`. Definitions are proposed by the agent and accepted by the person.
- **Headless checks (#245):** `slaw check`.
- **As-of on lake formats (#246).**
- **Standing questions (#247).**
- **Compute decision spike (#248):** page isolation, `run_code`, Python.
- **Aggregate-only agent tier (#249).**

The plan refines "outputs are never persisted" for receipts. That refinement goes into CLAUDE.md before #241 lands. The non-goals in §7 stand, apart from the Python line, which now points to #248.

