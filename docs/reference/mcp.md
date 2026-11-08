# MCP (Model Context Protocol)

Slaw exposes a **workspace's** tools (the same registry, `internal/agents`, that Studio's chat panel uses) to external MCP clients such as Claude Desktop and Claude Code:

- **`slaw mcp serve [--workspace DIR]`**: a headless server you launch from the client's own config (stdio, the default), or point a network client at (`--transport sse`).
- **Studio's built-in SSE server**: Studio owns the workspace it has open and can serve MCP on a loopback port (Settings → Workspace → Claude and other tools). A write that needs approval shows the same approval card the chat agent uses, in the Studio window; the card waits for you, and a declined or unanswered card changes nothing.

Every tool call, through either path, goes through the same sandbox gating, "Ask before changes" policy and result guardrails as Studio's assistant (vibe-app#63, vibe-app#70, vibe-app#79). There is one tool surface, not a looser one for MCP. The agent's SQL is an allow-list per tier ([reference/agent-policy.md](agent-policy.md)), and a write to a connection that is not a local file always needs approval, so headless MCP refuses it. `.slaw` files are no longer opened as workspaces: pass a workspace folder (a `.slaw` is a sharing bundle you import).

## `slaw mcp serve`

```
slaw mcp serve [--workspace DIR] [flags]

Flags:
      --workspace string   workspace folder (default: the nearest folder above the
                           current directory that holds a slaw.yaml)
      --transport string   stdio or sse (default "stdio")
      --addr string        listen address for --transport sse (default "127.0.0.1:0")
      --sandbox string     read-only, read-write, or full. Can only TIGHTEN the
                           workspace's own policy, never loosen it
```

The workspace is found the way git finds `.git`: `--workspace` (or the current directory) and then each parent until a `slaw.yaml` turns up. A `.slaw` path, as an argument or as `--workspace`, is refused with "`.slaw` files are bundles, not workspaces; open or create a workspace folder" (import a bundle with `slaw import <file.slaw>`). The old `<file.vibe>` argument, `--read-only-file` and `VIBE_PASSPHRASE` are gone.

### One owner per workspace: owner mode and proxy mode

DuckDB allows one read-write process or many read-only ones per database file, never a mix, so one process owns a workspace's hub at a time, recorded in `.slaw-local/hub.lock` (see [workspaces.md](workspaces.md)). `slaw mcp serve` looks at the lock first:

| Who holds the hub | What `slaw mcp serve` does |
|---|---|
| Nobody | **Owner mode.** Takes the lock, opens the workspace and its DuckDB hub, serves the tools. Writes go straight to their sources: there is nothing to save on exit, and a crash loses nothing but the call in flight. |
| Studio, serving MCP | **Proxy mode.** Passes stdio through to Studio's MCP server. The client sees the same tools, Studio's approval cards apply to writes, and there is no DuckDB lock error. |
| Another `slaw mcp serve` | Proxy mode, to that one (an owner over stdio also serves loopback SSE and publishes it in `hub.lock`, so a second client such as Claude Code next to Claude Desktop just works). |
| Studio with MCP off | Fails with a message naming it: "Studio (pid N) has this workspace open but its MCP server is off. Press Start in Studio under Settings → Workspace → Claude and other tools (Studio then keeps it on for this workspace), or close Studio." |

Proxy mode is a transparent pipe, not an MCP client: every SSE `message` event is copied verbatim to stdout (responses, notifications, server-to-client requests) and every stdin line is POSTed verbatim, so the tool list cannot drift and there is no client-side request timeout (a write waiting for an approval card in Studio can take minutes and is never retried by the proxy). Only the stream ending, a 401 or a refused connection means the owner is gone: then every request in flight is answered with a JSON-RPC error (-32001), a short message goes to stderr, and the process exits non-zero; reconnect the client to continue. A failed POST answers just that request. A proxy ends when stdin closes (after a brief drain of replies in flight) and never asks Studio to quit. `--transport sse` needs this process to be the owner.

