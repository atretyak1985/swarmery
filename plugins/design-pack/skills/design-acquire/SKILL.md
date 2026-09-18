---
name: design-acquire
description: "Resolve a design handoff into something measurable: parse a handoff prompt, unpack an exported HTML/zip, pull tokens from a design-system source, or record an honest degraded mode for screenshots-only input. NOT for the implementation workflow itself (that's design-build) and NOT for measuring a diff (that's design-verify)."
version: "0.1.0"
owner: "swarmery-core"
docs:
  status: reviewed
  source_sha: be52051a970c
  updated: 2026-08-06
---

# Purpose

Phase 1 of `design-build`. Four input shapes arrive in practice, and they do not promise
the same thing: only a self-contained HTML export is ground truth for geometry. This skill
turns whatever arrived into a path plus an honest statement of what that path can support, and
hands both back. It does not extract tokens (Phase 2) and it does not measure anything
(`design-verify`).

# The export route in Claude Design

This is the part operators do not find on their own — reproduce it for them exactly:

> In Claude Design (claude.ai/design) open the project → the **Share** menu in the top right
> corner → the **Export** section → then either
> **Claude Code — "Implement this design in code" → Send** (sends the handoff prompt into the
> Claude Code session), or
> **Project HTML → Download** (downloads a `.zip` with self-contained HTML/CSS).
> Pixel work needs **Project HTML** specifically: the handoff prompt describes intent, while
> the `.zip` contains what actually renders.

# Input shape 1 — a handoff prompt

The operator pasted a design tool's handoff prompt (or it arrived through **Send**).

1. Parse it. Look for a reference to a file or a URL carrying the actual markup.
2. If the prompt leads to HTML — fetch it and continue with shape 2. The prompt's prose is
   context, not a substitute.
3. If it does not — ask for `Project HTML` and show the route above. A handoff prompt on its
   own describes intent: it states what the screen means, not the computed values it renders
   at, so nothing downstream can be measured against it.

**Promises:** intent and structure. Not geometry, not exact values.

# Input shape 2 — a `.zip` or `.html` export

1. Unpack into the working directory: `.design-verify/source/<slug>/`.
2. Find the entry document (the top-level `index.html`, or the single `.html` at the root of
   the archive; if several are plausible, list them and let the operator pick — do not guess
   which screen is meant).
3. Check self-containment: scan for external network resources (remote stylesheets, webfonts,
   images, script tags pointing off-host). Record every one of them. They change what renders,
   so they change the measurement — a font fetched from the network that fails to load in the
   headless run silently reshapes every text block.
4. Pass the entry document's path forward.

**Promises:** full ground truth. This is the shape the rest of the pack is designed around.

# Input shape 3 — DesignSync or a design-system project

Pull tokens and components through whichever tool is available for that source.

Record the split honestly: **the tokens are trustworthy; the geometry of a specific screen is
not.** A design system says what a button is; it does not say where this screen puts it, how
much space is above it, or which of its variants this layout uses. If the goal is a pixel match
for a screen, an HTML export is still required — route above.

**Promises:** the token layer (colours, type, spacing, radii). Not screen layout.

# Input shape 4 — screenshots only

State this in full, as its own paragraph, before any work starts:

> Pixel accuracy cannot be guaranteed from images. Exact type size and tracking are not
> recoverable, real colour values are not recoverable after compression, and states (hover,
> focus, active, disabled) are not present at all. Ask for an HTML export instead — the route
> is above.

If the operator insists, continue — but:

- record the mode as `degraded: screenshots` in the Phase 4 plan **and** in the final report;
- never claim a pixel match, at any point, in any wording;
- in Phase 6 the implementation is compared against the screenshot as an **approximate**
  reference, and the result is reported as an approximation, with that word in it.

**Promises:** an approximation, labelled as one.

# Working directory

Everything this skill writes goes under `.design-verify/` in the project root — sources in
`.design-verify/source/<slug>/`, later phases add their own subdirectories.

Check the project's `.gitignore` for `.design-verify/` and remind the operator to add it if it
is missing. These are large, regenerable, machine-produced artefacts; they do not belong in the
history.

# Output back to `design-build`

- the design path (entry document, or the screenshot set);
- the input shape and the mode (`degraded: …` when applicable);
- external network resources found in shape 2;
- for shape 3, an explicit note that screen geometry was not acquired.

# How to use

## What it does

You have a design handoff — a pasted prompt, a downloaded `.zip`, a design-system link, or just screenshots — and you need to know what it can actually support before anyone writes code. This skill resolves that input into a concrete path on disk plus an honest statement of what that path promises: full geometry, tokens only, or an approximation. It stops the common failure where a screenshot gets treated as ground truth and a "pixel match" is claimed over something unmeasurable.

## When to use it

- An operator handed you a design export and you need to unpack it and find the entry document before implementation starts.
- Someone pasted a design tool's handoff prompt and you have to work out whether it leads to real markup.
- You have only screenshots and need the degraded mode written down before anyone promises accuracy.
- The design lives in a design-system source and you need the token layer pulled without over-claiming screen layout.

## When not to use it

- You want the full implementation workflow end to end — use `design-build`, which calls this skill as its first phase.
- You already have a built screen and want it measured against the design — use `design-verify`.
- You are checking UI behaviour with no reference design at all — use `browser-verification` in core.

## How to invoke

```
Skill(skill: "design-pack:design-acquire")
```

Invoke it with the handoff you received — a file path, an archive, a URL, or a description of what arrived. It reads the input, picks the matching shape, and does the unpacking or the asking from there.

## Inputs

- **The handoff itself** — a `.zip` or `.html` path, a URL, a pasted handoff prompt, a design-system reference, or a set of screenshots — required.
- **A screen or slug hint** — which screen is meant, when an archive contains several plausible entry documents — optional; you get asked if it is ambiguous and cannot be inferred.

## What you get back

Sources unpacked under `.design-verify/source/<slug>/` in the project root, and a short handoff back to the caller containing: the design path (entry document or screenshot set), the input shape and mode (`degraded: screenshots` when applicable), any external network resources found in an export, and — for a design-system source — an explicit note that screen geometry was not acquired. You also get a reminder to add `.design-verify/` to `.gitignore` if it is missing.

## Worked example

```
Skill(skill: "design-pack:design-acquire")
> input: ~/Downloads/project-html-export.zip, screen "orders/line-items"
```

The archive is unpacked to `.design-verify/source/orders-line-items/`, the entry
`index.html` is identified, and a scan finds one remote webfont. You get back the
entry path, shape "HTML export — full ground truth", and the webfont flagged as a
resource that will reshape text if it fails to load in the headless run.

## Related

- `design-build` — the whole workflow; start there unless you only need the input resolved.
- `design-verify` — measures a built screen against the design once implementation exists.
- `browser-verification` — behavioural UI checks when there is no design to measure against.
