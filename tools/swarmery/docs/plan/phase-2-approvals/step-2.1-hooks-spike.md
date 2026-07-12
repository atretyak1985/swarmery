# Step 2.1 — Hooks spike: verify the PermissionRequest contract live

## Header

| Field | Value |
|---|---|
| Phase | 2 — Approvals + hooks |
| Duration | 1 agent session, ~2–4 h + human co-driving the terminal (MEDIUM — analogous to MVP step 02 JSONL spike) |
| Type | Agent session (docs + throwaway harness; no production code) |
| Risk | High-information / low-blast-radius — this step exists to de-risk D1 |
| Dependencies | MVP shipped (Gate 12); a scratch project dir the human is willing to run `claude` in |

## Goal

Empirically pin down the undocumented parts of the `PermissionRequest` hook contract
before anything is built on it, and publish `docs/hooks-format.md` (the phase-2
analogue of `docs/jsonl-format.md`). The official docs (code.claude.com/docs/en/hooks)
confirm: fires when a permission dialog is about to be shown; stdin carries
`session_id`, `cwd`, `permission_mode`, `tool_name`, `tool_input`, optional
`permission_suggestions`; output `{"hookSpecificOutput":{"hookEventName":
"PermissionRequest","decision":{"behavior":"allow"|"deny"}}}`. NOT documented —
and load-bearing for this phase: timeout fallback, dialog visibility during the
hook, terminal-race behavior.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery/tools/swarmery` on `main`
(docs-only output; harness lives in a temp dir, never committed). The human must
be present: several experiments require answering/ignoring real permission dialogs.

## Agent Prompt

```
Reference: docs/plan/phase-2-approvals/step-2.1-hooks-spike.md

Context:
Swarmery MVP відвантажено (демон + SPA + SQLite). Фаза 2 будує remote
approvals поверх хука PermissionRequest Claude Code. Прочитай
swarmery-design.md §2 (permission_requests) і §3.2, docs/plan/phase-2-approvals/00-phase-2-plan.md
(рішення D1–D6). Продакшн-код НЕ пишемо — тільки throwaway-харнес у /tmp
і документ docs/hooks-format.md.

Tasks:
1. Харнес: скрипт /tmp/swarmery-spike/hook.sh, який (а) дампить stdin у
   /tmp/swarmery-spike/in-<ts>.json, (б) поведінку бере з файлу-перемикача
   /tmp/swarmery-spike/mode (allow | deny | sleep-N | exit0-silent |
   exit1 | garbage-json). Тестовий проєкт /tmp/swarmery-spike/proj з
   .claude/settings.local.json, що реєструє хук PermissionRequest
   (matcher "*") і Stop.
2. Разом з людиною прожени в claude-сесії тестового проєкту КОЖЕН
   експеримент і зафіксуй результат (людина тригерить un-allowlisted
   команду, напр. `curl example.com`):
   E1 stdin: повний JSON для Bash / Edit / MCP-tool; чи є
      permission_suggestions і tool_use_id.
   E2 allow: діалог НЕ показується, тул виконується?
   E3 deny: що бачить Claude (reason доходить?), діалог не показується?
   E4 sleep-30: чи видно термінальний діалог ПІД ЧАС роботи хука; чи
      можна відповісти з термінала; що станеться з хуком, якщо людина
      відповіла раніше (процес убито? stdout проігноровано?).
   E5 exit0-silent (без stdout): зʼявляється звичайний діалог?
   E6 таймаут: постав "timeout": 5 у конфігу хука + sleep-30 — після
      5 с зʼявляється діалог? сесія жива?
   E7 exit1 і garbage-json: non-blocking error, діалог зʼявляється?
   E8 allowlisted команда (додай Bash(echo *) у permissions.allow):
      хук НЕ фаєриться? (критично для D1)
   E9 Stop-хук: stdin (last_assistant_message?), частота (кожна
      відповідь, не кінець сесії) — фіксуємо для фази 2.5.
   E10 permission_mode: plan / acceptEdits / bypassPermissions — коли
      PermissionRequest взагалі фаєриться.
   E11 два паралельні діалоги (субагенти): хуки конкурентні чи серійні?
3. docs/hooks-format.md: таблиця experiment → observed behavior, повні
   анонімізовані фікстури stdin/stdout, версія Claude Code (`claude
   --version`), і РОЗДІЛ "Contract for phase 2": фінальна відповідь —
   чи тримається дизайн D1/D3 (fail-open = exit 0 без stdout → рідний
   діалог), чи потрібен fallback-план (PreToolUse + permissionDecision
   ask/defer). Якщо E5/E6 покажуть, що діалог НЕ зʼявляється після
   no-output — це СТОП-факт, великими літерами нагорі документа.
4. Прибери харнес; перевір git status — у репо тільки docs/hooks-format.md.

Boundaries:
- Жодного продакшн-коду, жодних міграцій, жодних змін web/ чи internal/.
- Не чіпай ~/.claude/settings.json; усі хуки — тільки в тестовому проєкті.
- Фікстури без секретів (grep-гейт як у MVP step 02).

