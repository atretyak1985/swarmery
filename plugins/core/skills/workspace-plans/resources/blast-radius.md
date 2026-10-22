# Blast radius — who calls what a phase changes

Before a phase runs, its doc says who calls the code it changes, so the
executor knows what it can break and the reviewer knows which call sites to
read. The planner computes it while writing each phase doc, right before that
doc's `## Risks` section, and records one `Blast radius:` line there. This is
read-only: it uses the indexes the project already maintains and never builds
one.

## Which symbols

Start from the phase's `## Forecast` `files` list.

- A file the phase **creates** has no callers yet — count it as `new`.
- For a file the phase **modifies**, take the top-level symbols the phase
  names as changing (Files to Modify, Implementation Details). When it names
  only the file, take every top-level declaration the file defines.
- A **caller** is a reference site (`file:line`) outside the symbol's own
  declaration and outside the phase's own Forecast `files` — references inside
  those files are part of the change itself. Test files count: a test that
  calls the symbol breaks with it.

## The chain

`/impact` (`commands/impact.md`) is a slash command; a headless planner cannot
type it, so run its shell equivalents from inside the repo the phase targets.
Take the first graph the repo actually has, then always finish with the sweep:

1. **Graft** — when `graft` is on PATH and `<repo>/graft/.graph/wiring.json`
   exists: `graft callers "<sym>" --depth 2`.
2. **Graphify** — otherwise, when `<repo>/graphify-out/graph.json` exists:
   `graphify query "what depends on <sym>?"`.
3. **ripgrep sweep** — always:
   `rg -n --no-heading -w "<sym>" --type go --type ts <repo>`. For a method or
   a generic name (`review`, `Run`) search the call form instead —
   `rg -n --no-heading "\.<sym>\(|\b<sym>\(" --type go --type ts <repo>` — and
   drop the declaration line and comment-only lines.
4. **Serena** — when `lsp-pack@<marketplace>` is `true` in the project's
   `.claude/settings.json` `enabledPlugins` and the session has the Serena MCP
   tools: `find_referencing_symbols` with `name_path` = the symbol and
   `relative_path` = its defining file. Serena resolves references through the
   language server, so its set is the reference; reconcile the sweep against
   it and name any delta. Without lsp-pack or without the tools, skip it and
   say so in the sources.

Never run `graft build`, `graphify update` or `graphify .` for this step. An
index the operator does not maintain is skipped, not built; one that
`graft check` reports stale is used as a lower bound and said to be stale.

## Counting

- The **count** is exact for ripgrep and Serena, and a lower bound (`≥N`) when
  it comes only from a graph tool.
- The **display cap is 20**: list up to 20 callers as `file:line`; above that,
  give the count and the packages only.
- Union all symbols into one line per phase; a per-symbol breakdown may follow
  as nested bullets.

## The line

Under `## Risks` (after `## Acceptance Criteria`, before `## Notes`; create the
section when the doc has none), as its first bullet:

```markdown
## Risks

- Blast radius: <N> callers in <pkgs> — <file:line, …> (sources: <graft|graphify|rg|serena>, …)
```

| Case | Line |
|---|---|
| ≤ 20 callers | `Blast radius: 5 callers in internal/orders, internal/api — internal/orders/service.go:412, internal/api/line_items.go:88, … (sources: rg, serena)` |
| > 20 callers | `Blast radius: 34 callers in internal/api, internal/orders — over 20, not listed (sources: rg)` |
| graph only | `Blast radius: ≥12 callers in … — … (sources: graphify, stale)` |
| nothing to call | `Blast radius: 0 callers — new files only (sources: rg)` |
| chain unavailable | `Blast radius: not computed — <reason>` |

`<pkgs>` are the callers' directories relative to the repo root (Go packages,
TS feature folders), comma-separated. When Serena and the sweep disagree, add
a nested `- delta: <what one saw and the other did not>` bullet.

## Who reads it

- **The executor** treats every listed caller as in scope for its tests.
- **The reviewer.** The daemon's reviewer brief repeats the line under
  `BLAST RADIUS:`; the reviewer reads those call sites against the diff and
  lists each one it read in its `kind: review` block's `checked_callers`
  (`file:line`).
- **The dashboard** lints `blast_radius_missing` on a phase whose Forecast
  lists `files` but whose doc carries no `Blast radius:` line in its own text
  (a line quoted inside a fenced block does not count). Shown on the phase
  beside the forecast lints, never enforced.
