# Swarmery — Data Model & UI Structure

Дизайн-документ для control plane агентної системи (Claude Code).
Мета: схема SQLite для MVP-спостереження, яка вже закладає майбутні шари —
approvals, чергу задач, менеджмент агентів/скілів, метрики якості та evals.

---

## 1. Принципи схеми

1. **Append-only event log як ядро.** Все, що відбувається в сесіях, — це події.
   Таблиця `events` з типізованим `payload` (JSON) — толерантна до змін формату
   JSONL Claude Code: незнайомі поля просто лягають у payload, парсер не падає.
2. **Атрибуція через nullable FK.** Кожна подія може посилатись на `agent_id`
   та `skill_id`. Метрики по агентах — це просто агрегати по events, без
   окремої "аналітичної" схеми на старті.
3. **Версіонування через content hash + git.** Агенти і скіли — файли на диску.
   Кожна зміна фіксується як версія (hash вмісту, опційно git SHA). Це дає
   rollback і A/B порівняння промптів пізніше — без зміни схеми.
4. **Rollup-таблиці замість важких запитів.** Дашборд не сканує мільйони подій:
   демон раз на N хвилин оновлює `daily_rollups`.
5. **Два джерела, одна дедуплікація.** Події приходять з hooks (live) і з
   парсера JSONL (backfill). Ключ дедуплікації: `uuid` запису, якщо він є;
   для рядків без uuid — SHA-256 від (шлях файлу + вміст рядка). Див. комент
   до `events.dedup_key`.

---

## 2. SQLite схема (DDL)

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- ============ Реєстр проєктів і сесій ============

CREATE TABLE projects (
    id            INTEGER PRIMARY KEY,
    path          TEXT NOT NULL UNIQUE,      -- /Volumes/Work/bloomblum
    slug          TEXT NOT NULL,             -- як у ~/.claude/projects/
    name          TEXT,                      -- людська назва (редагується в UI)
    first_seen    TEXT NOT NULL,             -- ISO 8601
    last_activity TEXT,
    archived      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    session_uuid TEXT NOT NULL UNIQUE,       -- з імені JSONL-файлу
    parent_uuid  TEXT,                       -- resume/fork ланцюжки; у форматі JSONL
                 -- джерела не спостерігалось (C4) — у MVP лишається NULL
    model        TEXT,                       -- claude-fable-5 ...
    git_branch   TEXT,
    cwd          TEXT,
    status       TEXT NOT NULL DEFAULT 'active',
                 -- active | waiting_approval | idle | completed | killed
                 -- MVP обчислює лише active|idle|completed евристикою (C5):
                 -- mtime файлу / фінальний system:turn_duration;
                 -- waiting_approval|killed зарезервовані для hooks (Phase 2)
    started_at   TEXT NOT NULL,
    ended_at     TEXT,                       -- nullable; = timestamp останнього рядка,
                 -- щойно сесія стала неактивною (session_end запису не існує)
    title        TEXT,                       -- перший промпт, обрізаний
    source       TEXT NOT NULL DEFAULT 'jsonl'  -- jsonl | hook | both
);
CREATE INDEX idx_sessions_project ON sessions(project_id, started_at DESC);
CREATE INDEX idx_sessions_status  ON sessions(status);

-- ============ Turns і Events (ядро) ============

CREATE TABLE turns (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL,            -- порядок у сесії; НЕ user/assistant
                -- чергування: один user-промпт породжує N assistant API-повідомлень (C2);
                -- seq виводиться групуванням записів за promptId (fallback: правило
                -- user-message-opener з docs/jsonl-format.md)
    role        TEXT NOT NULL,               -- user | assistant
    message_id  TEXT,                        -- API message.id (assistant-turns) —
                -- ключ дедуплікації usage / ідемпотентності; NULL для user-turns
    started_at  TEXT NOT NULL,
    ended_at    TEXT,
    -- usage дублюється дослівно на кожному з N JSONL-рядків однієї API-відповіді (C1):
    -- перед агрегацією токени ОБОВ'ЯЗКОВО дедуплікувати за message.id;
    -- user-turns не мають usage — колонки лишаються NULL
    tokens_in   INTEGER,
    tokens_out  INTEGER,
    tokens_cache_read  INTEGER,
    tokens_cache_write INTEGER,
    cost_usd    REAL,                        -- розрахунок по прайсу моделі
    UNIQUE(session_id, seq)
);

