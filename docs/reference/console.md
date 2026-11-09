# The Data Console

The Console is a DuckDB-CLI-style terminal inside Studio: a tab beside the result Grid in the Query Explorer. It runs on the same query path as Run, so a statement takes the same gates and status updates in both places. It is drawn with xterm.js; the session behind it is `internal/repl` in Go.

## Opening it

In the Query Explorer, choose the **Console** tab under the editor. It starts when the tab is first shown and keeps its session while you switch back to **Grid**. The **Run on** picker sets the console's connection too: changing it moves the console to that connection (the empty choice is the workspace default) and prints `current: NAME` above a fresh prompt. The console only moves at a clean prompt. While a statement is running, in the middle of a multi-line statement, or with text typed at the prompt, it stays where it is and the status line under the console says "Console still on X: finish the statement or press Ctrl+C first"; nothing you typed is touched. Move the picker back, or finish the statement and pick again. An empty line counts as empty however it got that way (backspace, history up and down). Connecting to a connection that is not attached yet is cancelled by Ctrl+C like a statement, and a `.conn` you type yourself updates what the status line believes. `.conn NAME` still works when you type it yourself.

A console that ends (you typed `.exit`, the window stopped responding, the workspace closed) shows "Console closed: reason" and a **Reopen** button. Reopening starts a new session with a fresh screen.

## In a terminal: `slaw repl`

`slaw repl` runs the same console, the same `internal/repl` engine, on your terminal. It mirrors the Studio tab, so the gates, the lazy attach, `metric()` expansion, the dot commands, the keys below and the history file are the same. It is for people: it needs a terminal on standard input and refuses to start otherwise, and the agent never reads a console.

```sh
slaw repl                       # the nearest workspace at or above the current folder
slaw repl -c prod_pg            # unqualified names resolve in connection prod_pg
slaw repl --workspace ~/work/sales
```

