---
description: Thin entry point for `/design-implement <export-path|url> [--route <path>] [--viewport WxH] [--from-screenshots] [--dry-run]` — parses and validates the argument shape only, then hands control to the `design-build` skill. No run logic lives here.
allowed-tools:
  - Bash
docs:
  status: reviewed
  source_sha: f801c707a561
  updated: 2026-08-06
---

# /design-implement — turn a design handoff into an implemented screen

## Usage

```
/design-implement <export-path|url> [--route <path>] [--viewport WxH] [--from-screenshots] [--dry-run]
```

Examples:

- `/design-implement ./handoff/checkout.html --route /checkout`
- `/design-implement ./handoff/checkout.zip --route /checkout --viewport 1440x900`
- `/design-implement https://<design-host>/share/<id> --route /checkout --dry-run`
- `/design-implement ./handoff/shots/ --route /checkout --from-screenshots`

This command is invoked the same way from an interactive session and from the
`ai-prompt` step of a scheduled routine, where the prompt arrives on a headless
session's stdin and nobody is there to address an agent by name. That is the
whole reason a command exists alongside `@design-implementer`: a stable entry
point that does not depend on an agent being named correctly in free text (the
same rationale as `plugins/jira-pack/commands/jira-fix.md:21-25`).

## What it does — and does not do

This is a **thin proxy**. It parses `$ARGUMENTS`, validates the *shape* of the
positional argument and the flags, and delegates everything else to the
`design-build` skill (`plugins/design-pack/skills/design-build/SKILL.md`),
which owns all of Phases 0-6: prerequisites, acquire, ground truth, recon, plan,
implement, verify.

In particular, this command does **not**:
- read, unzip, or parse the export — the skill's Phase 1 (`design-acquire`) does;
- check that `.claude/project.json` carries a `design` block, or that its
  `tokensFile` / `componentsRoot` / `routesRoot` paths exist — the skill's
  Phase 0 does, and prints the paste-ready fragment when they are missing;
- start `design.devCommand`, screenshot anything, compute a diff, or decide
  what to change — the skill's Phases 3-6 and `@design-implementer` own that.

If an explanation of **how** to diff, which region to fix first, or what counts
as a passing screen ever appears in this file, it leaked out of the skill and
belongs back there.

Argument parsing failure → print the usage error and stop. Never start the
skill on an unparseable argument.

## Argument parsing

1. **`<export-path|url>`** (first positional, required) — exactly one of:
   - a filesystem path ending in `.html` or `.zip`, or a directory — and it
     must exist;
   - an `http(s)` URL that parses.

   Missing or empty → print usage and stop.

2. **`--route <path>`** — optional value flag; must start with `/` (`^/`).
   Forwarded verbatim: whether that route exists under `design.routesRoot` is
   the skill's question, not this command's.

3. **`--viewport <WxH>`** — optional value flag; must match
   `^[0-9]{3,5}x[0-9]{3,5}$`. Omitted → the skill takes the authoring viewport
   from the export.

4. **`--from-screenshots`** — boolean flag declaring the degraded mode where
   only screenshots exist. **Incompatible** with a `.html` or `.zip` positional
   argument: markup means the run has ground truth, and the degraded mode would
   discard it. That combination → error naming both arguments, and stop.

5. **`--dry-run`** — boolean flag. It means *stop at the plan*, not *do
   nothing*: Phases 1-3 (acquire, ground truth, recon) still run, because they
   are what produces the Phase 4 plan and its approved file list. The run
   prints that plan and stops **before** `@design-implementer` is invoked — so
   no project file is written and no fix is attempted.

Only regex-level shape checks need Bash — no network calls and no reads of the
export belong here:

```bash
echo "$VIEWPORT" | grep -qE '^[0-9]{3,5}x[0-9]{3,5}$' || echo "usage: --viewport WxH, e.g. 1440x900"
```

## Delegation

Once the argument and flags parse cleanly, hand control to the
`design-build` skill with: the positional argument exactly as given,
`--route`, `--viewport`, and the two flag states. The skill re-derives
everything else from the `design` block of `.claude/project.json` and never
trusts this command's parsing beyond "here is an export-shaped argument and
here are the flags."

## Related

- `plugins/design-pack/skills/design-build/SKILL.md` — the six phases; the
  operator approves the Phase 4 plan before any project file is written.
- `plugins/design-pack/agents/design-implementer.md` — the executor the skill
  invokes for Phases 5-6, with its autonomy contract and eight STOP triggers.
- `plugins/jira-pack/commands/jira-fix.md` — the thin-proxy command format this
  file follows.

# How to use

## What it does

You have a design handoff — an exported HTML file, a zip, a share URL, or a folder of screenshots — and a screen in your project that should match it. This command is the entry point that turns that handoff into an implemented, verified screen. It checks only that your argument and flags are well formed, then hands the whole run to the `design-build` skill, which acquires the export, reads ground truth, plans the change, implements it, and measures the result with a pixel diff.

## When to use it

- You have a design export on disk (`.html`, `.zip`, or a directory) and want the matching route in your app built or corrected to match it.
- You want to see the plan and the file list before anything is written — run it with `--dry-run`.
- You only have screenshots of the design and accept the reduced accuracy that implies.
- You need a stable entry point for a headless or scheduled run, where no one is there to address an agent by name.

## When not to use it

- You want to measure an already-implemented screen against a design without changing code — use the `design-verify` skill.
- You only need the export parsed or the tokens pulled out of a design system — use the `design-acquire` skill.
- You want to check UI behaviour with no reference design at all — use the `browser-verification` skill in core.

## How to invoke

```
/design-implement <export-path|url> [--route <path>] [--viewport WxH] [--from-screenshots] [--dry-run]
```

Type it in an interactive session, or send it as the prompt of a headless run.

## Inputs

- `<export-path|url>` — required. A path ending in `.html` or `.zip`, an existing directory, or an `http(s)` URL.
- `--route <path>` — optional. The route in your app the design belongs to; must start with `/`.
- `--viewport WxH` — optional. Authoring viewport, e.g. `1440x900`. Omitted, the export's own viewport is used.
- `--from-screenshots` — optional. Declares the degraded mode where only screenshots exist. Rejected together with a `.html` or `.zip` argument, because markup is better ground truth.
- `--dry-run` — optional. Stop at the plan; nothing in your project is written.

## What you get back

A plan you approve before any project file changes, then the implemented screen and a pixel diff measuring it against the design. On a malformed argument you get a usage error and the run stops — the skill never starts on input that does not parse.

## Worked example

```
/design-implement ./handoff/checkout.zip --route /checkout --viewport 1440x900
```

The argument shape checks out, so control passes to the skill. It unpacks the export, reads your design tokens and components, inspects the current `/checkout` route, and shows you a plan naming the exact files it wants to touch. You approve it; the executor writes those files, screenshots the live route, and reports the diff regions it still needs to close.

## Related

- `design-build` skill — the six phases behind this command; read it when you want to know how a phase decides something.
- `design-implementer` agent — the executor the skill runs for the implement-and-verify phases, with its stop conditions.
- `design-verify` skill — prefer it when the screen already exists and you only want the diff.