CREATE TABLE events (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    turn_id     INTEGER REFERENCES turns(id),
    ts          TEXT NOT NULL,
    type        TEXT NOT NULL,
        -- tool_call | subagent_start | subagent_stop | skill_use
        -- | file_change | permission_request | permission_resolved
        -- | error | test_run | commit | user_prompt | session_end
        -- subagent_start/stop не існують як JSONL-записи — виводяться з
        -- tool_use name="Agent" та його matching tool_result (C6);
        -- sidechain-транскрипти лежать у <sessionId>/subagents/agent-<id>.jsonl,
        -- join до батьківської події через meta.json toolUseId.
        -- NOT ingested у MVP (ignore / лише payload, нових типів не додаємо):
        -- pr-link, checkpoint-записи, system-субтипи крім
        -- api_error/turn_duration/compact_boundary, attachments
    tool_name   TEXT,                        -- Bash, Edit, Agent, Skill...
    agent_id    INTEGER REFERENCES agents(id),   -- nullable: FK-резолюція лише для
                -- project-local агентів; вбудовані subagent-типи (Explore,
                -- general-purpose) не є рядками реєстру — NULL,
                -- ім'я лишається в payload (agentType)
    skill_id    INTEGER REFERENCES skills(id),   -- nullable: plugin-скіли не є рядками
                -- реєстру — NULL, ім'я лишається в payload (attributionSkill)
    parent_event_id INTEGER REFERENCES events(id), -- дерево делегування
    status      TEXT,                        -- ok | error | denied | timeout
    duration_ms INTEGER,
    payload     TEXT,                        -- JSON: сирі деталі події
    dedup_key   TEXT UNIQUE                  -- `uuid` запису, якщо він є (C3);
                -- для рядків без uuid — SHA-256 від (шлях файлу + вміст рядка).
                -- Sidechain-файли мають власний простір uuid; ключ мусить бути
                -- глобально унікальним across main+sidechain файлів
);
CREATE INDEX idx_events_session ON events(session_id, ts);
CREATE INDEX idx_events_agent   ON events(agent_id, ts);
CREATE INDEX idx_events_type    ON events(type, ts);

-- ============ Зміни файлів (diff-стрічка, out-of-scope детектор) ============

CREATE TABLE file_changes (
    id           INTEGER PRIMARY KEY,
    event_id     INTEGER NOT NULL REFERENCES events(id),
    session_id   INTEGER NOT NULL REFERENCES sessions(id),
    file_path    TEXT NOT NULL,
    change_type  TEXT NOT NULL,              -- create | edit | delete | rename
    additions    INTEGER,
    deletions    INTEGER,
    diff         TEXT,                       -- unified diff (або ref на blob)
    out_of_scope INTEGER NOT NULL DEFAULT 0  -- поза заявленим scope задачі
);
CREATE INDEX idx_fc_session ON file_changes(session_id);
CREATE INDEX idx_fc_path    ON file_changes(file_path);

-- ============ Approvals (шар 2: втручання) ============

CREATE TABLE permission_requests (
    id           INTEGER PRIMARY KEY,
    session_id   INTEGER NOT NULL REFERENCES sessions(id),
    event_id     INTEGER REFERENCES events(id),
    tool_name    TEXT NOT NULL,
    request_json TEXT NOT NULL,              -- що саме агент хоче зробити
    status       TEXT NOT NULL DEFAULT 'pending',
                 -- pending | approved | denied | expired | resolved_elsewhere
    requested_at TEXT NOT NULL,
    resolved_at  TEXT,
    resolved_via TEXT                        -- dashboard | terminal | mobile
);
CREATE INDEX idx_pr_pending ON permission_requests(status, requested_at);

-- ============ Черга задач (шар 2: оркестрація) ============

CREATE TABLE tasks (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    title       TEXT NOT NULL,
    prompt      TEXT NOT NULL,               -- spec-driven промпт
    priority    INTEGER NOT NULL DEFAULT 5,  -- 1 = найвищий
    status      TEXT NOT NULL DEFAULT 'queued',
                -- queued | running | needs_review | done | failed | cancelled
    session_id  INTEGER REFERENCES sessions(id),  -- яка сесія виконує/виконала
    agent_id    INTEGER REFERENCES agents(id),    -- цільовий агент (якщо є)
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    result_note TEXT,                        -- людська оцінка: ok / правив / відкат
    reverted    INTEGER NOT NULL DEFAULT 0   -- для quality-метрик
);
CREATE INDEX idx_tasks_queue ON tasks(status, priority, created_at);

-- ============ Агенти і скіли (шар 3: менеджмент) ============

CREATE TABLE agents (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,               -- code-reviewer, db-migrator...
    scope       TEXT NOT NULL,               -- global | project
    project_id  INTEGER REFERENCES projects(id),  -- NULL для global
    file_path   TEXT NOT NULL,               -- .claude/agents/xxx.md
    model       TEXT,
    tools_json  TEXT,                        -- дозволені tools
    description TEXT,
    current_version_id INTEGER,              -- FK на agent_versions (deferred)
    deleted     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(name, scope, project_id)
);

