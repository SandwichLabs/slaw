# Slaw documentation

New here? Start with [Install](install.md) and the [Quickstart](quickstart.md). To understand why Slaw is shaped the way it is, read [`direction.md`](direction.md), then the reference for the surface you are touching. Architecture and conventions for contributors are in [`../CLAUDE.md`](../CLAUDE.md).

## Using Slaw

Task-oriented guides for people who use Slaw. They are written to be linkable by heading.

| Doc | What it is |
|---|---|
| [`install.md`](install.md) | The install script and its options, supported platforms, macOS and Gatekeeper, upgrade, uninstall |
| [`migrating-from-vibe.md`](migrating-from-vibe.md) | Moving from Vibe (v0.5.0 and older) to Slaw: uninstall, install, rename a workspace's files, keep your settings |
| [`quickstart.md`](quickstart.md) | Install to a first query, a notebook and Claude Code over MCP, with no accounts |
| [`connections.md`](connections.md) | Recipes for every connection type and sign-in method (the field-level reference is [`reference/connections.md`](reference/connections.md)) |
| [`mcp-clients.md`](mcp-clients.md) | Claude Code, Claude Desktop and other MCP clients; sandbox tiers and approvals in plain words |
| [`troubleshooting.md`](troubleshooting.md) | Symptom-to-fix list, where the logs are, how to report a bug |
| [`privacy.md`](privacy.md) | What leaves your machine (nothing goes to Sandwich Labs) |
| [`architecture.md`](architecture.md) | An outsider-level overview of how the pieces fit |

