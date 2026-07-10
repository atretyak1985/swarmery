---
description: End-of-session landing ritual — close finished tasks, keep genuine WIP active, write a NEXT.md handoff, file follow-ups, and trim duplicated memory before you stop
allowed-tools:
  - Bash
  - Read
  - Write
  - Edit
  - Glob
  - Grep
---

# /land — land the plane

Run this **before you stop working** on a session that touched real tasks. It
reconciles the agent-workspace task cards with what this session actually did,
so the *next* session (yours or a teammate's) starts from an accurate state instead
of re-deriving it. "Land the plane" = leave the runway clear: nothing half-flipped,
nothing undocumented, one clear next step.

Work the checklist top to bottom. Each step is a small, verifiable action — do the
work, don't narrate it. Finish with the landing report in step 7.

**Paths used throughout:** the task CLI is the plugin's
`${CLAUDE_PLUGIN_ROOT}/bin/agent-work.sh`. It resolves the workspace from
`AGENT_PROJECT` (+ optional `AGENT_WORKSPACE_ROOT`); without `AGENT_PROJECT` it falls
back to a legacy project-local `.claude-workspace/`.

```bash
AW="${CLAUDE_PLUGIN_ROOT}/bin/agent-work.sh"
WS="${AGENT_WORKSPACE_ROOT:-/Volumes/Work/swarmery-workspace}/${AGENT_PROJECT:-}/workspace"
[ -d "$WS" ] || WS="${CLAUDE_PROJECT_DIR:-.}/.claude-workspace"   # legacy layout
```

---

## 1. Identify the task dir(s) this session worked on

Do not guess — cross-check three signals and keep only the intersection.

```bash
# a) active tasks the workspace knows about
bash "$AW" list active

# b) newest active task dir (both layouts: dated + flat legacy)
find "$WS/working" -mindepth 5 -maxdepth 5 -name README.md 2>/dev/null
find "$WS/working" -mindepth 2 -maxdepth 2 -name README.md 2>/dev/null

# c) files this session actually touched (activity tracker)
today=$(date +%Y%m%d)
for f in /tmp/claude-session-*-${today}.jsonl /tmp/claude-session-${today}.jsonl; do
  [ -f "$f" ] && jq -r 'select(.file!="") | .file' "$f" 2>/dev/null
done | sort -u
```

- The **canonical task id** is `yyyy-mm-dd-slug`; on disk the leaf folder is just the
  `slug` under `working/YYYY/MM/DD/`. `list` prints ids; the `find` gives you dirs.
- Match the touched-files list against what you remember doing this turn. A task that
  only shows up in `list active` but you never touched this session → leave it alone.
- If exactly one active task and it's obviously the one you worked on, proceed with it
  as the primary. If several, decide which is **primary** (still in-flight, gets the
  NEXT.md) vs which are **done**.

## 2. Per task worked on — reconcile card + SUMMARY

For **each** task this session advanced:

- Read its `README.md`. The card's status line is `- **Статус**: active | done | abandoned`
  (the field name the CLI greps for). Normalize any legacy vocab first
  (`in_progress` → `active`, `completed` → `done`) so the card, `list`, and the
  session-start hook agree.
- **Genuinely still in-flight** → keep `Статус: active`. Do not flip it.
- **Actually finished** → do NOT hand-flip the status; step 5 (`agent-work.sh complete`)
  does the `active → done` flip for you. Your job here is to make **SUMMARY.md** true first:
  - Ensure `SUMMARY.md` exists at the task root and reflects the **final** state — mandatory
    per `rules/ALWAYS.md`. Fill the result, changed files (a `git diff --stat` is fine),
    agents involved, deviations from plan, and Follow-ups.
  - `complete` only writes a *skeleton* if `SUMMARY.md` is missing and never overwrites — so
    write/fill it **before** step 5, or the archived task keeps an empty skeleton.

## 3. File discovered-but-unfinished work

- Every loose end goes into the **`Follow-ups`** section of the relevant `SUMMARY.md`
  (for done tasks) or the primary task's NEXT.md (for the in-flight one) — nothing
  discovered should evaporate when the session ends.
- **Jira tickets only when the user explicitly asks.** Creating/commenting is a write op — never
  a side effect of landing, and the read-only **`jira-tasks`** skill will not do it for you. When
  the user says "file these", create the tickets in the project's tracker, then use the
  `jira-tasks` conventions to **link** them: record the keys as a **`Tickets:`** line on the
  task card (`README.md`) — that line is the join key between the workspace task and its Jira
  issues. One line, comma-separated keys, e.g. `- **Tickets**: <PROJECT-KEY>-142, <PROJECT-KEY>-143`.

## 4. Write NEXT.md for the primary in-flight task

At the **task root** of the primary still-active task, write `NEXT.md` — **3–8 lines**,
optimized for a cold-start reader:

- The **single highest-priority** next step (one action, not a backlog).
- **Exact pointers**: the file path(s) and the command to run — no "look around for".
- **State a fresh session can't cheaply rediscover**: open MR/PR numbers, required merge
  order, what it's blocked-on, the branch name, any digest/pin that matters.

The **newest** `NEXT.md` under `working/` is the canonical cold-start pointer for the
next session (surface it via a session-start hook if the project wires one) — so keep it
current and keep it singular. If a stale NEXT.md exists on a task you just completed, it
moves to `archive/` with the task in step 5, so it won't shadow the live one.

Example NEXT.md:

```markdown
# NEXT

- DO: merge the deploy-repo MR FIRST, then the app MR (cross-repo order matters).
- Branch: feat/nav-target-visibility (app repo), already pushed.
- Verify after: run the e2e smoke flow locally, then on the staging environment.
- Blocked-on: nothing — deploy MR approved, app MR waiting on the staging deploy.
- Do NOT: strip the new inbound fields from the Zod schema (see SUMMARY Follow-ups).
```

## 5. Archive the done tasks

For each task confirmed **done** (SUMMARY.md filled in step 2):

```bash
bash "$AW" complete <task-id>   # e.g. 2026-07-06-land-command
# --latest is accepted if it is unambiguously the task you mean
```

`complete` flips `Статус: active → done`, stamps the completion date, writes a SUMMARY
skeleton only if none exists, moves the dir to `archive/YYYY/MM/DD/<slug>/`, and regenerates
the index. Then regenerate explicitly to be safe (idempotent):

```bash
bash "$AW" index
```

Do **not** run `complete` on a task you're keeping active.

## 6. Trim memory that duplicates the card

If a session memory file (`MEMORY.md` + its `memory/*.md`) now duplicates state that lives on
the task card — MR/PR numbers, statuses, branch names — **trim the memory entry to point at the
card** instead of carrying a second copy that will drift. Keep the memory line as a pointer:
"see task card `working/YYYY/MM/DD/<slug>/` / archived `archive/YYYY/MM/DD/<slug>/`". Convert any
relative dates to absolute while you're in there (`rules/ALWAYS.md` memory hygiene), and keep
the `MEMORY.md` index in sync.

## 7. Landing report

End with a compact report — checklist, not prose:

```
Landed:
  Closed (→ archive):  <task-id>, <task-id>        (or "none")
  Kept active:         <task-id>                    (or "none")
  NEXT.md written:     working/YYYY/MM/DD/<slug>/NEXT.md   (or "none — nothing in-flight")
  Follow-ups filed:    <n> in SUMMARY  ·  Jira: <PROJECT-KEY>-…  (or "none / not confirmed")
  Memory trimmed:      <file> → pointer             (or "n/a")
```

---

## Guardrails

- **Never** flip a task to done that you didn't actually finish just to close the loop —
  a false `done` archives live work out of the next session's view.
- **Never** open Jira tickets without explicit user confirmation (step 3).
- **One** NEXT.md that matters — the newest wins; don't leave two live ones competing.
- SUMMARY.md before `complete`, always — an empty archived skeleton is a defect (`rules/ALWAYS.md`).

## Related

- `/dashboard` — session stats + active/done task lists (read-only; good pre-flight for `/land`)
- `${CLAUDE_PLUGIN_ROOT}/bin/agent-work.sh` — the task CLI this ritual drives (`init` / `list` / `complete` / `index`)
- `jira-tasks` skill — read-only Jira access + the `Tickets:` card-line join-key convention
- `rules/ALWAYS.md` — workspace-artifacts + memory-hygiene gates this ritual enforces
