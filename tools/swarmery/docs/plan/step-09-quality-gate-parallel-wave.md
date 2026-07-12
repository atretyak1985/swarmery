# Step 09 — QUALITY GATE: parallel-wave verification

## Header

| Field | Value |
|---|---|
| Phase | 3 — Parallel wave (gate) |
| Duration | ~1 h (MEDIUM confidence) |
| Type | Quality gate — human review, per-branch checks |
| Risk | Medium — catching boundary violations here keeps integration cheap |
| Dependencies | Steps 06, 07, 08 complete |

## Goal

Verify each branch is independently green and stayed inside its boundaries before
merging. Review Agent B's screenshots (UX judgment is cheapest now — human review
point #3 from agent-tasks). Collect all `CONTRACT-REQUESTS.md` entries for step 10.

## Automation

Human, with quick shell checks. Screenshot review can happen asynchronously while
A and C are still running — don't serialize on it.

## Agent Prompt

```
Reference: docs/plan/step-09-quality-gate-parallel-wave.md

Context: три worktrees завершили роботу (feat/swarmery-ingest, feat/swarmery-frontend,
feat/swarmery-metrics). Це механічна частина gate; UX-рішення приймає людина.

Tasks (для кожної гілки):
1. go vet ./... && go test ./... (ingest, metrics); cd web && npm run build
   (frontend).
2. Перевір межі дифів: git diff main...HEAD --stat —
   ingest: тільки internal/{ingest,store}, routes.go (wave A), docs/, go.mod;
   frontend: тільки web/;
   metrics: тільки internal/cost, config/, один рядок в ingest, routes.go
   (wave C), cmd/.
3. Перевір docs/ws-protocol.md проти WSMessage у types.ts — поле в поле.
4. Збери в один список усі web/CONTRACT-REQUESTS.md записи з усіх гілок.

Output: таблиця гілка × (тести, межі, контракти) PASS/FAIL + список
contract-requests. Нічого не мерджити. Заповни Completion Report.
```

## Detailed Instructions

Human checklist:

1. Per-branch CI/local checks green (agent report above or run manually).
2. **Screenshots review** (`web/screenshots/`): subagent nesting readable? errors
   loud? mobile layout sane? File UX fixes as notes — small ones go to step 10,
   large ones back to Agent B before merge.
3. Boundary violations found → fix in the offending branch **before** step 10, not
   during integration.
4. Live-tail evidence from Agent A's report: < 3 s lag confirmed.
5. Pricing spot-check: open platform.claude.com pricing, compare 2–3 models against
   `config/pricing.json`.

## Success Criteria

- [ ] All three branches: tests/build green
- [ ] No out-of-boundary diffs (or fixed in-branch)
- [ ] `ws-protocol.md` ≡ frozen `WSMessage`; stats endpoint ≡ frozen `StatsToday`
- [ ] Screenshots reviewed; blocking UX issues resolved in-branch
- [ ] Consolidated CONTRACT-REQUESTS list handed to step 10
- [ ] GATE VERDICT recorded: PASS / FAIL(+branch)

## Navigation

Previous: [step-08-agent-c-metrics.md](step-08-agent-c-metrics.md) · Next: [step-10-integration.md](step-10-integration.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/reviewer: 2026-07-12 / human (screenshots) + controller & subagent (mechanics) · Branch heads: ingest be66787, frontend 6a77ecb, metrics 280b796 · Boundary violations: none (ingest additionally touched cmd/main.go — accepted, needed for --projects-root testability) · UX notes: none blocking — screenshots approved as-is · Verdict: PASS.
Contract-requests handed to step 10: (1) event_appended → {sessionId, event} — ACCEPTED; (2) Turn.model + turns.model column for exact recost — ACCEPTED; (3) session-list aggregates (toolCalls/costUsd/lastAction) — DEFERRED (nice-to-have). Note: pricing.json has a dated action — 2026-09-01 switch claude-sonnet-5 to $3/$15 and run recost.
```
