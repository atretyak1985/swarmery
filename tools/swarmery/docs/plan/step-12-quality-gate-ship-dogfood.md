# Step 12 — QUALITY GATE: ship on live data + dogfooding kickoff

## Header

| Field | Value |
|---|---|
| Phase | 4 — Integration, install, ship (final gate) |
| Duration | ~1 h gate + ongoing dogfooding (HIGH confidence for the gate itself) |
| Type | Quality gate — HUMAN (T4 dogfooding starts here) |
| Risk | Low — verification only |
| Dependencies | Step 11 |

## Goal

Final acceptance of the MVP on real data and real usage, then start dogfooding:
observe the *next* phase's work (approvals + hooks) inside Swarmery itself, capturing
the Phase 2 backlog. This is human review point #4 from agent-tasks.

## Automation

Human. No agent prompt required for the gate; the backlog capture below is a manual
habit, not a session.

## Agent Prompt

```
Reference: docs/plan/step-12-quality-gate-ship-dogfood.md

(Не потрібен для самого gate — чеклист виконує людина. Якщо хочеш
автоматизувати перевірки 1-3 нижче, дай агенту цей файл і попроси
виконати команди та звести PASS/FAIL таблицю; рішення verdict — за людиною.)
```

## Detailed Instructions

Gate checklist:

1. **Autostart**: `swarmery install` → `launchctl kickstart -k gui/$(id -u)/com.swarmery.daemon`
   (or re-login) → dashboard alive at `http://localhost:7777` with no manual start.
2. **Live capture**: start a real `claude` session in any project → appears in
   Overview < 3 s.
3. **Depth check**: open 3–4 session details across different projects (bloomblum,
   Skygor, swarmery): timeline complete, subagents nested, diffs render, cost shown.
4. **Cost sanity**: today's $ total plausible against expectations from the sessions run.
5. **MVP success criteria**: walk the checklist in [00-plan.md](00-plan.md) — all boxes.

Dogfooding kickoff (T4):

- Keep Swarmery open while running the next real work sessions (ideally the Phase 2
  approvals+hooks planning/build).
- Log every gap in `docs/plan/phase2-backlog.md` in the swarmery repo: missing
  timeline info, wrong statuses, filter needs, UX friction. Each entry:
  `- [screen] what was missing — why it mattered`.
- This backlog is the required input for the Phase 2 (Approvals) plan — per design
  doc §4 and the roadmap table in 00-plan.md.

## Success Criteria

- [x] Daemon survives kickstart/re-login; dashboard live without manual start
- [x] Live session visible < 3 s; 3–4 real session details verified
- [x] All MVP success-criteria boxes in 00-plan.md checked
- [x] `docs/plan/phase2-backlog.md` created (may start empty with the entry template)
- [x] GATE VERDICT recorded: SHIPPED / FAIL(+blocker)

## Navigation

Previous: [step-11-install-daemon.md](step-11-install-daemon.md) · Next: — (MVP complete; Phase 2 Approvals gets its own plan) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/reviewer: 2026-07-12 / owner · Verdict: SHIPPED · Backfill totals: 457 files / 114,687 lines / 11 projects / 115 sessions / 24,813 events, 0 errors, 9.7s; live-tail lag 134–359ms on the build session itself; today $45.05 (partial-sum warning on 2 <synthetic> turns) · First dogfooding notes: phase2-backlog.md seeded with 3 carried candidates · Phase-2 plan trigger date: when backlog has enough real entries (owner decides)
```