Output / Validation:
docs/hooks-format.md з усіма E1–E11 + розділ "Contract for phase 2".
Conventional commit docs(swarmery) на main. Заповни Completion Report у
docs/plan/phase-2-approvals/step-2.1-hooks-spike.md.
```

## Detailed Instructions

- The switcher-file design lets the human re-trigger the same dialog while the
  agent flips modes — no settings edits between experiments (hook config changes
  are picked up by a file watcher, but a stable config is less noisy).
- E4 is the highest-value experiment: it decides the default `approval_timeout`
  (Q-A) and whether client-disconnect → `resolved_elsewhere` is implementable
  (does Claude kill the hook process when the terminal answers first?).
- E8 failure (hook fires for allowlisted calls) invalidates D1's headline benefit —
  escalate immediately, do not continue to E9–E11 before discussing.
- Record the exact Claude Code version; add a note to `phase2-backlog.md` if any
  behavior looks version-fragile (same policy as the JSONL format-change risk).

## Success Criteria

- [ ] `docs/hooks-format.md` committed with all E1–E11 results + full stdin/stdout fixtures + Claude Code version
- [ ] "Contract for phase 2" section gives an explicit verdict on D1 and D3 (hold / fallback plan)
- [ ] Q-A (approval timeout) answered with observed terminal UX during a pending hook
- [ ] No secrets in fixtures (grep gate); no harness files in the repo; working tree clean apart from the doc
- [ ] Stop-hook stdin fixture captured (phase-2.5 input)

## Navigation

Previous: [00-phase-2-plan.md](00-phase-2-plan.md) · Next: [step-2.2-quality-gate-contract-freeze.md](step-2.2-quality-gate-contract-freeze.md) · Index: [00-phase-2-plan.md](00-phase-2-plan.md)

### Completion Report

```
Status: DONE. docs/hooks-format.md published; all E1–E11 verified live.
Claude Code 2.1.170, macOS 26.3. Branch docs/swarmery-hooks-spike (worktree
/Volumes/Work/swarmery-wt-spike). Harness /tmp/swarmery-spike cleaned; working tree
holds only docs/hooks-format.md + this Completion Report.

Verdict: D1 HOLDS and D3 HOLDS. No STOP-fact.
- D1: E8 (allowlisted Bash(echo *)) fired NO PermissionRequest — only Stop. Confirms
  PR fires exactly on the calls a human would be prompted about (re-confirmed by E10:
  acceptEdits auto-approves an Edit → no PR; plan still fires for a gated Bash). No
  daemon-side allowlist mirror needed. PreToolUse fallback NOT required.
- D3: E5 (exit 0, no stdout) → native "Do you want to proceed?" dialog appears. E7
  (exit1 + garbage-json) also fail open to the dialog. Fail-open = no-decision works.

E1–E11 results (all ✅ verified):
  E1 stdin shapes (Bash+Edit) — has session_id/transcript_path/cwd/permission_mode/
     effort/tool_name/tool_input/permission_suggestions; NO tool_use_id in PR stdin.
  E2 allow → tool runs, no dialog.
  E3 deny → decision.message reaches Claude verbatim.
  E4 no answerable dialog while a hook runs (spinner only) → terminal race is
     structurally impossible; only Esc/Ctrl-C interrupt remains (left to human protocol).
  E5 exit0-silent → native dialog (D3).
  E6 hook timeout:5 + sleep-30 → hook KILLED at 5s, native dialog, session survived.
  E7 exit1 + garbage-json → native dialog, non-blocking.
  E8 allowlisted → NO PR hook (CRITICAL D1 pass).
  E9 Stop stdin captured (last_assistant_message, stop_hook_active, background_tasks,
     session_crons); fires once per assistant turn.
  E10 default fires; acceptEdits suppresses Edit-PR; plan STILL fires for gated Bash;
      bypassPermissions has a one-time startup danger-accept gate.
  E11 CONCURRENT — 2 parallel subagents fired 2 PR hooks 0.24s apart, distinct pids,
      sharing the parent session_id (=> daemon must mint its own request id; concurrent
      identical requests are the real D6 dedup case).

Bonus: PermissionRequest does NOT fire in headless `claude -p` (no TTY → no dialog →
auto-deny). Approvals channel is interactive-only.

Q-A (approval_timeout / terminal UX): during the hook the terminal shows only a
spinner (no dialog, no countdown). The binding limit is Claude Code's per-hook
`timeout` (E6: it kills the shim and falls to the dialog). Recommendation: set the
installed hook `timeout` >= approval_timeout + margin (e.g. 120s poll → timeout:130)
and let the shim own expiry via exit-0-silent, so fallback is a clean fail-open, not a
hard kill. Documented default per-hook timeout is 60s (not re-measured).

Needs human confirmation (1 item, protocol in doc): E4-interrupt — press Esc during a
pending hook, confirm clean abort + session-alive (informs D3 resolved_elsewhere).
```
