# Run receipts: evidence, settings and clear

A **receipt** is a small file that says a question was asked and what it touched: the statement, the connections, the columns, how many rows and a keyed fingerprint of them. It never holds a result value or a credential. Receipts live in `.slaw-local/receipts/` in the workspace folder (git-ignored, never read into a `.slaw` bundle) and nothing is sent anywhere because of them. What a receipt holds, and who mints one, is in [privacy](../privacy.md#proposals-watch-history-and-receipts-stay-on-this-machine) and the [console](console.md#run-receipts); this page is how to read one (the Evidence drawer, `slaw receipts show` and `list`), how long they are kept and how to delete them.

## Evidence: reading a receipt

A value that a query produced has an **Evidence** button, and it opens the **Evidence drawer**: a panel on the right, under the top bar, that shows the receipt of the run behind the value. The page makes room for it (on a narrow window it overlays instead). It is one drawer for the whole app, so opening it from another value replaces what it shows. Closing or switching the workspace closes it and forgets the parameter values the windows were holding. Focus moves into it when it opens; **Esc**, or the close button (**Close evidence (Esc)**), closes it and puts focus back on the button that opened it.

Where the button is:

- the Query Explorer's result bar, next to the row count and the time;
- the footer of a notebook SQL cell;
- a data-bound A2UI `stat`, `table`, `barChart`, `lineChart` and each of the seven KINETIC charts (a small document icon, labelled **Show evidence for TITLE**), in notebooks and in `view: a2ui` artifacts;
- the result of a metric's **Try it**.

A value whose run has no receipt and nothing to say about why (inline data, a statement that is not a single read) has no button.

The drawer's sections, top to bottom:

| Section | What it says |
|---|---|
| **Summary** | One sentence ("One query on **lake**, read at snapshot **4812**. Run it at that snapshot and you read the same data."), then two badges: **Replayable** (every connection has a snapshot that can be read at), **Live only** (a snapshot says where the source was when it answered; you can't read that moment again) or **Not recorded**, and the verdict, **Read-only check passed** for a policy-gated run or **Your own query** for the editor, console and CLI. |
| **Query** | **As written** or **As run** (a pressed-state toggle): the statement as you wrote it, with `metric()` and `$name` intact, or the text the engine executed (after `metric()` expansion, the row cap and any wildcard expansion). The parameters (`$start 2026-07-01`) and the file the statement came from (`From queries/net_revenue.sql`). |
| **Connection and snapshot** | Each connection's name, type and status beside its snapshot: the value and what it is, "Recorded right after the run" and, for a replayable one, "current when the run finished". A snapshot that was **not recorded** says why ("Not recorded: not available for catalog tables" for an `iceberg_catalog`, "version pinned in the statement") and never offers replay. |
| **Result** | Rows "N returned of M" (and "truncated" when the run was cut), the columns as name and type ("(name withheld)" where a name could carry a value), and the result's fingerprint shortened as `9c1e04…b7a4`. "The receipt keeps a fingerprint of the rows, never the rows. The value on the card comes from this run, not from the receipt." |
| **Run** | When (time and duration), By ("You, opening the notebook", "The agent (run_query)") and From (the file-relative ref). |
| **Footer** | The receipt id, a copy button (**Copy receipt id**) and "Kept on this computer, never in the folder. `slaw receipts show` prints it." |

States other than a plain receipt:

- **Parameter values not kept.** With **Keep parameter values** off a receipt holds only the parameter names and a hash. The drawer shows the values the open run had (kept in this window's memory, never written anywhere), with a note: "Shown from this run. The receipt keeps only the names and a hash. Change in Settings > Workspace > Receipts." Opened later, with no run in memory, each value says **not kept**.
- **No receipt kept.** If the receipt could not be written, or this workspace keeps none, the drawer says "No receipt was kept for this run." with the reason (the receipt could not be written on this computer, or the receipts folder cannot be used) and shows the statement and connection the window still has. Reasons are a class, never an error message. A run never fails because its receipt could not be kept.
- **Opened from an id.** The title is the id and there is no value: "Receipts hold what was asked, never the answer."

A ref names the cell or the file the statement came from, not a copy of its text: if the cell was edited and not saved, or the file changed after the run, the saved text may differ from what ran. The receipt's SQL (**As written**, **As run**) is what ran.

The receipt of a run behind a notebook cell or an A2UI component names where it ran: `notebooks/<id>.slnb#<cell>` for a SQL cell (surface `notebook_cell`) and `notebooks/<id>.slnb#<cell>/<component>` for a component of an A2UI cell (surface `a2ui`). Studio checks the notebook, cell and component exist before it keeps the ref; one that does not match is dropped and the run is a plain `render` with no ref. Script views and HTML pages have no evidence button yet.

## Settings

How receipts are kept is **your own** setting for this workspace on this computer. It is saved with your other per-workspace settings in your config directory, never in `slaw.yaml` (so git does not carry it) and never in a bundle. If those saved settings were made for a different folder and not yet confirmed, they are ignored and the defaults apply.

| Setting | Meaning | Default | Allowed |
|---|---|---|---|
| Keep parameter values | A receipt keeps the values a question was run with, each cut to 256 bytes. Off keeps only the names and a fingerprint (`params_hash`). | on | on or off |
| Keep for (days) | A receipt older than this is removed. | 30 | 1 to 365 |
| At most (receipts) | The newest this many are kept. | 20,000 | 100 to 200,000 |
| Using at most (MB) | The newest receipts that fit in this much space are kept. | 64 | 1 to 1,024 |

A positive number outside its range is moved to the nearest bound when you save (a typo can neither switch pruning off nor delete everything). A limit left **blank** in Studio, or set to the word `default` on the command line, means the default; `0`, a negative number or text is refused, never read as the default. Settings are applied at once, to the open workspace: new receipts follow them, existing receipts are not rewritten, and a **lowered limit removes the oldest receipts** (in the background in Studio, so a moment after you save; the command line waits up to 5 seconds and says so if it could not). Every process that has the workspace open (Studio, `slaw mcp serve`) re-reads your settings file before each receipt and each prune, so a change made in another process, such as `slaw receipts settings --days 365`, reaches them without a restart; a settings file that cannot be read or is not trusted leaves the limits already in force, never the defaults. Receipts are also pruned when a workspace opens and every 500 receipts written. A receipt's age is its file's modification time.

The console keeps no receipts unless you type `.receipts on`; that is per console session and is not one of these settings.

## Studio

Settings > Workspace > Receipts (badge **This computer**): the checkbox, the three numbers with their units, a help line with the defaults and ranges, a usage line (`N receipts · X MB · oldest from <date>`), **Save**, and **Delete all receipts…**, which asks first. Under the usage line, **Recent receipts** lists the newest ten (what it was, when, who, how many rows, the file it came from); a line's **Evidence** button opens the drawer for that receipt by its id alone, which shows what was asked and never an answer. A blank box is the default (its placeholder names it) and a line under the boxes shows the values in force. If a number you typed was moved into range, the section says which.

## Delete all

**Delete all receipts** (and `slaw receipts clear`) removes every receipt file and nothing else. It never follows a symbolic link, and it leaves any file that is not a receipt in place. Your settings stay. So does the fingerprint key (`.slaw-local/receipts/key`): every Slaw process that has the workspace open holds the key in memory, so replacing it would split new receipts into two sets whose fingerprints cannot be compared. Delete the whole `receipts` folder by hand if you want a new key.

## From a terminal

```
slaw receipts show r_9c1e04...               # the drawer's sections as text
slaw receipts show r_9c1e04... --json        # the receipt record itself
slaw receipts list                           # newest first, one line each (default 50)
slaw receipts list --surface notebook_cell --limit 10 --json
slaw receipts settings                       # print the settings in force and what is kept
slaw receipts settings --keep-params off --days 7 --max-count 500 --max-mb 8
slaw receipts settings --days default        # back to the default for one limit
slaw receipts settings --json
slaw receipts clear                          # asks [y/N]
slaw receipts clear --yes                    # no question (needed without a terminal)
```

`show` prints a receipt in the drawer's sections (Summary, Query, Connections, Result, Run); it never holds a result value, so neither does what it prints. An id that is not a receipt, or has none, is an error. `list` prints the newest receipts, one line each: id, time, surface, ref, rows and the first line of the statement. `--surface` keeps one surface (`query_editor`, `console`, `cli_query`, `render`, `notebook_cell`, `a2ui`, `artifact`, `metric`, `assertion`, `watch`, `agent_run_query`, `agent_saved_query`, `agent_preview`) and `--limit` says how many (default 50, at most 500). It reads files in newest-first order and stops once it has enough, so a large store is cheap to list; it reads at most the newest 2,000 files, and when it stops there with nothing (or too little) found it says "none among the newest 2,000 receipts" rather than "No receipts.". Text mode prints every receipt field with control characters (escape sequences included) removed, since a receipt file can be edited by hand; `--json` prints the record as it is. All of these take `--workspace`.

With no flag `settings` prints the values in force, the defaults and allowed ranges, and the usage. With flags it saves them (the others keep their value), says which flags it had to move into range, and prints the result. `--keep-params` takes `on` or `off`; each number must be positive or the word `default` (`0` is refused). Both commands take `--workspace`.
