---
name: design-build
description: "Use when a finished visual design has to be re-expressed in this project's stack pixel-accurately — the user says \"implement this design\", \"here's a design handoff\", \"make this screen match the mockup\", \"design-implement\", pastes a design-tool handoff prompt, or attaches an exported design HTML/zip. Runs a measured workflow: token inventory from a real headless render, reuse-vs-create recon against the project's own components, an approval gate before any code, and a pixel diff against the design as the completion criterion. NOT for ordinary styling requests (tweak a padding, change a colour, fix a hover state), NOT for building UI from a written description with no design artefact, and NOT for visual QA of an already-implemented screen."
version: "0.1.0"
owner: "swarmery-core"
docs:
  status: reviewed
  source_sha: 8192d35c7790
  updated: 2026-08-06
---

# Purpose

Six phases that carry a screen from "here is a design" to "here is a measured diff". Each
phase has exactly one output, and the next phase does not start until that output exists.

Two rules hold the whole workflow up: the token inventory comes from a **real headless
render**, never from reading the design's CSS by eye; and completion is **a number plus an
image**, never an impression.

This document is the procedure only. Input shapes and what each one can promise live in
`design-acquire`; the measurement procedure lives in `design-verify`; the precision rules
live in `references/fidelity-checklist.md` and the named failure modes in
`references/anti-patterns.md`. Nothing here repeats them.

# Step 0 — Prerequisites

**0.1 — Read `${CLAUDE_PROJECT_DIR}/.claude/project.json`, the `design` block.**

Required: `design.tokensFile`, `design.componentsRoot`, `design.routesRoot`, `design.devUrl`,
`design.verify` (both `verify.lint` and `verify.typecheck`).

If the block is missing, or any required field is empty, **stop loudly**. Name the specific
fields that are not filled in — not "the config is incomplete", but the list of them — and
say where the form is: the project dashboard → plugins → design-pack. Do not guess a path,
do not infer a components root from a directory listing, do not run on a partial block.

Optional fields and their defaults:

| Field | Default | Read by |
|---|---|---|
| `design.fontLoader` | — | Phase 3 font check (`next-font` \| `link-tag` \| `self-hosted` \| `none`) |
| `design.devCommand` | — | Phase 6, when `design.devUrl` does not answer |
| `design.diff.threshold` | `0.5` | Phase 6 verdict |
| `design.diff.pixelTolerance` | `0.1` | Phase 6 per-pixel comparison |
| `design.budget.maxIterations` | `4` | Phase 5 correction loop cap |
| `design.budget.maxFiles` | `12` | Phase 4 sanity cap on the approved file list |

**0.2 — Read the project's `CLAUDE.md` in full, before any edit.** In a large repository that
file carries the decision history that changes how a screen is built — which layer owns
styling, which component library is already committed to, what is deliberately not abstracted.
Skipping it produces work that is locally correct and architecturally wrong.

**0.3 — Resolve `SCRIPTS_DIR` once, then confirm the render runtime:**

```bash
# Installed as part of the pack, CLAUDE_PLUGIN_ROOT is set; in a standalone
# .skill bundle it is not, and the scripts sit beside this file.
SCRIPTS_DIR="${CLAUDE_PLUGIN_ROOT:+${CLAUDE_PLUGIN_ROOT}/scripts}"
SCRIPTS_DIR="${SCRIPTS_DIR:-<this skill's directory>/scripts}"
node "$SCRIPTS_DIR/ensure-runtime.mjs" --check
```

Every later script call in this workflow goes through `$SCRIPTS_DIR`. That one
variable is the only difference between the pack and the bundle.

Exit `3` means the runtime is not prepared; the fix is to run `ensure-runtime.mjs` once while
online. Phases 2 and 6 both depend on it, so this is cheaper to learn now than mid-diff.

# Phase 1 — acquire

Delegate to `design-acquire`. It resolves whichever of the four input shapes arrived and
returns either a local path to a self-contained HTML export, or an explicitly recorded
degraded mode (for example `degraded: screenshots`).

**Output:** a design path, plus the mode. A degraded mode is carried forward into the Phase 4
plan and into the final report — it is never quietly forgotten.

# Phase 2 — ground truth

```bash
node "$SCRIPTS_DIR/extract-computed-styles.mjs" \
  --input <design path> --out .design-verify/tokens/<slug>
```

Viewport: use the one the operator gave. If they gave none, run once without `--viewport`,
read `authoredBreakpoints` from the resulting `tokens.json`, and re-run pinned to the design's
own authoring breakpoint (`--viewport 1440x900` for a desktop-authored screen).