CREATE TABLE agent_versions (
    id           INTEGER PRIMARY KEY,
    agent_id     INTEGER NOT NULL REFERENCES agents(id),
    content_hash TEXT NOT NULL,              -- sha256 вмісту .md
    git_sha      TEXT,                       -- якщо конфіги в git
    content      TEXT NOT NULL,              -- повний вміст на момент версії
    created_at   TEXT NOT NULL,
    change_note  TEXT,                       -- заповнюється з UI-редактора
    UNIQUE(agent_id, content_hash)
);

CREATE TABLE skills (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL,
    project_id  INTEGER REFERENCES projects(id),
    dir_path    TEXT NOT NULL,               -- папка зі SKILL.md
    description TEXT,                        -- з frontmatter — для лінтера
    current_version_id INTEGER,
    deleted     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(name, scope, project_id)
);

CREATE TABLE skill_versions (
    id           INTEGER PRIMARY KEY,
    skill_id     INTEGER NOT NULL REFERENCES skills(id),
    content_hash TEXT NOT NULL,
    git_sha      TEXT,
    content      TEXT NOT NULL,              -- SKILL.md (ресурси — по ref)
    created_at   TEXT NOT NULL,
    change_note  TEXT,
    UNIQUE(skill_id, content_hash)
);

-- Результати лінтера конфігів (роздутий CLAUDE.md, агент без Boundaries...)
CREATE TABLE config_lint_findings (
    id         INTEGER PRIMARY KEY,
    target     TEXT NOT NULL,                -- agent:12 | skill:3 | claude_md:...
    rule       TEXT NOT NULL,                -- oversized_context | no_boundaries
    severity   TEXT NOT NULL,                -- info | warn | error
    message    TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    resolved_at TEXT
);

-- ============ Метрики і evals (шар 4) ============

CREATE TABLE daily_rollups (
    day         TEXT NOT NULL,               -- YYYY-MM-DD
    project_id  INTEGER REFERENCES projects(id),
    agent_id    INTEGER REFERENCES agents(id),   -- NULL = увесь проєкт
    sessions    INTEGER NOT NULL DEFAULT 0,
    tasks_done  INTEGER NOT NULL DEFAULT 0,
    tasks_reverted INTEGER NOT NULL DEFAULT 0,
    tool_calls  INTEGER NOT NULL DEFAULT 0,
    errors      INTEGER NOT NULL DEFAULT 0,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    cost_usd    REAL    NOT NULL DEFAULT 0,
    wait_minutes REAL   NOT NULL DEFAULT 0,  -- час у waiting_approval
    PRIMARY KEY (day, project_id, agent_id)
);

