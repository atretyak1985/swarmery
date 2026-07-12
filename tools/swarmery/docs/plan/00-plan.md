# Swarmery Control Plane — Implementation Plan (v2)

> Supersedes `../swarmery-implementation-plan.md` (draft of 2026-07-12 14:17).
> Executors: follow steps in order; each phase ends with a quality gate that MUST pass
> before the next phase starts. Agent prompts are copy-paste-ready.

## Scope

Build **Swarmery** — a local control plane for monitoring Claude Code agent sessions:
a single Go daemon + embedded React SPA that parses JSONL transcripts from
`~/.claude/projects/`, stores them in SQLite (`~/.swarmery/swarmery.db`), and serves
a live dashboard at `http://localhost:7777`. This plan covers **Phase 1 (MVP,
observation only)** of the design doc plus daemon auto-start; approvals, tasks queue,
agents registry, analytics, and evals are later phases (see Roadmap).

- **Location**: `tools/swarmery/` inside the swarmery repo (module
  `github.com/atretyak1985/swarmery/tools/swarmery`). Owner decision 2026-07-12
  (supersedes the original R1 separate-repo choice): swarmery is part of swarmery.
  The marketplace no-build-step rule applies to `plugins/**`; `tools/swarmery` has
  its own build and a dedicated CI workflow (`.github/workflows/swarmery-ci.yml`).
- **Tech stack (frozen)**: Go 1.22+ (verified 1.22.3), modernc.org/sqlite (no CGO),
  fsnotify, github.com/coder/websocket, net/http ServeMux; React 18 + TypeScript
  strict + Vite + Tailwind, embedded via go:embed. No ORM, no frameworks.
- **Sources of truth**: `../swarmery-design.md` (schema + UI), `../swarmery-ui-mockup.html`
  (design language), `../swarmery-agent-tasks.md` (session decomposition T0–T4).

## Key deliverables

1. `docs/jsonl-format.md` — verified transcript format spec + anonymized fixtures
2. Working daemon: backfill + live tail of all `~/.claude/projects` transcripts
3. Dashboard: Overview + Sessions list + Session detail (Timeline, Diffs tabs)
4. Cost accounting per turn/session/day from `config/pricing.json`
5. `swarmery install|uninstall|status` — launchd auto-start on macOS
6. `docs/plan/phase2-backlog.md` — dogfooding notes feeding the Approvals phase

## Phases & steps

| Phase | Steps | Branch | Wall-clock estimate |
|---|---|---|---|
| 1. Bootstrap & JSONL spike | [01](step-01-bootstrap-repo.md), [02](step-02-jsonl-spike.md), [03 GATE](step-03-quality-gate-format-review.md) | main | 0.5 day (MEDIUM — spike is one agent session + one human review) |
| 2. Vertical slice & contract freeze | [04](step-04-vertical-slice.md), [05 GATE](step-05-quality-gate-contract-freeze.md) | main | 0.5–1 day (MEDIUM — analogous to prior scaffold sessions) |
| 3. Parallel wave A+B+C | [06](step-06-agent-a-ingest.md), [07](step-07-agent-b-frontend.md), [08](step-08-agent-c-metrics.md), [09 GATE](step-09-quality-gate-parallel-wave.md) | feat/swarmery-ingest, feat/swarmery-frontend, feat/swarmery-metrics (worktrees) | 1 day parallel (LOW-CONFIDENCE — three concurrent sessions, human review async) |
| 4. Integration, install, ship | [10](step-10-integration.md), [11](step-11-install-daemon.md), [12 GATE](step-12-quality-gate-ship-dogfood.md) | main | 0.5–1 day (MEDIUM) |

**Total: ~3–4 working days wall-clock** (basis: agent-session throughput on analogous
scaffolding tasks; parallel-wave estimate is an expert guess — [LOW-CONFIDENCE]).

Execution flow:

```
01 bootstrap ─► 02 spike ─► [GATE 03: format review] ─► 04 slice ─► [GATE 05: freeze] ─► 06 A ┐
                                                                                        07 B ├─► [GATE 09] ─► 10 integrate ─► 11 install ─► [GATE 12: ship + dogfood]
                                                                                        08 C ┘
```

## Architecture overview

Single binary: `internal/ingest` (scanner → fsnotify watcher → tolerant JSONL parser
→ dedup) writes append-only `events` + `sessions/turns/file_changes` to SQLite (WAL);
an internal event bus fans out to `internal/api` (REST + WS `/api/ws`);
`internal/cost` enriches turns with USD cost; the React SPA is embedded via go:embed.
The migration creates the **full** schema from the design doc §2 (all future-phase
tables) so later phases are additive-only. `agents`/`skills` tables are created
**before** `events` (FK ordering). Service table: `file_offsets(file_path,
byte_offset, inode)` for incremental tail across restarts.

## Quality gates

| Gate | Step | What it protects |
|---|---|---|
| Format review (human, critical) | 03 | JSONL→schema mapping mismatch caught before any code |
| Contract freeze | 05 | DB schema additive-only; `types.ts` + `/api/stats/today` shape frozen before parallel work |
| Parallel-wave verification | 09 | Each branch green + UX screenshots reviewed before merge |
| Ship & dogfood | 12 | Live-data end-to-end run; launchd survival; phase-2 backlog captured |

## Success criteria (MVP done)

- [x] `make build` produces one binary with embedded SPA; `go vet`, `go test`, `npm run build` green
- [x] Full backfill of this machine's history (13 projects) completes with 0 pipeline crashes
- [x] New live `claude` session appears in Overview in < 3 s without page reload
- [x] Repeated full backfill creates 0 duplicate events (dedup_key)
- [x] Session detail shows nested subagent block and unified diffs on a real session
- [x] `/api/stats/today` returns tokens + cost; unknown model → cost NULL (never 0)
- [x] `swarmery install` → daemon running after `launchctl kickstart`; `uninstall` clean
- [x] No secrets in committed fixtures (grep gate clean)

