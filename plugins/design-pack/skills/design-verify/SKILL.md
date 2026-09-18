---
name: design-verify
description: "Measure an implemented screen against its design export with a headless pixel diff and report the regions, the number and the side-by-side image. NOT for choosing what to build (that's design-build), NOT for resolving the design input (that's design-acquire), and NOT for behavioural UI checks without a reference design (that's browser-verification in core)."
version: "0.1.0"
owner: "swarmery-core"
docs:
  status: reviewed
  source_sha: 311ddb363ffb
  updated: 2026-08-06
---

# Purpose

Phase 6 of `design-build`, and the only place the measurement procedure is written down.
Mechanical on purpose: six steps, run in order, every one of them producing something the
operator can see.

# 1. Get a live target

Check that the app answers on `design.devUrl`. If it does not, start `design.devCommand` in the
background, wait until the URL answers, and **record that this process has to be stopped** when
verification finishes — a dev server left running outlives the task and confuses the next one.

Never point verification at a production origin. The target is the local dev URL from
`design.devUrl` plus the route under test.

# 2. Run the diff

`$SCRIPTS_DIR` is the one resolved in `design-build` Step 0.3
(`${CLAUDE_PLUGIN_ROOT}/scripts` inside the pack, the skill's own `scripts/`
directory inside a standalone bundle).

```bash
node "$SCRIPTS_DIR/screenshot-diff.mjs" \
  --design <design path> \
  --url <design.devUrl + route> \
  --viewport <authoring viewport> \
  --threshold <design.diff.threshold> \
  --pixel-tolerance <design.diff.pixelTolerance> \
  --out .design-verify/report/<slug>
```

The viewport is the authoring breakpoint carried over from Phase 2, not a convenient one. Add
`--full-page` when the screen is taller than the viewport and the part below the fold is in
scope.

Exit codes: `0` ok, `1` bad arguments or unreadable input, `2` runtime failure (the render
failed, or the URL was unreachable — check step 1 before blaming the diff), `3` the render
runtime is not prepared: run `ensure-runtime.mjs` once while online, see `design-build`
Step 0.3.

Artefacts written to the output directory: `design.png`, `impl.png`, `diff.png`,
`side-by-side.png`, `report.json`.

# 3. Read `report.json` — regions before verdict

**`pass` is a global percentage, and a global percentage cannot see small geometric drift.**
Measured on this pack's own runtime: a real 2px element shift scored `diffPercent` 0.02 against
a `threshold` of 0.5 — that is `pass: true` on a screen that is visibly wrong.

So read `regions` and `sizeMismatch` on **every** run, whatever the verdict says. A non-empty
dominant region is a finding regardless of `pass`.

Procedure, unconditional:

1. Sort `regions` by `shareOfDiff`, descending.
2. For each region, take its bounding box (`x`, `y`, `width`, `height`) and locate it on
   `side-by-side.png`; name the concrete element it lands on. "Region at 240,880 covering 61%
   of the diff" is not yet a finding — "the summary row sits 2px low" is.
3. Report the mapped list to the operator with `diffPercent`, `threshold` and `pass`.

When `pass` is `false`, that list is the work queue. When `pass` is `true` and `regions` is
non-empty, that list is still reported as findings — closing on the verdict alone is how the
2px shift above ships.

Fix in the order given by `references/fidelity-checklist.md`, rule 7: container sizes and
spacing first, then typography, then colours and shadows.

# 4. `sizeMismatch` is a layout bug

A non-empty `sizeMismatch` means the implementation and the design do not even occupy the same
box. That is a layout defect — a container width, a padding, a missing constraint — and it is
fixed first. It is never a reason to normalise the two images to a common size: normalising
hides the defect and leaves every downstream region misaligned by the same amount, which then
gets chased element by element.

# 5. Run the project's own gates

Run `design.verify.lint` and `design.verify.typecheck`. Both must be green. A pixel-perfect
screen that fails typecheck is not delivered work, and neither command is optional because the
diff looks good.

# 6. Show the operator the image and the number

Report `side-by-side.png` and `diffPercent` (with `threshold` and `pass`), plus the mapped
region list from step 3 and the state of step 5.

**Declaring the work finished without those two artefacts is forbidden.** "Looks the same" is
not an output of this pack; a number and an image are. If the run was made in a degraded mode
(`degraded: screenshots` from `design-acquire`), say so in the same message and call the result
an approximation.

# Cleanup

Stop the dev server if step 1 started it. Leave the artefact directory in place — the operator
may want to look at `diff.png` after the summary.

# How to use

## What it does

This skill measures an implemented screen against its design export. It starts the dev server if needed, runs a headless pixel diff, and turns the raw numbers into named findings — "the summary row sits 2px low" rather than "region at 240,880". You end up with a percentage, a side-by-side image, and a mapped list of the regions that differ.

## When to use it

- You have implemented a screen from a design export and need to know how close it actually is.
- A diff run passed the threshold but the screen still looks wrong, and you need the region list read properly.
- You are finishing a design handoff and need a number and an image before you can call the work done.
- You want the same measurement procedure applied outside the full implementation workflow.

## When not to use it

- You are deciding what to build or how to structure the implementation — use `design-build`.
- You still need to unpack, parse, or resolve the design input itself — use `design-acquire`.
- You are checking UI behaviour with no reference design to compare against — use `browser-verification` in core.

## How to invoke

```
Skill(skill: "design-pack:design-verify")
```

Invoke it directly when you want a measurement pass, or let `design-build` reach it as its verification phase.

## Inputs

- Design export path — an `.html` file, a directory containing one (`index.html`, or a single `.html`), or a `.zip` of either — required. A bare `.png` is not an accepted export: nothing rejects it, and the run silently measures against the browser's image viewer.
- Route under test — appended to the local dev URL from `design.devUrl` — required.
- Authoring viewport — the breakpoint the design was drawn at, not a convenient one — required.
- Threshold and pixel tolerance — read from `design.diff` in project config — optional, defaults come from config.
- Full-page flag — add it when the screen is taller than the viewport and the part below the fold is in scope.

## What you get back

An artefact directory containing `design.png`, `impl.png`, `diff.png`, `side-by-side.png`, and `report.json`. The final message carries `side-by-side.png`, `diffPercent` with its `threshold` and `pass`, the region list mapped to concrete elements, and the result of the project's own lint and typecheck. The directory is left in place so you can open `diff.png` afterwards; any dev server the run started is stopped.

## Worked example

```
Skill(skill: "design-pack:design-verify")
→ dev server already answering on the local dev URL
→ node screenshot-diff.mjs --design ./export/line-items/index.html \
    --url <devUrl>/orders/line-items --viewport 1440x900 \
    --threshold 0.5 --out .design-verify/report/line-items
→ diffPercent 0.02, threshold 0.5, pass: true
→ regions: one at 61% of the diff → "the summary row sits 2px low"
→ lint green, typecheck green
```

You get `pass: true` and a real finding in the same report — the 2px shift is reported as work to do, not closed on the verdict.

## Related

- `design-build` — prefer it when you are implementing a screen end to end; it calls this skill as its final phase.
- `design-acquire` — prefer it when the design input is not yet a measurable file.
- `browser-verification` — prefer it for behavioural checks where no reference design exists.