CREATE TABLE eval_suites (
    id         INTEGER PRIMARY KEY,
    agent_id   INTEGER NOT NULL REFERENCES agents(id),
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE eval_cases (
    id        INTEGER PRIMARY KEY,
    suite_id  INTEGER NOT NULL REFERENCES eval_suites(id),
    prompt    TEXT NOT NULL,                 -- еталонна задача
    check_cmd TEXT,                          -- напр. go test ./... або скрипт
    expected  TEXT                           -- опис очікуваного результату
);

CREATE TABLE eval_runs (
    id               INTEGER PRIMARY KEY,
    suite_id         INTEGER NOT NULL REFERENCES eval_suites(id),
    agent_version_id INTEGER NOT NULL REFERENCES agent_versions(id),
    started_at       TEXT NOT NULL,
    finished_at      TEXT,
    passed           INTEGER,
    failed           INTEGER,
    tokens_total     INTEGER,
    cost_usd         REAL
);

CREATE TABLE eval_results (
    id        INTEGER PRIMARY KEY,
    run_id    INTEGER NOT NULL REFERENCES eval_runs(id),
    case_id   INTEGER NOT NULL REFERENCES eval_cases(id),
    status    TEXT NOT NULL,                 -- pass | fail | error
    session_id INTEGER REFERENCES sessions(id),
    notes     TEXT
);
```

### Ключові рішення і trade-offs

| Рішення | Чому | Ціна |
|---|---|---|
| SQLite, не Postgres | Локальний демон, один користувач, zero-ops; WAL тримає write-нагрузку хуків | Немає віддаленого доступу до БД — але API все одно через демон |
| `payload` JSON у events | Формат JSONL Claude Code — internal, змінюється | Частина запитів через `json_extract`; критичні поля винесені в колонки |
| Повний `content` у versions | Rollback і diff без залежності від git-стану | Розмір БД росте; агентські .md малі — прийнятно |
| `parent_event_id` | Дерево делегування (оркестратор → субагенти) одним полем | Рекурсивні CTE для глибоких дерев — SQLite вміє |
| `result_note` + `reverted` у tasks | Метрики якості потребують людського сигналу — мінімальний UX: один тап "ok / правив / відкат" після задачі | Дисципліна заповнення; без цього шар 4 не працює |

---

## 3. UI структура

### Навігація (sidebar / bottom bar на мобільному)

```
● Overview        — пульс системи
● Approvals  (3)  — черга дозволів, бейдж
● Sessions        — живі і минулі сесії
● Tasks           — черга задач
● Agents & Skills — реєстр + редактор
● Analytics       — cost / quality
● Health          — стан самого Swarmery
```

### 3.1 Overview
- Активні сесії зараз (проєкт, агент, поточна дія, тривалість) — live через WS
- Pending approvals: топ-3 + кнопка в чергу
- Сьогодні: токени, $, задач виконано, помилок
- Спарклайн активності за 7 днів
- Останні завершені задачі зі статусом (ok / needs_review / failed)

### 3.2 Approvals
- Список pending: tool, що саме хоче зробити (згорнутий request_json), сесія, скільки висить
- Дії: Approve / Deny / Open session. Swipe-дії на мобільному
- Історія рішень (аудит: хто/звідки/коли)

### 3.3 Sessions
- Фільтри: проєкт, статус, агент, дата
- **Session detail** — головний екран, таби:
  - **Timeline**: промпт → tool calls → субагенти (розгортаються як піддерево) → skills → результат. Помилки червоним, out-of-scope зміни з прапорцем
  - **Diffs**: усі file_changes сесії, згруповані по файлах, unified diff
  - **Context**: які CLAUDE.md/rules підвантажені, розподіл токенів (історія vs системне vs tools), розмір контексту в часі
  - **Tree**: flame-graph делегування (по parent_event_id), час і токени на вузол
- Дії на живій сесії: Inject instruction, Kill

### 3.4 Tasks
- Kanban або список: queued → running → needs_review → done
- Створення задачі: проєкт, цільовий агент (опц.), spec-driven шаблон промпта (Context/Objective/Boundaries/Validation — преінжектиться)
- Ліміт паралельності per-проєкт, пріоритети drag-n-drop
- Після done — швидка оцінка: ✅ ok / ✏️ правив / ↩️ відкат (пише result_note/reverted)

### 3.5 Agents & Skills
- Реєстр: назва, scope (global/project), модель, останнє використання, задач за 30 днів, success-rate. Мертві агенти (0 використань 30+ днів) — приглушені
- **Agent detail**:
  - Метрики: задачі, % без правок, % відкатів, сер. токени/задача — по версіях
  - Версії: список agent_versions, diff між будь-якими двома, Rollback
  - Editor: форма (роль, модель, tools, boundaries) + raw .md таб з preview; Save = запис файлу + нова версія + git commit
  - Evals: suite агента, кнопка Run, історія прогонів по версіях
- **Sync view**: матриця агент × проєкт, дрейф версій, кнопка Push to all
- **Lint**: активні findings з config_lint_findings, по severity

### 3.6 Analytics
- Cost: по днях / проєктах / агентах / моделях; cost-per-task
- Quality: success-rate агентів у часі, топ error patterns, wait_minutes (втрачений час на approvals)
- Порівняння версій агента: до/після зміни промпта (A/B по метриках)

### 3.7 Health
- Стан колекторів: hooks (останній heartbeat), JSONL-watcher (lag), розмір БД
- Проєкти без хуків ("непідключені") + one-click інсталяція hook-конфігу
- Версія Claude Code на машині, попередження про зміну формату JSONL

---

## 4. Порядок імплементації (маппінг на схему)

1. **MVP**: projects, sessions, turns, events, file_changes + екрани Overview/Sessions. Тільки JSONL-парсер.
   Backfill-scope: лише транскрипти формату Claude Code ≥2.1 (Agent tool, окремі sidechain-файли); pre-2.1 (`Task`, inline sidechains) — out of scope для MVP.
   Перед step 06 (ingest-демон): watch-експеримент — підтвердити, що транскрипти append-only, а не переписуються in place; дизайн tail-follow залежить від цього (Q11).
2. **Approvals**: permission_requests + hooks (PreToolUse повертає рішення з дашборда). Екран Approvals.
3. **Agents registry read-only**: agents, skills, *_versions (скан файлової системи + fsnotify). Реєстр без редагування.
4. **Editor + git**: запис файлів, версіонування, rollback, lint.
5. **Tasks queue**: tasks + запуск headless-сесій (`claude -p --output-format stream-json`).
6. **Rollups + Analytics**, потім **Evals**.

Схема з п.1 вже містить усі FK для наступних шарів — міграції будуть адитивними (нові таблиці), без переписування ядра.
