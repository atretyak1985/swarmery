# plan/SUMMARY.md — the plain-language plan overview

Location: `{task-dir}/plan/SUMMARY.md`. The dashboard shows it at the top of a
plan's Summary tab, above the per-phase Completion Reports. Those reports are
the engineering record; this file is the one the operator reads first, weeks
later, to remember what the plan was for and what it changed.

Rules:

- Plain words. Name the problem before the solution. A term the operator would
  not use unprompted gets a one-line explanation, or is left out.
- No commit hashes, file paths or function names in the body. The phase reports
  and the task-root `SUMMARY.md` carry those.
- Numbers only where they tell the story (a cost delta, a before/after rate),
  each with its unit and, if possible, its comparison point.
- Honest status: shipped, partial or failed, and what is left. Say what the
  operator has to do, if anything.
- Written in the operator's working language (the one the task was asked in).
- ≤ 120 lines.

```markdown
# {Plan title, in words}: summary

**Status:** {shipped / partial / failed}, {released as … / merged on …}. {One
line on what continues elsewhere, if anything.}

## The task, in plain words
{The problem or problems, as the operator would describe them. 3–8 lines.}

## What was built
### 1. {Outcome in words}
- **{Effect}.** {What it does for the operator, with a number if one matters.}

{When the work introduces a mechanism with steps, add a small flow diagram in a
fenced block and a numbered "how it works" list.}

## Where to see it
| Page / command | What you'll find |
|---|---|

## What's left, and your part
- {Deferred work: where it moved, and when it is due}
- {Anything the operator has to do}
```