| Flag | Does |
|---|---|
| `-c`, `--connection NAME` | the connection whose catalog unqualified names use (default: the workspace's default connection); unknown names are refused |
| `--workspace DIR` | the workspace folder (default: the nearest folder at or above the current directory with a `slaw.yaml`) |

`slaw repl` opens the workspace's hub, so while Studio has the workspace open it fails with the "held" error naming Studio: use Studio's Console tab, or close Studio first.

The terminal is put in raw mode while it runs and restored on exit. Ctrl+C cancels the running statement and Ctrl+D leaves; the console follows terminal resizes. The terminal UI's REPL (`slaw studio` without `--gui`) draws its results with this console's duckbox renderer.

## Typing

Statements end with `;` and may span lines (the prompt changes to `·`). Dot commands start a line.

| Key | What it does |
|---|---|
| Enter | Submit the line |
| Ctrl+C | Cancel the running statement; at an idle prompt, forget the half-typed statement |
| Ctrl+D | Leave the console (on an empty line) |
| Ctrl+A / Ctrl+E | Start / end of line |
| Ctrl+B / Ctrl+F, Left / Right | Back / forward one character |
| Alt+Left / Alt+Right | Back / forward one word |
| Ctrl+K / Ctrl+U / Ctrl+W | Delete to the end of the line / to the start / the word before the cursor |
| Ctrl+T | Swap the two characters around the cursor |
| Ctrl+L | Clear the screen |
| Up / Down | Walk the history |
| Tab | Complete names from the live catalog (never connects a connection, never runs SQL) |
| Ctrl+Shift+C | Copy the selection (Cmd+C on macOS) |
| Paste | Your platform's paste shortcut (Shift+Insert, Cmd+V); a multi-line paste runs line by line |

## Dot commands

`.help` lists these in the console.

| Command | Does |
|---|---|
| `.help` | show the list |
| `.mode [box\|csv\|duckbox\|json\|line\|markdown]` | set how results are drawn |
| `.timer on\|off` | show how long each statement took |
| `.maxrows [COUNT]` | rows to draw (0: all fetched) |
| `.maxwidth [COUNT]` | columns to draw within (0: the terminal width) |
| `.nullvalue STRING` | how NULL is written |
| `.headers on\|off` | csv header row |
| `.receipts on\|off` | keep a [run receipt](#run-receipts) for each read in this session (off when the console opens) |
| `.columns` / `.rows` | one line per column, or rows as rows (the default) |
| `.tables [PATTERN]` | tables and views of the current connection |
| `.schema [PATTERN]` | CREATE statements of the current connection |
| `.conn [NAME]` | show the connections, or switch to one |
| `.read NAME` | run a saved query |
| `.save TITLE` | save the last statement as a query |
| `.clear` | clear the screen |
| `.exit`, `.quit` | leave the console |

Some DuckDB CLI commands (for example `.shell`) are refused with an explanation. `.conn` is a dot command, so it only works at a fresh prompt, not in the middle of a multi-line statement.

## Run receipts

A receipt is a small file that says a question was asked and what it touched: the statement, the connections, the columns, how many rows and a fingerprint of them. It never holds a row. A console does not keep receipts until you type `.receipts on`; the choice is for that console only and is not remembered, so a new console starts off. While it is on the prompt shows `[receipts]` (`ws ▸ D [receipts] `), and each plain read (a `SELECT`, `WITH ... SELECT`, `FROM`, `VALUES`, `TABLE` or `EXPLAIN`, one statement) ends with a dim `receipt r_...` line. A write, a script, `EXPLAIN ANALYZE`, a dot command (`.schema`, `.tables`) and a failed statement keep none. The receipt covers the rows the statement returned to the console (up to 10,000), not the lines drawn. A statement that looks like a credential is kept as its hash only. Receipts live in `.slaw-local/receipts/` (machine-local, git-ignored, never bundled); see [privacy](../privacy.md) and [receipts](receipts.md) for how long they are kept. `.receipts` alone says whether they are on, and the command answers "not available here" when the receipts folder could not be opened.

## History

History is kept per workspace in `.slaw-local/repl/history` (git-ignored, owner-only). Text is not recorded when it is refused by the gates, holds terminal control characters, or when the secret redactor would change it: a `password = '...'`, a bearer token, an API-key header, a JWT or a well-known token prefix (`sk-`, `ghp_`, `glpat-`, `xox*-`, `AKIA...`). The same rule decides what `.save` offers. Ordinary SQL that merely mentions `token` or `password` as a column name is recorded.

## Limits

- At most **4 consoles** per workspace (each holds one connection of the hub's pool).
- **10,000 lines** of scrollback.
- One input call carries at most 64 KiB; the window retries a full queue on its own.
- Output is flow-controlled: the window acknowledges what it has drawn, and a console that is more than about 1 MiB ahead waits instead of dropping bytes. A window that stops acknowledging for 30 seconds ends the console.
- The terminal uses WebGL when the webview offers it and falls back to the DOM renderer otherwise; the two look the same.
- Closing or switching the workspace ends every console and cancels what it was running.
- Statement text is never written to Studio's logs; debug lines carry a short digest and the statement's size, and DuckDB errors are logged without the statement echo (everything from "LINE 1:" on) and with the contents of every quoted run ('...', "...", $tag$...) replaced by "...", so only the error class and the unquoted words remain.

### Snapshot facts

Each connection a receipt lists also carries one fact about its state when the run finished, with a label: **replayable** (the fact names a version a later read can ask for again), **live only** (it says what the source looked like then, but the data can change under it, so the same question may answer differently later) or **not recorded** (it was not asked, or it did not answer in time). A connection that was not connected yet has no fact: Slaw never connects one to collect it. All facts for one run share 300 ms; one that has not answered by then is `not recorded` (value `timed out`) and is not asked again, because a later answer would describe a different moment.

| Connection | Fact | Label |
|---|---|---|
| DuckLake | the lake's current snapshot id (`ducklake_current_snapshot()`) | replayable |
| Iceberg table (a location) | the newest snapshot id of the table location the statement used | replayable |
| Iceberg catalog (REST, S3 Tables) | none: DuckDB does not say which snapshot a catalog table resolved to without re-binding it | not recorded |
| Delta table (a folder on this computer) | the newest commit number in `_delta_log` (a table in object storage: not recorded) | replayable |
| PostgreSQL | the server's WAL position, `pg_current_wal_lsn()` (a standby has none: not recorded) | live only |
| DuckDB file | the file's size and modification time | live only |
| S3, GCS, R2, Azure | the size and modification time of the objects the statement reads by name: up to 8, then a count (`3 objects: 120@1790000000,...` is the size in bytes at the Unix time; a long list is a count and a digest) | live only |
| MotherDuck, MySQL, SQLite, the in-memory catalog | status only | live only |

DuckDB does not report an object's ETag, so the size and modification time stand in for it. A value is a number, a position or a time. It never holds a file path, a bucket, a key, a host or an error message; a fact that could not be read says only `unavailable`. Object storage and table connections attach no catalog, so they are listed in a receipt only when the statement reads a URL or folder by name (after FROM or JOIN, or as the first argument of a `read_*`, `*_scan` or `read_blob` call) that lies in the connection's scope or at its table. A URL that is only mentioned (in a `WHERE`) lists nothing.

The snapshot recorded is the one current when the run finished; a commit during the run is not detected. When the statement itself pins a version (`version`, `snapshot_from_id` or `snapshot_from_timestamp` on an Iceberg or Delta reader, or a DuckLake `AT (VERSION ...)` or `AT (TIMESTAMP ...)`), the fact is `not recorded` with the value `version pinned in the statement`, since "the newest" is not what it read. Collectors look only at a connection's own definition (its file, folder or location), never at a path or URL written in the statement.

Collecting adds up to 300 ms to a receipted read (the budget for all of a run's facts together), and at most four collectors run at once in the whole process. A remote Postgres may often record `timed out`: the fact opens a fresh connection to the server, which can take longer than the budget.