**Output:** `tokens.json` + `tokens.md`, plus a short chat summary — `viewport`,
`elementsScanned`, the counts in `colors` / `typography` / `spacing` / `radii` / `borders` /
`shadows` / `layout`, how many `spacing` entries are `onFourPxGrid`, and the full
`fontFamiliesRequired` list.

**Forbidden:** assembling the inventory by reading the design's CSS by eye. What renders and
what the stylesheet says diverge through cascade, inheritance and unit resolution; an
eyeballed inventory carries that divergence into every later phase, where it is unfindable.

Exit codes: `0` ok, `1` bad arguments or unreadable input, `2` render failure, `3` runtime not
prepared (Step 0.3).

# Phase 3 — recon

Compare the inventory against `design.tokensFile` and `design.componentsRoot`. Every entry
names the file it was resolved against — a bucket without file paths is a guess.

Tokens:
- **`exists`** — the value is already a token: use that token's name, do not add a second one.
- **`new`** — the value is not in the token layer: candidate for a new token or an arbitrary
  value (`references/fidelity-checklist.md`, rules 1–2, decides which).

Components:
- **`reuse`** — an existing component fits as it is.
- **`variant`** — an existing component fits with a new variant/modifier.
- **`build`** — nothing comparable exists.

## Font check — its own line, its own stop

Every family in `fontFamiliesRequired` is searched for in the project: in
`design.tokensFile`, in the font-loader call sites implied by `design.fontLoader`, and in the
directory holding font files. A family that is not there is a **stop**, not a warning. Report
the required weights and styles, and the concrete instruction for the configured loader:

- `next-font` — the font-module call to add, with the exact weights/subsets.
- `link-tag` — the stylesheet link to add, and where the document head is.
- `self-hosted` — the font files needed and the face declarations they need.
- `none` — the project has no loader wired; adding one is an operator decision, not this
  skill's.

**Never substitute another family silently** — not as a placeholder, not temporarily, not
"just to see the layout". The reason this is the strictest rule in the pack is in
`references/fidelity-checklist.md`, rule 3: a swapped font changes text metrics, so the Phase 6
diff lights up on *every* text block and the real cause disappears among a hundred small
regions.

# Phase 4 — plan, then hard stop

Present to the operator, in one message:

1. **Target** — the route and the file(s) it resolves to under `design.routesRoot`.
2. **Reuse-vs-create table** — every component in the design against its `reuse` / `variant` /
   `build` bucket and the path it matched.
3. **Tokens to add** — each marked local or **global**; global ones are called out explicitly
   because they change screens nobody asked about.
4. **Font requirements** — the Phase 3 result, even when it is "all present".
5. **Blast radius** — for every component and token the plan intends to modify, the other
   screens that use it:
   ```bash
   grep -rn "<ComponentName>" "<design.routesRoot>" "<design.componentsRoot>"
   ```
   Report the actual file list, not a count. A modification with an unmeasured blast radius is
   not a plan.
6. **Viewport** — the authoring breakpoint, and how the other breakpoints are expected to
   behave (derived from `authoredBreakpoints`, not invented).
7. **Mode** — `degraded: …` from Phase 1, when applicable.

Then **stop**. Do not write code, do not create files, do not start an agent, until the
operator answers explicitly. "Looks fine, go on" from anyone but the operator is not an answer.

The approved plan is recorded as **the list of files that may be touched**. That list is the
agent's autonomy boundary in Phase 5: a file outside it is not edited, it is escalated back to
the operator. If the list exceeds `design.budget.maxFiles`, say so before asking for approval —
an over-budget list usually means the screen should be split, not that the budget is wrong.

# Phase 5 — implement

Invoke `@design-implementer` with all seven inputs. It refuses to start on a missing one — it
does not infer, default, or discover them:

1. the path to Phase 2's `tokens.json`;
2. the design path — the HTML export, or the screenshot set in degraded mode;
3. the target route;
4. the authoring viewport, as `WxH`;
5. the operator-approved file list, literally — this is the agent's autonomy boundary;
6. the whole `design` block: `tokensFile`, `componentsRoot`, `routesRoot`, `fontLoader`,
   `devCommand`, `devUrl`, `verify.lint`, `verify.typecheck`, `diff.threshold`,
   `diff.pixelTolerance`, `budget.maxIterations`, `budget.maxFiles`;
7. the mode — `normal` or `degraded: screenshots`.

