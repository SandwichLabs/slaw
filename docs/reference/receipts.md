# Run receipts: settings and clear

A **receipt** is a small file that says a question was asked and what it touched: the statement, the connections, the columns, how many rows and a keyed fingerprint of them. It never holds a result value or a credential. Receipts live in `.slaw-local/receipts/` in the workspace folder (git-ignored, never read into a `.slaw` bundle) and nothing is sent anywhere because of them. What a receipt holds, and who mints one, is in [privacy](../privacy.md#proposals-watch-history-and-receipts-stay-on-this-machine) and the [console](console.md#run-receipts); this page is how long they are kept and how to delete them.

## Settings

How receipts are kept is **your own** setting for this workspace on this computer. It is saved with your other per-workspace settings in your config directory, never in `slaw.yaml` (so git does not carry it) and never in a bundle. If those saved settings were made for a different folder and not yet confirmed, they are ignored and the defaults apply.

| Setting | Meaning | Default | Allowed |
|---|---|---|---|
| Keep parameter values | A receipt keeps the values a question was run with, each cut to 256 bytes. Off keeps only the names and a fingerprint (`params_hash`). | on | on or off |
| Keep for (days) | A receipt older than this is removed. | 30 | 1 to 365 |
| At most (receipts) | The newest this many are kept. | 20,000 | 100 to 200,000 |
| Using at most (MB) | The newest receipts that fit in this much space are kept. | 64 | 1 to 1,024 |

A number outside its range is moved to the nearest bound when you save (a typo can neither switch pruning off nor delete everything); a limit left blank or `0` means the default. Settings are applied at once, to the open workspace: new receipts follow them, existing receipts are not rewritten, and a **lowered limit removes the oldest receipts** (in the background, so a moment after you save). Receipts are also pruned when a workspace opens and every 500 receipts written. A receipt's age is its file's modification time.

The console keeps no receipts unless you type `.receipts on`; that is per console session and is not one of these settings.

## Studio

Settings > Workspace > Receipts (badge **This computer**): the checkbox, the three numbers with their units, a help line with the defaults and ranges, a usage line (`N receipts · X MB · oldest from <date>`), **Save**, and **Delete all receipts…**, which asks first. After a save the boxes show the values in force.

## Delete all

**Delete all receipts** (and `slaw receipts clear`) removes every receipt file and nothing else. It never follows a symbolic link, and it leaves any file that is not a receipt in place. Your settings stay. So does the fingerprint key (`.slaw-local/receipts/key`): every Slaw process that has the workspace open holds the key in memory, so replacing it would split new receipts into two sets whose fingerprints cannot be compared. Delete the whole `receipts` folder by hand if you want a new key.

## From a terminal

```
slaw receipts settings                       # print the settings in force and what is kept
slaw receipts settings --keep-params off --days 7 --max-count 500 --max-mb 8
slaw receipts settings --json
slaw receipts clear                          # asks [y/N]
slaw receipts clear --yes                    # no question (needed without a terminal)
```

With no flag `settings` prints the values in force, the defaults and allowed ranges, and the usage. With flags it saves them (the others keep their value), says which flags it had to move into range, and prints the result. `--keep-params` takes `on` or `off`; the three numbers must be positive. Both commands take `--workspace`.
