# design-pack

Design-handoff pack: `/design-implement` takes an exported design and re-expresses it in the
project's own stack — its tokens, its component library, its router — and then **measures** the
result against the design instead of asserting it looks right. Opt-in; most projects don't need
it, and it requires config before first use.

The pack exists because the expensive failure in design handoff is not "the agent could not
write the markup". It is that the agent silently invents a second design system next to the one
the project already has: new colour variables beside the existing tokens, a new button beside
the existing button, a font that was never installed quietly replaced by a fallback. So the flow
front-loads two read-only phases — a token inventory taken from a **real headless render** of the
export (computed styles, not a guess from source) and a reuse-vs-create survey of the project's
own components — and puts an **approval gate** between what it proposes and the first line of
code it writes.

Completion is a measurement, not a claim. The implemented route is served locally, screenshotted
at the design's viewport, and diffed against the export pixel by pixel; the run is done when that
diff is under the configured threshold and the project's own lint and type-check pass. A run that
cannot produce that number does not get to call itself finished — it reports what it could not
measure and why.

> **Status.** `0.1.0` ships the whole flow — the config contract, the verification runtime,
> the three skills, `/design-implement` and `@design-implementer`. What it has not yet had is
> a live run on a real design in a real project; until that happens, treat the numbers it
> reports as trustworthy and its ergonomics as unproven.

## Four ways a design arrives — and what each one can promise

The pack accepts four input shapes, and they are **not** equivalent. What it promises degrades
with the fidelity of what it is given, and it says so up front rather than at the end:

| Input | What can be promised |
|---|---|
| Project HTML export (`.zip` / `.html`) | pixel match is **measured**; this is the expected path |
| Handoff prompt from Claude Design | the same, **if** the prompt leads to an HTML export; otherwise it degrades to the row below |
| Design system via DesignSync | tokens and components are trustworthy; **screen geometry is not** |
| Screenshots only | **pixel accuracy is not guaranteed** — the pack says this out loud and asks for an HTML export |

The bottom row is the load-bearing one. From a screenshot there is no computed style to read: font
stacks, exact spacing, border radii, and shadow parameters are inferred, and an inferred value that
looks close in review is exactly the kind of drift this pack was built to prevent. It will still
implement from screenshots when that is all that exists — it will not pretend the result was
measured.

## Enable per project

```jsonc
"enabledPlugins": { "design-pack@swarmery": true }
```

## Required config

`/design-implement` cannot read these facts off the design export — they belong to the project:
where the tokens live, where components live, how the app is served for a screenshot, and which
checks must pass. They are declared in the project's `.claude/project.json` under a `design`
block. Missing required keys are a loud stop, never a guess.

### From the dashboard

The pack declares the block in its own `requirements.json`, so the dashboard can **ask** you for
it instead of leaving you to discover it from a failed run:

1. **Enable the pack** for the project. Its row on the project's plugins panel then shows a
   `needs-config` chip while the declared key is missing or incomplete.
2. **Press `configure`.** The form is rendered straight from
   `plugins/design-pack/requirements.json` — the same fragment
   `overlays/_schema/project.schema.json` validates against, so the form cannot ask for a shape
   the schema would reject.
3. **Press `probe`** to fill the fields the pack nominated (`tokensFile`, `componentsRoot`,
   `routesRoot`, `devCommand`, `devUrl`, `verify.lint`, `verify.typecheck`). The probe is a
   read-only inspection of the repository; suggestions stay typeable, never a fixed dropdown, and
   a probe that fails costs you the suggestions and nothing else.
4. **Press `save`.** Exactly the `design` key is written into `.claude/project.json`, merged into
   what is already there.

### By hand

The same block, the same schema — edit `.claude/project.json` directly if you prefer:

```json
"design": {
  "tokensFile": "src/app/globals.css",
  "componentsRoot": "src/components",
  "routesRoot": "src/app",
  "fontLoader": "next-font",
  "devCommand": "npm run dev",
  "devUrl": "http://localhost:3000",
  "verify": { "lint": "npm run lint", "typecheck": "npx tsc --noEmit" },
  "diff": { "threshold": 0.5, "pixelTolerance": 0.1 },
  "budget": { "maxIterations": 4, "maxFiles": 12 }
}
```

- `tokensFile`, `componentsRoot`, `routesRoot`, `devUrl` and `verify` are required. Without the
  first three there is no reconnaissance phase; without `devUrl` there is no diff to measure; and
  a run that ends without lint and type-check is not a finished run.
- `devCommand`, `fontLoader`, `diff` and `budget` are optional — they have working defaults, or
  the server can be started by hand.
- `devUrl` must be a **local** origin. A shared, staging, or production URL is never a valid
  answer here: the pack screenshots whatever that URL serves.