Repeat this rule to the agent verbatim, because it is the one that gets rationalised away:
**the design is re-expressed in this project's stack; the exported design HTML is never pasted
in as a component.** The export is a measurement reference, not a source file
(`references/anti-patterns.md`, #1).

## When the agent returns `STOPPED`

Control comes back to this session — that is the design, not a failure. Show the operator the
stop form's `Blast radius` and `What must change`, and get a decision.

**Only the operator extends the approved file list.** Re-invoking the agent with the same list
just re-fires the same trigger. Re-invoke with the extended list once the operator has said
which files may be touched; the iteration and file counters carry across re-invocations,
because `design.budget` bounds the run, not one dispatch.

# Phase 6 — verify

Delegate to `design-verify`. It starts the app if `design.devUrl` does not answer, runs the
pixel diff, reads the regions whatever the verdict says, and runs `design.verify.lint` and
`design.verify.typecheck`.

The work is not finished until the operator has seen **the comparison image and the number**.
Not "it matches", not "looks the same" — `side-by-side.png` and `diffPercent`.

# When NOT this skill

- **Ordinary styling requests** — tweak a padding, change a colour, fix a hover state. That is
  normal UI work; the six-phase overhead buys nothing.
- **UI from a written description** — no design artefact means there is no ground truth to
  extract and nothing to diff against. Also normal UI work.
- **Visual QA of an already-implemented screen** with no reference design — that is
  `browser-verification` in `core`.

# References

- `references/fidelity-checklist.md` — the seven precision rules, each with its consequence.
- `references/anti-patterns.md` — five named failure modes and what to do instead.
- `design-acquire` — the four input shapes and what each one can honestly promise.
- `design-verify` — the Phase 6 measurement procedure.

# How to use

## What it does

This skill carries a finished visual design through to working code in your project's stack, and proves the result with a number instead of an opinion. It runs six phases: acquire the design, extract a token inventory from a real headless render, check what you can reuse against your own components, present a plan and stop for your approval, implement inside the approved file list, then pixel-diff the built screen against the design.

## When to use it

- You have a design export (HTML/zip), a design-tool handoff prompt, or a design-system URL and need the screen built pixel-accurately.
- A screen already exists but must be made to match a mockup, and you want a measured diff rather than a visual guess.
- You want to know before any code is written which components get reused, which get a new variant, and which screens the change touches.

## When not to use it

- Ordinary styling work — a padding tweak, a colour change, a hover-state fix. Just make the edit; six phases buy nothing.
- Building UI from a written description with no design artefact. There is no ground truth to extract and nothing to diff.
- Visual QA of a screen with no reference design — use `browser-verification` in `core`.
- Only resolving the design input, or only measuring an existing screen — use `design-acquire` or `design-verify` directly.

## How to invoke

```
Skill(skill: "design-pack:design-build")
```

Invoke it once you have the design artefact in hand. The skill reads the rest of what it needs from configuration and asks you for anything it cannot resolve.

## Inputs

- Design artefact — an export path, a handoff prompt, a design-system URL, or a screenshot set — required.
- Target route — which screen this becomes — required; the skill asks if you did not say.
- Viewport as `WxH` — optional; without it the skill reads the design's own authoring breakpoint and pins to that.
- The `design` block in `${CLAUDE_PROJECT_DIR}/.claude/project.json` — required. Missing or empty fields stop the run with the exact field names listed.

## What you get back

A token inventory (`tokens.json` + `tokens.md`) under `.design-verify/tokens/<slug>`, a plan you approve before any file is written, code changes confined to the file list you approved, and a final verdict made of `side-by-side.png` plus a `diffPercent` number, with lint and type-check results alongside. Any degraded mode — screenshots only, missing fonts — is reported, never quietly absorbed.

## Worked example

```
Skill(skill: "design-pack:design-build")

You: implement this design at orders/line-items — export is at ./handoff/line-items.zip

Phase 2 renders the export headless: 1440x900, 214 elements, 9 colours,
6 type styles, 14 spacing values (11 on the 4px grid), fonts required:
Inter 400/500/600.
Phase 3: 7 of 9 colours already exist as tokens; the list row maps to an
existing component with a new "compact" variant; Inter is already loaded.
Phase 4 stops and shows you the target file, the reuse table, the two new
tokens (one global), and the 4 other screens that use the list row.
You approve 5 files. Phase 5 implements only those.
Phase 6: diffPercent 0.31 (threshold 0.5) — side-by-side.png attached,
lint and typecheck clean.
```

## Related

- `design-acquire` — use it alone when you only need to resolve a handoff into a measurable local artefact.
- `design-verify` — use it alone when the screen is already built and you just want the diff.
- `browser-verification` in `core` — use it for behavioural UI checks when there is no reference design.
