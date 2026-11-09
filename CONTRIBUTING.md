# Contributing to Slaw

Thanks for helping. Slaw is a desktop app to analyze your data in a secure private workspace. Read [`docs/direction.md`](docs/direction.md) (the north star) and [`docs/README.md`](docs/README.md) (index of every doc) before a larger change. [`CLAUDE.md`](CLAUDE.md) is a compact map of the code and conventions and is useful for humans too.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems go through [SECURITY.md](SECURITY.md), not a public issue.

## Build from source

Prerequisites:

- Go at the version in `go.mod` (1.27.x) or newer, with `CGO_ENABLED=1` and a C/C++ toolchain (DuckDB is embedded through cgo; no system DuckDB is needed).
- Node 24 (see `.nvmrc`) and npm, for the Studio frontend.
- [Task](https://taskfile.dev) for the automation in `Taskfile.yml` (there is no Makefile).
- For Studio (the desktop app): the [Wails CLI](https://wails.io) v2.17.1 (the version of `github.com/wailsapp/wails/v2` in `go.mod`).
- `jq`, which `task build:studio*` uses to stamp the version into `wails.json` for the length of the build.
  - Linux: `build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`.
  - macOS: Xcode Command Line Tools.

```bash
task build           # build the slaw CLI binary
task test            # Go tests plus the Studio Vitest suite (task test:frontend is Vitest only)
task lint            # golangci-lint (task lint:fix auto-fixes)
task studio:dev      # Studio dev server with hot reload
task build:studio    # Studio desktop app (build:studio:linux on Linux)
task                 # default: lint + test + build
```

Notes:

- Use `task studio:dev`, not bare `wails dev`. It sets the build tags `webkit2_41,duckdb_arrow`.
- Studio embeds `studio/frontend/dist`. On a fresh clone run `cd studio/frontend && npm ci && npm run build` once before `go build ./...` or `go vet ./...`.
- One package: `go test -v ./internal/workspace/`.

## Before you open a pull request

- `task lint` and `task test` pass. If you touched the frontend, `npm run build` and `npm test` in `studio/frontend` pass too.
- Tests accompany behavior changes. Tests use testify, sit next to the source, and use DuckDB, a `t.TempDir()` file store, or the package mocks.
- Docs move with code. Keep `docs/reference/` and the architecture tree in `CLAUDE.md` current; `internal/docsref` fails when they drift.
- UI changes: describe how you checked them in a real browser. [`docs/contributing/rodney.md`](docs/contributing/rodney.md) explains the Rodney QA workflow against the Wails dev server. Include screenshots in the PR.

### Conventions that reviewers enforce

- Code lives in `internal/` packages. Wrap errors with context (`fmt.Errorf("...: %w", err)`), pass `log/slog` loggers by injection, and avoid globals.
- One tool surface: new agent capabilities are `ToolDef`s in `internal/agents`, never bespoke tools in `internal/llm`.
- Trust rules: never put a credential in a config file, bundle, log line, or an error shown to the UI or agent. The agent SQL policy can only get stricter.
- Stylesheets use `--slaw-*` roles, never hex colors. Keep the `:focus-visible` ring; do not write `outline: none` without it.
- Never build SQL from user input without the quoting helpers.

## Commit messages

Commits follow conventional-commit style with the issue number in the scope:

```
feat(#164): open the launch workspace from slaw studio --gui
fix(#163): MotherDuck attach, database picker, token hints
docs(#54): architecture review final
refactor(#54): slice 5 - shared connection state
```

Types in use: `feat`, `fix`, `docs`, `refactor`, `chore`, `ci`, `test`. Keep the subject short and say why in the body when it is not obvious. Pull requests are squash-merged and the PR number is appended.

## Sign your work (DCO)

Slaw uses the [Developer Certificate of Origin](https://developercertificate.org/) instead of a CLA. There is no agreement to sign and you keep your copyright. By adding a sign-off you certify that you wrote the change or have the right to submit it under the project license (Apache-2.0).

Sign every commit with `-s`:

```bash
git commit -s -m "fix(#123): describe the change"
```

This appends `Signed-off-by: Your Name <you@example.com>`, which must match the commit author. Forgot? Fix the last commit with `git commit --amend -s --no-edit`, or a whole branch with `git rebase --signoff main` and a force-push. Bot-authored commits (for example Dependabot) are exempt. A CI check (`DCO`) fails a pull request when any non-merge commit lacks a matching sign-off.

## Pull requests

- Open or find an issue first for anything beyond a small fix, and link it in the PR.
- Keep a PR to one concern. Describe what changed and why, and how you tested it.
- Expect review on security invariants, tests, and docs before style.

## Dependency updates

Dependabot opens one grouped PR per ecosystem each week for minor and patch bumps; major versions are upgraded by hand in their own tested PR. A Dependabot PR cannot fix up generated files itself, so before merging one, check out its branch, run `task deps:refresh` (it regenerates `THIRD_PARTY_NOTICES.md`, which CI checks, and `studio/frontend/package.json.md5`, which `wails build` otherwise rewrites), and push the result as a signed-off commit.

## License

Contributions are licensed under the [Apache License 2.0](LICENSE).
