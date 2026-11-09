# Slaw reference

This is the shipped reference documentation for the Slaw application surface —
the SandwichLabs analysis workspace: a folder of plain files that points at your data, which stays in its connections. It is split by
surface so each area is easy to find:

| Reference | Covers |
|---|---|
| [`notebooks.md`](notebooks.md) | **Notebooks** (sql/markdown/a2ui cells) and the **A2UI** component reference |
| [`artifacts.md`](artifacts.md) | **Data artifacts**: `artifacts/<name>/`, the manifest, parameter types, date defaults, view rules, controls, `pivot` and drill-down |
| [`semantics.md`](semantics.md) | **The semantic layer**: `semantics/` entities, metrics, joins and the glossary, their formats, caps and name rule, `slaw semantics`, the proposals inbox |
| [`watches.md`](watches.md) | **Watches**: `watches/` standing questions (a saved query or metric and a condition), checked on demand with `slaw watch`, outcomes and the local history |
| [`receipts.md`](receipts.md) | **Run receipts**: how long they are kept (your own setting), Keep parameter values, usage, Delete all, `slaw receipts settings\|clear` |
| [`workspaces.md`](workspaces.md) | **Workspaces**: the folder layout, `slaw.yaml`, connection scopes and precedence, the agent limit, file formats, conflicts |
| [`import-office.md`](import-office.md) | **Import** (CSV/TSV/XLSX) into a connection's table and table/query **export** (CSV, Parquet, XLSX, JSON) |
| [`templates.md`](templates.md) | Bundled starter **templates** |
| [`themes.md`](themes.md) | Studio **themes**: built-ins, the theme file format, colour roles, where preferences live |
| [`connections.md`](connections.md) | **Connections**: every type (DuckDB file, Postgres, MySQL, MotherDuck, SQLite, S3/GCS/R2, Azure, DuckLake, Iceberg, Delta), fields, credentials, read-only, Test, extensions |
| [`console.md`](console.md) | **The Data Console**: the terminal tab in the Query Explorer, keys, dot commands, history rules, limits |
| [`mcp.md`](mcp.md) | **MCP**: `slaw mcp serve` (stdio/SSE), client configuration, SSE auth and security |
| [`editor.md`](editor.md) | **Your own editor**: `$VISUAL`/`$EDITOR` from the TUI and `slaw query -e`, and `slaw lsp` (completion and hover) for Neovim, Helix and eglot |
| [`llm-config.md`](llm-config.md) | **LLM provider** configuration and the API key |
| [`agent-policy.md`](agent-policy.md) | **The agent's SQL policy**: what each tier allows, the three-gate rule for writes, file and URL rules, the never-list, `list_connections` |

A test in `internal/docsref/` keeps these docs honest: every A2UI component
type registered in `internal/a2ui`, every starter template in
`internal/templates` and every `slaw.yaml` / connections.yaml key in the
workspace schema must appear here, as must every assertion kind, join cardinality and metric format of the semantic layer.

> **Direction note.** Per [`../direction.md`](../direction.md), the page
> system (`pages.md`, page directives, the web preview server, the Studio page
> editor/runtime views) was retired in vibe-app#69 — Notebooks (vibe-app#66) and A2UI artifacts
> (vibe-app#67) are the only authoring surface now. Older data-holding `.vibe` files (and their pages) are not opened or
> imported at all (vibe-app#110 was dropped), so there is nothing left to document about pages. Forms, banded reports, macros, the switchboard, and App Mode —
> the Access application layer — were removed in vibe-app#68. Do not reintroduce either
> surface.
