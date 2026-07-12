# Step 03 — QUALITY GATE: human review of the JSONL format spec

## Header

| Field | Value |
|---|---|
| Phase | 1 — Bootstrap & JSONL spike (gate) |
| Duration | 30–60 min (HIGH confidence — reading one doc) |
| Type | Quality gate — HUMAN, critical, blocking |
| Risk | High if skipped — schema/format mismatch would poison every later step |
| Dependencies | Step 02 |

## Goal

Catch any mismatch between the observed JSONL format and the SQLite schema in
`swarmery-design.md` §2 **now**, while fixing it costs one doc edit instead of a
parser rewrite. This is human review point #1 from the agent-tasks flow.

## Automation

Human review. Optionally one short agent session for the mechanical checks (prompt
below), but the mapping judgment call is yours.

## Agent Prompt

```
Reference: docs/plan/step-03-quality-gate-format-review.md

Context: репозиторій Swarmery після spike (step 02). Це механічна частина
quality gate; рішення по мапінгу приймає людина.

Tasks:
1. Перевір, що кожен рядок кожного fixture — валідний JSON:
   for f in testdata/fixtures/*.jsonl; do node -e "require('fs').readFileSync(process.argv[1],'utf8').trim().split('\n').forEach((l,i)=>{try{JSON.parse(l)}catch(e){console.error(process.argv[1]+':'+(i+1));process.exit(1)}})" "$f"; done
2. grep -rinE 'api[_-]?key|token|secret|password|sk-|ghp_|AKIA' testdata/fixtures/ — має бути пусто.
3. Перевір, що fixture з субагентом реально містить Task tool_use і його завершення.
4. Звір мапінг-таблицю в docs/jsonl-format.md проти DDL у swarmery-design.md §2:
   перелічи поля схеми, для яких у форматі НЕ знайшлося джерела, і поля формату,
   яким нема місця в схемі.

Output: короткий звіт PASS/FAIL по пунктах 1-3 + список розбіжностей з п.4.
Нічого не виправляй сам. Заповни Completion Report.
```

## Detailed Instructions

Human checklist (the actual gate):

1. Read `docs/jsonl-format.md` end-to-end; scrutinize the **mapping table** and
   **Open questions**.
2. For each schema field with no observed source (e.g., `turns.tokens_*`,
   `events.duration_ms`, `sessions.git_branch`) decide: derive it, make it nullable,
   or fix the design doc.
3. If the mapping diverges from `swarmery-design.md` §2 — **edit the design doc now**
   and commit (`docs: align schema with observed JSONL format`). Additive-only from
   step 05 onward.
4. Confirm the subagent fixture genuinely exercises a Task call chain.
5. Decide any Open questions that block the parser; the rest may carry forward.

## Success Criteria

- [x] All fixture lines valid JSON; secrets grep clean; subagent fixture confirmed
- [x] Every table in design §2 core (projects/sessions/turns/events/file_changes) has a confirmed or explicitly-nullable source in the mapping
- [x] Design-doc amendments (if any) committed before step 04 starts
- [x] No unresolved Open question that blocks parser implementation
- [x] GATE VERDICT recorded below: PASS / FAIL(+reason)

## Navigation

Previous: [step-02-jsonl-spike.md](step-02-jsonl-spike.md) · Next: [step-04-vertical-slice.md](step-04-vertical-slice.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/reviewer: 2026-07-12 / human (PASS with recommendations, applied by agent)
Verdict: PASS
Design-doc edits: C1-C6 comments, turns.message_id added, FK nullability notes,
                  backfill scope >=2.1, Q11 watch-experiment note
Open questions carried: Q2-Q10 (Q11 must resolve before step 06)
```