Two clients started at the same moment (Claude Desktop and Claude Code) both work: the first takes the lock and publishes `mcp: {pending: true}` in its very first write, the second waits up to 15 s for the real endpoint and then proxies.

`--sandbox` in proxy mode is sent to the owner as an `X-Slaw-Sandbox` header on the SSE stream and **enforced by the owner**: for that session the effective mode is the stricter of the owner's policy and the header (tighten-only, bound at connection time, applied to every tool, including content tools). Unknown values are rejected with 400. The owner advertises `mcp.caps: ["sandbox-header"]` in `hub.lock`; a proxy started with `--sandbox` refuses an owner without it.

### Stdout is the protocol channel

Over stdio, `slaw mcp serve` duplicates stdout to a private handle for the protocol and points file descriptor 1 at stderr for the rest of the process. A Go `fmt.Println`, a C library's `printf`, or a MotherDuck sign-in link printed by DuckDB therefore lands on stderr and cannot corrupt the JSON-RPC stream. Prompts, notices and logs are all stderr.

### Sandbox and "Ask before changes"

The effective policy is the workspace's: this person's chosen tier (default **read-write**) clamped to the committed `slaw.yaml` ceiling, and "Ask before changes" on unless this person turned it off and the ceiling allows it (`Workspace.AgentPolicy`). `--sandbox` can only make a session **more** restrictive, never less: a launch config, which may be shared in a dotfiles repo, can never grant more than the owner chose.

