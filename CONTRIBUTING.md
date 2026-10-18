# Contributing to swarmery

This repository is two things, and they contribute differently. **`plugins/**`** is
the marketplace — agents, skills, commands and hooks written as markdown and bash,
with no build step; a change ships when consumers run `/plugin update`.
**`tools/swarmery/`** is the control plane — a Go daemon with an embedded React SPA,
with its own module, CI and release tags, excluded from the marketplace rules below.
`CLAUDE.md` is the shortest complete description of the layout; read it first.

## Testing a plugin change before it ships

The thing nobody guesses: **your local edits are not what your session runs.**
Installed plugins are served from `~/.claude/plugins/cache`, so editing
`plugins/core/agents/tech-lead.md` changes nothing until that cache changes. Load the
working tree directly instead, for one session:

```bash
claude --plugin-dir plugins/core                              # repeatable per pack
claude --plugin-dir plugins/core --plugin-dir plugins/web-pack
```

After committing, `bash scripts/sync-cache.sh` rsyncs `plugins/**` into the installed
cache (designed to run from a `.git/hooks/post-commit` hook; safe to run by hand). It
skips packs you do not have installed.

> [!WARNING]
> Never register this checkout as a local marketplace. `marketplace.json` claims the
> name `swarmery`, and a local-path registration **replaces** the GitHub source
> globally — breaking `/plugin update` for every consumer. `--plugin-dir` is the
> supported way to run uncommitted work.

## Where a new component belongs

Components are born **project-local**, in a consumer's own `.claude/`. Promotion flows
up only: when a *second* project needs the same thing it graduates to a domain pack;
when every project needs it, to `core`. Never copy a framework file downward into a
project — that recreates the fork-and-sync rot this repo exists to remove. Promotion
is mechanical: de-flavor it → move it into the pack or `core` → bump that plugin's
semver → delete the donor's local copy ([`docs/EXTENDING.md`](docs/EXTENDING.md)). On
a name collision a consumer's local component wins — the intended override, not a
reason to fork.

A **pack** is the right home for vocabulary only some projects have (a hardware
domain, a tracker, a design tool); `core` is for what *every* consumer needs. Open a
Pack idea issue before writing code: which domain, which projects would enable it,
which agents and skills it carries, and why `core` is the wrong home. A pack that
needs project config declares it in `requirements.json`, gated in CI against
`overlays/_schema/project.schema.json`.

## Vendor neutrality

`plugins/**` must contain **zero** project-specific tokens: no company or product
names, no internal repo names, no environment aliases, no cloud regions. Per-project
flavor is read at runtime from `${CLAUDE_PROJECT_DIR}/.claude/project.json`. Domain
vocabulary is legitimate inside its own pack and forbidden in `core`; prose uses
neutral placeholders (`apps/<mainApp>`, `<device>`) or neutral example domains
(`orders/line-items`). See [`docs/NEUTRALITY.md`](docs/NEUTRALITY.md) — the ratchet is
`bash scripts/scan-flavor.sh` and it must print `✓ clean`.

## Versioning

Any change under `plugins/<name>/` bumps that plugin's `version` in
`plugins/<name>/.claude-plugin/plugin.json`; consumers adopt through `/plugin update`,
so an unbumped change is an invisible one. The marketplace's `metadata.version` tracks
the `core` version. Control-plane releases are cut separately, by pushing a
`swarmery-v*` tag.

## The gates your PR must pass

CI (`.github/workflows/ci.yml`) runs four jobs. `validate` is twelve gates, all
runnable locally from the repo root:

```bash
# 1  JSON manifests parse
for f in .claude-plugin/marketplace.json plugins/*/.claude-plugin/plugin.json \
  plugins/*/requirements.json plugins/core/hooks/hooks.json overlays/*/*.json; do
  node -e "JSON.parse(require('fs').readFileSync('$f'))" || echo "INVALID: $f"; done
bash scripts/check-plugin-requirements.sh   # 2  requirements match the schema
bash scripts/check-plugin-versions.sh       # 3  semver plugin.json, marketplace in step
# 4  shell syntax
find plugins scripts -name '*.sh' -print0 \
  | while IFS= read -r -d '' f; do bash -n "$f" || echo "SYNTAX: $f"; done
# 5  shellcheck — CI floor is error severity; run it bare for the stricter signal
find plugins scripts -name '*.sh' -print0 | xargs -0 shellcheck -S error
# 6  every suite in scripts/tests/, discovered — not listed
for t in scripts/tests/*.test.sh; do bash "$t" || echo "FAILED: $t"; done
# 7  agent frontmatter: name + description inside the first 15 lines
for f in plugins/*/agents/*.md; do head -15 "$f" | grep -q '^name:' \
  && head -15 "$f" | grep -q '^description:' || echo "PROBLEM: $f"; done
bash scripts/validate-agent-refs.sh         # 8  no dead refs or pinned models
bash scripts/tests/cc-version-floor.test.sh # 9  Claude Code version floor
bash scripts/docgen/check-coverage.sh       # 10 '# How to use' on every item
bash scripts/docgen/apply-counts.sh --check # 11 published counts match the tree
bash scripts/scan-flavor.sh                 # 12 neutrality — must say "✓ clean"
```

Gates 10 and 11 are ratchets with no slack: docs coverage sits at 106/106, and every
published count is spliced in from the corpus by `scripts/docgen/counts.sh`. Never
raise a floor to make a build pass. `mods` installs a pinned Claude Code CLI and runs
`bash scripts/tests/mods-ci.test.sh` (strict validate + `claude plugin test` of every
mod plugin; without the CLI the script prints SKIP). The other two jobs are `agent-evals` (promptfoo,
in `evals/`; skips cleanly without `ANTHROPIC_API_KEY`) and `secret-scan` (gitleaks
over full history).

## Control-plane changes (`tools/swarmery/`)

`swarmery-ci.yml` builds the web bundle, then runs `go vet ./...`, `go test` under a
**gated 70 % coverage floor** (excluding `cmd/swarmery`, `web`, `internal/docsfs`),
then a binary build. Locally: `cd tools/swarmery && make build && make test`.
`make build` must come first — `web/embed.go` embeds `web/dist`, so `go vet` fails on
a clean checkout until the bundle exists. Never add plans, specs or design docs under
`tools/swarmery/docs/plan/`; that tree is a frozen record of shipped phases.

## Commits, PRs and license

Conventional commits (`feat:`, `fix:`, `refactor!:`, `chore:`), one sentence, present
tense. Fill in the pull-request template: what changed, the gates you ran with their
output, the semver bump, screenshots for UI changes. Every real routing bug or
output-contract regression should also become a case in `evals/`.

Contributions are accepted under the license of the region you touch:
[Apache-2.0](LICENSE) everywhere except `tools/swarmery/`, and
[PolyForm Noncommercial 1.0.0](tools/swarmery/LICENSE) inside it — see
[`NOTICE`](NOTICE). Report security issues through
[`SECURITY.md`](SECURITY.md), never in a public issue.