## Fixes vs the superseded draft (carried R1–R7 + new F1–F8)

Kept from draft: ~~R1 separate repo~~ (superseded 2026-07-12 by owner decision — lives in `swarmery:tools/swarmery`), R2 module `atretyak1985`, R3 mockup as frontend
reference, R4 install task, R5 fixture secret scan, R6 FK migration ordering,
R7 `--port`/`SWARMERY_PORT`. New in v2:

| # | Fix |
|---|---|
| F1 | Draft's bootstrap copied docs from `/Volumes/Work/swarmery/temp_files/` — path does not exist. Corrected to the task dir `/Volumes/Work/swarmery-workspace/swarmery/workspace/working/2026/07/12/swarmery-control-plane/docs/` |
| F2 | `GET /api/projects` added to the T1 slice (mvp-prompt requires it; frontend project filter needs it; no downstream task owned it) |
| F3 | Dependency-budget conflict resolved: mvp-prompt allowed golang-migrate, but T1's "max 3 non-stdlib deps" forbids it → own embedded-SQL migration runner, mandated explicitly |
| F4 | `/api/stats/today` response shape frozen in `types.ts` at Gate 05 (Agent B consumes it, Agent C implements it — draft left the contract implicit → drift risk) |
| F5 | Minimal GitHub Actions CI (vet+test+build) added at T1 to protect main during the parallel wave |
| F6 | Session status enum: MVP computes only `active|idle|completed`; `waiting_approval|killed` reserved for Phase 2 — stated explicitly so ingest doesn't invent them |
| F7 | Session-detail tabs Context and Tree explicitly deferred (design §3.3 lists 4 tabs; MVP ships 2) |
| F8 | Both Agent A (`/api/ws`) and Agent C (`/api/stats/today`) touch `internal/api` — draft said only A may; route registration is structured in T1 to keep their merge conflict-free |

## Top risks

| Risk | L | I | Mitigation |
|---|---|---|---|
| JSONL format undocumented / changes between CC versions | High | High | Spike-first (step 02), tolerant parser (`type='unknown'` + raw payload), human gate 03 |
| Contract drift across 3 parallel branches | Med | High | Gate 05 freeze, `web/CONTRACT-REQUESTS.md`, fixed merge order ingest→metrics→frontend, CI on main |
| Secrets leaked into committed fixtures | Med | High | Mandatory grep gate in steps 02 & 03 |
| Stale/wrong model pricing → wrong $ totals | Med | Med | Web-verified pricing.json; unknown model → NULL + warn; `swarmery recost` backfill |
| fsnotify misses events on macOS | Med | Low | 2 s fallback rescan + offset-based tail |
| SQLite write contention (daemon + CLI ingest simultaneously) | Low | Med | WAL mode; `recost`/`ingest` CLI warns if daemon is running |

## Progress checklist

- Phase 1: [x] 01 [x] 02 [x] 03-GATE
- Phase 2: [x] 04 [x] 05-GATE
- Phase 3: [x] 06 [x] 07 [x] 08 [x] 09-GATE
- Phase 4: [x] 10 [x] 11 [x] 12-GATE — SHIPPED 2026-07-12

## How to use

1. Human runs step 01 (bootstrap) directly — 15 min of shell commands.
2. For each agent step: open a fresh Claude Code session in the stated directory and
   paste the block from the step's **Agent Prompt** section verbatim.
3. At each GATE step, the human performs the checklist; do not proceed on a red gate.
4. Executors append a filled Completion Report to the bottom of their step file
   (these live in the swarmery repo copy under `docs/plan/` after step 01).

## Roadmap after MVP (design doc §4 — separate plans later)

2. Approvals (`permission_requests` + PreToolUse hooks) ← backlog from Gate 12 ·
2.5. Reporter + Reports (narratives, self-filling checklists, live view, weekly
digest → [phase-2.5-reporter-agent-d.md](phase-2.5-reporter-agent-d.md)) ·
3. Agents registry read-only · 4. Editor + git versioning · 5. Tasks queue
(headless `claude -p`) · 6. Rollups/Analytics, then Evals.

## Files Analyzed

- `/Volumes/Work/swarmery-workspace/swarmery/workspace/working/2026/07/12/swarmery-control-plane/` — `README.md`, `logs/{sessions,agents}.md`, and `docs/{swarmery-design.md, swarmery-agent-tasks.md, swarmery-mvp-prompt.md, swarmery-ui-mockup.html, swarmery-implementation-plan.md}` (originally analyzed at `…/swarmery/temp/swarmery-control-plane/`, since relocated here)
- `/Volumes/Work/swarmery/CLAUDE.md` (marketplace hard rules → R1), `/Volumes/Work/swarmery/plugins/core/skills/refactor-plan/SKILL.md`
- Environment verified 2026-07-12: Go 1.22.3, Node v24.1.0, 13 dirs in `~/.claude/projects/`, `/Volumes/Work/swarmery/tools/swarmery` free, remote `github.com/atretyak1985/*`, `/Volumes/Work/swarmery/temp_files` absent (→ F1)

Phase-ordering alternatives considered: (a) single monolithic session per
`swarmery-mvp-prompt.md` — rejected: two known-risky points (format spike, parser)
need human gates, and one session cannot parallelize; (b) parallelize from day one —
rejected: no frozen contract yet; (c) chosen: spike → slice → freeze → 3-way parallel
→ integrate, matching `swarmery-agent-tasks.md` with fixes F1–F8.