> **The "using Slaw" agent skill derives from these pages.** [SandwichLabs/agent-marketplace#1](https://github.com/SandwichLabs/agent-marketplace/issues/1) builds a skill for agents and support from `install.md`, `quickstart.md`, `connections.md`, `mcp-clients.md`, `troubleshooting.md` and `reference/`. These docs are the single source: change them here, keep headings stable (other docs and the skill link to them), and the skill follows.

## Product

| Doc | What it is |
|---|---|
| [`direction.md`](direction.md) | The north star: workspaces, connections, the eight primitives, and (§9) what changed from the plan |
| [`plans/answers-you-can-check.md`](plans/answers-you-can-check.md) | **Accepted direction, not built** (umbrella #250, epics #241-#249): every number comes with its evidence. Run receipts and the evidence drawer, SlawBench, the verifier, a semantic layer as files, `slaw check`, as-of on lake formats, standing questions, a compute decision spike and an aggregate-only agent tier; reviewed by an independent judge, whose changes are recorded in the plan |
| [`plans/run-receipts.md`](plans/run-receipts.md) | **Accepted, not built** (2026-11-09): the build plan for E1, run receipts (#241). What a receipt holds and never holds, where it is minted (`engine.Manager.run`), the per-person settings (keep parameter values, retention), slices R0-R5 and the owner's decisions |
| [`plans/semantic-layer-lakehouse.md`](plans/semantic-layer-lakehouse.md) | **Proposal, not built** (2026-11-07): the semantic layer over S3 Tables, Iceberg catalogs and DuckLake. What a semantic layer is, what Slaw has, gaps G1-G10 and phases; issue drafts in [`plans/semantic-layer-issues.md`](plans/semantic-layer-issues.md); implementation plan (phase 1 in detail: `iceberg_catalog`, read by people and the agent on the main hub with warnings (direction change 2026-11-07), then context for the agent) in [`plans/semantic-layer-lakehouse-impl.md`](plans/semantic-layer-lakehouse-impl.md) |
| [`plans/data-console.md`](plans/data-console.md) | **Part A built, Part B in progress** (autocomplete #256 built; console epic #255, R1 engine #258 built): SQL autocomplete in the query writer and notebook cells (the Go `internal/lsp` completer behind a binding), and a DuckDB-CLI-style data console in Studio drawn with a terminal widget (xterm.js, chosen after the R0 spike), with a script-plus-console Query Explorer; feasibility, phases and owner decisions |
| [`plans/workspaces-and-connections.md`](plans/workspaces-and-connections.md) | The implementation plan for epic vibe-app#105 (delivered; "as built" summary at the top) |
| [`plans/bring-your-own-agent.md`](plans/bring-your-own-agent.md) | Accepted plan for epic vibe-app#157: OpenAI, Ollama and Cloudflare providers (done), your own Claude Code / Codex / Gemini CLI / OpenCode over MCP, then in Studio's panel over ACP, and the security posture of that mode; only phase A and the spike are built |
| [`plans/ducklake-catalogs.md`](plans/ducklake-catalogs.md) | Plan for #118 (DuckLake catalogs in SQLite and in object storage, with a storage dependency); delivered in PRs #119-#125, released in v0.1.3 |
| [`plans/storage-setup-and-sso.md`](plans/storage-setup-and-sso.md) | Plan for #130 (set up the storage connection from the lake form) and #131 (AWS SSO sign-in for S3); delivered in nine steps (PRs #132-#147, released in v0.1.4 with SSO as a preview); real IAM Identity Center verification and the close-out list are in the plan |
| [`plans/data-artifacts.md`](plans/data-artifacts.md) | Plan for epic #162 (data artifacts; level 1 delivered, level 2 not built): parameterised, live views stored as `artifacts/<name>/` files, with named queries bound by value only, agent and MCP tools, Studio controls, pivot and drill-down, and a conditional custom-view level gated on an isolation spike |
| [`plans/library.md`](plans/library.md) | **Built (L1-L5)**: one **Library** rail entry for notebooks, live views (A2UI artifacts), pages (HTML) and chart scripts, with a type picker for New, an agent hand-off and Home counts; UI only, storage, bundles, MCP and the CLI unchanged; `create_dashboard` keeps its name and points to `write_artifact` |
| [`plans/html-artifacts.md`](plans/html-artifacts.md) | **Built (P1-P6)**, extending #162: HTML artifacts, Claude-style pages shared in a workspace. A Go `html/template` page bound to the artifact's named queries. Static pages render in Studio in a script-less frame; scripted pages open in a page window Slaw owns, with declared capabilities (`state`, `download`, `ask`). The allow prompt, the agent tool, the skill, bundle entries and `slaw artifact view` are built; each step's *As built* records the departures. macOS scripted pages carry an accepted leak (#189) and had an owner run on macOS 26.6.2 (2026-10-22; macOS 14/13 not yet run) |

| [`plans/rename-to-slaw.md`](plans/rename-to-slaw.md) | **Accepted** (2026-11-01; the rename is #316, the copy pass and v0.6.0 follow): rename Assay (never shipped) to Slaw, the SandwichLabs analysis workspace, in one pass with no compatibility to keep. The name checks, every name that changes, the cutover steps and the PR order |
| [`plans/rename-to-assay.md`](plans/rename-to-assay.md) | **Superseded by rename-to-slaw** (2026-11-01): the Vibe to Assay pass (PR A, #315) and the name checks behind it, plus the user-facing copy pass (PR B) checklist and drafts; icon brief #311 |
| [`plans/open-source-launch.md`](plans/open-source-launch.md) | The open-source launch plan (epic #51): audit findings, decisions (name, licence, history, reporting, DCO) and the v0.1.0 shape |
| [`plans/spike-duckdb-hub-findings.md`](plans/spike-duckdb-hub-findings.md) | Findings from the DuckDB hub spike that fed the plan |
| [`plans/spike-editor-lsp-findings.md`](plans/spike-editor-lsp-findings.md) | Findings from the editor + language server spike (landed in #112): `$EDITOR` hand-off from the TUI and `slaw query -e`, `slaw lsp` over a catalog snapshot, Neovim/Helix setup, and what is left |
| [`plans/spike-console-r0-findings.md`](plans/spike-console-r0-findings.md) | Findings from the data console R0 spike (#257): `ghostty-web` 0.4.0 versus `@xterm/xterm` 6.0.0 in WebKitGTK and WKWebView (Chromium baseline), the results matrix, API gaps, bundle sizes, the go-libghostty experiment (no) and the decision (xterm.js, chosen 2026-10-28) |
| [`plans/code-mode.md`](plans/code-mode.md) | Proposal: a `run_code` tool where the agent writes a program that calls the Slaw tools in a sandbox, through the same gates (not built; review notes in §13 favour a Starlark or SQL result-handle spike before Monty) |
| [`plans/monty-client.md`](plans/monty-client.md) | Proposal: the Go client for the Monty worker that Slaw would own, the worker build from source, packaging, tests and the upgrade procedure (deferred by the §13 review) |
| [`plans/spike-code-mode-findings.md`](plans/spike-code-mode-findings.md) | Research for code mode: Cloudflare, Anthropic, Bifrost and Muse, and the sandboxes a Go process can use (Monty, Starlark, goja, QuickJS, WASI) |
| [`plans/spike-byo-agent-findings.md`](plans/spike-byo-agent-findings.md) | Findings from the bring-your-own-agent spike (vibe-app#149): per-agent MCP registration, `instructions`, elicitation and ACP behaviour for Claude Code, Codex, Gemini CLI and OpenCode |

## Reference

The shipped surface. [`reference/README.md`](reference/README.md) is the index; `internal/docsref` keeps it in step with the code.

| Doc | Owns |
|---|---|
| [`reference/workspaces.md`](reference/workspaces.md) | The folder layout, `slaw.yaml`, scopes, the CLI, Studio's launcher, `.slaw` bundles |
| [`reference/connections.md`](reference/connections.md) | Every connection type, fields, credentials, read-only, Test, extensions |
| [`reference/agent-policy.md`](reference/agent-policy.md) | The agent's SQL policy: tiers, the three-gate write rule, file and URL rules, the never-list |
| [`reference/console.md`](reference/console.md) | The Data Console: the Query Explorer's terminal tab, keys, dot commands, history, limits |
| [`reference/mcp.md`](reference/mcp.md) | `slaw mcp serve` (stdio/SSE), client config, SSE auth |
| [`reference/editor.md`](reference/editor.md) | `$EDITOR` from the TUI and `slaw query -e`; `slaw lsp` for Neovim, Helix and eglot |
| [`reference/llm-config.md`](reference/llm-config.md) | LLM provider configuration and the API key |
| [`reference/notebooks.md`](reference/notebooks.md) | Notebooks and the A2UI component reference |
| [`reference/semantics.md`](reference/semantics.md) | The semantic layer: `semantics/` entities, metrics, joins and the glossary; formats, caps, name rule, `slaw semantics`, proposals an agent files and you accept |
| [`reference/watches.md`](reference/watches.md) | Watches: `watches/` standing questions with a condition, `slaw watch list/show/check/history`, outcomes, the local check history |
| [`reference/receipts.md`](reference/receipts.md) | Run receipts: retention and Keep parameter values (yours, never shared), usage, Delete all, `slaw receipts settings`/`clear` |
| [`reference/artifacts.md`](reference/artifacts.md) | Data artifacts: the manifest, parameter types, view rules, controls, pivot, drill-down, running, sharing |
| [`reference/import-office.md`](reference/import-office.md) | Import (CSV/TSV/XLSX) and export |
| [`reference/templates.md`](reference/templates.md) | Starter templates |
| [`reference/themes.md`](reference/themes.md) | Studio themes |

## Examples

Runnable workspaces to open in Studio or work through on the command line. Each README is a walkthrough with real output; `internal/docsref` checks that the definitions still validate.

| Doc | What it is |
|---|---|
| [`examples/README.md`](examples/README.md) | Index of the example workspaces: the semantic layer (metrics, entities, joins, glossary, assertions, profiling, proposals, `metric()`) on small public datasets |
| [`examples/penguins/`](examples/penguins/README.md) | Palmer Penguins: one table, three metrics, an assertion that fails on real data, a saved query with a parameter |
| [`examples/chinook/`](examples/chinook/README.md) | Chinook music store: six entities, profiled joins, a metric settled by a disagreement, a notebook, the proposal flow |
| [`examples/gapminder/`](examples/gapminder/README.md) | Gapminder: population-weighted life expectancy, a join with right-side orphans |

## Design

| Doc | What it is |
|---|---|
| [`design/README.md`](design/README.md) | Where Studio's designs live now: the Slaw Design System (tokens, components, voice) and the Design canvases (the workspaces and connections screens moved over from the vibe-app#106 mockups, and the screens for #250). The original HTML mockups and spec are in [`archive/design/workspaces-connections/`](archive/design/workspaces-connections/SPEC.md). |

## Contributing

| Doc | What it is |
|---|---|
| [`contributing/rodney.md`](contributing/rodney.md) | Driving Studio in a headless browser with Rodney for QA |
| [`assets/README.md`](assets/README.md) | Where the README and release screenshots come from and how they were made |
| [`../tools/ast-grep/`](../tools/ast-grep/sgconfig.yml) | The architecture lint rules CI runs (`ast-grep scan -c tools/ast-grep/sgconfig.yml cmd internal studio`) |

## Packaging

| Doc | What it is |
|---|---|
| [`packaging.md`](packaging.md) | How v0.1.0 ships (unsigned tarballs and `install.sh`), the macOS spike findings, file association wiring, and the later signed-distribution plan (epic #4) |

## Reviews

| Doc | What it is |
|---|---|
| [`reviews/cleanup-2026-10/FINDINGS.md`](reviews/cleanup-2026-10/FINDINGS.md) | The post-vibe-app#105 cleanup survey (vibe-app#137) and its review verdicts |
| [`reviews/arch-2026-10/`](reviews/arch-2026-10/) | The architecture review for #54: rubric, findings, judge rulings. Its ast-grep rules are in `tools/ast-grep/` |

## Archive

[`archive/`](archive/README.md) holds superseded plans and research (Access parity, the original MVP spec, early system design). It is history, not guidance.
