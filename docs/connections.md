# Connection recipes

A connection is a place your data lives. Slaw attaches each one into the workspace's embedded DuckDB the first time something uses it, so you can query `connection_name.schema.table` and join across them. This page is the how-to; the complete field list, status meanings and security notes are in the [connections reference](reference/connections.md).

## How connections work

- **Add one** in Studio (the **+** in the connections rail, "New connection": pick a type, fill the form, **Test**, **Save**) or on the command line with `slaw conn add NAME --type TYPE ...`.
- **Read-only by default.** Add `--read-only=false` (or switch it off in the editor) to allow writes. The read-only flag is a guard on Slaw's side; for a real guarantee use a read-only database role. MotherDuck has no read-only attach.
- **Scope** is where the definition is kept: `local` (default; this computer, this workspace), `shared` (in the committed `slaw.yaml`; teammates get the definition and enter their own credentials) or `user` (all your workspaces). Use `--scope`. DuckLake, Iceberg and Delta may be defined in any scope; a shared one carries a warning (table metadata decides which files DuckDB reads).
- **Credentials never go in the folder.** They go into your OS keychain, or are read from environment variables, or are asked for each time. Nothing secret is ever typed on a command line.
- **Check it** with `slaw conn test NAME` (a throwaway session, 10 second limit) and `slaw conn list` (status per connection).
- **DuckDB extensions** (postgres, mysql, httpfs, ...) are downloaded once, only when you ask for them (Install, `--install`; Excel import and export fetch `excel` the first time). A new connection reads `extension-missing` until you add `--install` to `slaw conn test` or `slaw conn connect`, or press **Install** in Studio. See [Privacy](privacy.md) and [troubleshooting](troubleshooting.md#extension-download-blocked-or-offline).

### Sign-in methods

| Method | `--sign-in` | Where the secret lives |
|---|---|---|
| Keychain (default) | `keychain` | Your OS keychain (macOS Keychain, or a Secret Service such as GNOME Keyring or KWallet). On a terminal, `slaw conn add` prompts and stores it. Without a terminal: `slaw conn credentials set NAME` reads it from stdin. |
| Environment | `env:PREFIX` | Read-only from variables `PREFIX_USER`, `PREFIX_PASSWORD`, `PREFIX_TOKEN`, `PREFIX_KEY_ID`, `PREFIX_SECRET`. `env:MOTHERDUCK` is special: it reads `MOTHERDUCK_TOKEN`. Good for CI, servers and machines with no keychain. You can keep them in a workspace `.env` after you allow it (`slaw workspace env allow`). |
| Prompt | `prompt` | Asked each time; held in memory until Slaw quits. |

Manage saved credentials with `slaw conn credentials set|clear|status NAME`. They are write-only: Slaw never shows a stored secret. Full mapping and the `.env` rules: [workspaces reference, credentials](reference/workspaces.md#credentials).

## Local DuckDB, CSV and Parquet

Every workspace starts with a `local` DuckDB connection (`data/local.duckdb`). Files need no connection at all.

```sh
slaw import data/orders.csv --into orders          # CSV, TSV or xlsx into a table
slaw query "SELECT * FROM read_parquet('data/events.parquet') LIMIT 10"
slaw export orders -o data/orders.parquet          # csv, parquet, json or xlsx
```

Another DuckDB file:

```sh
slaw conn add archive --type duckdb --path /path/to/archive.duckdb
```

In your own SQL (Studio's editor, `slaw query`) any readable path works. The agent can read files only inside the workspace's `data/` folder. See [agent policy](reference/agent-policy.md#files-and-urls).

## SQLite

```sh
slaw conn add legacy --type sqlite --path data/legacy.sqlite
```

No credentials. A path inside the workspace can be `shared`; an absolute path only in your own scopes.

## MotherDuck

```sh
export MOTHERDUCK_TOKEN=...        # from app.motherduck.com
slaw conn add md --type motherduck --database my_db --sign-in env:MOTHERDUCK
slaw conn test md --install
```

Or leave out `--sign-in` on a terminal and paste the token when asked, to keep it in the keychain. In Studio the editor has a database picker once the token is entered. Things to know:

- A named database attaches as one catalog: `md.main.table`.
- To reach several databases of the account, add one connection per database with the same sign-in (`slaw conn add newsroom --type motherduck --database newsroom --sign-in env:MOTHERDUCK`); each is its own catalog (`newsroom.raw.table`). A four-part name such as `md.newsroom.raw.table` does not work in DuckDB.
- It cannot be attached read-only. For read-only access use a MotherDuck read-scaling token.
- A stray `MOTHERDUCK_TOKEN` in your environment is **not** used unless the connection's sign-in is `env:MOTHERDUCK`.
- One MotherDuck account per workspace.

## Postgres

```sh
slaw conn add prod_pg --type postgres --host db.internal --database analytics
# prompts for the user and password and keeps them in the keychain
slaw conn test prod_pg --install
slaw query "SELECT count(*) FROM prod_pg.public.orders"
```

Use a **read-only database role**: Slaw's read-only flag stops ordinary writes but is not a database permission. For TLS add `--option sslmode=require` (`disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`). For CI or servers: `--sign-in env:PGPROD` with `PGPROD_USER` and `PGPROD_PASSWORD`.

## MySQL and MariaDB

```sh
slaw conn add shop --type mysql --host db.internal --database shop --option ssl_mode=required
```

MySQL 8's default login needs an encrypted connection, so use `ssl_mode=required` (or `preferred`). Same read-only-role advice as Postgres.

## S3, Google Cloud Storage and Cloudflare R2

```sh
slaw conn add lake --type s3 --prefix s3://acme-lake/raw/ --region us-east-1
slaw conn add r2data --type r2 --prefix r2://bucket/path/ --option account_id=<32 hex chars>
slaw conn add gcsdata --type gcs --prefix gcs://bucket/path/
```

The credential is an access key id and secret (prompted, or `--sign-in env:PREFIX` with `PREFIX_KEY_ID` and `PREFIX_SECRET`; `PREFIX_TOKEN` for an S3 session token). For an S3-compatible store add `--option endpoint=...` (and, if needed, `url_style`). Once connected, query files by URL under the prefix:

```sql
SELECT * FROM read_parquet('s3://acme-lake/raw/2026/jan.parquet') LIMIT 10;
```

A `*` or `**` wildcard works too (`read_parquet('s3://acme-lake/raw/2026/*.parquet')`, `FROM 's3://acme-lake/raw/**/*.csv'`, `glob('s3://acme-lake/raw/**')`): Slaw lists the matching objects when the query starts and reads that list. It is a point-in-time list, capped at 10,000 objects per query, and a pattern with no match is an error. See the object-storage notes in `reference/connections.md`.

### Sign in to S3 with your AWS SSO profile

If your company signs in to AWS through IAM Identity Center, use the profile you already have in `~/.aws/config` instead of a key:

```sh
slaw conn add dev --type s3 --prefix s3://acme-dev/ --region us-east-1 --sign-in aws-sso:dev
slaw conn credentials sign-in dev     # prints a page and a code; or run: aws sso login --profile dev
slaw conn credentials status dev
```

Only the profile name is saved (on this computer, never in `slaw.yaml`). The short-lived keys are fetched when a workspace that uses the connection opens and whenever it is used, renewed before they end, and never written to a file. When the session ends the connection says it needs you to sign in again and reads nothing (it never falls back to anonymous access). In Studio a banner offers **Sign in**; `slaw mcp serve` only reports the state, it never starts a sign-in. A lake whose files sit under this prefix uses the same sign-in. The details are in [`reference/connections.md`](reference/connections.md#aws-sso-sign-in-s3).

## Azure Blob / ADLS

```sh
slaw conn add events --type azure --prefix az://events/2024/ \
  --option sign_in=account_key --option account_name=acmelake
```

`sign_in` is `account_key` (default), `connection_string` or `service_principal` (also needs `--option tenant_id=...`). The secret is prompted or comes from `env:PREFIX`.

## DuckLake, Iceberg and Delta

```sh
slaw conn add wh --type ducklake --path data/wh.ducklake --option data_path=data/wh_files --read-only=false
slaw conn add sales --type delta --path data/sales
slaw conn add events_ice --type iceberg --path s3://acme-lake/warehouse/events/
```

- Any scope. In a committed `slaw.yaml` or a bundle it carries a warning: Table metadata in this lake decides which files DuckDB reads. Anyone who can write it can make a query read local files, fetch any web or local-network URL, or read other storage your connections cover.
- A `path` or `data_path` that is a URL must sit inside the scope of a storage connection (S3, GCS, R2 or Azure), which supplies the credential. In Studio the form shows whether one covers the URL and **Set up storage** opens a prefilled storage form; on the CLI `slaw conn add` prints the `slaw conn add ...` command that is missing. **Test** tries the storage connection first and says which of the two failed.
- The extension (`ducklake`, `iceberg`, `delta`) downloads once when you press Install or pass `--install`.
- The agent can read Iceberg and Delta tables with `iceberg_scan`, `iceberg_metadata`, `iceberg_snapshots` and `delta_scan`, under the same path and URL rules as `read_parquet` (a table folder inside `data/` or a storage URL inside a ready storage connection's scope; never a metadata file). See [agent-policy](reference/agent-policy.md).

## Sharing connections with a team

Add with `--scope shared` to write the definition into `slaw.yaml`, commit it, and each teammate enters their own credentials. Move one later with `slaw conn move NAME --to user|shared|local`. Details: [workspaces reference](reference/workspaces.md).
