# Rodney: browser QA for Studio

Rodney is a Go CLI that drives a **persistent headless Chrome** instance. Each
command is a short-lived process that connects to the same long-running browser
over WebSocket, so tabs, cookies, and page state persist between calls.

[Rodney](https://github.com/simonw/rodney) is the headless Chrome CLI we use for QA. Install it with
`go install github.com/simonw/rodney@latest` (Chrome or Chromium must be installed), then run
commands as `rodney <command>`. The Slaw-specific recipe is at the end of this page.

---

## Command reference

### Browser lifecycle

```
rodney start                    Launch headless Chrome
rodney stop                     Shut down Chrome and clean up
rodney status                   Show PID, WebSocket URL, active page
```

### Navigation

```
rodney open <url>               Navigate (auto-adds http:// if missing)
rodney back                     History back
rodney forward                  History forward
rodney reload                   Reload current page
```

### Page info / extraction

```
rodney url                      Print current URL
rodney title                    Print page title
rodney html [selector]          Full page HTML, or element outer HTML
rodney text <selector>          Element text content
rodney attr <selector> <name>   Element attribute value
rodney pdf [file]               Save page as PDF (default: page.pdf)
```

### JavaScript

```
rodney js <expression>          Evaluate JS expression
```

The expression is auto-wrapped as `() => { return (<expr>); }`.
Output: strings printed raw, objects/arrays pretty-printed as JSON.

### Interaction

```
rodney click <selector>         Click element
rodney input <selector> <text>  Clear field then type text
rodney clear <selector>         Clear input
rodney file <selector> <path|-> Set file on <input type="file"> (- = stdin)
rodney download <sel> [file|-]  Download href/src target (- = stdout)
rodney select <selector> <val>  Select dropdown option by value
rodney submit <selector>        Submit form
rodney hover <selector>         Hover
rodney focus <selector>         Focus
```

### Waiting / synchronization

```
rodney wait <selector>          Wait for element to appear (default 30s)
rodney waitload                 Wait for page load event
rodney waitstable               Wait for DOM to stop changing
rodney waitidle                 Wait for network idle
rodney sleep <seconds>          Sleep N seconds (decimals OK)
```

### Screenshots

```
rodney screenshot [-w N] [-h N] [file]   Page screenshot (default 1280×720, full page)
rodney screenshot-el <sel> [file]        Element screenshot
```

### Tab management

```
rodney pages                    List tabs (* = active)
rodney page <index>             Switch to tab by index
rodney newpage [url]            Open new tab
rodney closepage [index]        Close tab (default: active; can't close last)
```

### Element queries

```
rodney exists <selector>        Exit 0 if exists, 1 if not
rodney count <selector>         Print match count
rodney visible <selector>       Exit 0 if visible, 1 if not
```

### Accessibility

```
rodney ax-tree [--depth N] [--json]              Full accessibility tree
rodney ax-find [--name N] [--role R] [--json]    Find nodes by name/role
rodney ax-node <selector> [--json]               A11y info for one element
```

---

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `ROD_CHROME_BIN` | `/usr/bin/google-chrome` | Chrome binary path |
| `ROD_TIMEOUT` | `30` | Element query timeout (seconds) |
| `HTTPS_PROXY` / `HTTP_PROXY` | — | Authenticated proxy (auto-detected) |

State file: `~/.rodney/state.json`
Chrome profile: `~/.rodney/chrome-data/`

---

## Essential patterns

### Always start before use, stop when done

```bash
rodney start
# ... commands ...
rodney stop
```

If the browser is already running, `rodney status` will confirm. Always
call `rodney stop` when finished to clean up the Chrome process.

### Wait after navigation

Pages with JavaScript need time to render. After `rodney open`, use one of:

```bash
rodney open https://example.com
rodney waitstable        # best default — waits for DOM to settle
```

Choose the right wait:
- `waitload` — fires on the load event (fast, but JS may not be done)
- `waitstable` — waits for DOM mutations to stop (good for SPAs)
- `waitidle` — waits for network requests to finish
- `wait <selector>` — waits for a specific element to appear

### Extract data

```bash
title=$(rodney title)
link=$(rodney attr "a.primary" href)
content=$(rodney text ".article-body")
data=$(rodney js 'JSON.stringify(window.__DATA__)')
```

### Conditional logic with exit codes

`exists` and `visible` return exit code 0 (true) or 1 (false):

```bash
if rodney exists ".error-message"; then
    echo "Error: $(rodney text '.error-message')"
fi
```

### Multi-step form interaction

```bash
rodney input "#username" "user@example.com"
rodney input "#password" "secret"
rodney click "#login-button"
rodney waitstable
```

### File upload

```bash
rodney file "#file-input" /path/to/document.pdf
```

### Download files

```bash
rodney download "a.download-link" ./output.zip    # save to file
rodney download "img.hero" -                       # pipe to stdout
rodney download "a#report" ./report.pdf            # from href
```

Handles both regular URLs and data: URLs. Uses the page's cookies/session.

### Screenshot with custom viewport

```bash
rodney screenshot -w 1920 -h 1080 wide.png
rodney screenshot-el ".chart" chart.png
```

### Multi-tab workflows

```bash
rodney newpage https://other-site.com
rodney page 0          # switch back to first tab
rodney click ".btn"
rodney page 1          # switch to second tab
rodney text "h1"
rodney closepage 1     # close second tab
```

### Accessibility testing

```bash
# Dump full tree
rodney ax-tree --depth 3

# Find all buttons and check they have names
rodney ax-find --role button --json | jq '.[] | select(.name.value == "")'

# Inspect a specific element
rodney ax-node "#submit-btn" --json
```

---

## Important notes

- **Selectors** are CSS selectors (e.g., `#id`, `.class`, `div > p`, `[data-attr="val"]`).
- **Timeouts** default to 30 seconds for element queries. Override with `ROD_TIMEOUT`.
- **Chrome must be installed** — Rodney does not bundle it. Set `ROD_CHROME_BIN` if it's not at the default path.
- **One browser at a time** — `rodney start` fails if Chrome is already running. Use `rodney status` to check.
- **Quotes matter** — wrap selectors and JS expressions in quotes to prevent shell expansion.
- The `js` command auto-wraps expressions. Don't write `() => { ... }` yourself — just pass the expression directly.
- `input` selects all existing text before typing, effectively replacing the field content.
- `download` works with `href` attributes on links and `src` attributes on images. It fetches using the page's JS context so cookies and auth are preserved.

---

## Driving Slaw (workspaces, vibe-app#111)

Studio has no file to unlock any more: it starts at the **launcher** and opens a workspace *folder*.

- Run `task studio:dev` and drive **`http://localhost:34115`** (Wails-bound, has `window.go.main.App.*`), never `:5173`.
- Keep QA out of your real preferences: start the dev server with `SLAW_CONFIG_DIR=$(mktemp -d)` (the recent list and your per-workspace settings live there). Put a `slaw` binary on `PATH` if you want "Copy Claude config" to work.
- Create and open a workspace headlessly (semicolons break `rodney js`, use the comma operator or `.then`):

```bash
rodney js 'window.go.main.App.CreateWorkspace({name:"QA",dir:"",parentDir:"/tmp/qa",git:false,template:"order-entry",inPlace:false}).then(r => r.status)'
rodney js 'window.go.main.App.OpenWorkspace("/tmp/qa/qa").then(r => r.status)'   # also: SwitchWorkspace({path}), CloseWorkspace()
```

- Qualified table names flow to the datasheet: `rodney js 'location.hash = "#/tables?table=local.main.customers"'`.
- Read-only / Take over: start `sleep 300 | slaw mcp serve --workspace DIR &` first, then `OpenWorkspace(DIR)`; the banner (`[data-testid=readonly-banner]`) offers Take over. Never `pkill -f` for cleanup.
- A change on disk (conflict banner): edit a `notebooks/*.slnb` file in a shell while the notebook is open; an unsaved edit in the title field shows `[data-testid=conflict-banner]`, no edit reloads quietly.
- The quit flow (`app:before-close`, a save slower than 3 s, "Quit anyway?") cannot be driven headless: Wails fires `OnBeforeClose` only from a real native window close, and `App.Quit()` really exits the dev app. Check it by hand in the `task studio:dev` desktop window (close the window during a slow save); the logic is pinned by `useAppEvents.spec.ts`. With `wails dev` the native window and a Rodney tab both answer `app:before-close`, and the one with nothing pending can call `Quit` first; that two-window race is a harness artefact, not an app bug.

## The Semantics view (E4)

The epic's exit round trip, in a real browser. Start the dev server with a throwaway config dir (above), create a workspace, and put data and a saved query in it through the bindings (no file, no real LLM needed):

```bash
q() { rodney js "window.go.main.App.RunQuery({sql:\"$1\",connection:\"\",tabId:\"qa\"}).then(r => 'ok').catch(e => 'ERR ' + e)"; }
q "CREATE TABLE sales AS SELECT * FROM (VALUES ('east','2026-01',100),('west','2026-01',80),('west','2026-02',-20)) t(region, month, amount)"
rodney js "window.go.main.App.SaveQueryOn({id:'net_revenue_base',title:'Net revenue base',sql:'SELECT region, month, amount FROM sales',description:'',rev:'',connection:''}).then(r => r.id)"
```

1. **A proposal.** With the local model at `localhost:8080` (`SetLLMSettings({provider:"local",endpoint:"http://localhost:8080/v1",model:...})`), ask the agent in chat (or `AgentChatStream`) to propose a metric with `propose_definition`; there is no approval card, the proposal files straight into the inbox. Without a model, write a valid proposal file into `<workspace>/.slaw-local/proposals/<id>.yaml` by hand (the id shape is `20261029T153012Z-metric-net_revenue-7f3a`; `definition:` is the exact metric document). It is validated on read, and the watcher reports it (`workspace:changed-on-disk`, kind `proposal`).
2. **The badge.** `[data-testid=semantics-badge]` on the Semantics nav item shows the open count without a reload.
3. **The inbox.** `#/semantics?kind=proposal`: click the proposal card; `[data-testid=proposal-diff]` has one `[data-op]` line per line (`+`, `-`, space). Accept (`[data-testid=proposal-accept]`) opens the new metric card (`[data-testid=metric-card]`).
4. **Try it.** Tick `[data-testid=try-dim-region]`, click `[data-testid=try-run]`: `[data-testid=result-grid]` has a row per region. **Run assertions** (`[data-testid=run-assertions]`) shows `[data-testid=assertion-summary]`.
5. **The watcher.** Edit `semantics/metrics/<name>.yaml` in a shell (`sed -i`): the card's measure text changes within a second, no click.
6. **Stale.** Write a second proposal for the same name with `replaces_rev` set to a revision that is not the file's: the preview shows `[data-testid=proposal-stale]` and the button reads "Accept anyway". Clicking it overwrites. To see "changed again", edit the file after the preview opened and click Accept.
7. **A join.** Write two entities and `semantics/joins/<name>.yaml` over tables you created, open it and click `[data-testid=join-profile]`: `[data-testid=join-measured]` fills in; declare a cardinality the data contradicts to see `[data-testid=join-warning]`.
8. **A page that reads a metric (#282).** Create `artifacts/rev/artifact.yaml` (`view: html`, a query `SELECT * FROM metric('net_revenue')`) and `html/index.html.tmpl` (`{{ query "q" }}`); open `#/library/artifact/rev`: the allow prompt lists `[data-testid=metrics-read-net_revenue]`. Allow it, then edit the metric's `measure:` on disk: the trust screen switches to "A metric this page reads changed (net_revenue), though its own files did not" and asks again (Settings > Pages says "A metric it reads changed"). An allowed page window or static page that was open closes at once, and a `slaw.run` after the edit is refused ("a metric this page reads changed"), with no half-second of grace. An artifact with no `metric()` stays allowed through the same edit.

Screenshots go to `qa-screenshots/` (git-ignored), once in each theme (`[data-testid=theme-toggle]`).

## Update checks without GitHub (dev builds only)

A `dev` binary (`task studio:dev`, `go run`) honours two environment variables that a release build ignores, so the update UI can be driven against a fake release server:

- `SLAW_UPDATE_BASE_URL=http://127.0.0.1:PORT` serves everything `internal/selfupdate` would ask github.com and api.github.com for: `/SandwichLabs/slaw/releases/latest` (a 302 to `/SandwichLabs/slaw/releases/tag/vX.Y.Z`), `/api/repos/SandwichLabs/slaw/releases/tags/vX.Y.Z` (JSON `body`, `html_url`) and `/SandwichLabs/slaw/releases/download/vX.Y.Z/{install.sh,checksums.txt}`. `http://` is allowed only through this hook, and the installer is told to fetch its archive from the same server (`SLAW_DOWNLOAD_BASE`).
- `SLAW_UPDATE_PRETEND_VERSION=v0.1.0` makes the check compare against that version instead of `dev` (and makes Studio ask the first-run question, which a plain dev build never does).

The hooks apply to any binary whose version is `dev`, so a `go install` build honours them too; such a build is never manifest-managed (it is not listed in an install manifest), so it can check but not apply unless you fake a manifest as below.

Applying needs the running binary to be listed in an install manifest: point `XDG_DATA_HOME` at a temp dir and write `slaw/install-manifest` there (`version=0.1.0` plus `F <64 hex> /abs/path/to/the/running/binary`). Use a temp `HOME`, `XDG_CONFIG_HOME` (or `SLAW_CONFIG_DIR`) and `XDG_DATA_HOME` so your real preferences and install are untouched, and a fake `install.sh` that only rewrites the temp manifest's `version=` line. Never run the real `install.sh` against your own `~/.local`.

## AWS sign-in without AWS (the `ssofake` dev build)

`task studio:dev:ssofake` runs the Wails dev server with the `ssofake` build tag (`studio/ssofake.go`), which points AWS sign-in at a loopback fake IAM Identity Center and lifts the AWS-endpoint rule so a connection can read a local MinIO. It exists for Rodney QA of the sign-in banner and the editor card; it is never part of CI or a release (`scripts/check-no-ssofake.sh` fails the build if a shipped build names the tag).

1. Start the fake: `go run ./tools/ssofake --addr 127.0.0.1:38999 [--minio 127.0.0.1:PORT --secret MINIOSECRET]` (with `--minio` the portal hands out MinIO STS credentials, so reads work; `--cred-life 6m` is how long it says they last, and Slaw renews five minutes before).
2. Give the dev server a home with `~/.aws/config` holding an SSO profile (`sso_start_url = https://corp.awsapps.com/start`, `sso_region = us-east-1`, an account and a role) and start it with `SLAW_SSOFAKE_OIDC=http://127.0.0.1:38999 SLAW_SSOFAKE_PORTAL=http://127.0.0.1:38999`. The build opens no browser for the sign-in page; it logs the address instead.
3. Drive the fake from the shell: `curl 127.0.0.1:38999/_ctl/approve` (the person approved in the browser), `/_ctl/deny`, `/_ctl/expire` (the session ended: the portal refuses its tokens and the refresh), `/_ctl/state`.