- `fontLoader` decides the wording of the instructions you get when a design font is missing from
  the project. The font is never silently substituted.
- Schema: `overlays/_schema/project.schema.json` (`design` property), mirrored in this pack's
  `requirements.json`; `scripts/check-plugin-requirements.sh` fails CI if the two drift.

## What this pack deliberately does NOT do

- **It does not commit and it does not create branches.** It writes files into the working tree
  you point it at and stops there; staging, committing, branching, pushing, and opening a PR stay
  with you or with the core agents you already use for that.
- **It never touches a shared or production origin.** The only URL it opens is the local
  `devUrl`; it does not deploy, and it does not screenshot a deployed environment.
- **It does not declare completion without a measured diff.** No screenshot comparison, no
  "done" — an unmeasurable run reports itself as unmeasured rather than passing.
- **It does not invent a parallel design system.** New tokens and new components are proposed at
  the approval gate, before any code exists, and only ever as an extension of what the project
  already has.
- **It does not run unattended past its budget.** `budget.maxIterations` and `budget.maxFiles`
  are hard stops: when either is reached the decision goes back to the operator instead of the
  agent widening its own scope.

## Tests

Two of them, deliberately split by cost:

- `bash scripts/tests/design-pack-contract.test.sh` — static, runs in CI. Eleven greps over the
  shipped docs asserting the safety wording is still there (read-only probe, the export route
  labels, the screenshot warning, the eight STOP triggers, the ban on completion without a
  comparison image, the thin-proxy line budget). It never runs a model and never opens a browser.
- `bash plugins/design-pack/scripts/test.sh` — the browser smoke: five checks against the
  bundled fixtures, including a 2px mutation the region clustering has to localise. **Local
  only** — it needs the pinned Chromium (~275 MB on first prepare), so CI does not run it. Run
  it after touching anything under `scripts/`. Without a prepared cache and with
  `DESIGN_PACK_TEST_OFFLINE=1` it skips cleanly instead of failing.

## Standalone `.skill` bundle

The pack is the supported way to install this. If you need the workflow in a project that does
not use this marketplace, bundle the skill by hand — the CLI has no packaging command
(`claude plugin` offers `install`/`validate`/`tag`, nothing that builds a `.skill`), so the
bundle is a zip with exactly one root directory:

```bash
PACK=plugins/design-pack
STAGE=$(mktemp -d)/design-build && mkdir -p "$STAGE"
cp "$PACK/skills/design-build/SKILL.md" "$STAGE/SKILL.md"
cp -R "$PACK/skills/design-build/references" "$STAGE/references"
cp -R "$PACK/scripts" "$STAGE/scripts"
cp "$PACK/skills/design-acquire/SKILL.md" "$STAGE/references/design-acquire.md"
cp "$PACK/skills/design-verify/SKILL.md"  "$STAGE/references/design-verify.md"
(cd "$(dirname "$STAGE")" && zip -qr design-build.skill design-build -x '*/.DS_Store')
```

Install it by unpacking that directory into the skills directory the CLI loads
(`claude plugin init` scaffolds into `~/.claude/skills/<name>/`, which is the same shape).

What the bundle gives up, stated plainly:

- **No config form.** `requirements.json` belongs to the pack, so the dashboard cannot render
  the `design` block for a bundled skill — fill it into `.claude/project.json` by hand.
- **No `@design-implementer`.** Agents ship with the pack; in the bundle the workflow runs in
  the session that invoked it, and the autonomy contract is prose rather than an agent boundary.
- **No `/design-implement` command** — invoke the `design-build` skill by name.
- `design-acquire` and `design-verify` travel as `references/*.md` rather than as sibling
  skills, so they are read, not delegated to.

> **The bundle trades an enforced boundary for a written one.** Inside the pack, the eight STOP
> triggers are an agent contract: the run stops and hands the decision back before a global
> token, a font, or a shared component is touched. In the bundle there is no agent, so those
> same triggers are **advisory text** that the session may rationalise past. That is acceptable
> in a supervised interactive session, where you see the stop and decide. Do not point the
> bundle at an unattended headless run — use the pack there, or accept that the blast-radius
> guarantee is gone.

Everything else is identical: `SCRIPTS_DIR` resolves to `${CLAUDE_PLUGIN_ROOT}/scripts` inside
the pack and to the skill's own `scripts/` inside the bundle, and that one variable is the only
difference between the two.

The bundling recipe above is hand-copied and nothing in CI checks it, so a new file under
`skills/design-build/references/` or `scripts/` lands in the bundle only because `cp -R`
happens to sweep the whole directory. If the layout ever gains a file outside those two trees,
update the recipe in the same commit.
