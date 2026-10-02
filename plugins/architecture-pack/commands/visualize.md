---
description: Thin entry point for `/visualize <source…|"topic"> [--out <file.html>] [--lang <code>] [--publish|--no-publish]` — parses and validates the argument shape only, then hands control to the `visual-explainer` skill. No run logic lives here.
allowed-tools:
  - Bash
docs:
  status: draft
  updated: 2026-10-02
---

# /visualize — explain a document or a topic as an interactive page

## Usage

```
/visualize <source…|"topic"> [--out <file.html>] [--lang <code>] [--publish|--no-publish]
```

Examples:

- `/visualize docs/design/investigation.md`
- `/visualize docs/spec.md docs/adr/0007-storage.md --out docs/storage-explainer.html`
- `/visualize "how the release pipeline promotes a build" --lang en`
- `/visualize notes/retro.md --publish`

The command exists so a headless run or a scheduled routine has a stable entry point that does not
depend on the skill being picked from free text.

## What it does — and does not do

This is a **thin proxy**. It parses `$ARGUMENTS`, checks the *shape* of the sources and flags, and
delegates everything else to the `visual-explainer` skill
(`plugins/architecture-pack/skills/visual-explainer/SKILL.md`), which owns reading the source, the page plan,
building from the shell, verification and delivery.

It does **not** read the sources' content, choose visuals, write HTML, start a browser or publish
anything. If guidance on how a page should look or how to verify it ever appears here, it leaked
out of the skill and belongs back there.

Argument parsing failure → print the usage error and stop. Never start the skill on an unparseable
argument.

## Argument parsing

1. **`<source…|"topic">`** (required, one or more) — each positional is either an existing file
   (`.md`, `.markdown`, `.txt`, `.rst`, `.adoc`, `.html`) or, when it is a single quoted string that
   is not a path, a topic to explain from the conversation and the repository. A path that does not
   exist → error naming it, and stop.
2. **`--out <file.html>`** — optional; must end in `.html`. Omitted → the skill writes
   `<first-source-stem>.explainer.html` next to the first source (for a topic: the current
   directory, slugged from the topic).
3. **`--lang <code>`** — optional; must match `^[a-z]{2}(-[A-Za-z]{2})?$`. Omitted → the language of
   the source.
4. **`--publish` / `--no-publish`** — optional and mutually exclusive. Omitted → the skill asks
   whether to publish once the page is verified, and only when a publishing tool is available.

Only shape checks need Bash:

```bash
for f in "${SOURCES[@]}"; do [ -f "$f" ] || echo "source not found: $f"; done
[ -z "$OUT" ] || echo "$OUT" | grep -qE '\.html$' || echo "usage: --out must end in .html"
```

## Delegation

Once the arguments parse, hand control to the `visual-explainer` skill with the sources (or the
topic), `--out`, `--lang` and the publish choice. The skill re-reads everything itself and trusts
this command only for "here are sources that exist and flags that parse."

## Related

- `plugins/architecture-pack/skills/visual-explainer/SKILL.md` — the procedure, shell, scripts and checks.
- `plugins/core/skills/html-reporting/SKILL.md` — agent reports in the house style, not explainers.
- `plugins/design-pack/commands/design-implement.md` — the thin-proxy command format this file follows.

# How to use

## What it does

You have a document — an investigation, a spec, a plan, meeting notes — or a topic you just
discussed, and you want a page that explains it to people who will not read the original. This
command checks that your sources exist and your flags are well formed, then hands the whole job
to the `visual-explainer` skill, which plans the page, builds it with diagrams, charts, tooltips
and a details drawer, and measures it in two widths and two themes.

## When to use it

- You want to walk your team through a long design document in a meeting.
- A spec or an investigation needs a visual summary with the details one click away.
- You explained something in the conversation and want it as a page you can share.
- You need a stable entry point for a headless or scheduled run that produces an explainer.

## When not to use it

- You want an agent's task summary or audit report — that is `html-reporting`.
- You already have a Mermaid `.mmd` file and only want to view it — that is `mermaid-viewer`.
- You want slides, a Word document or a PDF — use a slides or document skill.

## How to invoke

```
/visualize <source…|"topic"> [--out <file.html>] [--lang <code>] [--publish|--no-publish]
```

Type it in an interactive session, or send it as the prompt of a headless run.

## Worked example

`/visualize docs/design/investigation.md --out docs/explainer.html` checks that the file exists and
that `--out` ends in `.html`, then starts the skill. The skill reads the investigation, plans one
chapter per question a reader has, builds `docs/explainer.html` from its shell, runs the static check
and the four browser passes, and reports what it verified. You get the file path, and — if you said
yes to publishing — a private link.
