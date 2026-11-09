# Workspaces

A **workspace** is a folder, optionally a git repository, that holds your notebooks, saved queries and skills as ordinary files, plus a small `slaw.yaml` that marks it. Your data stays wherever it already lives; the workspace only describes how to reach it.

This page is the file-level reference, the command line, and how Studio opens, creates and switches workspaces (vibe-app#111). Everything below is implemented by `internal/workspace` and its stores. `.slaw` here means a bundle (see "Sharing without git"); the older data-holding container files are not opened by anything any more.

## In Studio

- **Launcher.** With no workspace open Studio shows the launcher: recent workspaces (name, folder, git branch, last opened; a folder that has gone missing keeps its row with "Folder not found", **Locate…** and remove, and is never dropped automatically), **Open folder…** (Ctrl/Cmd+O) and **New workspace…** (Ctrl/Cmd+N). Up/Down move between rows, Enter opens, Delete or Backspace removes a row from the list (with an Undo). The folder's git branch is read from `.git/HEAD` in plain Go; Studio never runs `git` when it opens a folder (a repository's own config can name programs to run). The list lives in your app preferences, never in the workspace.
- **Open folder on a plain folder.** A folder with no `slaw.yaml` is never initialised silently: Studio asks "Make this folder a workspace?" and, on yes, adds `slaw.yaml`, `notebooks/`, `queries/`, `skills/` and `data/`, leaving your other files alone.
- **New workspace.** Name, where to put it (a new folder named after the name), a git repository (when `git` is installed) and a starter template (see [`templates.md`](templates.md)).
- **Switcher.** The folder button in the top bar (Ctrl/Cmd+Shift+O) lists the open workspace, recents, Open and New. Switching saves open notebooks first, releases the old workspace's hub, then opens the next. A copied folder that shares another workspace's `id` offers **Regenerate id**; saved settings that were made for a different folder at the same path offer a re-confirm.
- **Read-only and Take over.** If another process (usually `slaw mcp serve`) owns the workspace's database, Studio opens it read-only: notebooks, queries and skills stay editable (they are files), local DuckDB files show as in use, and a banner offers **Take over…**. Taking over asks the other program to let go (Claude Desktop and Claude Code sessions that were using it reconnect on their own, and then go through Studio). Studio itself lets go when another program asks.
- **Changed on disk.** The workspace is watched. An open notebook, query, skill or data artifact (its Source tab) that changes on disk with no unsaved edit is reloaded quietly; with an unsaved edit a banner offers **Reload from disk**, **Keep mine** and **Save mine as a copy** (a skill or artifact copy is named `<name>-copy`, then `-copy-2`, and so on, never over an earlier one). A save that would overwrite a newer file is refused, never forced.
- **Constraints come from the database.** Studio coerces typed values for datasheet edits but no longer carries its own required-field or validation rules (those were Access-era metadata): `NOT NULL`, `CHECK`, keys and foreign keys declared in the database are what stop a bad row, and the datasheet shows the database's message.
- **No Save.** Notebooks, queries and skills save as you go; table edits go straight to their connection. A data artifact's Source tab is the exception: it has a **Save** button, an Unsaved badge and a prompt before you leave with unsaved text.
- **Settings.** *Workspace*: name, default connection, the assistant's **team limit** (committed, can only tighten), **this computer** (your own choice, up to the team limit), and Studio's MCP switch with **Copy Claude Code config** / **Copy Claude Desktop config**. *App*: theme and the agent's model, including its API key as a write-only field ("In your keychain, Replace, Clear", and a one-time offer to move a plaintext key out of `~/.slaw.yaml`; see [`llm-config.md`](llm-config.md)). *About*: the stamped version, commit and build date (the same values as `slaw --version`), the Go and DuckDB versions, **Copy build info**, **Report a bug** (opens GitHub's issue chooser with nothing prefilled), links to the licence and privacy pages, and a searchable **Third-party notices** viewer (the embedded `THIRD_PARTY_NOTICES.md`). Neither Workspace nor App shows a token.

Studio opens a workspace at launch without showing the Launcher first:

- **A path argument**: `slaw studio --gui <dir>` (or `--workspace <dir>`, or `slaw-studio <dir>`). A folder inside a workspace opens the enclosing workspace; a folder with no `slaw.yaml` above it is passed through, and Studio asks "Make this folder a workspace?".
- **No argument, started from a terminal inside a workspace** (`slaw studio --gui`, or running `slaw-studio` there): the nearest folder above the current directory that holds a `slaw.yaml`.
- **Started from a desktop launcher** (current directory is your home folder or `/`) or from a folder outside any workspace: the Launcher, as always. For this automatic search, a `slaw.yaml` in your home folder, in a folder above it, or in `/` is never opened (a stray file there would otherwise capture every desktop launch). Name it explicitly to open it.
- **If it cannot be opened** (the folder is gone, `slaw.yaml` does not parse), the Launcher appears with the error. A workspace whose database another process owns opens read-only with the usual **Take over…** banner.

