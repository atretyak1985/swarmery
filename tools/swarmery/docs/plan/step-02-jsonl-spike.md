# Step 02 — T0 spike: JSONL transcript format (no code)

## Header

| Field | Value |
|---|---|
| Phase | 1 — Bootstrap & JSONL spike |
| Duration | 1 agent session, ~1–2 h (MEDIUM confidence — read-and-document task) |
| Type | Agent session (research/docs only) |
| Risk | Medium — everything downstream depends on this doc being right |
| Dependencies | Step 01 |

## Goal

Produce `docs/jsonl-format.md` — an evidence-based spec of the Claude Code transcript
format — plus 3 anonymized, secret-free fixtures. This is the single riskiest unknown
of the project: the format is internal and undocumented, so the parser must be written
against observed reality, never from memory.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery/tools/swarmery`. Read-only access to
`~/.claude/projects/` (13 project dirs verified present on this machine).

## Agent Prompt

```
Reference: docs/plan/step-02-jsonl-spike.md

Context:
Репозиторій Swarmery, дизайн — swarmery-design.md (прочитай розділи 1-2).
Claude Code зберігає транскрипти сесій у ~/.claude/projects/<slug>/<id>.jsonl.
Формат внутрішній і недокументований. Це spike-задача: тільки дослідження
і документація, жодного production-коду.

Tasks:
1. Знайди в ~/.claude/projects 3-4 РІЗНІ за характером сесії: одна з
   викликами субагентів (Task), одна з великою кількістю tool calls
   (Edit/Bash), одна коротка проста. Подивись перші й останні ~200 рядків
   кожної + вибірково середину.
2. Створи docs/jsonl-format.md з фактичною структурою:
   - типи записів (message types) і їх поля
   - як виглядає tool_use / tool_result, де назва tool і аргументи
   - як розпізнати виклик субагента (Task, subagent_type) і його завершення
   - як розпізнати використання skill
   - де лежать usage-токени (in/out/cache_read/cache_write) і назва моделі
   - як звʼязані рядки між собою (uuid, parentUuid, порядок)
   - як виглядають зміни файлів (Edit/Write/MultiEdit) — що є в аргументах
   - session-level поля: cwd, git branch, version
   - все незрозуміле — в окрему секцію "Open questions"
3. Створи testdata/fixtures/: 3 скорочені АНОНІМІЗОВАНІ .jsonl файли
   (заміни реальні шляхи/код/назви на плейсхолдери, збережи структуру
   1-в-1). Один обовʼязково з субагентом.
4. Додай у docs/jsonl-format.md мапінг "рядок JSONL → таблиці зі
   swarmery-design.md" (sessions / turns / events / file_changes):
   який тип запису куди лягає, що йде в payload; окремо — з чого
   виводиться turns.seq (UNIQUE(session_id, seq) у схемі).

Boundaries:
- Нічого не пиши в ~/.claude/ — тільки читання.
- Жодного .go/.ts коду. Тільки docs/ і testdata/.
- НЕ вигадуй поля, яких не бачив у реальних файлах. Якщо чогось не
  знайшов (напр. skill-виклик) — чесно зазнач в Open questions.
- БЕЗПЕКА FIXTURES: транскрипти можуть містити секрети (API-ключі,
  токени у виводі Bash). Перед фіналізацією прожени по fixtures
  grep -inE 'api[_-]?key|token|secret|password|sk-|ghp_|AKIA' і
  вичисти всі збіги плейсхолдерами. Жодного реального ключа в git.

Output / Validation:
docs/jsonl-format.md повний за пунктами вище; fixtures валідні (кожен рядок —
валідний JSON); grep на секрети чистий. Наприкінці: 10-рядкове резюме — що у
форматі несподіваного і які Open questions лишились. Закоміть
(docs: JSONL transcript format spike + anonymized fixtures) і заповни
Completion Report у docs/plan/step-02-jsonl-spike.md.
```

## Detailed Instructions

- Good spike candidates: `~/.claude/projects/-Volumes-Work-swarmery/`,
  `-Volumes-Work-bloomblum/`, `-Volumes-Work-Skygor/` — pick the most recent, largest
  files (`ls -laS`).
- The design-doc mapping (§2) the spike must validate: `sessions.session_uuid` ←
  file name; `turns` ← user/assistant messages; `events` ← tool_use/tool_result/
  Task/Skill; `file_changes` ← Edit/Write/MultiEdit arguments; `dedup_key` ←
  `session_uuid:line_number`.
- If the observed format contradicts the schema (e.g., token usage lives per-message
  not per-turn), record it in the mapping section — Gate 03 decides schema fixes.

## Success Criteria

- [ ] `docs/jsonl-format.md` covers all 8 bullet areas + table mapping + Open questions
- [ ] 3 fixtures in `testdata/fixtures/`, every line parses as JSON (`node -e` check in Gate 03)
- [ ] ≥1 fixture contains a real subagent (Task) call chain
- [ ] `grep -rinE 'api[_-]?key|sk-ant|ghp_|AKIA|password' testdata/fixtures/` → 0 hits
- [ ] Zero `.go`/`.ts` files created; `~/.claude/` untouched

## Navigation

Previous: [step-01-bootstrap-repo.md](step-01-bootstrap-repo.md) · Next: [step-03-quality-gate-format-review.md](step-03-quality-gate-format-review.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/agent: 2026-07-12 / Claude Code agent (resumed after prior agent crash mid-task)
Commit SHA: 38b132a37fe5baa800821b0103f84d987d25233e
Sessions examined: 4 in depth (swarmery/9f22596e… subagent session + its subagents/ dir;
  bloomblum/948a823d… tool-heavy; Skygor/7f4fbd6b… long interactive; swarmery/2019f909…
  short) + corpus-wide scans over 115 files / 13 project dirs (v2.1.111–2.1.197)
Open questions count: 11
Surprises:
  - Subagent tool is named "Agent", not "Task"; MultiEdit never observed (0/115 files).
  - Sidechains are SEPARATE files (<sessionId>/subagents/agent-<17hex>.jsonl), never
    inline; isSidechain is false on every main-transcript line in the whole corpus.
  - One API response is split across N JSONL lines (one content block each) with usage
    duplicated verbatim on every line — naive summation inflates tokens ~2-4x.
  - No session header and no session-end record; cwd/gitBranch/version repeat per line.
  - 13 top-level record types incl. undocumented checkpoints (last-prompt, mode,
    permission-mode, ai-title) re-emitted up to ~149x per file.
  - dedup_key = session_uuid:line_number breaks for sidechains/split lines — use uuid.
```
