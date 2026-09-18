# Gate hardening — the warn→block decision record

A guard that starts blocking on a **date** is a guard that gets switched off. This document is the
gate for every warn-mode hook rule in `plugins/core/hooks/`: a rule moves from `warn` to `block` when
its row here is filled from counted hits and a false-positive review — never because a date arrived.

The rule is deliberately conservative, and it comes from the guards' own experience: *a rule that is
too quiet costs one retro line, a rule that is too loud costs the whole guard.*

## How to fill a row

1. Read the counts:

   ```bash
   scripts/guard-hits.sh --from 2026-08-24        # per-rule hits + distinct sessions
   scripts/guard-hits.sh --rule multi-mutation --raw   # the actual commands, to review
   ```

   The reader resolves the same log path the hook writes to (`bash-shape-guard.jsonl` in the
   project's workspace metrics dir; override with `BASH_SHAPE_GUARD_LOG`).

2. Review **every** hit for that rule, or a documented sample if the count is large, and decide for
   each whether the command it refused was genuinely malformed. Record how many were not.
3. Fill the row: hits, sessions, false positives, decision, date reviewed.
4. Only then raise that rule's exit code in the hook. Enforcement is **per rule** — one noisy rule
   must not hold back the clean ones, and must not be dragged into blocking by them.

A rule with an unreviewed false positive stays in `warn`, independently of every other rule.

## `bash-shape-guard.sh`

Log: `bash-shape-guard.jsonl` · Reader: `scripts/guard-hits.sh` · Review deadline: **2026-10-01**
(`ENFORCE_FROM` in the hook is that deadline, not a trigger — nothing in the hook reads it to decide
anything.)

| Rule | Hits | Sessions | False positives reviewed | Decision | Reviewed on |
|---|---|---|---|---|---|
| `heredoc` | 494 (483 in window) | 13 (11 in window) | 22 sampled + exhaustive machine scan → **2 false positives** ([review][fp-review]) | **stay in warn** — 2 > 0 | 2026-09-14 |
| `multi-mutation` | 0 | 0 | n/a — never fired (≥53 hits masked, see below) | stay in warn | 2026-09-14 |
| `sleep-before-read` | 0 | 0 | n/a — never fired | stay in warn | 2026-09-14 |
| `worktree-escape` | 0 | 0 | n/a — never fired (≥4 hits masked, and blind to `.worktrees/`, see below) | stay in warn | 2026-09-14 |
| `ambiguous-git` | 0 | 0 | n/a — never fired | stay in warn | 2026-09-14 |

[fp-review]: `reports/heredoc-false-positive-review.md` in the consumer's private
workspace task dir
`<workspace>/<project>/workspace/working/2026/09/14/agent-friction-guards-hardening/`
— the burn-in log lives with the project whose sessions produced it, so the
review does too; this row is the repo-side record of what it found.

Window read: 2026-09-01 → 2026-09-14, via
`scripts/guard-hits.sh --from 2026-09-01 --to 2026-09-14`. Log spans
2026-08-28 → 2026-09-14; every record is rule `heredoc`, decision `warn`.

**What the first read found (2026-09-14).** The window is no longer empty. 494 decisions across 13
sessions, from 2026-08-28 to 2026-09-14 — and every single one of them is `heredoc`. One rule
accounts for the entire log, which is itself a finding: the other four were never reached, never
applicable, or both. `heredoc` is a busy and mostly accurate rule with a narrow, reproducible
defect, so it stays in `warn` until the defect is fixed and re-burned-in; the other four have no
evidence to flip on.

The period *before* the counter still has no honest number: the three older rules shipped in warn
mode with their decisions going only to stderr, which reaches the model but is not durably
queryable. Do not backfill these rows from transcripts — the window starts at the counter, and the
counts above are exactly what it recorded. (Transcripts were read during the review, but only to
recover the full text of individual commands the log truncates at 200 chars; no count in this table
comes from them.)

**Why `heredoc` stays in `warn` at 2 false positives out of 494.** Both are the
same defect and both reproduce against the hook today: the regex at
`bash-shape-guard.sh:142` matches any `<<` followed by a word, with no
requirement that it opens a token or sits outside a quoted argument. So a
bit-shift in inline code (`node -e '… const m=1<<b; …'`, a read-only bitmask
decode) and a search string (`grep -n "…\|<<EOF\|…" <file>`, a read-only grep)
are both refused as "this command writes file content through an inline
heredoc", with advice to use `Write` — for commands that write nothing. Under
`block` neither has any way to comply, and the second means an agent cannot grep
a file for the string `<<EOF` at all. The rule is otherwise right: 492 of 494
hits are genuine heredocs, and 62% of the log is `python3 - <<'PY'` doing
`s.replace(old, new)` on one file — the `Edit` tool's job done the long way.
Tighten the regex, add both refused commands as regression tests, re-burn-in,
then fill a fresh row.

### Zero hits is a measurement artefact here, not a clean bill of health

Four rules read 0. None of that zero is evidence they are safe to flip:

- **The `heredoc` rule fires first and `refuse()` exits.** It is the first rule
  in the file (`:139`), so every later rule was unreachable on all 494 logged
  commands. Replaying the whole log through a scratch copy of the hook with the
  heredoc test disabled (the repo file untouched) yields **53 `multi-mutation`**
  and **4 `worktree-escape`** hits — lower bounds, since the log truncates each
  command at 200 chars (`LOG_CMD_MAX`). Those rows read 0 because they never got
  to look, not because the shapes did not occur.
- **`worktree-escape` cannot see a dot-prefixed worktree layout.** Its cwd test
  at `:293` is `*/worktrees/*`, which needs a `/` immediately before
  `worktrees`. A cwd of `<repo>/.worktrees/<tree>` does not match, so
  `worktree_root` is never set and conservatism rule 2 ("no root ⇒ no opinion")
  silences the rule — for 44 of the 494 records. `.claude/worktrees/` and
  `.swarmery/worktrees/` do match; the blind spot is exactly the project-root
  `.worktrees/` convention.
- **`multi-mutation` is silent on read-only chains by construction.**
  `is_mutating()` (`:178-211`) returns false for `git status`, `git log`,
  `grep`, `ls`, so `git status --short; git log --oneline -5; grep -rn TODO src`
  passes while `mkdir -p /tmp/x && touch /tmp/x/a` is caught. That is the
  documented intent, but it means the rule does not cover the compound
  *exploratory* shape the permission classifier also refuses, so its hit count
  will always understate the friction the guard was built to remove.
- **`sleep-before-read` and `ambiguous-git`** have no evidence either way — no
  recorded hits and no structural reason found for or against. "Never fired" is
  the honest statement; it is not "clean".

`worktree-escape` and `ambiguous-git` are newer than the other three and start their own burn-in from
scratch. Per-rule enforcement exists precisely so a new rule can burn in without the older ones
waiting for it, and without it being dragged into blocking by them. Their first burn-in read
(above) produced no hits for either — for `worktree-escape`, demonstrably because it could not see
this project's worktrees rather than because none were escaped.

## `agent-routing-guard.sh`

Log: `agent-routing-guard.jsonl` · Reader: `scripts/guard-hits.sh --log <path>` · Review deadline:
**2026-10-15** (`ENFORCE_FROM` in the hook is that deadline, not a trigger).

The record shape is `bash-shape-guard.jsonl`'s, so the reader works unchanged — it only needs to be
pointed at the other basename:

```bash
scripts/guard-hits.sh --log "$AGENT_WORKSPACE_ROOT/$AGENT_PROJECT/workspace/metrics/agent-routing-guard.jsonl"
```

| Rule | Hits | Sessions | False positives reviewed | Decision | Reviewed on |
|---|---|---|---|---|---|
| `gp-search-routing` | not yet counted | not yet counted | not yet reviewed | warn — new rule, burn-in starts 2026-09-14 | — |

**What the false-positive review has to answer here**, because this rule judges intent from a
description rather than from syntax: of the dispatches it flagged, how many were genuinely
search-shaped work that `Explore` could have done? A brief that says "investigate the failing
migration and fix it" is an implementation task wearing a research verb, and if those dominate the
hits the vocabulary needs narrowing before the exit code is raised — not after.

**Why it ships in warn.** The rule has no burn-in data, and the half of it that carries the real
value — the completion-criterion and step-ceiling requirement in the second paragraph — reaches the
model on stderr in `warn` exactly as it would in `block`. Blocking buys only the re-dispatch, and
buys it at the cost of being wrong in public on a judgement call this rule is making for the first
time.

## Rules retired from this table

None yet. When a rule reaches `block`, leave its row in place with the evidence that justified the
flip — the row is the record of *why*, and deleting it turns an argued decision back into a bare
constant in a script.