A `.slaw` file (the OS opening one with Slaw, or `slaw studio --gui x.slaw`) opens the **Import .slaw…** dialog instead, and anything else gets a plain notice. On macOS the Finder hands Studio a folder or `.slaw` file as an Apple Event rather than an argument, and while Studio is already running it is handled the same way. How the OS associations are wired is in [`../packaging.md`](../packaging.md#1a-file-opening-as-built-vibe-app105-slice-3).

## Layout

```
acme-analytics/
├── slaw.yaml                 committed: id, name, default connection, agent limit, shared connections
├── notebooks/
│   └── weekly-revenue.slnb    one notebook per file (pretty JSON)
├── queries/
│   └── top-customers.sql     SQL with a `-- slaw:` header line
├── skills/
│   └── revenue-definitions/
│       └── SKILL.md          same format as the built-in skills
├── artifacts/
│   └── regional-sales/       one folder per data artifact, see "Data artifacts" below
│       ├── artifact.yaml     title, typed parameters, named queries
│       ├── view.a2ui.json    the A2UI view (or view.js for `view: script`, a custom view you must allow)
│       └── html/             instead of a view file, for `view: html`: template, script and assets of an HTML page you must allow
├── semantics/                entities, metrics, joins and a glossary, see [semantics.md](semantics.md)
│   ├── entities/customer.yaml
│   ├── metrics/net_revenue.yaml
│   ├── joins/orders_customers.yaml
│   └── glossary.md
├── watches/                  standing questions with a condition, see [watches.md](watches.md)
│   └── stuck_orders.yaml
├── data/                     local files and local .duckdb files
├── .gitignore                .slaw-local/, data/*.duckdb, data/*.duckdb.wal
├── .gitattributes            *.slnb text eol=lf
└── .slaw-local/              never committed; non-trust state only
    ├── hub.lock              held while a process owns the workspace, see "One owner at a time"
    ├── nonce                 this folder's mark, see "Re-cloning"
    ├── proposals/            definitions an agent proposed, waiting for you, see [semantics.md](semantics.md#proposals-an-agent-suggests-you-decide)
    ├── watches/              history.jsonl: the outcome of each watch check, see [watches.md](watches.md#history)
    └── state.json            open tabs, last notebook
```

Your own settings for a workspace are **not** in the folder (review vibe-app#109). They live in your user config directory, next to your app preferences:

```
<config dir>/slaw/                       $SLAW_CONFIG_DIR, or the OS user config dir + /slaw
├── connections.yaml                     "All my workspaces" connections
└── workspaces/<id>-<hash>/local.yaml    this workspace on this computer: agent loosening,
                                         sign-in methods, "This computer" connections
```

`<id>` is the workspace id and `<hash>` a short hash of the folder's real path, so two clones of one repository on one machine keep separate settings. The reason is trust: a folder arrives with whatever its authors committed, and these settings can loosen the agent, redefine connections and choose how credentials are fetched, so Slaw only trusts them from a place only you write to. A `.slaw-local/connections.yaml` found inside a folder is never read; opening the workspace reports a warning that it was ignored.

**Re-cloning.** The first time a workspace is opened Slaw writes a random mark into the folder's git-ignored `.slaw-local/nonce`, and records the same value in `local.yaml` when you save settings. If you delete the folder and clone something else (a different repository that happens to carry the same `id`) to the same path, the marks differ, so everything in `local.yaml` (your loosening, sign-in methods, connections) is ignored and a warning explains why. Saving settings is refused until you confirm they belong to this folder (`TrustLocal`). A `local.yaml` with no mark (hand-written or older) is trusted and gets one the next time it is saved.

A relative `SLAW_CONFIG_DIR` is ignored with a warning, since it would mean a different folder for every launch.

`Init` creates everything above except `.slaw-local/`, which appears on the first open (it holds the folder's mark, below). It merges into an existing `.gitignore` and `.gitattributes` and never replaces them, leaves other files in the folder alone, and refuses a folder that already is, or sits inside, a workspace. `slaw.yaml` is written last, so a failed init never leaves something that looks like a workspace but is not. With git on your PATH it can also run `git init`.

## Finding a workspace

`Discover(start)` walks up from `start` to the nearest folder containing a `slaw.yaml`, the way git finds `.git`. The marker is `slaw.yaml`, not a `.slaw/` folder, so it can never be confused with a `.slaw` exchange file. A directory named `slaw.yaml` does not count, and neither does a symlinked `slaw.yaml` (it is refused when opening).

## slaw.yaml

Committed and shared. It carries no secrets and nothing per-person. Unknown keys, duplicate keys and a second YAML document are all errors, so a typo is reported instead of ignored.

<!-- keys:slaw.yaml -->
| Key | Meaning |
|---|---|
| `version` | Schema version, currently `1`. A newer number is refused. |
| `id` | A UUID generated when the workspace is created. It keys keychain entries, so clones of one repository on one machine share credentials. Required. |
| `name` | Display name. Defaults to the folder name. |
| `default_connection` | Connection that unqualified table names run against. `Init` sets it to `local`. |
| `sandbox` | The team limit for the agent: `read-only`, `read-write` or `full`. See "The agent limit". |
| `ask_before_writes` | `true` makes the approval gate mandatory for everyone. It does not cover `propose_definition`: a proposal goes to the machine-local inbox without a card and changes nothing until a person accepts it. |
| `connections` | Shared connection definitions, below. |
<!-- /keys -->

```yaml
version: 1
id: 7f3c2a10-5b1e-4c52-9d0e-2a6f1b8c4d90
name: Acme Analytics
default_connection: local
sandbox: read-write
ask_before_writes: true
connections:
  - name: prod_pg
    type: postgres
    host: db.internal
    port: 5432
    database: analytics
    credentials: keychain
    options:
      sslmode: require
  - name: local
    type: duckdb
    path: data/local.duckdb
    read_only: false
```

### Connection definitions

A definition says what a connection needs, never how you sign in.

<!-- keys:connection -->
| Key | Meaning |
|---|---|
| `name` | The catalog alias. Must match `^[a-z_][a-z0-9_]{0,62}$` and cannot be `system`, `temp`, `memory` or `main`. |
| `type` | `duckdb`, `postgres`, `sqlite`, `motherduck`, `s3`, ... (lower-case). |
| `path` | A file, for file-based types. Relative paths resolve against the workspace folder. |
| `host` | Server host name. |
| `port` | Server port, 0 to 65535. |
| `database` | Database name. |
| `region` | Cloud region, for storage types. |
| `scope` | The storage prefix a storage connection covers, such as `s3://bucket/`. |
| `read_only` | Defaults to `true`. Set `false` to allow writes. |
| `credentials` | A suggestion for how to sign in: `keychain`, `prompt`, `env:PREFIX` or `aws-sso:PROFILE`. Only a hint: it never chooses the sign-in. |
| `options` | Extra non-secret settings such as `sslmode`. |
<!-- /keys -->

- **No secrets.** A `password`, `token` or `key` field does not exist, so it is an unknown key. Option names that look like credentials are rejected, and so is any value that embeds a password (`postgres://user:pw@host`, `user:pw@host`).
- **Environment variables, only in your own scopes (review vibe-app#109).** `path`, `host`, `database`, `region`, `scope` and option values may contain `${NAME}` in your own files (`local.yaml`, the user `connections.yaml`) but **never in `slaw.yaml`**, which is written by someone else: a committed `${VAR}` is a parse error, so a pull cannot read your environment. A variable that is not set is a hard error that names the connection and field, never a silent empty string; an empty but set variable is fine. A variable whose name looks like a credential (`...SECRET...`, `...TOKEN...`, `...PASSWORD...`, `...KEY...`, `AUTH...`) is refused even in your own scopes. `$$` is a literal `$`. The name, type and credentials are never expanded.
- **Paths (review vibe-app#109).** In `slaw.yaml` every `path` must be relative and stay inside the workspace folder, including through symbolic links: an absolute path, a `~`, a drive letter or a `..` that leaves the folder is a parse error. Your own scopes may use an absolute path, which is your explicit choice, but Slaw only ever creates folders inside the workspace; an absolute path in your own scope whose folder does not exist is reported as an issue when the session opens. (An absolute path in `slaw.yaml` never gets that far: the file does not open.) A relative path in an "All my workspaces" connection is refused, because it belongs to no workspace.
- **Fingerprint.** A hash of type, host, port, database and scope is stored with a keychain entry. If a pull changes any of them, the connection asks you to sign in again rather than sending a saved password to a different server.

## Scopes and precedence

Connection definitions come from three files and are merged most-specific-wins.

| Scope | File | UI word |
|---|---|---|
| local | `<config dir>/slaw/workspaces/<id>-<hash>/local.yaml` | This computer |
| shared | `slaw.yaml` | Team |
| user | `<config dir>/slaw/connections.yaml` | All my workspaces |

`<config dir>` is `$SLAW_CONFIG_DIR` when set, otherwise the operating system's user config directory, the same place Slaw keeps its preferences. A missing file is an empty scope. A user file that cannot be read is skipped with a warning, so one broken file never makes every workspace unopenable.

- **Local beats shared beats user.** When two scopes define the same name, the more specific entry replaces the other whole; fields are not merged. The merged list reports the scope each connection came from and which lower scopes it shadows.
- **A user entry never overrides** a workspace one.
- **Sign-in is per person.** `sign_in` in your `local.yaml` maps a connection name to `keychain`, `prompt`, `env:PREFIX` or (S3 only) `aws-sso:PROFILE` for that person. A user-scope connection uses `sign_in` from the user file. Otherwise `keychain`: the definition's `credentials` value is only a UI hint (review vibe-app#115, see Credentials).
- **Status precedence**, when several apply to one connection: `extension-missing`, then `needs-credentials`, then `error`, then `locked-by`, then `not connected`, then `ok`.

<!-- keys:local -->
| Key | Meaning |
|---|---|
| `version` | Schema version, currently `1`. |
| `connections` | This computer's private connections and overrides, same shape as above. |
| `sign_in` | Connection name to sign-in method, for this person. |
| `sign_in_fingerprint` | Connection name to the definition fingerprint the sign-in was chosen for (written by Slaw). |
| `sign_in_server` | Connection name to the host-level description of that server, so "the server changed (a to b)" can name both (written by Slaw). |
| `agent` | This person's agent settings: `sandbox` and `ask_before_writes`. |
| `receipts` | This person's [run receipt](receipts.md) settings: `keep_params`, `days`, `max_count`, `max_mb` (all optional; a missing one is the default). Set in Settings > Workspace > Receipts or `slaw receipts settings`. |
| `mcp_port` | The loopback port Studio's MCP server last used for this workspace, so a client's saved address survives a restart. Not a secret; the bearer token changes on every start. |
| `nonce` | Written by Slaw. Ties the file to the folder it was made for (see below). |
<!-- /keys -->

<!-- keys:user -->
| Key | Meaning |
|---|---|
| `version` | Schema version, currently `1`. |
| `connections` | Connections available in every workspace on this computer. |
| `sign_in` | Connection name to sign-in method, for those connections. |
| `sign_in_fingerprint` | Connection name to the definition fingerprint the sign-in was chosen for (written by Slaw). |
| `sign_in_server` | Connection name to the host-level description of that server, so "the server changed (a to b)" can name both (written by Slaw). |
<!-- /keys -->

`.slaw-local/state.json` holds `lastNotebook` and `openTabs`; it carries no trust and stays in the folder. It is never a reason for anything to fail: an unreadable file is an empty state.

## Credentials

**(review vibe-app#115) A shared `credentials:` value is only a hint.** A `credentials:` line in the committed `slaw.yaml` may pre-select a method in the UI; it is never the effective method, because a pulled commit must not choose how your credentials are fetched (a committed `credentials: env:PG_PROD` plus a host of its choosing would otherwise send your environment password there). The effective method comes only from your own per-person `sign_in`, defaulting to the keychain; an empty keychain is *needs-credentials*. Every `sign_in` entry (env included) is stored with the fingerprint of the definition it was chosen for (`sign_in_fingerprint`, in your `local.yaml` or user `connections.yaml`), and if the definition no longer matches (a pull changed the host, scope, region or any option), even an environment credential, or an `aws-sso:PROFILE` sign-in, is withheld as "the server changed since you signed in" until you choose it again and confirm (`slaw conn credentials set <name> --sign-in env:PREFIX --confirm-rebind`; Studio asks). A hand-written `sign_in` without a fingerprint is withheld the same way.

A connection's secrets never live in `slaw.yaml`, in `local.yaml`, in a log line, in the agent's context or in a bundle. They are kept per person, by a **sign-in method** you choose per connection (it is stored in your `local.yaml`, or the user `connections.yaml` for user connections, never committed):

| Method | Where the secret is | Notes |
|---|---|---|
| `keychain` (default) | your OS keychain, service `slaw` | one JSON entry per connection: `user`, `password`, `token`, `key_id`, `secret`, plus a fingerprint and a host-level description of the server |
| `env:PREFIX` | environment variables, read only | see the mapping below |
| `prompt` | process memory, until Slaw quits | "Remember in my keychain" is off by default and promotes the entry to the keychain only when you tick it |
| `aws-sso:PROFILE` | none stored: short-lived keys are resolved from your AWS IAM Identity Center profile when needed | S3 only. `PROFILE` is a profile in your `~/.aws/config` (letters, digits and `_ . + @ -`, up to 64 characters). Bound to the definition fingerprint like `env:` (a pulled change to the scope, region or endpoint withholds it until you choose it again and confirm). Refused for any endpoint that is not an S3 host under `amazonaws.com`, and with `use_ssl: "false"`. Nothing can be stored under it (`credentials set` and Studio's key fields are refused). Only per person, never in `slaw.yaml`: a committed `credentials: aws-sso:x` stays a hint. See [connections.md](connections.md#aws-sso-sign-in-s3) |

There is **no plaintext-file fallback**. With no keychain (SSH, a locked collection, no desktop) Slaw says so and names the `env:` alternative. Every keychain call has a five second timeout, so a locked keychain reads "not reachable" instead of freezing Studio.

**Environment mapping** for `env:PREFIX` (read at connect time, never written):

| Variable | Fills |
|---|---|
| `PREFIX_USER` | username (readable) |
| `PREFIX_PASSWORD` | password |
| `PREFIX_TOKEN` | token |
| `PREFIX_KEY_ID` | S3/GCS/R2 key id, or the Azure service principal client id (readable) |
| `PREFIX_SECRET` | S3/GCS/R2 secret key, or the Azure account key, connection string or client secret |

`env:MOTHERDUCK` therefore reads `MOTHERDUCK_TOKEN`: MotherDuck's token is only ever used when you choose that source, never picked up from the ambient environment. An env credential has no fingerprint (choosing it is your say-so).

### The workspace `.env` (#168)

People keep sign-in variables in a git-ignored `.env` in the workspace folder (`MOTHERDUCK_TOKEN=...`), and a desktop launcher gives Studio none of the shell's environment. So `env:PREFIX` sign-ins can read a workspace's `.env`, **after you allow it**, direnv-style:

- **Opt-in per file.** A workspace with an undecided `.env` shows a non-blocking banner (Studio) or, on a terminal, a one-time question (the CLI). It lists **variable names only, never values**, and which connections' `env:` sign-ins they would satisfy. **Allow** is remembered against the file's absolute path **and a sha256 of its content**: any edit makes it "changed" and Slaw asks again, like `direnv allow`. **Deny** is remembered by path and holds until revoked (edits do not re-nag). Denying while a connection already signed in from the `.env` is connected takes effect when the workspace is reopened. **Not now** asks again next time the workspace opens.
- **Concurrent edits.** Studio and the CLI may record decisions at the same time; one update can be lost, which fails safe (you are asked again).
- **Where decisions live.** In `dotenv.json` in your own config directory, beside the workspace's `local.yaml` (`<config dir>/workspaces/<id>-<hash>/`). Never in the workspace folder or a bundle. Allow and deny decisions about custom artifact views (`view: script`) are kept the same way in `views.json` beside it (one shared implementation, `internal/trustfile`), keyed by the artifact folder's canonical path plus a hash of its files; see [artifacts.md](artifacts.md#custom-views-view-script).
- **Status.** Settings > Workspace > *.env file* and `slaw workspace env status` show *allowed*, *denied*, *changed since allowed*, *not decided* or *none*, with **Allow**, **Deny** and **Revoke**.
- **Parsing.** Plain dotenv only: `KEY=value`, `export KEY=value`, single or double quotes (double quotes understand `\n \t \" \\`), `#` comments, CRLF. Nothing is executed or expanded (`$VAR`, `$(cmd)` and backticks stay literal text) and `.envrc` is never evaluated. Invalid lines are skipped with a warning naming the line and variable, never the value. Multi-line quoted values (a PEM block) are skipped whole with one warning. A file over 64 KiB, not a regular file (a FIFO, say), or a symbolic link that points outside the workspace is ignored (the status says why).
- **Scope of the values.** They go into a **per-workspace overlay** that `env:PREFIX` sign-ins consult before the process environment (via `secrets.EnvStore`'s getenv seam). They are never put in the process environment (`os.Setenv`), so they do not reach other workspaces, the agent, agent tools, sandboxed commands or MCP clients. Values of credential-named variables (the redactor's secret-name rule, plus `*_PASS` and `*_URL`/`*DSN`) and any value of 16 characters or more are registered with the redactor, so they do not appear in a log, an error, the UI or the agent; short plain settings such as `PORT=5432` are not masked (they would mangle every error that mentions the number). The status bindings (`GetDotEnvStatus` and friends) return names and state only.
- **Changes while open.** The watcher reports a `dotenv` change. An allowed file that changes becomes *changed*; **the values loaded earlier stay in use until you allow the new content**, and Allow reloads them without reopening the workspace. MotherDuck takes its token only when a workspace opens, so Studio reopens the workspace after you allow a `.env` that supplies it.
- **Git.** If git tracks the `.env`, the prompt, Settings and `env status` warn that a credential is committed and recommend `git rm --cached .env` and rotating it. `slaw workspace init` adds `.env`, `.env.*` and `!.env.example` to `.gitignore`. The check runs git with `core.fsmonitor` off and only when a `.git` exists, and is cached.
- **Headless.** `slaw mcp serve` never prompts: it uses only a saved allow and logs one line (state and variable names) when a `.env` is present but not allowed. The CLI prompts only on a terminal; without one it uses only an existing allow.
- **Not exported.** A `.slaw` bundle never includes `.env` (it holds only notebooks, queries, skills and sanitised connection definitions; a test checks).

**Write-only.** Studio, MCP and the agent can set, replace, clear and ask whether a secret is set, never read it back. Only secrets are write-only: the username and the key id are readable and shown, and redacted errors keep `user='rosa'` while hiding the password. The status the UI gets is `{method, username or keyId, secretSet, fingerprintMatches, available, detail}`.

**Fingerprint.** A keychain or prompt credential is bound to a hash of the connection's type, host, port, database, storage scope, path, region and every option (endpoint, url_style, hostaddr, sslmode, account and so on), checked at resolve time on the environment-resolved definition. If a pull changes any of them the connection becomes *needs-credentials* ("the server changed since you signed in (db.internal to db-new.internal). Sign in again") and the saved password is not sent to the new server. Renaming a connection or toggling read-only does not change it; any option does, on purpose (a redirected endpoint must ask you to sign in again). Re-saving after a server change starts the secret fresh, so the old password is not carried over.

**Redaction.** Every secret value Slaw resolves is registered with a redactor that scrubs it (also as echoed SQL-quoted, URL-escaped or JSON-escaped), plus credential-shaped text: `password='...'`, `password=...`, prefixed names such as `aws_secret_access_key=`, `motherduck_token=` and `PGPASSWORD=`, the DDL form `PASSWORD '...'`, `"password": "..."`, `scheme://user:pw@host` (the password only) and `Bearer ...`. It scrubs these from connect and query errors and from logs (DuckDB's Postgres errors echo `password='...'` verbatim). Only secret-bearing options (`PASSWORD`, `SECRET`, `*TOKEN`, `ACCOUNT_KEY`, `CONNECTION_STRING`, ...) are registered, never plain ones like `HOST`, `USER`, `DATABASE` or `REGION`, so `user='rosa'` stays readable and ordinary words are never masked. Secret DDL (`CREATE TEMPORARY SECRET`) runs only on the session's dedicated non-logging connection, under the attach lock, with every value quoted; the statement text is never logged or returned, and on failure every quoted literal in DuckDB's error is replaced. The agent's SQL policy refuses `CREATE`/`DROP SECRET`, `duckdb_secrets()`, `which_secret()`, any `ATTACH`/`SET`/`COPY`/... that names a secret, in every sandbox tier including `full`, with no approval path.

The agent's own model key is handled the same way: see [`llm-config.md`](llm-config.md).

See [connections.md](connections.md) for each connection type, its fields, what read-only guarantees and the MotherDuck notes.

## The agent limit

The committed `sandbox` and `ask_before_writes` are a **ceiling, not a switch**: they can only tighten what the agent may do. Turning the gate off or raising the tier is stored per person in your own `local.yaml` (`agent:`), outside the folder, so a commit someone pulls can never disable your safeguards; a repository that commits a `.slaw-local/connections.yaml` with `sandbox: full` changes nothing.

What the agent actually gets is `EffectivePolicy(committed, local)`:

- **Sandbox** is your own choice (read-write when you have not made one), lowered to the committed limit. You can go below the limit freely and up to it, never above. A committed `full` is not a grant: with nothing set locally you still get read-write.
- **Ask before writes** is on unless you turned it off locally and the committed file does not require it. A committed `true` cannot be loosened by anyone. Neither setting covers `propose_definition`: it files into the machine-local proposals inbox without a card, and nothing under `semantics/` changes until a person accepts it (the tier still applies: read-only refuses it). A committed `false` or no value loosens nobody.
- **Anything unrecognised** fails closed to `read-only`.

The gate can be turned off only for the workspace's own local files (`duckdb` and `sqlite` connections). For every other connection (Postgres, MotherDuck, anything that is not a local file) the agent **always asks**, whatever your setting, and headless MCP refuses those writes. A write also needs the connection to be writable (`read_only: false`); see [agent-policy.md](agent-policy.md) for the whole policy, tier by tier.

## File formats

Every format is written the same way every time, so a save, load, save cycle is byte-identical and git diffs show only real changes.

### Notebooks: `notebooks/<id>.slnb`

Pretty JSON, 2-space indent, no HTML escaping (a `<` in SQL stays a `<`), keys in a fixed order (`schemaVersion`, `title`, `cells`), cell ids kept stable, one trailing newline. Outputs are never stored. Format version 2 (a cell may name a `connection`).

- **The file name is the id.** There is no `id` and no `updatedAt` inside the file, so a rename in an editor or `git mv` is a rename and a timestamp never dirties a diff. The modification time is the "updated" time.
- **File names.** Notebook and query ids are 1 to 64 characters of lowercase letters, digits, `-` and `_`, starting with a letter or digit, and not a Windows device name (`con`, `nul`, `com1`, ...). A file with another name, a malformed file, or one over 10 MB still appears in the list, with an error, so it can be found and fixed; it is never loaded or overwritten.
- **Creating** names the file after the title (`Weekly Revenue` becomes `weekly-revenue.slnb`), adding `-2`, `-3` when taken. **Renaming** changes the title only and keeps the file name, so an id stays stable.
- Every load rebuilds the notebook field by field from a whitelist, so a hand-edited or newer file can never smuggle in a bad cell.

### Queries: `queries/<id>.sql`

```sql
-- slaw: {"title":"Top customers","description":"Biggest spenders","connection":"prod_pg","parameters":[{"name":"min","type":"number","required":true}]}
SELECT customer, sum(amt) AS total
FROM orders
WHERE amt > $min
GROUP BY 1
```

The first line is a SQL comment holding one line of JSON, so any SQL tool still runs the file and the header is a single diff line. Keys: `title`, `description`, `connection` (the connection whose catalog unqualified names use; empty means the workspace default) and `parameters`. A leading byte-order mark and blank or indented lines before the header are ignored. A file without the header is a valid query titled by its file name. A header that cannot be parsed (bad JSON, parameters of the wrong shape) lists the query with an error and refuses saves, so parameters it failed to read are never silently dropped. Line endings are normalised to LF on save. As with notebooks, the file name is the id.

**Parameters in Studio and the CLI (#235).** A declared parameter is `{name, type, required, default}`, with `type` one of `text`, `number`, `integer`, `date` or `boolean` and `default` a string (`"15"`, `"true"`, `"2026-01-31"`; a date may also be `today`, `today-<n>d`, `start-of-month` or `start-of-year`, resolved when the query runs). All three surfaces bind through `queries.Bind`, the binder behind the agent's `run_saved_query`: the SQL text runs as saved and a value only ever travels as a bound parameter, never as SQL text. A `$name` the SQL uses that is not declared, a value for an unknown name and a value of the wrong type are refused; a parameter left out takes its default, and one with no default must be given (in Studio an empty input for a parameter with no default counts as missing, for text too). The statement itself is held to the agent's shape rules: exactly one statement, every `$name` a parameter token (not inside a string literal, quoted name, `$$` body or comment), no bare `?` or `$1`, and no parameter as the argument of `query()`, `query_table()` or a file reader. Each parameter's name and type are checked too, so a hand-written header with a bad one is refused with the reason.

- **Studio.** Opening a saved query in the Query Explorer shows one typed input per declared parameter above the editor, pre-filled with the default (a relative date default stays blank, and Go resolves it). A bad value is shown beside its input and nothing runs. Run sends the saved query's id and the typed values with the buffer's text (`RunQuery`'s `queryId` and `params`), so you can edit the SQL and still run it against the declared parameters; a `$name` you add to the text without declaring it is refused. Parameters are edited in the file's header (or by the agent's `save_query`); Save keeps them. While a run is in flight a **Stop** button appears beside Run: it calls `CancelRun("query:<tabId>")`, which cancels the run's context and interrupts the statement in DuckDB, and the result area shows "Query cancelled." instead of an error (`RunQuery` returns the fixed error `cancelled`). A second run in the same tab replaces the first.
- **CLI.** `slaw query --saved ID [-p name=value ...]` runs `queries/ID.sql` on its connection (`-c` overrides it). `-p` is repeatable, each value is typed by the declaration (`-p min_n=9`, `-p flag=true`; numbers are plain decimals, no hex or underscores), and `-p` needs `--saved`. `--saved` takes no statement and no `--edit`.
- **Ad-hoc text.** Text in the editor (or `slaw query "<sql>"`) with a `$name` and no saved query behind it is unchanged: nothing declares the parameter, so the engine's own error is shown. Save the statement as a query and declare the parameter to run it.

### Skills: `skills/<name>/SKILL.md`

```markdown
---
name: revenue-definitions
description: How we define revenue, net of refunds
table_name: orders
source: import
---

# Revenue
...
```

The same frontmatter as the built-in skills, so Claude Code and other tools can read them. `table_name` (the skill loads only when that table has rows) and `source` (omitted when `user`) are optional. The folder name is the skill name and must equal the `name` in the frontmatter; a skill that does not match lists with an error and is not used, not guessed at. Other files in a skill folder are left alone.

Saving is lossless: frontmatter keys Slaw does not know (`allowed-tools`, `license`, `metadata`, comments) are kept as written and in order, and only blank lines around the body are trimmed, so indentation survives. Line endings are normalised to LF.

### Data artifacts: `artifacts/<name>/`

A data artifact is two files in a folder named for it (an HTML page is `artifact.yaml` plus an `html/` folder, see [artifacts.md](artifacts.md#html-pages-view-html-static-pages-render-in-studio); its machine-local `state` lives in `.slaw-local/artifacts/<name>/state.json`, never in the folder): `artifact.yaml` (the manifest: title, typed parameters with defaults, named queries) and its view file: `view.a2ui.json` (the A2UI view) or, with `view: script`, `view.js` (a custom view that runs only after you allow it; your allow or deny is kept in `views.json` in your user config directory, beside `dotenv.json`, never in the folder). The name is a store id (1 to 64 lowercase letters, digits, `-` and `_`). Anything else in the folder is left alone. Both files are one unit: one revision covers them, and a folder with only one of them lists with an error. The formats, caps and view rules are in [artifacts.md](artifacts.md); `slaw artifact list` prints each artifact's name, title, view kind and, for a broken one, why (the same facts Studio's Library shows, never a query).

## Editing from outside, and conflicts

Files are the source of truth. You can edit them in an editor, `git pull`, or let Claude Code write them.

- **Compare-and-write.** Reading a file captures its content hash (its revision). A save that finds a different hash on disk returns a conflict (`ErrConflict`) and changes nothing. The hash decides, not the modification time, so `touch` or checking out identical content is not a conflict. Saving over a file you never read is also a conflict, and so is deleting one that changed since it was read. The surrounding app offers reload, overwrite or save as copy; overwrite is an explicit option.
- **Revisions travel with the copy.** A notebook, query, skill or data artifact carries its `rev` (for an artifact, one revision over both of its files, so either file changing is a conflict); an editor that holds a copy across user time sends it back with the save (Studio does for all four), so a change made by a git pull, an editor or an agent in the same process is a conflict too. A save that sends no `rev` falls back on the revision the store last saw (refreshed by every read, list and write): that fallback is for code that reads and writes in one breath, and never for an editor. The agent can do better: `list_skills` and `read_skill` return a skill's `rev`, and `create_or_update_skill` takes it back as `rev` (and returns the new one), so a skill changed since the agent read it is refused. Without a `rev` the tool only creates: an existing skill is refused with a pointer to `read_skill`, and the last-seen fallback never applies to the agent (as with `write_artifact`). The notebook and query agent tools never replace content they did not read (`add_query_cell`, `add_markdown_cell` and `create_dashboard` append to a notebook read in the same call, `update_cell`, `delete_cell` and `move_cell` take the `rev` that `get_notebook` returned and refuse a notebook that changed since, and `save_query` and `build_query` create, or replace a query only when sent its `id` and the `rev` from `list_queries`).
- **Atomic writes.** Every write goes to a temp file in the same folder, is synced, then renamed over the target, so a crash never leaves half a file. Temp files left behind by a crash are swept when a workspace is opened. Files over 10 MB are refused. Writers in different processes are not serialised (there is no cross-process lock yet), so the window between the check and the rename is small but real.
- **Safe paths.** Nothing is read or written outside the workspace. `..`, absolute paths and symbolic links that lead outside are refused, and a symbolic link as a file itself is refused.
- **The watcher** reports changes made by anything other than Slaw's own stores, as `notebook`, `query`, `skill`, `artifact` (any file of `artifacts/<name>/`, `view.js` included), `semantics` (a file under `semantics/`; the id is `entities/<name>`, `metrics/<name>`, `joins/<name>` or `glossary`), `watch` (a file under `watches/`; the id is the watch name), `config` or `dotenv` events (created, changed, deleted), after a short debounce. It registers each directory on its own (the operating system does not watch folders recursively), including `skills/<name>/`, `artifacts/<name>/`, `semantics/` with its three folders, `watches/`, and folders created later. Its own writes are ignored by content hash. A folder removed or renamed away and later recreated (a branch checkout and back) is watched again. The state of the files when `Watch` returns is the baseline, so load content after starting it. Your own per-person settings live outside the folder and are not watched.
- **Copying a workspace** (`cp -r`) copies its `id`, so both copies would share keychain entries. Given the list of workspaces you have opened, `DuplicateIDs` reports another live path with the same id and `RegenerateID` gives the copy a new one, editing only the `id:` line.

## Opening a workspace as a session

`OpenSession` builds the workspace's DuckDB hub and registers every merged connection. Local `duckdb` file connections attach straight away (they are local files, nothing to dial); every other type connects on first use. `${ENV}` is expanded only in your own scopes. `default_connection` becomes the default catalog, so `SELECT * FROM sales` reads the default connection's table. A connection that cannot be set up (a missing environment variable, a relative path that escapes the folder, a path in your own scope whose folder does not exist, a type that is not available yet) is reported as an issue with a redacted message and left out, and the rest of the workspace still opens.

## One owner at a time (the hub lock)

DuckDB lets one process write a `.duckdb` file, or many read it, never a mix. So exactly one process per workspace owns the hub: Studio, or a headless `slaw mcp serve`. Ownership is a non-blocking `flock` on `.slaw-local/hub.lock` (the same crash-safe lock `.slaw` files used, `internal/filelock`). It dies with the process, so a crash never leaves the workspace locked; the leftover file is ignored.

`hub.lock` is mode `0600` JSON, written by the owner: `app`, `pid`, `host`, and while it serves MCP an `mcp` object (`url`, `token`), plus `control: "sigusr1"` when it can be asked to let go. Another process reads it with `hub.ReadHolder` without holding the lock, and only trusts it when all of these hold:

- the flock is really held right now (a file left by a crash, or committed to a cloned repository, is ignored);
- it is a regular file owned by the current user with no group or world permissions (this is a same-user trust model; the token is protected by file permissions and your user directory, nothing more);
- the endpoint is a plain `http://` loopback address;
- `.slaw-local` is a real directory (not a symlink) owned by you, and `hub.lock` is a single-link regular file: the lock is opened with `O_NOFOLLOW`, so a symlink or hard link committed to a repository is refused, never written through.

Take over: `hub.RequestRelease` sends `SIGUSR1` to the owner after checking it is on this host, is not the caller, and advertised `control: "sigusr1"`; `hub.TakeOver` then waits for the lock and returns the new hold. A signal rather than a control socket because a socket path under a deep folder can exceed the OS limit and a signal opens no extra listener. The owner's handler closes its session, releases the lock and exits.

On non-unix platforms the lock and take over are no-ops (Windows is not a release target).

## Command line

The CLI works on a workspace found the way git finds `.git`: `--workspace DIR`, or the current directory and then each parent until a `slaw.yaml` turns up. A `.slaw` path where a workspace folder is expected is refused with "`.slaw` files are bundles, not workspaces; import one with `slaw import <file>` or create a workspace with `slaw workspace init`"; `slaw import <file.slaw>` is the exception: it imports a bundle. `slaw verify`, `compact`, `rekey`, `dump`, `passphrase` and `info <file>` belonged to the container and are gone (the names stay registered, hidden, to say so).

| Command | What it does |
|---|---|
| `slaw workspace init [dir] [--name N] [--git] [--template T]` | Wraps `workspace.Init` (above). `dir` defaults to `.`. `--git` runs `git init` when git is installed. `--template` (`contacts`, `inventory`, `order-entry`, `training-registrations`) seeds `data/local.duckdb` with the template's sample tables and writes its starter notebook to `notebooks/`. Prints the next steps. `slaw init [dir]` is an alias. |
| `slaw workspace info [--workspace DIR] [--json]` | Root, id and name; every connection with its scope, status (the same words as `slaw conn list` and Studio's rail: `ok`, `not-connected`, `needs-credentials`, `extension-missing`, `locked-by`, `error`), read-only flag and (for attached ones) table count; notebook, query and skill counts; the effective agent policy; who owns the hub. Never takes the hub or creates a data file. |
| `slaw workspace env status\|allow\|deny\|revoke [--workspace DIR] [--json]` | The workspace `.env` decision (see "The workspace `.env`"). `status` prints the state, variable names and the connections they feed, never values. |
| `slaw query "<sql>" [-c conn] [--format table\|csv\|json] [--limit N] [--read-only]` (or `--saved ID [-p name=value]...` in place of the statement) | One statement through the session. `-c` makes that connection's catalog the default for unqualified names. `-` reads the statement from standard input. `--limit` defaults to 1000 (0 for all), and a cap is reported on standard error. |
| `slaw import <file> [--into [conn.]table] [--mode ...] [--key ...]` | See `import-office.md`. A `.slaw` file is routed to `slaw import bundle`. |
| `slaw export bundle [-o file.slaw] [--notebooks a,b] [--queries ...] [--skills ...] [--artifacts a,b] [--semantics a,b] [--watches a,b] [--with-connections] [--with-local-connections a,b] [--encrypt] [--force]` | Write a `.slaw` bundle of the workspace's content (everything unless some are named). Takes no hub lock: it only reads files. See "Sharing without git". |
| `slaw watch list\|show\|check\|history` | The workspace's [watches](watches.md): standing questions with a condition, checked on demand through the read-only policy path. `check` exits 0 when every watch is ok, 1 when any alert fired, 2 when none fired but some could not be checked. See [watches.md](watches.md). |
| `slaw artifact list [--json] [--workspace DIR]` | The workspace's data artifacts: name, title, view kind (`a2ui`, `script`, `html (static)` or `html (scripted)`) and (for a broken one) the error, as the artifact kinds in Studio's Library show them. Reads files only; there is no `slaw artifact run` or `write`. |
| `slaw artifact render <name> [--out DIR] [-p name=value ...] [--force] [--workspace DIR]` | Draws an allowed **static** `view: html` page to `index.html`, `_slaw/theme.css` and the page's static files (default folder `./<name>-render`), through the read-only, policy-checked query path. Refuses scripted pages, A2UI and script views, and a page that is not allowed as it is now. See [artifacts.md](artifacts.md#rendering-and-the-static-host). |
| `slaw artifact view status\|allow\|deny\|revoke <name> [--hash H] [--json] [--workspace DIR]` | A `view: script` artifact's or HTML page's trust decision, as the Studio prompt and Settings > Workspace > Pages make it: `status` shows state, hash and source (for a page also its kind, queries with their connections, declared capabilities and files); `allow` shows the source and asks (or takes `--hash` from `status` with `--yes`) and is refused if the files changed since; `deny` holds until `revoke`. Kept in `views.json` in your config directory. |
| `slaw export notebook <id> [-o file.slnb]` / `slaw import notebook <file.slnb> [--on-conflict ...]` | One notebook, through the same checks as a bundle. |
| `slaw import bundle <file.slaw> [--on-conflict rename\|overwrite\|skip] [--with-connections all\|a,b] [--dry-run]` | Import a bundle into the workspace. |
| `slaw export <[conn.]table> \| --sql "<query>" [-o file]` | See `import-office.md`. |
| `slaw studio [dir] [--workspace dir] [--gui] [--read-only]` | The terminal UI: a SQL REPL on the session, a read-only table browser across every attached connection (remote tables are listed without counts), and a notebook list. Connection problems are shown at the top of the REPL, since the alternate screen hides stderr. `--gui` launches the desktop app instead, opening `dir` there, or the workspace around the current directory (never one found only in your home folder, above it, or in `/`). |
| `slaw mcp serve [--workspace DIR]` | See [`mcp.md`](mcp.md). |
| `slaw update [--check] [--yes]` | Look up the latest stable release (an explicit, plain request to github.com) and compare it with this build; if it was installed by `install.sh`, offer `Install vX? [y/N]` (terminal only) or install with `--yes`, by running that release's verified `install.sh`. Otherwise it only reports. Exit 0 when the check worked, 1 on an error. See [install](../install.md#upgrade) and [privacy](../privacy.md). |
| `slaw config updates [off\|weekly]` | Show or set Studio's weekly release check (the Settings, About toggle). Unset until you answer; either answer is saved in your user preferences, never in the workspace. |

**Who may write.** A command that can write (`query`, `import`, `export` without `--read-only`, `studio`) first takes the workspace's hub lock (see "One owner at a time"). If Studio or `slaw mcp serve` holds it, the command stops and names the holder: `workspace acme is open in Slaw (pid 4242), which owns its database; close it, or re-run with --read-only`. Only commands that have a `--read-only` flag (`query`, `export`, `studio`) suggest it. It skips the lock and attaches every connection read-only (and refuses `COPY ... TO`). DuckDB cannot mix a writer with readers on one file, and an owner keeps the workspace's local `.duckdb` files open for writing, so while someone owns the workspace `--read-only` reaches only its other connections (MotherDuck, Postgres, ...). A locked local file is reported in plain words ("a local DuckDB file that Slaw (pid 4242) has open for writing"), and `studio` does not offer read-only when the default database is one. A workspace that has no data file yet reports "has no data file yet" instead of creating one. `studio` offers `--read-only` interactively when stdin is a terminal.

**SQL checks.** The CLI is you acting directly, not the agent, so SQL runs as written and the agent's allow-list does not apply. As a courtesy `query` (and the TUI's REPL) refuse `ATTACH`, `DETACH`, `INSTALL`, `LOAD`, `SET` / `RESET` and `CREATE`/`DROP SECRET`, with an explanation (connections come from the workspace, credentials from the keychain). This is a check for common slips, not a security boundary: DuckDB's configuration lock does not itself stop those statements. The check understands comments and quoted text (`'...'`, `"..."`, `E'...'`, `$tag$...$tag$`). `export --sql` must be exactly one `SELECT`, judged by DuckDB's parser (`json_serialize_sql`), since the text is placed inside `COPY (...)`. A write to a connection that is `read_only` fails with a hint to set `read_only: false`. Writes go straight to their connection; there is nothing to save.

## Sharing without git: `.slaw` bundles

A `.slaw` file is a **bundle**: a zip of a workspace's notebooks, queries, skills, data artifacts, semantic definitions and watches, and optionally its connection definitions, for sharing by email or chat when git is not in the picture. It is implemented by `internal/bundle`; Studio and the CLI are two front ends to it. It is a person's action: no agent tool and no MCP method can export or import a bundle (a test keeps `internal/agents`, `internal/llm` and `internal/mcp` from importing the package).

**A bundle holds no data and no credentials.** There is no `data/` directory and no `.duckdb` file in it. Connection definitions never carry a sign-in method, a keychain reference, a fingerprint, a nonce or any per-person setting; they are read from the committed team definitions (and the "This computer" ones the person ticks) and then sanitised with the strictest rule there is, the one for the committed `slaw.yaml`: no absolute path or path outside the workspace, no `${ENV}` reference, nothing credential-shaped, and any option whose value is an absolute path is left out. A definition that cannot be made portable is left out and named in the manifest's `warnings`. The workspace's own default `local` connection is not exported, since there is no data to go with it.

### Format, version 2

```
manifest.json
notebooks/<id>.slnb
queries/<id>.sql
skills/<name>/SKILL.md
artifacts/<name>/artifact.yaml
artifacts/<name>/view.a2ui.json   (or artifacts/<name>/view.js for view: script)
artifacts/<name>/html/<path>      (one entry per file, instead of a view file, for view: html)
semantics/entities/<name>.yaml
semantics/metrics/<name>.yaml
semantics/joins/<name>.yaml
semantics/glossary.md
watches/<name>.yaml
connections.yaml            (optional)
```

A data artifact is two entries of kind `artifact` with the same `id` (its name): `artifact.yaml` and the view file its manifest names (`view.a2ui.json`, or `view.js` for `view: script`). They are one item in the preview, in the export counts and in Replace, and a bundle that holds only one of the two, both view files, or the wrong view file for the manifest's kind is refused. A page (`view: html`) is `artifact.yaml` plus one entry per file under `html/`, with no other view file; the file set must pass the same layout and template rules as the store (paths, extensions, caps, the closed template functions, the URL allow-list) while the bundle opens, so a bad page refuses the whole bundle before anything is written. There is no `artifacts/<name>/view.html` entry, and a bundle never carries an allow or a page's `.slaw-local` state: a script view or a page arrives undecided and opens as source.

A semantic definition is one entry of kind `semantics` whose `id` is `entities/<name>`, `metrics/<name>`, `joins/<name>` or `glossary`. Each is decoded with the semantics store's strict decoder (byte cap, no anchors, aliases or unknown keys, the name inside equal to the file name, the caps and name rule in [semantics.md](semantics.md)) while the bundle opens, so one bad file refuses the whole bundle. Import writes the file's own bytes; a name that exists is renamed (`net_revenue_2`, with the name inside rewritten, and a metric's saved query or a join's entities that the same bundle renamed are followed), replaced against the revision just read, or skipped. The glossary cannot be kept beside another, so a clash defaults to skip. Only these four paths are accepted under `semantics/`: a proposal, or anything under `.slaw-local/`, is never an entry.

A watch is one entry of kind `watch` whose `id` is the watch name: `watches/<name>.yaml`. It is decoded with the watches store's strict decoder (the caps and name rule in [watches.md](watches.md)) while the bundle opens, so one bad watch refuses the whole bundle before anything is written. Import writes the file's own bytes; a name that exists is renamed (`stuck_orders_2`), replaced against the revision just read, or skipped, and a watch whose saved query or metric the same bundle renamed is pointed at the new name. A watch's check history lives in `.slaw-local/watches/` and is never an entry.

`manifest.json` has `kind: "slaw/bundle"`, `formatVersion: 2`, `createdAt`, `createdBy` (the app version), `workspaceName`, `warnings`, and `contents`: one `{path, kind, id, sha256, size}` per entry. The entries are the stores' own file bytes, so an export followed by an import into an empty workspace gives byte-identical files. `connections.yaml` is `version: 1` plus `connections:` and nothing else.

### Locking a bundle

A bundle is unencrypted by default. **Lock with a passphrase** (Studio) or `--encrypt` (CLI) wraps the whole zip in one [age](https://age-encryption.org) envelope with a scrypt passphrase recipient, from `filippo.io/age`; there is no bespoke cryptography here. The age header (`age-encryption.org/v1`) is the unencrypted marker: the importer recognises it and asks for the passphrase before anything else. A wrong passphrase says "wrong passphrase", a damaged file says it is damaged, and Slaw cannot recover a forgotten passphrase. The CLI asks twice on a terminal; for scripts `SLAW_BUNDLE_PASSPHRASE` supplies it (less safe: other processes of yours can read the environment). Studio's passphrase crosses the bridge in one direction only and is not stored.

### Importing

Studio: **Import .slaw…** in the workspace menu imports into the open workspace; **Import .slaw…** on the launcher creates a new workspace from the bundle. The file is opened, verified and previewed first; nothing is written until **Import**. Each item that already exists in the workspace gets its own choice: **Keep both** (the new one is saved as `<id>-2`, `-3`, ...), **Replace mine** (a compare-and-write against the revision just read, so a file changed in between is reported, never clobbered) or **Skip**; every item can also be unticked. CLI: `--on-conflict rename|overwrite|skip` (default rename) and `--dry-run`.

Import treats the file as hostile:

- The set of entry names is closed: the closed list of entries above (`manifest.json`, `notebooks/<id>.slnb`, `queries/<id>.sql`, `skills/<name>/SKILL.md`, the files of `artifacts/<name>/` (the manifest and a view file, or the manifest and its `html/` files), the four `semantics/` paths, `watches/<name>.yaml`, `connections.yaml`). Absolute paths, `..`, backslashes, drive letters, directories, symlinks, nested paths, encrypted zip entries and any unknown file refuse the whole bundle. Ids and skill names must pass the stores' own id rules.
- Every entry's SHA-256 and size must match the manifest, and the manifest must list exactly the entries in the archive.
- Caps: 10 MB per entry, 50 MB unpacked in all, 2,000 entries, 64 MB on disk. Declared sizes are checked before decompression and the real byte count is enforced while reading, so a zip bomb is refused. An encrypted bundle's scrypt work factor is capped at 18, the value Slaw itself encrypts with; a header asking for more is refused ("this bundle was made with settings Slaw won't use").
- Each document is parsed by its own store's rules (`notebooks.Coerce`, `queries.Decode`, `skills.Decode`, `semantics.Decode*`, and for an artifact `artifacts.Decode` plus `artifacts.ValidateView` over the pair) before anything is written; an artifact is written through its store with the same compare-and-write as the rest. A skill can never claim `source: built-in` (it is imported as an ordinary skill, with a warning) and cannot take a built-in's name; every skill that arrives in a bundle is saved with `source: import` and badged **Imported** in the Skills view.
- Writes go through `notebooks.Store`, `queries.Store`, `skills.Store` and `artifacts.Store`.
- **An imported artifact is structural only.** The bundle package cannot import the agent policy, so it checks an artifact's shape, not whether its SQL is allowed. A hostile query imports and is refused when it runs: every run, from any source, repeats the policy checks (see [artifacts.md](artifacts.md) and [agent-policy.md](agent-policy.md#data-artifacts)). Read a bundle from someone you do not know before you open its artifacts, as with its notebooks.
- **Connections** are never added unless the person names them (Studio: tick them in the preview; CLI: `--with-connections all|a,b`). The preview shows everything a definition sets (host, database, path, region, storage scope, every option) and warns about TLS turned off, `sslmode=disable|allow|prefer`, a non-AWS endpoint for `s3`, and a storage scope that is a whole bucket. A definition always arrives **read-only**, whatever it asks for (Overwrite keeps your own read/write choice, and clears your sign-in choice and saved credential for that name, so it arrives needing credentials). They go to **This computer** for the workspace, never the committed `slaw.yaml`, and arrive with no credentials, so each shows *needs credentials* until you sign in. Move one to the team later with the usual scope move. A bundle cannot replace a team or all-workspaces connection of the same name; the imported one is renamed (`name_2`) or skipped. An imported notebook still runs its SQL when opened, as any notebook from a git pull does; read a bundle from someone you do not know before you open its notebooks.

A file that is a **formatVersion 1** bundle or a file with SQLite's header gets one plain message: "This is an older Vibe file that holds data. Slaw doesn't open these." An SQLCipher container has no header at all, so it, like anything else that is not a bundle, says "this is not a Slaw bundle (an older Vibe file that held data is not opened either)". A bundle from a newer Slaw asks you to update.

Text the bundle's maker wrote (workspace name, created-by, the manifest's own warnings) is length-capped, stripped of control and bidirectional characters, and shown apart as "From the person who made this bundle". Notebook ids are kept as they are, including `nb_<uuid>` ones, so Replace replaces. Studio reads each existing item's revision when it previews, and Replace only replaces that version.

Single notebooks: `slaw export notebook <id> [-o x.slnb]` and `slaw import notebook <x.slnb> [--on-conflict ...]` (it also accepts Studio's single-notebook export) use `bundle.NotebookPackage`, the bundle's own import path.

The single-notebook export and import in Studio (`ExportNotebook` / `ImportNotebook`) use the same size cap and `bundle.CheckNotebook`, so a notebook is judged by one rule whether it arrives alone or in a bundle.