A write needs three gates (tier, writable connection, the person's approval); [agent-policy.md](agent-policy.md#writes-need-three-gates) owns that rule and its exact messages. What it means over MCP, where there is no one to show an approval card to:

- **While "Ask before changes" is on, an MCP client can read but any write is refused outright, except `propose_definition`.** A proposal files into the machine-local inbox without a card and changes nothing until the person accepts it, so "Ask before changes" does not gate it (the tier still does). To let MCP clients change the workspace's local files without asking, turn the gate off in **Slaw → Settings → Workspace → The Slaw assistant → On this computer → "Ask me before it changes data"**; there is no flag or environment variable that bypasses it from the MCP side.
- **A content write is validated first, then refused as "valid, needs approval".** `write_artifact`, `add_query_cell`, `add_markdown_cell`, `update_cell`, `delete_cell`, `move_cell`, `create_dashboard`, `create_or_update_skill`, `save_query`, `build_query` and `write_watch` run all of their checks (decode, schema, the read-only SQL vet, which for `create_dashboard` runs on every `sql`-bound component and lists each failure by component path, the table gate, a stale or missing `rev`) before the approval gate, with nothing written. An invalid request gets its normal validation error, so the agent can fix it and try again. A valid one, when nobody can approve, is refused with "your request is valid and ready, but it was NOT saved yet ... Ask the person to approve it", error code -32001 and a structured payload, `structuredContent: {"valid": true, "needs_approval": true, "nothing_written": true}`, next to the text and repeated as a second text block, for clients that show the model only an error's text. For `write_artifact` that payload also carries `queries: [{name, columns: [{name, type}]}]` and for `save_query` and `build_query` `columns: [{name, type}]`, the result columns the vet read, so the agent can check a view's column references without a write. The text names both settings and which one blocks: "Sandbox: read-write (\"Change rows\", allows this). Ask before changes: on (this is what stopped it)". The text says it writes `artifacts/<name>/` (or `skills/<name>/SKILL.md`, or `queries/<id>.sql`, or a notebook) in the workspace folder, not "the local database". With an approver (Studio's card) nothing changes: the card is shown first and the write follows the person's OK.
- **A tier refusal says what is needed.** `method "write_artifact" (category "content") is not allowed in read-only mode: it writes files in the workspace folder (artifacts, notebooks, skills, saved queries), which needs the read-write tier ("Change rows"), and this workspace's agent is at read-only ("Look only"). Nothing was changed. To allow it, set the agent to "Change rows" (read-write) or higher in Slaw → Settings → Workspace → The Slaw assistant → On this computer, or restart the MCP server without a tighter --sandbox.` The tier buttons in Studio are "Look only" (`read-only`), "Change rows" (`read-write`) and "Full control" (`full`).
- **A write to a connection that is not a local `duckdb`/`sqlite` file (Postgres, MotherDuck, ...) is refused even with the gate off**, because approval is forced for those connections. Nothing is changed.
- To make those changes with approval, open the workspace in Studio, which shows its approval card (naming the non-local connection) while `slaw mcp serve` proxies to it.

### What a client is told on connect

`initialize` returns real `serverInfo` and `instructions`, the same on stdio and SSE:

- **`serverInfo`**: name `Slaw` when Studio serves it and `slaw mcp serve` when the headless server does (the hub-lock app name), and the build's version (`dev` for an unstamped build).
- **`instructions`**: a short orientation, well under 2 KB (Claude Code truncates at 2,048 characters): the workspace name and folder; the sandbox tier and the "Ask before changes" state **at connect time**, with what that means (refused with no one to approve; or an approval card in Studio; or direct), and where the person changes it; the absolute `data/` folder (file readers read only there, by absolute path); the connection names (names only, no types, URLs or credentials; at most 12, then "and N more"); and a pointer to `help_search ""` and to `read_skill` `duckdb-sql` and `data-artifacts` (`a2ui` before a dashboard or an artifact view, and `semantic-layer` before working on `semantics/`).

The text is built when each client connects, on `initialize` or, for a client on protocol 2026-07-28, on `server/discover`; a `--sandbox` ceiling on a proxied session is reflected. In Studio it follows the saved settings, so a client that connects after the person changed the tier or the gate sees the new state. A headless `slaw mcp serve` fixes its policy when it starts and never re-reads it, so a change applies only after a restart (the refusal text and the instructions say so). A client already connected keeps the text it was given. Over stdio the process stderr also gets a one-line notice when the gate is on, which a client does not show the model.

### Tool annotations

`tools/list` carries MCP annotations derived from each tool's category (`internal/mcp/annotations.go`), so a client can auto-approve reads and warn on writes:

| Category | `readOnlyHint` | `destructiveHint` | `idempotentHint` | `openWorldHint` |
|---|---|---|---|---|
| introspect | true | false | true | false, except `introspect_schema`, `describe_table`, `run_saved_query` and `preview_query` (they read a connection, which may be remote): true |
| content | false | false (a replace needs the `rev` it read, so it cannot clobber a newer version), except `delete_cell` and `update_cell`, which remove or overwrite a cell: true | true for `write_artifact`, `write_watch`, `create_or_update_skill`, `save_query` and `build_query` (a replace needs the `rev` it read), else false (`propose_definition` files a new proposal each time) | false (files in the workspace folder) |
| data, schema | false | true | true for `update_record` and `delete_record`, else false | true (a connection may be remote) |

`run_query` is a data tool, so it is destructive even though a `SELECT` is not: the hint describes what the tool may do. Its `sql` is one statement as a string (a non-string is refused by name; one trailing semicolon is fine), `params` is `{name: value}` for `$name` in the SQL (a bare `?` is refused), the optional `connection` is the connection whose catalog unqualified names resolve against (a name from `list_connections`; an unknown one is refused; the workspace's default connection when omitted), which a `metric()` whose saved query is pinned to a connection needs, and a script returns its last statement's result only, so a read before the last statement is refused. A zero-row result (or a `count(*)` of 0) that compared a column with string literals carries a `note` listing the values that column holds in the table the statement reads. Details: [agent-policy.md](agent-policy.md).

Reads are not gated by approval; what the agent may read, and the never-list, are in [agent-policy.md](agent-policy.md). Every denial returns its reason as a tool error, and `list_connections` tells a client what is connected.

### Data artifact tools

Three tools author and read [data artifacts](artifacts.md) (`artifacts/<name>/`): parameterised, live views whose named queries re-run when the artifact opens or a parameter changes. They are in the one registry, so Studio's assistant and MCP clients see the same tools with the same gates.

| Tool | Category | Notes |
|---|---|---|
| `list_artifacts` | introspect | Name, title, description, parameter and query counts, `rev`; a broken folder is listed with its error. |
| `get_artifact` | introspect | One artifact's manifest and view as data, and its `rev`. A query with very long SQL comes back with `sql: null` and `sql_omitted: true`, never a partial copy (send `keep_sql: true` for it to keep it); for `view: script` the `view.js` text comes back as `view_source` (over about 30 KB: `view_source: null`, `view_source_omitted: true`, kept with `keep_view_source: true` and the `rev`); never results. |
| `write_artifact` | content | `name`, `manifest`, `view` (an A2UI page; for `view: a2ui`) or `view_source` (the `view.js` text, at most 1 MB; for `view: script`), optional `keep_view_source`, optional `rev`. Refused under `read-only`; asks first when "Ask before changes" is on, so headless MCP refuses it like `create_or_update_skill`, but only after validating it (a valid request is told it is ready and needs approval). Every query is vetted against the read-only SQL policy before anything is written. The reply carries `queries: [{name, columns: [{name, type}]}]`, each query's result columns, on a write and in the refusal's payload when only approval is missing. Every check (manifest, view, column references, script syntax, page rules) runs in one pass, before the approval gate, and the findings come back together; see [artifacts.md](artifacts.md#authoring-with-the-agent). |

### Semantic definitions

`list_definitions` and `get_definition` read the [semantic layer](semantics.md) and are introspect tools, so a read-only agent can use metrics (`metric('name')` in `run_query`, see [semantics.md](semantics.md#using-a-metric-in-sql)). `propose_definition` is a content tool: `read-write` and `full` may use it, `read-only` may not. It is exempt from "Ask before changes": it files without an approval card, because accepting it is the approval. It does not write `semantics/`: it files a proposal in the person's inbox (`.slaw-local/proposals/`, machine-local), and **no tool or MCP method can accept, reject or write a definition**; the person accepts with `slaw semantics accept` or in Studio. Details, formats and caps are in [semantics.md](semantics.md#proposals-an-agent-suggests-you-decide).

| Tool | Notes |
|---|---|
| `list_definitions` | `{entities, metrics, joins, glossary, proposals: {open}}`; each item has its `name` and `rev`, an unreadable file its `error` (paths scrubbed). |
| `get_definition` | `kind` (`entity`, `metric`, `join`, `glossary`) and `name`. Returns `{kind, name, rev, definition}`; a metric adds `parameters` (the declared `$name` parameters of its saved query) and `query_connection`, or `query_error` when the saved query is missing. For the glossary, `name` is a term and `rev` is that term's own (`term-…`; `file_rev` is the file's); without `name`, `rev` is the file's. Send a term's rev as `replaces_rev` to change it. |
| `propose_definition` | `kind`, `name`, `definition`, `rationale`, optional `evidence` (strings) and `replaces_rev` (the `rev` from `get_definition`; required to replace an existing name). The definition is validated with the kind's own decoder before anything is filed, so a bad measure or a reserved word is refused with the reason. The reply is `{proposed: true, id, ...}`; the `id` is made by Slaw. Refused past 200 open proposals ("accept or reject some first"). It never asks for approval, so it files the same way headless and in Studio. |

### Watches

`list_watches` and `get_watch` read the [watches](watches.md) in `watches/` and are introspect tools, allowed in every tier. `write_watch` is a content tool: `read-write` and `full` may use it, `read-only` may not. It asks first when "Ask before changes" is on, so headless MCP refuses it, but only after validating it (a valid request is told it is ready and needs approval, with the canonical `file` and any `warnings`). **No tool or MCP method checks a watch, reads a check's reason or sees result rows**: the person checks in Studio or with `slaw watch check`.

| Tool | Notes |
|---|---|
| `list_watches` | `{watches: [...]}`: each has `name`, `description`, `question`, `when`, `condition`, `rev`, and an unreadable file its `error`. A watch that has been checked also has `last_checked` (RFC 3339) and `last_status` (`ok`, `alert`, `error`); never a reason, value or rows. |
| `get_watch` | `name`. Returns the same fields plus `yaml` (the canonical file text) and `rev`. |
| `write_watch` | `name`, `question` (`{query, params?}` or `{metric}`), `when` (`{rows: any\|none}`, `{above}`, `{below}` or `{outside: [low, high]}`), optional `description` and `rev` (from `get_watch`; without it the write only creates and an existing name is refused). Validated with the file decoder; the saved query or metric must exist. The reply is `{saved, created, rev, file, note, warnings?}`; a required parameter with no value is a warning. |

### Saved queries

`save_query` and `build_query` write `queries/<id>.sql` and are content tools, so `read-write` may save a query like an artifact or a notebook cell; they validate before the approval gate and answer "valid and ready" when nobody can approve. `list_queries` and `run_saved_query` are introspect tools.

| Tool | Notes |
|---|---|
| `list_queries` | `{queries: [...]}`, every saved query as `{id, rev, name, sql, engine, parameters}` (plus `description`, `connection`, and `error` for a file that cannot be read). An empty workspace gives `{queries: []}`. Like every `list_*` tool it returns an object, never a bare array (`list_definitions` is keyed by kind). |
| `save_query` | `title`, `sql`, optional `description`, `connection`, `parameters`, `id` and `rev`. The SQL is vetted at save like an artifact query: one statement, a plain read, bound against the schema on its connection (a bad column fails with the binder's error and candidates), so `SELEC 1` and `DELETE FROM t` are refused and nothing is written. Without `id` it creates, and a title already in use (compared without regard to case) is refused with the existing id, never forked to `-2`. To replace a query send its `id` and the `rev` from `list_queries`; a missing or stale `rev` is a refusal. The reply carries `id`, `rev` and the result `columns`; the approval refusal's payload carries the same `columns`. |
| `build_query` | `name`, `table`, optional `conditions`, `combinator`, `columns`, `id`, `rev`. Renders the QBE conditions to SQL and saves it the same way. A value is typed by its column: `"15"` against an `INTEGER` column becomes the number 15, and `true`/`false` against a `BOOLEAN`; a value that is not that type is refused by name. |
| `run_saved_query` | `id`, optional `params` `{name: value}`. Runs the stored SQL through `run_query`'s pipeline on the query's own `connection`, with the same sandbox, wildcard, row-cap and result rules. It runs reads only: a file that has since become a write (a hand edit) is refused. |

Parameters: write a value that varies as `$name` and declare it as `parameters: [{name, type, default}]`; `type` is `text`, `number`, `integer`, `date` or `boolean` and `default` is a string (`"15"`, `"true"`, `"2026-01-31"`; a date may also be `today`, `today-<n>d`, `start-of-month`, `start-of-year`). Every `$name` the SQL uses must be declared and every declared name used. The check binds the defaults; `run_saved_query` binds `params` by type (a number for `number` and `integer`, a boolean for `boolean`, a string for `text` and `date`; a wrong type is refused, not converted) and unnamed parameters take their default. The declaration lives in the query file's `-- slaw:` header (`parameters`).

A script view written over MCP is stored but **never runs until the person allows it in Studio or the CLI**: there is no tool, method or other path for a client to allow, deny or revoke a view, and every edit asks again ([artifacts.md](artifacts.md#custom-views-view-script)). A client reads the format with `read_skill` `data-artifacts`. `write_artifact` without a `rev` only creates; to replace an artifact send the `rev` from `get_artifact`, and a change made since is a refusal, not an overwrite ([agent-policy.md](agent-policy.md#data-artifacts)). `create_or_update_skill` follows the same rule: without a `rev` it only creates a new skill name, and replacing a skill needs the `rev` from `read_skill` or `list_skills` (a skill bound to a `table_name` is visible to those only while the table has rows). There is no tool that runs an artifact's queries or returns its results.

### Skill and result shapes

`read_skill` returns `name`, `description`, `instructions` (the skill text, exactly as stored, never framed), `framing` (the one framing sentence that marks the text as reference material, never operator orders; it also heads the `<skills>` block in the in-app prompt), `source`, `table_name` and `rev`. Send only `instructions` back to `create_or_update_skill`: it strips any leading copies of the trust note (repeated, with surrounding whitespace) before saving, so the framing never reaches `SKILL.md`. Empty lists in a tool result are `[]`, never `null`. `create_record` returns `{created, rows_affected}`; DuckDB has no last-insert id, so read the new row back with `run_query` if you need its key. `help_search` with an empty query returns a compact index (topic, title and a one-line summary); search by topic name or keywords for the full text.

### Take over

Studio, opening a workspace a headless `slaw mcp serve` owns, can offer to take over: `hub.TakeOver(ctx, ws, "Slaw", timeout)` signals the server (`SIGUSR1`; on Linux through a pidfd, re-verifying the lock still names that pid after the pid is pinned, so a recycled pid is never signalled; it checks it is a live owner on this host that advertised release control; if a client auto-reconnects and a different releasable owner appears while waiting, that one is asked too), the server closes its session, releases the lock and **exits with status 0**, and Studio gets the lock. The Claude session that was using it sees its server end and must reconnect (Claude Code: `/mcp`; Claude Desktop: restart the server or the app); the reconnected `slaw mcp serve` then proxies to Studio. Nothing is deleted, and open notebooks are plain files so they are unaffected.

### For Studio (vibe-app#111)

```go
hold, err := hub.AcquireHub(ws, hub.AppStudio, hub.WithReleaseHandler(onReleaseRequested))
var held *hub.HeldError              // errors.Is(err, hub.ErrHubHeld): open read-only, or take over
hold, err = hub.TakeOver(ctx, ws, hub.AppStudio, 10*time.Second, opts...)
sess, issues, _ := workspace.OpenSession(ctx, ws, logger)   // only after the lock is held
m, err := hub.StartWorkspaceMCP(ws, sess, hold, hub.WithMCPRememberPort())
m.Handler.SetApprover(approve)       // Studio's approval cards; proxies get them too
m.URL(); m.Token()                   // for the Settings screen; token is a secret
m.Stop()                             // MCP off: clears the endpoint from hub.lock
hold.Release()                       // after closing the session
cfg, _ := hub.BuildClaudeConfig(ws.Root(), exe)   // exe from hub.LocateSlaw()
holder, err := hub.ReadHolder(ws.Root())          // who holds it, without the lock
```

How the workspace launcher wires it (`studio/workspace_open.go` and `studio/workspace_mcp.go`; the old vibe-app#111):

1. **Opening a workspace:** `AcquireHub` first. On `ErrHubHeld`, read `held.Holder` and show the read-only banner (SPEC 7 C): "Open read-only" opens `workspace.Open` only (stores are files, so notebooks/queries/skills stay editable) and does **not** call `OpenSession` for the local `duckdb` connections, which show `locked-by(pid)`; "Take over" calls `TakeOver`, then continues as the owner. `ErrNotReleasable` or a timeout is shown as the reason, never a hang.
2. **Releasable owner:** pass `WithReleaseHandler` (one per process; Studio holds one workspace at a time). The handler must ask the person if needed, close the session, stop MCP, `Release()`, and switch the window to the launcher or read-only. A second releasable hold in the process is an error (`ErrOneReleasableHold`).
3. **MCP switch (Settings → Workspace → Claude and other tools):** On = `StartWorkspaceMCP` then `m.Handler.SetApprover(...)` wired to the existing approval card (an approval can take minutes; the proxy waits). Off = `m.Stop()`. Reflect whether a holder advertises MCP with `ReadHolder`. Wrap the handler with `WithMCPHandlerWrap` if Studio needs its own lock around calls. Studio remembers the choice per workspace in the per-person preferences document (`mcpWorkspaces`, keyed by the workspace's path; never `slaw.yaml`, never a bundle) and starts the server again when the workspace opens if it was on; Stop records off, and a failed restart is logged and shown in the status (`startError`) without blocking the open.
4. **Copy Claude Desktop / Claude Code config:** `hub.BuildClaudeConfig(ws.Root(), exe)` with `exe` from `hub.LocateSlaw()`; copy `.Desktop` or `.Code`. Never show or copy the token with these.
5. **Workspace switcher:** release the old workspace's hold (stop MCP, close the session, `hold.Release()`) **before** acquiring the new one. The release signal is process-wide, so only one releasable hold may exist at a time, and a second `WithReleaseHandler` fails with `ErrOneReleasableHold`.
6. **Quit/close:** `m.Stop()`, close the session, then `hold.Release()` (the lock goes last).

### Copy Claude config

`hub.BuildClaudeConfig(root, exe)` returns the two snippets Studio copies. **Neither contains a token**: they launch `slaw mcp serve --workspace DIR` over stdio, and when Studio owns the workspace that process reads the token from `hub.lock` at run time.

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "slaw-acme-analytics": {
      "command": "/path/to/slaw",
      "args": ["mcp", "serve", "--workspace", "/path/to/acme-analytics"]
    }
  }
}
```

Claude Code:

```
claude mcp add slaw-acme-analytics -- /path/to/slaw mcp serve --workspace /path/to/acme-analytics
```

The server name is `slaw-<folder>` so two workspaces do not collide. With "Ask before changes" on (the default), the client can read but every write is refused until the gate is off or Studio owns the workspace.

### Security notes

- `hub.lock` is `0600` and holds the endpoint and bearer token of a local server. The trust model is **same user only**: a reader refuses the file unless it is a regular file owned by the current uid with no group or world permission bits, ignores it unless the flock is really held, and accepts only a plain-`http` loopback URL, so a lock file committed to a cloned repository (or left by a crash) can never point a client, or its token, anywhere.
- The lock file is never written through a link: it is opened with `O_NOFOLLOW`, and must be a regular, single-link file owned by you (a committed `hub.lock -> victim` symlink, a hard link or a FIFO is refused, not truncated or hung on); `.slaw-local` itself must be a real directory owned by you.
- The token never appears on a command line, in the Claude config snippets, in the structured logs, or in error messages. It is regenerated each time a server starts. The one place it is printed is `slaw mcp serve --transport sse`, which writes it to stderr once so a network client can be configured; with stdio (what the Claude snippets use) it is never printed. Anything that can read your files as you can read it; that is the same trust as your own keychain-free local config.
- A proxy sends the token only as an `Authorization: Bearer` header.
- `SIGUSR1` is only sent to a pid whose lock file says it advertised release control, on this host, with the lock verified held and (Linux) the process pinned by a pidfd. One signal listener exists per process and is never torn down, so a stray signal cannot kill a Slaw process.

### Needs sign-in is reported, never prompted

A connection signed in with `aws-sso:PROFILE` ([connections.md](connections.md#aws-sso-sign-in-s3)) resolves its keys from the person's SSO session when it is used. Headless `slaw mcp serve` (and every CLI command) only reads that session: with none, or an ended one, the connection is *needs-credentials* with the hint "AWS session for profile dev has expired. Sign in, or run `aws sso login --profile dev`", and a statement that needs it fails at once with that text (the agent's refusal of an `s3://` URL names the connection that covers it but is not ready, and its hint; nothing waits, and no browser or sign-in flow can be started from MCP: the device sign-in lives only behind a Studio binding and `slaw conn credentials sign-in`). A session made elsewhere meanwhile (`aws sso login`) is noticed by the next statement, at most every 30 seconds, so a long-running server needs no restart. `list_connections` shows the status word and, for such a connection, `sign_in: {kind, subject, reason}` (the profile name and `missing` or `expired`; never an account, role or start URL). The agent reaches the same scope rule as with any storage connection: SSO keys change nothing it may read, and `~/.aws` is outside `data/`.

### SSE transport security

Both `--transport sse` and Studio's built-in MCP server bind to
`127.0.0.1` only and require a random bearer token, generated fresh each
time the server starts and printed to stderr (`--transport sse`). Studio never
shows it: the copied Claude config launches `slaw mcp serve`, which reads it from
`hub.lock` (same user only). A request is rejected if it:

- carries a browser `Origin` header at all (a legitimate MCP client never
  sets one — only a website running in a browser tab does, via
  fetch/XHR/EventSource — so its mere presence, regardless of value, is
  treated as a CSRF/DNS-rebinding attempt and blocked with `403`);
- has a non-loopback `Host` header (`404`/`403` defense in depth against DNS
  rebinding);
- is missing or has the wrong `Authorization: Bearer <token>` (`401`).

For the initial `GET /sse` connection only, a `?token=<token>` query
parameter is also accepted as a fallback, for a non-browser client whose SSE
library can't set custom headers on that specific request. (A browser
`EventSource` still can't use it: the browser attaches an `Origin` header,
which is rejected first.) **Every
other request — in particular the `POST` a client sends to make an actual
tool call — must use the `Authorization: Bearer <token>` header; `?token=`
on a `POST` is always rejected with `401`.** This is deliberate, not a gap:
the URL an SSE session posts tool calls to is generated by the MCP server
library itself and never carries the token, so there is no "generate the
token into the POST URL" option without forking that library — and every
real MCP client (Claude Desktop, an SDK, curl) can set arbitrary headers on
a POST, so the header is never actually a burden there. Only a hand-rolled
`EventSource`-based client needs the query-param fallback at all, and only
for its one `GET`.

## Claude Desktop configuration

See "Copy Claude config" above: no passphrase and no token are needed. The older `VIBE_PASSPHRASE` setup applied to `.vibe` files and is gone.

## Tool list

`slaw mcp serve` (owner or proxy) and Studio's SSE server all advertise exactly
`agents.ListToolsForMode(agents.ModeMCP)`, which is every tool that is not task-mode only. Call `tools/list` for the live set. By category (the sandbox tier that allows each is in [agent-policy.md](agent-policy.md#what-each-tier-allows)):

| Category | Tools |
|---|---|
| introspect (every tier) | `list_connections`, `introspect_schema`, `describe_table`, `list_notebooks`, `get_notebook`, `list_queries`, `run_saved_query`, `list_skills`, `read_skill`, `list_artifacts`, `get_artifact`, `list_definitions`, `get_definition`, `list_watches`, `get_watch`, `help_search`, `propose_schema`, `present_data`, `preview_query` |
| data | `run_query`, `create_record`, `update_record`, `delete_record`, `bulk_insert` |
| content | `add_query_cell`, `add_markdown_cell`, `update_cell`, `delete_cell`, `move_cell`, `create_dashboard`, `create_or_update_skill`, `write_artifact`, `save_query`, `build_query`, `propose_definition`, `write_watch` |
| schema (`full` only) | `create_table`, `add_column`, `drop_table`, `import_csv` |

Task-mode-only tools (`write_cell`, `need_more_info`) are
never listed or dispatchable over MCP — those exist purely for the in-app
task-mode notebook-cell authoring loop (vibe-app#66).
