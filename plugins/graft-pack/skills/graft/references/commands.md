# graft — full flag surface

Verified against `@nanonets/graft` **0.16.0**. Flags move between minor versions;
check `graft <cmd> --help` before relying on one that is not listed here.

Global options come *before* the subcommand: `--dir <path>` (index location,
default `<repo>/graft`), `--provider`, `--model`, `--api-key`, `--base-url`.
Only `--deep` builds need a provider or key; nothing else in this file does.

Every read command takes a trailing optional `[dir]` argument — the repository
root, defaulting to the nearest ancestor holding a `graft/` index — and
`--no-refresh`, which answers from the graph as-is instead of checking freshness
first. The hooks in this pack pass `--no-refresh` so a hook can never trigger a
rebuild on the interactive path.

## ask — ranked retrieval

```
graft ask <query> [dir] [-n <limit>] [--source] [--full] [--in <path>] [--json]
```

- `-n, --limit <n>` — max results (default 8).
- `--source` — inline the code at each hit; the pack becomes the answer.
- `--full` — with `--source`, whole definition spans instead of ≤8-line crux excerpts.
- `--in <path>` — restrict to nodes under a path prefix, filtered *before* scoring.
- `--no-graph-rank` — lexical ranking only, no connectivity re-rank (ablation).

`--json` returns `{query, mode, hits[], coverage, coverageStrong}`. Each hit is
`{kind, title, pointer, snippet, score}`, where `pointer` is `path:LSTART-LEND`.
`coverage` and `coverageStrong` are the two numbers `graft-prompt-context.sh`
gates on — see `graph-shape.md`.

## callers — who references a symbol

```
graft callers <symbol> [dir] [--direction in|out] [-d <n|all>] [--in <path>] [--json]
```

- `<symbol>` — bare (`createOrder`), qualified (`Class.method`), or package-qualified.
- `--direction in` (default) — callers. `out` — callees.
- `-d, --depth <n|all>` — transitive hops; `all` walks the full connected closure.

There is **no `--file` flag**: `callers` is symbol-scoped. A per-*file* blast
radius comes from the wiring graph directly, which is what `graft-blast.sh` does.

## grep — exhaustive search over indexed files

```
graft grep <pattern> [dir] [-i] [--fixed] [--in <path>] [--json]
```

Hits are grouped by enclosing symbol and ranked by coupling, which is the whole
difference from plain ripgrep. `--fixed` treats the pattern as a literal.

## skeleton — one file's API surface

```
graft skeleton <file> [dir] [--json]
```

`<file>` is a repo-relative path or a unique basename.

## map — repo orientation

```
graft map [dir] [--max-dirs <n>] [--json]
```

Directory clusters, per-directory hubs, global hotspots. `--max-dirs` defaults
to 16; the remainder is reported as a `dropped` count rather than silently cut.

## build / check — index lifecycle

```
graft build [dir] [--deep] [-e <exts...>] [-j <n>] [--no-reuse] [--lsp]
graft check [dir]
```

A plain `build` is free and local: wiring graph plus per-file cards, replaying
unchanged files from the extraction cache. `--deep` adds the LLM pass (concept
map, per-symbol summaries) and needs a provider key. `check` exits non-zero when
the index is stale relative to the code — the CI form of the same question.

`graft build` adds its own output directory to `.gitignore` automatically: the
index is a local cache, and each machine builds its own.

## blast — diff-scoped radius, for CI

```
graft blast [dir] [--base <ref>] [-d <n|all>] [--format text|markdown|mermaid|json]
```

Blast radius of a *diff* (working tree vs HEAD, or vs `--base`'s merge base).
`--format markdown` is a PR comment with a Mermaid diagram. Deliberately not
what the PostToolUse hook uses: it is diff-scoped and runs a freshness check,
so it is a CI tool, not an interactive one.

## Commands this pack deliberately does not use

- `graft mcp` — serves the graph over MCP. A second, overlapping route to the
  same data; the pack stays on the CLI so the hooks and the skill agree.
- `graft stats` — reports a session's graft-vs-source mix and "tokens saved".
  Not surfaced anywhere in this pack on purpose (see the skill's rule 6).
- `graft viz`, `graft init` — a local server and a per-project hook writer. The
  marketplace pack *is* the hook wiring, so `graft init` would duplicate it.
