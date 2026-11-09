#!/usr/bin/env bash
# Fails when the package import graph breaks the layering in CLAUDE.md.
#   1. Nothing under internal/ imports cmd or studio (production, test and external-test imports).
#   2. cmd/ never imports studio (cmd launches the desktop app by exec).
#   3. Domain packages do not import the agent / MCP / UI layer in production code
#      (agents, llm, mcp, hub, tui). Their tests may: an integration test drives the stack.
#   4. agents imports neither llm nor mcp; mcp does not import hub.
#   5. No import cycles (go list reports one as an error).
# Usage: scripts/check-layering.sh   (run from anywhere)
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
mod="$(go list -m)"
fail=0
report() { echo "layering: $1" >&2; fail=1; }

# Layer of an internal package (a sub-package shares its parent's layer). Every
# internal package must be classified here: a new one fails until it is added.
layer_of() {
  case "$1" in
    internal/agents|internal/agents/*) echo agents;;
    internal/llm|internal/llm/*) echo llm;;
    internal/mcp|internal/mcp/*) echo mcp;;
    internal/hub|internal/hub/*) echo hub;;
    internal/tui|internal/tui/*) echo tui;;
    internal/docsref|internal/docsref/*) echo docs;;
    internal/workspace|internal/workspace/*|internal/connections|internal/connections/*|internal/connname|internal/connname/*|\
    internal/sandbox|internal/sandbox/*|internal/session|internal/session/*|internal/engine|internal/engine/*|internal/crud|internal/crud/*|\
    internal/importer|internal/importer/*|internal/notebooks|internal/notebooks/*|internal/queries|internal/queries/*|\
    internal/bundle|internal/bundle/*|internal/skills|internal/skills/*|internal/artifacts|internal/artifacts/*|internal/filestore|internal/filestore/*|\
    internal/awssso|internal/awssso/*|internal/filelock|internal/filelock/*|internal/storageurl|internal/storageurl/*|internal/storageglob|internal/storageglob/*|internal/sqllex|internal/sqllex/*|internal/strictyaml|internal/strictyaml/*|internal/semantics|internal/semantics/*|internal/watches|internal/watches/*|internal/receipts|internal/receipts/*|internal/jsonenc|internal/jsonenc/*|\
    internal/dotenv|internal/dotenv/*|internal/trustfile|internal/trustfile/*|internal/usersql|internal/usersql/*|internal/config|internal/config/*|\
    internal/prefs|internal/prefs/*|internal/selfupdate|internal/selfupdate/*|internal/buildinfo|internal/buildinfo/*|internal/templates|internal/templates/*|internal/secrets|internal/secrets/*|\
    internal/editor|internal/editor/*|internal/repl|internal/repl/*|internal/lsp|internal/lsp/*|internal/a2ui|internal/a2ui/*|internal/pagehost|internal/pagehost/*|internal/pagestate|internal/pagestate/*|internal/pagetheme|internal/pagetheme/*|internal/testsupport|internal/testsupport/*) echo domain;;
    *) echo unclassified;;
  esac
}

check_graph() {
  local graph="$1" pkg prod test xtest err short imp layer
  while IFS='|' read -r pkg prod test xtest err; do
    if [ -n "$err" ]; then report "$pkg: $err"; continue; fi
    short="${pkg#"$mod"/}"
    all="$prod $test $xtest"
    case "$short" in
      internal/*)
        for imp in $all; do
          case "$imp" in
            "$mod/cmd"|"$mod/cmd/"*|"$mod/studio"|"$mod/studio/"*) report "$short imports $imp (internal must not import an adapter)";;
          esac
        done
        layer="$(layer_of "$short")"
        case "$layer" in
          unclassified) report "unclassified package $short: add it to layer_of in scripts/check-layering.sh";;
          domain)
            for imp in $prod; do
              case "$imp" in
                "$mod/internal/agents"|"$mod/internal/agents/"*|"$mod/internal/llm"|"$mod/internal/llm/"*|"$mod/internal/mcp"|"$mod/internal/mcp/"*|"$mod/internal/hub"|"$mod/internal/hub/"*|"$mod/internal/tui"|"$mod/internal/tui/"*)
                  report "$short imports $imp (domain packages stay below the agent/MCP/UI layer)";;
              esac
            done;;
          agents)
            for imp in $prod; do
              case "$imp" in "$mod/internal/llm"|"$mod/internal/llm/"*|"$mod/internal/mcp"|"$mod/internal/mcp/"*) report "$short imports $imp";; esac
            done;;
          mcp)
            for imp in $prod; do
              case "$imp" in "$mod/internal/hub"|"$mod/internal/hub/"*) report "$short imports $imp";; esac
            done;;
        esac
        ;;
      cmd|cmd/*)
        for imp in $all; do
          case "$imp" in "$mod/studio"|"$mod/studio/"*) report "$short imports $imp";; esac
        done
        ;;
    esac
  done <<< "$graph"
}

# "pkg|prod imports|test imports|xtest imports|error"; the error is printed with %q so a multi-line cycle stays on one line.
fmt='{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}|{{if .Error}}{{printf "%q" .Error.Err}} {{printf "%v" .Error.ImportStack}}{{end}}'
# Default build, then with the integration tag so tagged files are seen too.
for tags in "" "integration"; do
  graph="$(go list -e -tags "$tags" -f "$fmt" ./...)"
  check_graph "$graph"
done

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "layering: ok"
