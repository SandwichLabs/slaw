# Example workspaces

Three small Slaw workspaces that show the [semantic layer](../reference/semantics.md) on public data. Each one is a folder you can open in Studio (**Open folder**) or work through in a terminal; each README is a walkthrough with the real output of every command.

| Workspace | Data | Shows |
|---|---|---|
| [`penguins/`](penguins/README.md) | Palmer Penguins, 344 rows, CC0 | The beginner example: one table, one entity, three metrics, a glossary, an assertion that really fails (exit status 1), a saved query with a parameter |
| [`chinook/`](chinook/README.md) | Chinook music store, six tables, MIT | The relational example: entities, five joins with declared cardinalities profiled into `measured:` blocks (one fan-out; two with right orphans), a metric with `settled_by`, a notebook, the proposal flow |
| [`gapminder/`](gapminder/README.md) | Gapminder excerpt, 1,704 country-years, CC BY 4.0 | Weighted averages: population-weighted life expectancy, total GDP, a join whose right-side orphans are normal |

## Run one

The data is committed as CSV under each `data/` folder. An entity's `table` is a SQL name, not a file path, so the first step in every workspace loads the CSVs into the workspace's local DuckDB (`data/local.duckdb`, git-ignored). (A query can also read a file in `data/` without the import, by a path relative to the workspace, such as `read_csv('data/penguins.csv')`; the entities and metrics here name tables, so they need the import.)

```
cd docs/examples/penguins
slaw import data/penguins.csv --key auto
slaw semantics list
slaw semantics check
slaw query "SELECT * FROM metric('penguin_count', by => 'species')"
```

The README of each workspace has its own import commands and the rest of the tour.

## Work in a copy

Profiling rewrites `measured.at` (and the counts) in the committed join files, and an import writes generated skills, so work in a copy:

```
cp -r docs/examples/chinook ~/slaw-chinook && cd ~/slaw-chinook
```

or run `git checkout -- docs/examples` afterwards to put the committed files back.

## What each workspace contains

```
slaw.yaml           from `slaw workspace init`
.gitignore           keeps .slaw-local/ and data/*.duckdb out of git
data/*.csv           the committed data
queries/*.sql        saved queries, with a `-- slaw:` header (some declare parameters)
semantics/
  entities/*.yaml
  metrics/*.yaml
  joins/*.yaml
  glossary.md
notebooks/*.slnb     cells that call metric()
README.md            the walkthrough
```

## Keeping them honest

`internal/docsref` walks `docs/examples/*/` in `go test ./internal/docsref/`. It decodes and validates every semantics file with the real store, parses every saved query header, loads every notebook and checks that each metric's saved query exists. It opens no database and reaches no network, so it cannot check the output shown in a README; those were produced by running the commands.

## Licences

The code and definitions are the project's. The data keeps its own licence: Palmer Penguins is CC0, Chinook is MIT (Luis Rocha), and Gapminder is CC BY 4.0 (credit Gapminder). Each README says what was changed from the original.
