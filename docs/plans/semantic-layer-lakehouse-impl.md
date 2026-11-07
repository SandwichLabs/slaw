# The semantic layer over a lakehouse: implementation plan

Status: **plan, not built (2026-11-07).** It implements [`semantic-layer-lakehouse.md`](semantic-layer-lakehouse.md) (gaps G1-G10); the issue drafts are in [`semantic-layer-issues.md`](semantic-layer-issues.md) (N1-N10). Nothing here is filed. Phase 1 is detailed down to files and acceptance; phase 2 to work packages; phases 3 and 4 are an outline. [Decisions](#decisions-2026-11-07) are recorded at the end.

The goal: the Slaw assistant and Claude Code over MCP answer business questions through `semantics/` and `metric()`, on tables kept in **S3 Tables**, **Iceberg REST catalogs** and **DuckLake**, with the same policy and limits as today.

> **Direction change (Zac, 2026-11-07, after S0 and L1–L2 review): open, not confined.** "Let's not restrict iceberg catalogs. This tool should be able to run the same queries DuckDB itself can. Just warn where necessary." This supersedes D1–D3 and the parts of L2–L5 built on them:
> - **The agent reads `iceberg_catalog` on the main hub like any other catalog**, joins included. There is no lake reader and no `trust_writers` toggle; L4 as written below is **dropped**. L5 becomes "agent policy and tools over Iceberg on the main hub".
> - **Vended credentials are off.** Every attach writes `ACCESS_DELEGATION_MODE 'none'`, so data reads go through the person's storage connections, and the deny secrets keep failing uncovered buckets closed. S0 item 8's scope takeover therefore cannot happen. A vend-only catalog needs a storage connection that covers its data.
> - **Read-only by default stays.** `read_only: false` is allowed, and writes follow the usual rules: the tier, a writable connection, and approval, which is always asked for a non-local connection.
> - **No own-scope rule** for `ducklake`, `iceberg`, `delta` or `iceberg_catalog`. A committed `slaw.yaml` or a bundle may define them; credentials still never go in either.
> - **The agent may call `iceberg_scan`, `iceberg_metadata`, `iceberg_snapshots` and `delta_scan`**, under the same path and URL rules as `read_parquet`.
> - **Warn where necessary.** The S0 item 6 risks remain by design. A table's writers can point its metadata at local files (rows come back with name mapping), loopback or local-network http, Azure, or any S3 object a storage connection covers. Those risks are stated in plain words on the connection form, in Test, in `connections.md` and in `agent-policy.md`. `http_timeout` is still set on the hub (S0 item 11).
> - **Never metadata the agent could have written:** the lake readers refuse local metadata files, and COPY refuses `metadata`/`_delta_log` paths (#349). `iceberg_catalog` gains a `default_namespace` option for unqualified names (#347).
>
> The S0 findings below are kept as the record of what the warning describes.

## Facts this plan rests on (checked against the code, 2026-11-07)

- **Iceberg today is a table, not a catalog.** `tableSourceType` (`internal/connections/tablesource.go`) attaches nothing; the person reads with `iceberg_scan('<path>')`. `iceberg_scan`, `iceberg_metadata`, `iceberg_snapshots` and `delta_scan` are refused for the agent in every tier by the syntactic layer, before any `EXPLAIN` (`docs/reference/agent-policy.md`, #20).
- **Own-scope rule.** `workspace.OwnScopeOnlyType` (`internal/workspace/config.go:391`) covers `ducklake`, `iceberg`, `delta`. It is enforced at save (`conn_crud.go:30`), at open (`connect.go:148`, skipped with an issue) and on bundle import (`internal/bundle/validate.go:188`).
- **Storage dependency exists.** `ResolveConnection` runs `checkStorageURLs` and fills `ResolvedConnection.Requires` (`connect.go:177-183`); the hub connects requirements first and reports "needs the `x` connection: ..." (`internal/session/requires.go`). `workspace.StorageFor` (`storage_for.go:96`) is the one "which storage connection covers this URL" rule, used by Studio's storage line (`useStorageDetect.ts`, `FindStorageFor`) and the CLI hint.
- **Only storage types widen agent reads.** `session.StorageScopeType` (`registry.go`) is the only way a connection exports a scope. A catalog type exports none.
- **Uncovered object-storage URLs fail closed.** Every session that loads httpfs gets lowest-priority deny secrets for `s3`, `r2`, `gcs`, `gs` (empty keys, endpoint `deny.invalid`) and `s3_url_compatibility_mode` on (`internal/session/storage_deny.go`, #135, #138). Their `SCOPE` is the bare scheme (`s3`, `r2`, `gcs`, `gs`; `storage_deny.go:65-70`), and DuckDB picks the secret with the **longest matching scope**: a storage connection's `s3://bucket/` beats them, and so does **any** secret whose scope starts `s3://`, a vended one included (see D2 and S0 item 8). There is **no deny secret for Azure**, and nothing covers `http(s)://` or local paths.
- **The hub is not filesystem-confined.** `hardening.go` sets autoinstall/autoload off, community extensions off, persistent secrets off and locks the configuration (`hardeningDSN`, `expectedSettings` at `hardening.go:79-91`), but never sets `enable_external_access` (so it stays on) and reads `allowed_directories` back as `[]`, because the person's own SQL reads local files. Only the policy's syntactic parse runs on a separate in-memory DuckDB with `enable_external_access=false`.
- **The policy allows any attached catalog** (`agent-policy.md` §"What the agent may run": `SELECT` over "any attached catalog including remote ones"). Plan operators are matched by name pattern (`*_SCAN`, `DUCKLAKE_*`), and a plan may contain no more file/URL readers than the statement wrote out (`sql_plan.go:103-110`). A `planNode` carries `name`, `children` and `extra_info` only (`sql_plan.go:18-22`): the database a scanned table belongs to is not in the plan, so "which catalog does this scan read" has to come from the classifier, not the plan.
- **The classifier resolves references against the live catalogs and screens view bodies.** `loadCatalog`/`loadDatabases` (`sql_catalog.go:110,157`) snapshot every attached database's tables and views from the user DB on each classification, and a view's `CREATE VIEW` text is screened recursively (`sql_policy_select.go:374`, `defScreen`). Lazily registered connections a statement names are attached by `Session.ConnectReferenced` (`session_connect.go:472`), called from the handler (`handler.go:248`, used at `handler_query.go:158,267`) **before** the policy's `EXPLAIN`.
- **Secrets are invisible to the agent.** `duckdb_secrets()`, `which_secret()` and `duckdb_secret_types()` are refused by the syntactic layer (`sql_names.go:161-163`, `sql_classify.go:336`, through views too) and again in plans (`sql_plan.go:24`); `duckdb_databases` and `pragma_database_list` are off the system-object allow-list (`sql_catalog.go:37`). A vended secret is therefore not readable by the agent. S0 item 8: DuckDB's error text names URLs and endpoints but never the vended key. `secret_string` redacts the secret but shows the vended `key_id`, which is one more reason `duckdb_secrets()` stays refused.
- **Person checks already run through the agent handler.** `hub.ReadOnlyRunner` (`internal/hub/agent.go:69`) is `NewAgentHandler(..., ModeReadOnly)`; `workspace.RunAssertions`/`ProfileJoin`/`MetricQuery` and `CheckWatches` (`watches_run.go:94`) take it, the CLI opens through `openReadOnlyRun` (`cmd/semantics_run.go:111`, `PolicyGatedRuns`), and Studio builds the same handler (`studio/app_agent.go:353`). Routing that lives in `agents.Handler` therefore covers D3 with no second seam; the Query Explorer (`ExpandForPerson`, `internal/hub/person.go:62`) is the only person path that bypasses the handler.
- **Credentials already fit.** `secrets.Credential` has `Token`, `KeyID` and `Secret` (`internal/secrets/credential.go`), enough for an Iceberg REST bearer token or OAuth2 client credentials. No new credential kind is needed, only a new DuckDB secret `TYPE iceberg` step.
- **SSO keys rotate in place.** aws-sso keys are short-lived and `internal/session/rotate.go` replaces the storage secret under the same name; an attached DuckLake keeps reading (ducklake-catalogs.md step 6).
- **DuckDB's Iceberg attach (docs for 1.5/2.0):**
  - S3 Tables: `ATTACH '<table-bucket ARN>' AS x (TYPE iceberg, ENDPOINT_TYPE s3_tables)` with an S3 secret.
  - Glue (not in phase 1, D4): `ATTACH '<account_id>[:catalog]' AS x (TYPE iceberg, ENDPOINT_TYPE glue)`, which needs `SECRET` naming an S3/AWS secret.
  - REST: `ATTACH '<warehouse>' AS x (TYPE iceberg, URI '<endpoint>', SECRET <iceberg secret>)`, with `AUTHORIZATION_TYPE oauth2|sigv4|none`.
  - **`ACCESS_DELEGATION_MODE` defaults to `vended_credentials`**, and we write it out as such (D2). The extension turns the `config` of a LoadTable response into a DuckDB secret (key, token, **endpoint**, region, scoped to the table's location, as far as the docs say): every one of those fields is chosen by the catalog, so S0 item 8 measures them.
  - `TABLE_RESOLUTION` defaults to `lazy`: an unresolved table lists one column of type `UNKNOWN`.
  - Writes through an attached catalog are supported upstream, so read-only must be enforced.
  - All of this must be re-checked on our pinned duckdb-go v2.10506.0 / DuckDB 1.5.6 (spike S0).

## Phase 1 (P0): reach the data, and let the agent read it (N1, #20)

### Work packages and PR sequence

Sequential, each a Sonnet implementer in a worktree, Opus adversarial review, live QA where there is UI. This is the same loop as ducklake-catalogs.md and watches.

| # | Package | PR | Depends on |
|---|---|---|---|
| S0 | Spike on DuckDB 1.5.6: attach forms, secrets, read-only, plan operators, URL behaviour, `disabled_filesystems` | none (findings into this doc) | |
| L1 | Test fixture: REST catalog + MinIO under `integration` | 1 | S0 |
| L2 | The `iceberg_catalog` type: definition, attach, secrets, extensions, Test | 2 | L1 |
| L3 | Workspace resolution, dependency, Studio form, CLI, docs (person reads end to end) | 3 | L2 |
| L4 | ~~The lake reader and `trust_writers`~~ dropped (direction change); `http_timeout` hardening moves to L5 | 4 | L3 |
| L5 | Agent policy and tools over Iceberg on the main hub; `metric()` over Iceberg over MCP; `http_timeout` | 5 | L3 |
| L6 | End to end on real AWS: S3 Tables via SSO; Rodney QA; release notes | 6 | L5 |

No release after L3 (D7): phase 1 ships once both halves are ready, people's reads (L1-L3) and the agent's (L4-L5), after L6. Between L3 and L5 on `main`, the agent refuses `iceberg_catalog` references with a clear message.

### S0. Spike (throwaway Go tests, not committed; findings recorded here)

Answer on duckdb-go v2.10506.0 / DuckDB 1.5.6, with the hub's hardening applied (autoinstall off, lock_configuration):

1. **Extensions.** Which load for each endpoint kind: `iceberg` + `avro` + `httpfs`, and whether SigV4 needs `aws`. Each must be installable through the consent flow (`InstallExtension` allow-list).
2. **Signing secret.** Does `SECRET 'slaw_<storage>'` (our storage secret, `TYPE s3` with a `SCOPE`) sign the S3 Tables catalog endpoint, and do data reads under the table bucket pick the same secret by scope? Does a rotated (replaced) secret keep an attached catalog working?
3. **Read-only.** Does `READ_ONLY` on an Iceberg attach refuse `INSERT`/`CREATE TABLE`/`DROP` (and which error)? The write plan operator names (`ICEBERG_INSERT`?) go into the write matcher either way.
4. **Plan shape.** The `EXPLAIN (FORMAT JSON)` operator name of a scan over an attached Iceberg table (`ICEBERG_SCAN`, or a `TABLE_SCAN` carrying a reader function). Does the policy's reader-count rule ("no more file/URL readers than the statement wrote") count it?
5. **Bind-time fetches.** What `EXPLAIN SELECT * FROM x.ns.t` fetches (REST LoadTable, `metadata.json`, manifest list, manifests). `TABLE_RESOLUTION lazy` vs a listing (`SHOW ALL TABLES`, `duckdb_columns()`).
6. **Hostile locations** (crafted table in the L1 fixture): a manifest list, a manifest or a data file at `file:///<abs>`, `/abs/path`, a relative path, `http://127.0.0.1:<port>/x.parquet`, an out-of-scope `s3://other/`, `az://other/`. Record which are fetched, by which component, at bind or at scan, and whether a local Parquet file's rows come back.
7. **`disabled_filesystems`.** On a second in-process DuckDB with `disabled_filesystems = 'LocalFileSystem'` (and separately `'LocalFileSystem,HTTPFileSystem'`), can it still attach and read REST and S3 Tables? The REST client may use DuckDB's `http_util` rather than `HTTPFileSystem`, so the second form may well work: record which. Are the hostile cases in item 6 refused? Does a manifest naming `http://127.0.0.1:<port>/x` or `http://169.254.169.254/` still produce a request with `HTTPFileSystem` disabled (watch the fixture's listener)? Is `S3FileSystem` unaffected by disabling `HTTPFileSystem` (it must be, or data reads stop)? Can the setting be locked and read back like the rest of `expectedSettings`? With no `azure` extension loaded, what does an `az://` location fail with?
8. **Vended credentials (on, D2).** With `ACCESS_DELEGATION_MODE 'vended_credentials'` set explicitly:
   - What appears in `duckdb_secrets()`: name, `TYPE`, `SCOPE`, `ENDPOINT`, `REGION`, `USE_SSL`, `URL_STYLE`, and whether it is `TEMPORARY`. When is it created (attach, listing, first bind, first scan), when refreshed, and does it survive `DETACH` and a re-`ATTACH` under the same name?
   - **Scope contest.** Have the fixture return a LoadTable `config` with each of: location `s3://warehouse/ns/t/` (honest), `s3://` (bare), `s3://<the person's bucket>/` (equal to a storage secret's scope) and `s3://other/` (outside every scope), each with `s3.endpoint` pointed at a second loopback listener. Then read (a) the table, (b) `s3://other/x.parquet` through `read_parquet` as the person, (c) a file under the person's own storage scope. Record for each which secret signed it and which endpoint received the request. The deny secrets' scope is the bare `s3`, so a vended scope of `s3://` is expected to **beat them**; a tie with an equal-length storage scope is undefined upstream. Whatever wins decides the L4 note below.
   - Does a vended secret's value or endpoint reach an error message (a 403 against the attacker endpoint, a wrong-region message)? The hub's redactor only knows our own credentials.
   - Is a vended secret on the lake reader invisible to the main hub, and the reverse (two in-process DuckDBs, two secret managers)?
   - Does S3 Tables still need the storage connection's secret to sign the catalog endpoint when data reads use vended credentials, and does it also vend for the data bucket (whose name is not the table bucket's)?
9. **Comments.** Do Iceberg field `doc` and table `comment` properties surface in `duckdb_columns().comment` / `duckdb_tables().comment` (for N4)?
10. **Listing fetches.** Under `TABLE_RESOLUTION lazy`, which of `duckdb_tables()`, `duckdb_columns()`, `SHOW ALL TABLES`, `DESCRIBE` and `information_schema.columns` trigger a LoadTable or a `metadata.json` read, per table or for every table? The answer decides what `engine.ListCatalog`, `lsp.Live`, the LSP snapshot and Test may run on the main hub against an untrusted catalog (L3).
11. **Cancellation and timeouts.** Does a REST or S3 fetch during bind honour the context cancel and the statement timeout `session.Query` applies (`session.go:350`), or does a hung catalog endpoint hang the pinned connection? Needed for L4's "same timeout and cancellation" claim and for `CancelRun` on a watch check.

Stop rules: if item 7 fails (the confined instance cannot read a REST catalog with the local filesystem off), revisit D1 before L4. If item 8's scope contest shows a vended scope of `s3://` or of the person's bucket taking over reads outside the table (expected), the L4 note on where vended secrets may live applies before L2 merges.

#### S0 findings (2026-11-07)

Run on duckdb-go v2.10506.0 / DuckDB 1.5.6, against the `apache/iceberg-rest-fixture` with MinIO. The hub's open-time hardening, the deny secrets and `s3_url_compatibility_mode` were applied, and a storage secret was scoped `s3://warehouse/`. Neither stop rule fires for D1. Item 8 fired as expected, and Zac settled it (D2).

1. **Extensions:** `httpfs`, `avro` and `iceberg` are enough for REST, and for S3 Tables SigV4 with an explicit `TYPE s3` secret. A fake-key attach reached `s3tables.us-east-1.amazonaws.com` and got a 403, with `aws` not loaded. `aws` is needed only for `credential_chain`, which Slaw does not use. No new extension name is needed.
2. **Signing and rotation:** `SECRET 'slaw_<storage>'` is accepted on an S3 Tables attach; real signing waits for L6. A `CREATE OR REPLACE` of the storage secret keeps an attached REST catalog reading. A replaced *wrong* key was not noticed on the next read, because metadata and file state are cached, so a revoked key fails late, not at once.
3. **Read-only:** `READ_ONLY` is honoured. `INSERT`, `CREATE` and `DROP` fail with `Invalid Input Error: Cannot execute statement of type "INSERT" on database "cat" which is attached in read-only mode!`. `duckdb_databases().readonly` is true.
4. **Plan shape:** the scan operator is `ICEBERG_SCAN` (`extra_info.Function = ICEBERG_SCAN`). There is no write operator to name, because a read-only attach refuses writes before planning.
5. **Bind-time fetches:** `EXPLAIN` does a LoadTable and reads `metadata.json` and the **manifest list**. A local manifest list was read during `EXPLAIN`. Data files are read only at scan: an http data file was not fetched by `EXPLAIN`. `count(*)` is answered from manifest metadata without opening a data file, so threat tests must read a column (`max(id)`).
6. **Hostile locations on an unconfined DuckDB (the main hub):**

   | Location | Result |
   |---|---|
   | Data file at a local absolute path, `file://` URL or relative path | Read |
   | Local file without field ids | Read: rows come back as NULLs, but the row count leaks |
   | Local file with `schema.name-mapping.default` set (a table property any writer can set) | Read: the **sentinel row came back in full** |
   | Manifest list at a local path | Read during bind |
   | `http://127.0.0.1:<port>/` | **Fetched (SSRF)** |
   | Uncovered `s3://other/` | Failed closed at `deny.invalid` |
   | `s3://other/` covered by one of the person's storage connections | Read with the person's key |
   | `az://` with no azure extension | Refused: missing extension |
   | `az://` with azure loaded and no credentials | "No valid Azure credentials found" |
7. **`disabled_filesystems = 'LocalFileSystem,HTTPFileSystem'`** blocks every local and http case above, at bind and at scan, with `Permission Error: File system LocalFileSystem has been disabled by configuration`.
   - The REST catalog client and S3 reads still work, because neither goes through `HTTPFileSystem`. So both filesystems are disabled on the lake reader.
   - The setting locks. A later `SET` fails with "the configuration has been locked".
   - It **reads back empty** through `current_setting()` and `duckdb_settings()`, so `expectedSettings` cannot check it. The lake reader verifies it by behaviour instead: after the lock, `read_text('<a file known to exist>')` and `read_text('http://127.0.0.1:1/')` must fail with that Permission Error. Otherwise the reader refuses to open.
   - Extensions must be `LOAD`ed **before** the setting. `LOAD` needs the local filesystem.
   - With HTTPFileSystem off, a vended `s3.endpoint` still sends requests to any host (item 8).
8. **Vended credentials:** the fixture vends nothing itself, so a proxy added a `config` to LoadTable responses.
   - The extension creates per-table `TEMPORARY` secrets named `__internal_ic_<uuid>-<ns>-<table>__...`, with provider `iceberg`. Their `SCOPE` is the **table's `location`**, normalised with a trailing slash. Endpoint, key and region come from the catalog.
   - **The scope contest:**
     - A location of `s3://` produced a vended secret that **beats the deny secrets for every uncovered bucket**.
     - A location of `s3://other/`, equal to the person's own `slaw_other` scope, **won the tie** over the person's secret.
     - A location of `s3://other/sub` took over that subtree.
     - Every one of these vended secrets carried the catalog's chosen `s3.endpoint`. A read under the scope went to that endpoint: the test's loopback listener received the request. The person's key was not sent.
   - The secrets **survive `DETACH`**.
   - The vended endpoint is honoured even with `HTTPFileSystem` disabled (S3FileSystem), so a catalog can make the lake reader send requests to any host.
   - Two in-process DuckDBs have separate secret managers.
   - Error text names the URL and the endpoint, never the vended key. `secret_string` redacts the secret, but **shows the vended `key_id`**, and the agent policy already refuses `duckdb_secrets`.
   - S3 Tables vending waits for L6.
9. **Comments:** neither a table `comment` property, set through the REST API, nor anything else surfaced in `duckdb_tables().comment` or `duckdb_columns().comment`. `COMMENT ON` is "Not implemented" for Iceberg. N4's Iceberg half is a follow-up.
10. **Listing:** under lazy resolution, `duckdb_tables()`, `duckdb_columns()` and `information_schema.columns` make **no** LoadTable call; each table shows one `UNKNOWN` column. `DESCRIBE` does a LoadTable (two GETs). So a names-only listing is safe on the main hub. Anything that describes a table is a bind.
11. **Cancellation:** a context cancel **does not interrupt** a hung LoadTable. A 3 s context deadline was ignored, and the pinned connection hung until killed.
    - `SET http_timeout = <seconds>` does bound it: `http_timeout = 2` failed after 9.5 s, which is consistent with the default retries.
    - So the hub and the lake reader both set `http_timeout`, and `http_retries` if needed, before the lock, and read them back. `CancelRun` on a watch over a hung catalog returns only when that timeout fires.

**Consequences folded into L2-L4:**
- No `aws` extension.
- The lake reader disables both filesystems and verifies them by behaviour.
- `http_timeout` is part of the hardening.
- Threat tests read a column.
- There is **one lake-reader instance per untrusted catalog**, because a vended scope chosen by one catalog's tables could take over another catalog's reads in a shared secret manager.
- A trusted catalog vends on the main hub, with the risk written into the toggle (D2).

### L1. Test fixture

- `internal/testsupport/icebergtest` (integration tag only, beside `miniotest`):
  - starts the Apache Iceberg REST fixture container (`apache/iceberg-rest-fixture`, JDBC/SQLite catalog, `CATALOG_WAREHOUSE=s3://warehouse/`, S3FileIO pointed at the shared MinIO) on an ephemeral port;
  - skips the test when no container runtime is available, as `minio_integration_test.go` does.
- **Seeding without Python:** a throwaway, unhardened DuckDB in the test attaches the fixture read-write and creates `ns.orders` (a few hundred rows, one table and column comment) and `ns.events` (two snapshots, for E6 later).
- **Crafted tables:** a helper that rewrites a seeded table's metadata in MinIO, pointing its manifest list, a manifest and a data file at each hostile location from S0 item 6, then commits through the REST API so the catalog serves it. Writing Avro manifests uses a Go Avro library in test code only (`github.com/hamba/avro/v2`; a test-only dependency, so `task notices` is unchanged unless L4 needs it at runtime).
- **Auth:** the fixture runs with no auth by default. Add a bearer-token mode (a tiny reverse proxy in the test that requires `Authorization: Bearer <needle>`) so that credential handling and redaction are tested, with the token value `iceberg_token_needle1` searched for in every status, error, log line and agent result.
- S3 Tables has no local fixture: it is covered by unit tests of the attach SQL, plus L6 on a real account behind an env-gated test (`SLAW_TEST_S3TABLES_ARN`, skipped otherwise, never in CI).
- **Vended credentials:** the REST fixture vends nothing itself (S0 item 8). The fixture's proxy adds a vended `config` to LoadTable responses, so D2's path is tested in CI.
- **Crafted vending:** the bearer-token proxy above also rewrites a chosen table's LoadTable response (`config` and `metadata-location`) so a test can vend a credential with a bare `s3://` scope, the person's own bucket as scope, or an `s3.endpoint` pointing at a second loopback listener that records every request it gets. This is S0 item 8's scope contest, kept as a permanent test for L4's acceptance.
- **Hostile listener:** a loopback HTTP server that records requests and serves a Parquet file carrying a sentinel value; the hostile manifests in S0 item 6 point at it, and the acceptance tests assert it saw no request from the lake reader.

**Acceptance:** `go test -tags integration ./internal/testsupport/icebergtest/...` brings the fixture up and down, and seeds and crafts tables; it skips cleanly without Docker.

### L2. The `iceberg_catalog` type (`internal/connections`)

> **Superseded in part (direction change):** attach with `ACCESS_DELEGATION_MODE 'none'`, no `trust_writers`, `read_only` a default not a force, no own-scope rule, and an optional `default_namespace`. As built: PR #347.

**Definition.** A new file `internal/connections/icebergcatalog.go`, registered in `model.go`'s `init`:

```yaml
# <config dir>/slaw/connections.yaml or the workspace's local.yaml (own scopes only)
connections:
  - name: aws_lake                 # s3, signed in with aws-sso; scope covers the table bucket's data
    type: s3
    scope: s3://4d1c...--table-s3/
    region: us-east-1
    options: { sign_in: aws-sso, profile: analytics }
  - name: s3t
    type: iceberg_catalog
    options:
      endpoint_type: s3_tables      # s3_tables | rest
      warehouse: arn:aws:s3tables:us-east-1:111122223333:bucket/sales
      storage: aws_lake             # s3_tables: the s3 connection whose identity signs the catalog
      trust_writers: false          # default; true lets agent reads run on the main hub (D1)
  - name: polaris
    type: iceberg_catalog
    options:
      endpoint_type: rest
      uri: https://catalog.example.com/api/catalog
      warehouse: analytics
      auth: oauth2                  # oauth2 | bearer | none
      oauth2_server_uri: https://catalog.example.com/api/catalog/v1/oauth/tokens
```

- **`Validate`** (no network), with one closed set per kind:
  - `endpoint_type` is required and one of two (`oneOf`): `s3_tables`, `rest`. `glue` is refused with "not supported yet" (D4).
  - `warehouse` is required:
    - `s3_tables`: an ARN `arn:aws(-cn|-us-gov)?:s3tables:<region>:<12 digits>:bucket/<name>`;
    - `rest`: at most 200 printable characters with no control characters.
  - `storage` is required for `s3_tables` and refused for `rest`. It names a connection (the `connname` rule).
  - `uri` is required for `rest` and refused for the others. It must be `https://` or `http://` (http only for a loopback host, so a token is never sent in clear to a remote host), with no userinfo, no query and no fragment.
  - `auth` (`rest` only) defaults to `oauth2`. `oauth2_server_uri` follows the same URI rule, is optional, and is only valid with `oauth2`.
  - `trust_writers` is an optional boolean, default false (D1). It is valid only in the person's own scope, which `iceberg_catalog` always is.
  - `read_only` must be true: a writable `iceberg_catalog` is refused. Writes are out of scope, and `Validate` is the one place that covers save, open and hand edits (the ducklake-catalogs.md pattern).
  - Unknown options are refused (`unknownOptions`).
- **`SecretSteps`** (run on the non-logging path):
  - `rest` + `bearer`: `TYPE iceberg, TOKEN <Credential.Token>`.
  - `rest` + `oauth2`: `TYPE iceberg, CLIENT_ID <KeyID>, CLIENT_SECRET <Secret>[, OAUTH2_SERVER_URI]`.
  - `rest` + `none`: no secret.
  - `s3_tables`: no secret of its own. They sign with the storage connection's secret, so `NeedsCredential` is false and the credential lives once, on the s3 connection, with its existing sign-in methods (keychain, env, prompt, aws-sso).
- **`AttachSQL`**, every value through `QuoteLiteral` / `QuoteIdent`, with no credential in the string:
  - `ATTACH '<arn>' AS "s3t" (TYPE iceberg, ENDPOINT_TYPE s3_tables, SECRET 'slaw_aws_lake', ACCESS_DELEGATION_MODE 'vended_credentials', READ_ONLY)`;
  - REST: `URI`, `SECRET 'slaw_<name>'` or `AUTHORIZATION_TYPE none`, `ACCESS_DELEGATION_MODE 'vended_credentials'`, `READ_ONLY`.
  - The delegation mode is always written out, never left to DuckDB's default, so a change upstream cannot silently change it. `AttachSQL` takes the mode from the `Spec` (a `session.Spec` field or an attach option the hub sets), because the same definition attaches with `'vended_credentials'` on the lake reader and, for an untrusted catalog on the main hub, with `'none'` (the L4 note on vended secrets). `Validate` never sees the mode: it is where the catalog attaches, not what the person wrote.
  - S0 item 3: `READ_ONLY` is honoured (`Cannot execute statement of type "INSERT" on database "cat" which is attached in read-only mode!`).
- **`SpecExtensions`**: `httpfs`, `avro`, `iceberg` (S0 item 1: no `aws`). There is no new extension name, so the install allow-list is unchanged. Add `iceberg_catalog` to `extensions_parity_test.go`.
- **`Probe`** (Test): attach in the throwaway session with the storage requirement brought up first (`test_required.go`, as for a lake). List namespaces (`SELECT schema_name FROM duckdb_schemas() WHERE database_name = ...`, capped at 50), then `DESCRIBE` one table. Detail: "3 namespaces, 41 tables; orders: 12 columns". A Test without the storage requirement does not attach. It says "signs with `aws_lake`: set it up first", the same as `tableSourceType.Probe`'s rule.
- **Test also reports data locations.** With vended credentials on, the agent's reads (lake reader) and a trusted catalog's reads use the catalog's vended secrets; the person's Query Explorer reads of an **untrusted** catalog still go through their storage connections (L4 note), so the line is advice there, not decoration. Read the `location` of up to 5 tables (S0: through `iceberg_metadata()` on the person-only test path, or the REST LoadTable response). Run them through `StorageFor` so the form can say "Table data is in `s3://4d1c...--table-s3/`, read with **aws_lake** · ok" or "... needs a storage connection". This matters for S3 Tables, whose data bucket name is not the table bucket's name.
- **`TypeInfo`**:
  - label "Iceberg catalog (S3 Tables, REST)";
  - fields `endpoint_type` (enum), `warehouse`, `storage` (a connection picker of s3 connections), `uri`, `auth`, `oauth2_server_uri`, `trust_writers` (a checkbox, "Trust this catalog's writers", with the residual risk spelled out beside it);
  - capabilities `Catalog: true, Writable: false, ReadOnlyAttach: true, Remote: true, Test: true`;
  - `FingerprintFields` `type, option:endpoint_type, option:warehouse, option:uri, option:trust_writers`;
  - `SignInMethods` `keychain, env, prompt` for `rest`, none for `s3_tables` (it signs with the storage connection).
- **`Remote()`** is true, so the schema cache trusts explicit invalidation only. The rail's refresh re-lists.

**Files:** `internal/connections/icebergcatalog.go` (+ `_test.go`), `model.go` (register), `extensions_parity_test.go`, `newtypes_test.go`, `test_required.go`.

**Acceptance:**
- Unit tests for every `Validate` refusal and for the attach SQL and secret DDL of each kind. A golden test asserts that no credential reaches `AttachSQL`.
- Under `integration`, against the L1 fixture: Test returns namespaces, tables and columns with a bearer token. A wrong token is a redacted auth error, and the needle never appears.
- A writable definition is refused at save, at open and when hand-edited.

### L3. Workspace resolution, the dependency, Studio and the CLI (person reads end to end)

- **Own scope:** add `iceberg_catalog` to `OwnScopeOnlyType` and to `OwnScopeOnlyMessage`'s wording. Bundle import drops it through `validate.go:188` with no new code; add a test. A committed definition in `slaw.yaml` is skipped at open with an issue.
- **Requires:** `ResolveConnection` (`connect.go`) sets `Requires` to the `storage` connection for `s3_tables`. It must exist, be an `s3` connection of this workspace and not be shared-scope-only-blocked.
  - There is no URL to check at open: data locations come from the catalog at read time, and an uncovered `s3://` location fails closed at the deny secret.
  - Re-registration of dependents when a storage connection changes already exists (`TestSyncReregistersDependentsOfAChangedStorageConnection`). Add `iceberg_catalog` to the dependent types in `studio/connections_deps.go:40`.
- **Error mapping:** a read that fails against `deny.invalid` is rewritten, in the hub's error path, to "table data at `s3://bucket/` is outside every storage connection's scope: add one whose scope contains it". The URL comes from DuckDB's message. The rewrite is a pure function with tests, and it never adds the raw error to the status.
- **Catalog listing:** `engine.ListCatalog` (`internal/engine/catalog.go:52`) already walks attached catalogs one lazy level at a time. A lazily resolved table shows its column count as unknown until opened. Cap the namespaces listed per catalog (200, with a "more" line) so a large S3 Tables bucket cannot stall the rail.
  - **Describing is a bind.** S0 item 10: names-only listings (`duckdb_tables()`, `duckdb_columns()`, `information_schema.columns`) make no LoadTable call under lazy resolution. `DESCRIBE` does, so anything that describes a table is a bind. Describing every table on the rail, in `lsp.Live`'s rebuild on `catalogChanged` (`studio/connections_catalog.go:66`), in the LSP snapshot or in Test would fetch each table's `metadata.json` on the **main hub**. So for an `iceberg_catalog`: the rail's namespace and table levels come from `duckdb_tables()`/`duckdb_schemas()` only, columns are fetched per table when a person opens that table, `lsp.Live` and the snapshot carry table names without columns for untrusted catalogs (a capped per-table fetch is fine for trusted ones), and Test's `DESCRIBE` is one table, chosen by the person or the first by name.
- **Lazy connect on the main hub:** `Session.ConnectReferenced` attaches a pending connection whenever a statement names it. A person's statement naming an untrusted catalog connects it on the main hub (their choice, as with `iceberg_scan` today). The agent's path is L4's concern: there the same call must not attach an untrusted catalog on the main hub.
- **Extensions:** `connections.ExtensionsFor` feeds `workspace.ExtensionNeeds`, so Settings > App > Extensions lists the catalog under `iceberg`/`avro`/`httpfs`.
- **Studio** (`ConnectionEditorView.vue` stays generic; the fields come from `TypeInfo`):
  - an `endpoint_type` select that shows only its kind's fields (one small `visibleWhen` rule in `useConnectionForm.ts` if the form has none yet);
  - a storage-connection picker filtered to `s3`;
  - the storage line under Test's reported data locations (`useStorageDetect` with the reported URLs);
  - a "New s3 connection" shortcut that pre-fills the data location as its scope (the #134 pattern);
  - `studio/connections_crud.go`: the type description and the type order, after `ducklake`;
  - `connections_extensions.go`: the description line.
- **CLI** (`cmd/conn.go`):
  - `slaw conn add s3t --type iceberg_catalog --option endpoint_type=s3_tables --option warehouse=arn:... --option storage=aws_lake`;
  - `--help` examples for each kind;
  - `slaw conn credentials set polaris` for the REST token or client secret, as for any type.
- **Docs:**
  - `docs/reference/connections.md`: a new `### iceberg_catalog` section, and the iceberg section's "Not built" line points to it;
  - `agent-policy.md`: "not yet readable by the agent", until L5;
  - the CLAUDE.md architecture tree;
  - `internal/docsref` passes.

**Acceptance:**
- Against the fixture, a person attaches `cat`, and `SELECT count(*) FROM cat.ns.orders` works in the Query Explorer, `slaw query`, the console and a notebook cell run by the person.
- The tables appear in the rail and in `list_connections` (`catalog: true`, no `scope`).
- An entity `table: cat.ns.orders` with `connection: cat` passes `slaw semantics check` (compile and a `row_count`).
- Committed in `slaw.yaml`, the connection is refused with an issue; in a bundle it is dropped.
- With the storage connection signed out, the catalog shows "needs the `aws_lake` connection: it needs credentials".
- The agent's `run_query` on `cat.ns.orders` is refused, with a message naming L5's rule ("this catalog's tables are not readable by the agent yet") and not a raw error.

### L4. The lake reader: a confined DuckDB for the agent's Iceberg reads (#20) — dropped (direction change above)

This is the security heart of phase 1. Per D1, every catalog is confined by default, and a person can mark one catalog `trust_writers: true` to opt it out.

**Threat.** The catalog definition is the person's, but its **metadata** is written by anyone who can commit to a table: pipelines, colleagues, a compromised job. Iceberg metadata names files by URL at three levels: the metadata file, the manifest list and manifests, and data and delete files. DuckDB follows those URLs, some while it binds (and so during the policy's own `EXPLAIN`), some while it scans. Today:
- an uncovered `s3/r2/gcs/gs` URL fails closed at a deny secret;
- a **local path**, an **`http(s)://`** URL or an **Azure** URL is read with the host's own access.

So a crafted manifest can make an agent query read a Parquet file on the person's disk, or send requests into their network (S0 item 6 measures exactly which). Vetting the names in Go cannot close this. The objects can be overwritten between the vet and DuckDB's read, and a new commit can re-point an old snapshot id. #20's review found the same for `iceberg_scan`.

**Design (D1):** run every agent statement that references an **untrusted** `iceberg_catalog` (the default) on **one in-process DuckDB per untrusted catalog**, the *lake reader*. It is opened through the same `session.OpenDB` + `Wrap` hardening, plus:
- `disabled_filesystems = 'LocalFileSystem,HTTPFileSystem'`. S0 item 7 showed that the REST client and S3 reads do not need either one. Set it after the extensions are loaded and before `lock_configuration`. It reads back empty, so the reader verifies it by behaviour: a local `read_text` and an `http://` `read_text` must both fail with the Permission Error. Otherwise the reader refuses to open, and the agent's reads of untrusted catalogs are refused, never routed to the main hub.
- **One lake-reader instance per untrusted catalog**, opened lazily on that catalog's first agent read. S0 item 8: a vended secret's scope is the table location, so one catalog's tables could take over another catalog's reads in a shared secret manager.
- `http_timeout`, and `http_retries` if needed, are set before the lock, on the main hub too (S0 item 11). A context cancel does not interrupt a hung catalog request.
- Only `httpfs`, `avro` and `iceberg` are loaded (S0 item 1: no `aws`). The `azure` extension is never loaded on the lake reader, so an `az://` location has no filesystem to read it (there is no Azure deny secret to lean on). The lake reader never carries an `azure`, `gcs` or `r2` storage connection: the only storage connections it attaches are the `s3` ones an `s3_tables` catalog requires.
- No local catalogs: only its one untrusted `iceberg_catalog`, the storage secret it requires (the same secret DDL, the same rotation through the hub's `rotate` loop), the deny secrets, and that catalog's own vended secrets (D2). The agent's reads of an untrusted catalog therefore vend into the lake reader's secret manager, not the main hub's; the person's reads are a different matter (the note below).
- No `data/` access, no `COPY`, no file readers: the policy for the lake reader allows only references to its attached catalogs and the allow-listed scalar and aggregate functions. `DataDirProvider` and `ProtectedPathsProvider` are not implemented for it, so the existing path rules refuse every file literal.
- The same statement timeout, cancellation and row caps as the hub (S0 item 11: a bind-time fetch ignores the context and is bounded only by `http_timeout`), its own smaller `memory_limit` and `threads`, and its own `temp_directory`.

> **Note on D2 and D3 (review, 2026-11-07): where vended secrets live.** An untrusted catalog is attached **twice**: on the lake reader for the agent, and on the main hub for the person's Query Explorer (D3). A vended secret is created by whatever process attaches, so the person's reads bring the catalog's vended secrets into the **main hub's** secret manager, where the person's own storage secrets and the deny secrets live. DuckDB chooses a secret by the longest matching scope and the deny secrets' scope is the bare `s3`, so a catalog that vends a secret scoped `s3://` (or the person's bucket) with its own `s3.endpoint` takes over every uncovered S3 read on the main hub and can redirect reads under its scope to a server it controls: it sees the key names the person reads and can serve forged rows (the person's own key is not sent, since DuckDB signs with the vended one). The lake reader isolating vended secrets only helps the agent's half. Mitigation that keeps D2 and D3 as decided: **vended credentials are on wherever the catalog's trust allows them.** On the lake reader every untrusted catalog attaches with `'vended_credentials'`. On the main hub a **trusted** catalog attaches with `'vended_credentials'` (the toggle's text then says "trust this catalog's operator and writers"), and an **untrusted** catalog attaches with `ACCESS_DELEGATION_MODE 'none'`, so the person's reads of it go through their storage connections' scopes and fail closed elsewhere, as every S3 read does today. Consequence to state in `connections.md`: a vend-only catalog (Polaris with no data-bucket storage connection) is readable by the agent and by `semantics check` and `watch check`, and in the Query Explorer only once the person adds a storage connection covering its data or trusts the catalog. **S0 item 8 confirmed the takeover** (bare `s3://`, a tie with the person's bucket, a subtree). Zac kept the plan's split: `'none'` for an untrusted catalog on the main hub, and `'vended_credentials'` for a trusted one, with the risk written into the toggle (D2).

**Routing** (`internal/hub`, `internal/agents`). The rule lives in `agents.Handler`, so the in-app agent, MCP, `ReadOnlyRunner` (assertions, watches, D3) and the artifact page runner all get it:
- **Classify references by database.** After `metric()` expansion, every table reference in the statement, and in the body of every view or macro the classifier screens (`defScreen`), is resolved to its database with DuckDB's default-catalog rules (`catalog.lookup`); the database name is matched against the registered connections. The set is then: exactly one untrusted `iceberg_catalog` (route to that catalog's lake reader), none of them (main hub, as today), or anything else, meaning two untrusted catalogs or a mix (refuse). An unqualified reference resolves to the default catalog, which is never an untrusted catalog on the lake reader: the lake reader has no default catalog, so an unqualified name there is refused with "name the catalog: `cat.ns.table`".
- **Main hub, agent path: deny before lookup.** Because the person may have attached an untrusted catalog on the main hub, the main-hub classifier must refuse any reference whose database is an untrusted catalog **before** the catalog snapshot can resolve it, including through a person's view (`CREATE VIEW v AS SELECT * FROM cat.ns.t`, caught by the view screen) and a table macro. And `connectReferenced` (`handler.go:248`) on the agent's path must skip untrusted catalogs, so an agent statement never attaches one on the main hub: give `Session.ConnectReferenced` a filter, or have the handler pass the names it may connect.
- **Lake reader path.** The statement is classified against the lake reader's own catalog snapshot and `EXPLAIN`ed there, so bind-time fetches happen inside the confinement, which answers #20's EXPLAIN-time question. Then it runs there with the same `RunReadOnlySQLIn` caps.
- A **trusted** catalog (`trust_writers: true`) is attached to the main hub only, and agent statements over it run there like any other catalog, joins included. It is not attached to the lake reader.
- A statement that mixes an untrusted `iceberg_catalog` with any other connection (a trusted catalog included) is refused with "join Iceberg tables with other data in your own SQL; the agent reads Iceberg catalogs on their own". People's SQL is unaffected and keeps using the main hub.
- `metric()`: a metric whose saved query is on an `iceberg_catalog` expands as today (`expandMetrics` runs first, `policy_handler.go:73`). The expanded `SELECT` routes by the same rule. A saved query's `connection` naming the catalog is the normal case; `run_saved_query` and the `connection` of an agent-authored notebook cell route the same way.
- `describe_table` and `introspect_schema` call `sess.DescribeTable`/`ListTables` (`handler_introspect.go:29-30,61`): they take the session the catalog name routes to.

**The trust toggle's residual risk** (shown in the form and in `connections.md`): on a trusted catalog, anyone who can commit to its tables, and the catalog's operator, can make an agent query, a semantics check or a watch check read with the person's own access:
- a local file (any path the person can read; a Parquet file's rows come back as query results);
- an `http(s)://` URL, including loopback services and the cloud metadata endpoint (`169.254.169.254`): a request from this machine, with what the response contains readable as data;
- an Azure URL (no deny secret exists for Azure) and any S3, GCS or R2 object inside the scope of **any** of the person's storage connections, not only the one that signs this catalog;
- with vended credentials (on for a trusted catalog, D2), a secret whose scope and endpoint the catalog chose (S0 item 8). This reaches past agent queries: a table at `s3://` switches off the uncovered-bucket protection for **every** read in the main hub. A table at one of the person's bucket scopes sends the person's own reads of that bucket to the catalog's endpoint, which can serve forged rows. These secrets stay until the hub closes, even after the catalog is detached.
What it cannot do: read the person's credentials (DuckDB signs with the matching secret and never returns it; `duckdb_secrets()` is refused), or write anywhere (`READ_ONLY`, the write matcher, and `COPY ... TO` stays inside `data/`). Turn it on only for catalogs whose operator and writers you would trust with your machine. Flipping it re-registers the catalog (it is in `FingerprintFields`) on both sessions.

**Lifecycle (where the second DuckDB lives):**
- Each lake reader is a `session.Session` (`session.New` with the confined options and its own `Registry` restricted to `iceberg_catalog` and `s3`), opened lazily per untrusted catalog on its first agent read. `hub.Opened` holds the set, and `Close` closes them before the main session. It is in-memory and takes no lock, so a read-only CLI open (`slaw semantics check`, `slaw watch check`, `PolicyGatedRuns`) and `slaw mcp serve` each get their own; Studio's hub ownership is unchanged.
- `AgentAccess` (`internal/workspace/agentaccess.go`) exposes it through a new optional interface in `agents` (`LakeReaderProvider`: the session for a catalog name, and which connection names are untrusted catalogs), nil for tests and for a legacy container, in which case Iceberg references are refused, never routed to the main hub.
- One connection, two statuses: Studio's rail shows the main hub's status (the person's attach); the lake reader's failure to attach reaches only the agent's error ("the agent's reader for `cat` is down: ...") and `list_connections`' `agent_reads` field. Both sessions get the storage secret rotation (`rotate.go`) and `WithStatusListener`; `catalogChanged` is fired by the main hub only.
- Studio's `connections_sync.go` re-registers a changed connection on both sessions; a toggle flip moves the catalog from one to the other without restarting.
- Cancellation: the agent turn's context (`agent_turn.go`) and `CancelRun`'s run keys already cancel through `ctx`; the lake reader's `Query` pins its own `Conn`. S0 item 11: a context cancel does not interrupt a hung bind. `http_timeout` bounds it, and `CancelRun` returns when that fires.
- Extensions install into the shared `extension_directory` once; the lake reader loads from it and never installs.

**Alternatives not taken:**
- A Go Avro vetter per snapshot: TOCTOU on objects and metadata, it duplicates the reader, and it still trusts DuckDB not to fetch more than the vetted set.
- Hub-wide `disabled_filesystems`: breaks the person's local files.

**Files:**
- `internal/session/lakereader.go`: the confined options (`WithDisabledFilesystems`, the restricted registry, the extension set), sharing `connectRequires`/secret steps and rotation with the hub; `session_connect.go`: `ConnectReferenced` takes a filter of connectable names.
- `internal/session/hardening.go`: an option for `disabled_filesystems`, set before the lock and verified by behaviour (a local and an `http://` `read_text` must fail with the Permission Error), plus `http_timeout`/`http_retries` with read-back, on both kinds of session.
- `internal/workspace/session.go` (`OpenSession` wires the reader factory), `internal/hub/open.go` (`Opened` holds the readers and closes them first), `internal/hub/agent.go` (`NewAgentHandler` hands the reader to the handler).
- `internal/agents/handler_query.go`, `policy_handler.go` (the route decision after `expandMetrics`, before `classify`), `sql_policy_select.go` (the database-of-reference check, including inside view bodies), `handler_introspect.go` (session by route), `handler.go` (the `LakeReaderProvider` interface and the `connectReferenced` filter).
- `internal/workspace/agentaccess.go` (the provider), `studio/connections_sync.go` and `connections_deps.go` (two sessions).
- `internal/connections/icebergcatalog.go`: the attach option for the delegation mode (L2).

**Acceptance (integration, L1 fixture, through the agent handler and over `slaw mcp serve`):**
- Every hostile location from S0 item 6 that the main hub would read is refused or fails on the lake reader, at `EXPLAIN` and at scan. A local Parquet file's sentinel value never appears in a result, an error or a log, and the hostile HTTP listener records no request.
- An honest table reads.
- The reader's behavioural check of `disabled_filesystems` passes. A build that fails it refuses to open the reader, and the agent's Iceberg reads are then refused, never routed to the main hub.
- A mixed statement is refused with the message above.
- On the main hub, with the person having attached untrusted `cat`: the agent's `SELECT * FROM cat.ns.t`, `SELECT * FROM v` (a person's view over `cat.ns.t`) and a table macro over it are all refused there and routed to or refused for the lake reader; `describe_table cat.ns.t` runs on the lake reader; a pending untrusted catalog is never attached on the main hub by an agent statement.
- Vended secrets: the crafted-vending fixture (L1) vends a secret scoped `s3://` with a loopback endpoint; a person's `read_parquet('s3://other/x.parquet')` on the main hub still fails at `deny.invalid` (the untrusted catalog attached there with `'none'`), the loopback endpoint records no request from the main hub, and `duckdb_secrets()` is refused for the agent on both sessions. With `trust_writers: true` the same vend is accepted on the main hub, and the form's warning named it.
- With `trust_writers: true`, the same catalog is read on the main hub, a join with a local DuckDB table works, and the lake reader does not attach it. Flipping the toggle back moves it to the lake reader without restarting Studio.
- Rotation: a replaced storage secret keeps the reader working (aws-sso rotation test with `ssofake`).
- A hung catalog endpoint (the fixture proxy stalls) is cut by `http_timeout`, not by the context (S0 item 11), and `CancelRun` on a watch check returns when that fires.

### L5. Agent policy and tools over Iceberg (main hub after the direction change)

> **Superseded in part (direction change):** there is no lake reader; the agent's reads run on the main hub (PR #347 allows the `ICEBERG_SCAN` plan operator over attached catalogs and the Iceberg write operators under the usual gates; PR #349 allows `iceberg_scan`/`delta_scan` under the `read_parquet` rules). What remains for L5: `http_timeout`, MCP end to end, the skill note and #20.

- **Plan operators:** add `ICEBERG_SCAN` (S0 item 4) to the read matcher. There is no write operator, because a read-only attach refuses writes before planning. Teach the reader-count rule that an attached Iceberg scan is a catalog scan, not a file reader.
- **Tools:**
  - `list_connections` shows `type: iceberg_catalog, catalog: true`, no `scope`, and `agent_reads: iceberg-only` (untrusted) or `agent_reads: joinable` (trusted), plus the lake reader's status for an untrusted one;
  - `introspect_schema` and `describe_table` run on the lake reader for untrusted catalogs (the session-by-route from L4);
  - `describe_table` triggers the lazy resolution (bounded by `http_timeout`, S0 item 11); `introspect_schema` lists table names only for an `iceberg_catalog` (S0 item 10), so one call cannot bind a whole bucket.
- **MCP instructions** (`internal/hub/instructions.go:84`) list connection names; an untrusted catalog gets a suffix "(Iceberg: read on its own, no joins)" so a client knows before its first refusal.
- **`metric()`:** nothing new in `semantics/expand.go`. Add tests that a metric over `cat.ns.orders` expands and routes, and that `run_saved_query` on a saved query with `connection: cat` routes.
- **Assertions and watches** run through `hub.ReadOnlyRunner` (`internal/hub/agent.go:69`, verified: it is `NewAgentHandler` in the read-only tier, and `workspace.RunAssertions`/`CheckWatches` and the CLI's `openReadOnlyRun` take it). They therefore route the same way (D3) with no new code outside the handler: a person's `slaw semantics check` and `slaw watch check` on an untrusted catalog run on the lake reader, and on a trusted one on the main hub. A watch history entry and an assertion result name which reader ran it, so an alert on an untrusted catalog can be told from one on trusted data.
- **Artifact queries** (`RunArtifactQuery`, `RunArtifactViewQuery`, the page runner) go through the same handler and so route too; a live `sql` binding over an untrusted catalog runs on the lake reader.
- **Skill and docs:**
  - the `semantic-layer` skill gets a short "tables in Iceberg catalogs" note (three-part names; no joins with other connections unless the catalog is trusted);
  - `agent-policy.md` gets the lake reader section, replacing "Iceberg and Delta are not agent readers" for catalogs. Delta and `iceberg_scan` stay refused.
  - Comment on #20 with the design, and close #20's Iceberg half when L6 passes.

**Acceptance:** `metric('net_revenue', by => 'region')` over a saved query on `cat.ns.orders` returns the same rows to:
- the in-app agent in the `read-only` tier;
- Claude Code over `slaw mcp serve`;
- the person's Query Explorer.

`INSERT`, `CREATE TABLE`, `DROP` and `iceberg_scan` on the catalog are refused in every tier.

### L6. End to end on AWS, QA, release

- An env-gated test on a real account. It runs on Zac's machine, never in CI.
  - S3 Tables via aws-sso with vended credentials: attach, list, `metric()` through MCP, once untrusted and once trusted.
  - S0's deferred items: S3 Tables SigV4 signing with `slaw_<storage>` (item 2), and whether S3 Tables vends for the data bucket, whose name is not the table bucket's (item 8).
  - Credentials are scanned for in every output, as in `TestFlociLakeEndToEnd`.
- Rodney QA in Studio: add `aws_lake` and `s3t` through the form (including the trust checkbox and its warning), Test (namespaces, data location line), browse from the rail, run a notebook cell, then ask the assistant for a defined metric.
- Docs:
  - `connections.md` (S3 Tables walkthrough with the data-bucket scope);
  - `agent-policy.md`;
  - CHANGELOG with an "Opening in an older Slaw" note: an `iceberg_catalog` in your own connections is an unknown type to 0.7 and older.
- Release: this is the first release with `iceberg_catalog` (D7), carrying both the person and the agent halves.

**Phase 1 exit:** an entity on `s3t.ns.t` passes `slaw semantics check`, and the agent answers a `metric()` question on it over MCP in the read-only tier.

## Phase 2: context reaches the agent (N4, N5, N6, N3)

Each package is one PR, in this order: N4 → N6 → N5 → N3. N6 before N5 because the index is cheap and measurable; N5 then adds search for workspaces past the index cap.

### N4. `describe_table` returns comments and the semantic layer's notes

1. **Session:** add `Comment string` to `session.ColumnInfo` and `TableDescription` (`internal/session/types.go:144-152`), read from `duckdb_columns().comment` / `duckdb_tables().comment` in `introspect.go`.
   - Comments are capped at 500 characters each and 2,000 per table, with control and bidi characters escaped as `<U+XXXX>` (the watches sample rule).
   - S0 item 9: no Iceberg comment surfaces, so Iceberg comments are a follow-up.
2. **Agents** (`handler_introspect.go:70-77`):
   - `describe_table` adds `comment` per column and a table `comment`, each with `source: catalog`.
   - When an entity's `table` resolves to the described table, it also adds `semantics: {entity, grain, id_column, columns: [{name, description, pii}], metrics: [names], source: semantics}`. Resolution uses the entity's `connection` (or the default) plus its one-to-three-part `table`, compared after quoting rules.
   - Drop the always-false `pk` field, or fill it from `duckdb_constraints()`. It is misleading as it stands (#221).
3. **`introspect_schema`** carries only a `has_semantics: true` flag per table, to stay inside the result budget.
4. **Follow-up (separate PR):** `slaw semantics import-comments` files entity column-note **proposals** from catalog comments.

**Files:**
- `internal/session/{types,introspect}.go`;
- `internal/agents/handler_introspect.go`, `tools.go` (descriptions);
- `internal/workspace/agentaccess.go` (an entity-by-table lookup on `semantics.ReadStore`);
- `docs/reference/agent-policy.md`, `semantics.md`.

**Acceptance:**
- With `COMMENT ON COLUMN orders.amount IS 'cents'` and an entity on `orders`, `describe_table orders` returns both, each labelled with its source.
- A table with no entity and no comments is unchanged except for the `pk` decision.
- A comment with control characters is escaped.
- The tool-result budget holds on a 500-column table.

### N6. The semantics index in the prompt and in MCP instructions

1. **Index builder** in `internal/semantics`: metric `name: title` lines (then entity names), sorted. It is capped at 40 entries and 2 KiB, with a last line "N more: call search_definitions" (or `list_definitions` until N5 lands). It is empty when `semantics/` is empty.
2. **In-app prompt** (`internal/llm/runner.go:147-198`, the prompt body; `prompt.go:34` is the group map): add the index after the tool groups. Raise `promptCeiling` (`prompt_test.go:24`, now 11,650) by the index's cap and no more, with a test that an empty workspace adds zero bytes.
3. **MCP instructions** (`internal/hub/instructions.go:77-88`): today they name connections (capped at 12, `maxInstructionConnections`) and say "read_skill semantic-layer before working on semantics/". Add the same index. The text is from connect time, so add a "definitions may change; list_definitions is current" line.
4. **Advisory hint on `run_query`:** when the statement aggregates over a table that an entity covers and a metric on that entity exists, add a `hint` ("`net_revenue` is defined on this table; consider metric('net_revenue')"). Ride beside the existing `note` (`handler_query.go:353`); never refuse. Skip it when the statement already uses `metric()` (`metrics_used` is non-empty).

**Acceptance:**
- The index appears in both places and respects both caps.
- An empty `semantics/` adds nothing.
- The hint appears for `SELECT sum(amount) FROM orders` on the Chinook example and not for a `metric()` call.
- The before/after measurement waits for N8 (phase 3).

### N5. `search_definitions`, synonyms and glossary references

1. **Model:**
   - `synonyms: []` on entities and metrics (D6). Not on metric dimensions, which have no object of their own to put them on.
   - At most 20, each 1-80 characters, one line, no control characters.
   - The strict decoder (`internal/semantics/decode.go`, `validate.go`) and the field list in the `propose_definition` description (`tools.go:362`) are updated together; the decoder refuses unknown keys, so this is not free.
2. **Glossary refs:** in a term's section, a single line `Refs: metric/net_revenue, entity/orders`. It is parsed by `scanGlossary` (`internal/semantics/glossary.go`), unknown kinds are refused, and a missing target is a `lint` finding later.
   - Chosen over fenced YAML because a term's text may not contain a `## ` line and the glossary is meant to read as prose.
3. **Search:** `search_definitions(q, kinds?, limit?)` is an `introspect` tool, so it is available in every tier.
   - It does lexical BM25-style ranking in Go over name, title, description, synonyms, dimension names and glossary text, with field weights (name and synonyms the highest).
   - It returns `{kind, name, title, score, why}`, where `why` names the matched field.
   - It is case- and punctuation-folded, with simple plural stemming. Limit 10, maximum 50.
   - It adds a `help_search` topic entry (`internal/agents/help.go:113`, the existing "semantics" topic).
4. **Studio:** the Semantics view shows synonyms on cards, and its filter box uses the same search binding.

**Acceptance:**
- With `net_revenue` carrying the synonym `sales after refunds`, the query "sales after refunds last quarter" ranks it first.
- A collision (two metrics with the same synonym) is allowed here and reported by N10's lint.
- `propose_definition` round-trips `synonyms`.

### N3. Connection templates

1. **`slaw.yaml`:** a `connection_templates:` block with `{name, type, expect: {endpoint_type?, warehouse?, uri?, path?, catalog?}, description}`.
   - It is validated by the type's own `Validate` on a synthesised spec, with every credential field refused.
   - It never attaches and is never resolved into a `Spec`.
   - ~~It is allowed for own-scope-only types: that is its purpose.~~ There is no own-scope rule any more (direction change): templates now only make connection names portable.
   - It is decoded in `internal/workspace/config.go`.
2. **Adopt:**
   - Studio shows a banner, "This workspace expects an Iceberg catalog called `s3t`", with **Set up**, which opens the form pre-filled with the name and `expect` fields fixed and saves to the person's own scope.
   - The CLI equivalent is `slaw conn adopt <name> [--scope local|user]`.
   - Adopting a template that a defined connection already matches is a no-op.
3. **Checks:**
   - `slaw semantics check` and the compile row report a definition whose connection is an unadopted template: "`s3t` is a template in slaw.yaml: run `slaw conn adopt s3t`".
   - A defined connection whose fields differ from its template's `expect` is a warning in the rail ("differs from the workspace's template: warehouse").
4. **Bundle:** templates travel in `slaw.yaml` like the rest of it. Nothing about trust changes.

**Acceptance:** clone a workspace with a template and `semantics/` on `s3t.*`, adopt it in one step, and `check` passes. A template never attaches, even when it is hand-edited to look complete, and a template carrying a token is refused.

## Phases 3 and 4 (outline)

- **N8 `slaw eval` (phase 3, part of #242):**
  - `evals/<name>.yaml` and `internal/evals` (strict decode like watches).
  - `slaw eval run` drives the configured agent headless in `read-only` with no approver. It reuses `internal/llm`'s task mode and records `tools_called`, `metrics_used` (already returned by `run_query`), tokens and duration.
  - Results go to `.slaw-local/evals/history.jsonl` (no rows), and the exit codes follow `watch check`.
  - No agent tool reads `evals/`: add it to the agent's protected paths, and add a structural test like the watches one.
  - Pilot: 20 questions over the Chinook example, then the lake domain.
- **N2 DuckLake on Postgres (P1, can run beside phase 2):**
  - `catalog: postgres` with an option `metadata_connection: <postgres connection>` for the credential.
  - The attach is `ducklake:postgres:` using that connection's secret. S0-style spike: does `METADATA_PARAMETERS` or a named secret carry the login on 1.5.6?
  - `PostAttach` and `ducklake_vet.go` take the metadata schema. The `__ducklake_metadata_*` hiding is unchanged.
  - Read-only by default, and approval is always asked for writes.
- **#287 parameters and grain:**
  - `metric('m', by => ..., params => {start: '2026-01-01'})`, bound by `queries.Bind`. Today `expand.go:582` refuses a metric's `$param` outside `metric()`.
  - Then `time_dimension` + `grains` on a metric. This is also what watches need for "this week" windows; see the watches window question raised on 2026-11-07.
  - Then composite `id_columns`.
- **N7 ratio metrics:** `ratio: {numerator, denominator}`, expanded by the one expander, depth 1, judged by the policy. The precondition stays: a metric's own saved query may not call `metric()` (`expand.go:425`).
- **N9 usage mining:** optional `purpose`/`keywords` on the query tools, a capped `.slaw-local/intent.jsonl` until E1 receipts exist, and `slaw semantics suggest` filing proposals only.
- **N10 lint and owners:** `slaw semantics lint` over files plus `.slaw-local/catalog.json` (the LSP snapshot), running no query; optional `semantics/OWNERS`.
- **E6 as-of (#246):** once L4 exists, `AT (VERSION => ...)` on lake-reader tables for the person and the agent. Snapshot functions go on the lake reader's allow-list only.
- **Glue (`endpoint_type: glue`):** dropped from phase 1 (D4). It reuses `s3_tables`' SigV4 signing; add it with an env-gated real-account test when someone needs it.

## Decisions (2026-11-07)

**D1, D2 and D3 are superseded by the direction change at the top of this plan.** D4–D7 stand.

Settled by Zac. Where a decision differs from the plan's recommendation, the recommendation is noted.

1. **D1. How the agent reads Iceberg: confined by default, with a per-catalog trust toggle.** Every `iceberg_catalog` is read by the agent on the confined lake reader (L4) unless the person sets `trust_writers: true` on it. A trusted catalog is read on the main hub and can be joined with other data, with the residual risk documented in L4. (Recommended: the lake reader only, with the toggle later.)
2. **D2. Vended credentials: on.** `ACCESS_DELEGATION_MODE 'vended_credentials'`, set explicitly. This supports REST catalogs that only vend (Polaris) and S3 Tables without a data-bucket storage connection. S0 item 8 must show that vended secrets are scoped to table locations and do not outrank the deny secrets elsewhere; if that fails, revisit before L2 merges. (Recommended: off.) *Review note:* the deny secrets' scope is the bare scheme, so a vended scope of `s3://` is expected to outrank them; the L4 note keeps D2 by vending on the lake reader always and on the main hub only for a trusted catalog. **Settled after S0 (Zac, 2026-11-07): vended credentials stay on for trusted catalogs too.** S0 item 8 measured that a trusted catalog's table location sets the vended scope. That lets the catalog's writers turn off the uncovered-bucket protection and redirect the person's own reads of a covered bucket to the catalog's endpoint, and the secrets outlive `DETACH`. Zac accepted this as part of what "trust" means. The `trust_writers` checkbox text and `connections.md` must say it in plain words: "a trusted catalog's operator and writers can change where your own S3 reads go, and the answers they get". An untrusted catalog still attaches with `'none'` on the main hub (L4 note), and vends only on its own lake-reader instance.
3. **D3. Person checks follow the catalog's trust.** `semantics check` and `watch check` over an untrusted catalog run on the lake reader, and over a trusted one on the main hub. The Query Explorer always uses the main hub. *Review note:* the Explorer's main-hub attach of an untrusted catalog is what carries its vended secrets into the main hub (L4 note); the plan attaches it there with delegation `'none'` (settled with D2).
4. **D4. Endpoint kinds: `s3_tables` and `rest`.** Glue is dropped from phase 1 and refused by `Validate` as not supported yet.
5. **D5. One `iceberg_catalog` type with `endpoint_type`.** As recommended.
6. **D6. Synonyms on entities and metrics only (N5).** As recommended.
7. **D7. No release after L3.** Phase 1 ships once both halves are ready, people's reads and the agent's, after L6. (Recommended: 0.8.0 after L3.)

## Corrections made to the drafts (2026-11-07)

Checked against the code while writing this plan; both drafts are edited to match.

- **`describe_table`** returns `{name, type, not_null, pk}` per column, with `pk` always false (`handler_introspect.go:70-78`). It does not return position; `position` is only in `session.ColumnInfo` (`types.go:144-149`). (N4, G4.)
- **MCP clients do get a semantics hint.** The MCP instructions say to `read_skill semantic-layer` before working on `semantics/` (`internal/hub/instructions.go:85`). What is missing is any content from `semantics/`. The in-app prompt mentions `propose_definition` (`runner.go:176`) and names the Meaning tools (`prompt.go:34`). (N6.)
- **N7's premise:** any saved query may call `metric()`. What is refused is a **metric's own** saved query calling `metric()` (`expand.go:425`).
- **N1's credential:** no new credential kind is needed (`secrets.Credential` has `Token`, `KeyID`, `Secret`). The AWS kinds should sign with the storage connection rather than hold credentials of their own. DuckDB's default is to **accept** vended credentials; D2 keeps them on, and the mode is written out explicitly on every attach so an upstream default change cannot move it (`'vended_credentials'`, or `'none'` for an untrusted catalog on the main hub, L4 note). *(Superseded, direction change 2026-11-07: vended credentials are off, and every attach writes `'none'`.)*
- **Review corrections (2026-11-07):** `sql_catalog.go` is the catalog snapshot, not the reference classifier (that is `sql_classify.go` + `sql_policy_select.go`); the plan JSON carries no database name for a scan; `hardening.go` never sets `enable_external_access` (it is on by default); the deny secrets' scopes are bare schemes; `handler_query.go`'s empty-result note is at line 353; the MCP instructions text is `instructions.go:77-88`.
- **#242's title** is "Epic E2: VibeBench"; the draft comment called it SlawBench.
- **`list_definitions`' 500** is the store's per-kind file cap (`semantics.MaxFilesPerKind`), not a tool parameter.
