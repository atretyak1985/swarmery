# System config formats — observed spec (Phase 4, Step 01)

**Status:** discovery result for the System section (agents / skills / hooks / commands /
plugins / templates registry). Everything below is evidence from real config files on this
machine — nothing is taken from memory or third-party docs. Where something was *not*
observed, it says so explicitly. This doc is the normative parsing reference for sysscan
(step-03), playing the same role [`jsonl-format.md`](jsonl-format.md) plays for ingest and
[`hooks-format.md`](hooks-format.md) for the approvals channel.

**Evidence base:**

- Claude Code `2.1.170` (homebrew cask `claude-code@latest`), macOS (arm64).
- 159 agent `.md` files (89 plugin-cache + 70 project-local), 142 `SKILL.md`
  (7 user + 88 plugin-cache + 47 project-local), 72+ command `.md`, 21 settings files,
  3 plugin `hooks.json`, 15 cached plugin trees across 3 marketplaces.
- Projects enumerated from the daemon DB (`~/.swarmery/swarmery.db`, `projects` table):
  11 real paths + 1 `(unknown)` placeholder row.
- Binary-string analysis of the CC `2.1.170` executable for the hook-disable question.

> **Secret gate:** every example below was re-checked for tokens. A regex sweep
> (`sk-ant-|ghp_|github_pat_|glpat-|AKIA|xox[baprs]-|Bearer |eyJ|token=|secret=|password=|api[_-]?key`)
> over all 21 settings files returned **0 hits** (§8). Absolute home paths are shown as
> `<home>` where they are not load-bearing.

---

## 0. Source map (where config lives on this machine)

| Tier | Path | Exists here | Contents |
|---|---|---|---|
| User agents | `~/.claude/agents/` | **dir exists, effectively empty** (only `.DS_Store`) | 0 agent files |
| User skills | `~/.claude/skills/<name>/SKILL.md` | yes | 7 `gitnexus-*` skills |
| User commands | `~/.claude/commands/` | **does not exist** | — |
| User settings | `~/.claude/settings.json` | yes | hooks, enabledPlugins, model, statusLine… |
| User settings.local | `~/.claude/settings.local.json` | **does not exist** | — |
| Project | `<project>/.claude/{agents,skills,commands,settings.json,settings.local.json}` | varies per project | see per-section tables |
| Plugin cache | `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/` | yes (3 marketplaces) | plugin root (§5) |
| Marketplace clones | `~/.claude/plugins/marketplaces/<name>/` | yes (3 clones) | full repo clone (§5) |
| Plugin metadata | `~/.claude/plugins/{installed_plugins,known_marketplaces,blocklist}.json` | yes | §5 |
| Daemon app dir | `~/.swarmery/` | yes | `swarmery.db`, `bin/`, `logs/`, `hook.log` |

Canonical app dir is **`~/.swarmery`** (any `~/.swormery` spelling in phase briefs is a
typo). The planned backup root for config surgery is **`~/.swarmery/config-backups/`** —
it **does not exist yet** (verified 2026-07-13); step-10 creates it.

`projects` rows are the project universe for the scan. Two edge cases observed in the
table itself: a `(unknown)` placeholder row (id 12, non-absolute path — skip exactly like
`hookcfg.ProjectsFromDB` does) and paths containing **spaces**
(`/Volumes/Work/Naomi School 24`) — shell-quoting hazard for any exec-based scanning.

Observed on: 2026-07-13, machine-local.

---

## 1. Agents — `agents/**/*.md` with YAML frontmatter

### 1.1 Where agents were found

| Source | Count | Layout notes |
|---|---|---|
| `~/.claude/agents/` | 0 | dir exists, only `.DS_Store` |
| `/Volumes/Work/CarsFinders/.claude/agents/` | 6 | flat `*.md` (duplicated verbatim in the `CarsFinders/cars-infrastructure` sub-project) |
| `/Volumes/Work/Naomi School 24/.claude/agents/` | 20 | **nested subdirs**: `core-workflow/ design/ quality/ specialists/` |
| `/Volumes/Work/Sergiys/.claude/agents/` | 43 | nested subdirs **plus** a stray `README.md` and a `hooks/post-task-completion.md` inside the agents tree |
| `/Volumes/Work/Skygor/.claude/agents/` | 1 | `devnext-operator.md` (mirrored in `Skygor/scripts`) |
| `/Volumes/Work/PetsHalo/.claude/agents/` | 0 | empty dir |
| plugin cache `*/agents/*.md` | 89 | `swarmery/core@1.2.0` 44, `agentry/*@1.0.0` 45; **no official (`claude-plugins-official`) plugin ships agents** |

Scanner consequences: recurse into subdirectories; skip non-frontmatter helper files
(`README.md`) and anything whose first line is not `---` (in this corpus every real agent
file starts with `---`, so that filter is safe); tolerate empty/near-empty dirs.

### 1.2 Frontmatter fields actually observed (159 files)

| Field | Freq | Type / example | Notes |
|---|---|---|---|
| `name` | 159/159 | `tech-lead` | matches file stem in every observed case |
| `model` | 159/159 | `claude-fable-5`, `claude-sonnet-5`, `claude-haiku-4-5`, `claude-sonnet-4-6`, `claude-opus-4-8`, `claude-opus-4-7` | |
| `permissionMode` | 159/159 | `default` \| `acceptEdits` \| `plan` | |
| `maxTurns` | 159/159 | `200` | integer |
| `color` | 159/159 | `purple` | |
| `description` | **151/159** | one-liner or folded scalar | **8 files in Naomi School 24 lack it entirely** (`design/*`, `quality/*`) — real missing-field example |
| `skills` | 145/159 | YAML block list | one file (`swarmery core tech-lead`) lists `deployment` **twice** — duplicate-entry edge case |
| `autonomy` | 153/159 | `auto` \| `semi-auto` | non-CC key (framework-specific) |
| `version` | 143/159 | `1.0.0` | non-CC key |
| `owner` | 143/159 | `platform-team` | non-CC key |
| `effort` | 122/159 | `high` \| `max` \| `xhigh` | |
| `background` | 16/159 | | |
| `memory` | 31/159 | `project` | |
| `disallowedTools` | 6/159 | | |
| `isolation` | 8/159 | `worktree` | |
| `tools` | **0/159** | — | **never observed** on this machine, despite being a documented CC key and despite `agents.tools_json` in the design schema — the column will be NULL-heavy; parse it if present, never require it |

Every field must be treated as optional by the parser. CI in the swarmery repo only
enforces `name` + `description` within the first 15 lines — and even that is violated by
project-local (non-plugin) agents here.

### 1.3 YAML gotchas observed (all real)

- **Comments between keys** — `# Rationale: …` lines inside frontmatter (swarmery-style
  agents do this heavily). A naive `key: value` line-parser must skip `#` lines.
- **Folded scalars** — `description: >` followed by an indented multi-line block
  (`Naomi School 24/.claude/agents/core-workflow/tech-lead.md`). Line-oriented parsing
  breaks here; use a YAML parser.
- **Block lists** — `skills:` with `  - item` lines, including duplicates.
- **Missing keys** — see `description` above; example file with no description at all:

```yaml
# /Volumes/Work/Naomi School 24/.claude/agents/design/api-designer.md
---
name: api-designer
model: claude-fable-5
effort: high
permissionMode: plan
memory: project
color: green
autonomy: auto
...
```

### 1.4 Reference example (plugin cache)

```yaml
# ~/.claude/plugins/cache/swarmery/core/1.2.0/agents/tech-lead.md
---
name: tech-lead
description: Orchestrate executor agents through the 9-phase workflow with gap analysis, ...
model: claude-fable-5
# Rationale: T0 architect tier. ...
effort: max
permissionMode: default
memory: project
color: purple
autonomy: auto
maxTurns: 200
version: 1.1.0
owner: platform-team
skills:
  - deployment
  - deployment          # <- real duplicate, kept verbatim
  - context-optimization
---
```

Observed on: 2026-07-13, machine-local.

---

## 2. Skills — `skills/<dir>/SKILL.md` (+ sibling resources)

### 2.1 Where skills were found

| Source | Count | Frontmatter keys seen |
|---|---|---|
| `~/.claude/skills/*/SKILL.md` | 7 (all `gitnexus-*`) | `name`, `description` only; dir contains **only** SKILL.md |
| plugin cache `**/SKILL.md` | 88 | `name` 88, `description` 88, `version` 44, `owner` 44, `disable-model-invocation` 35, `allowed-tools` 30, `color` 22, `tools` 2, `license` 1 |
| project `.claude/skills/**/SKILL.md` | 47 (Sergiys 43-ish, Skygor 3, PetsHalo 1, bloomblum 1) + 6 (Naomi) | same shape plus one custom key: `mermaid-version` (1×) — arbitrary extra keys happen |

`name` + `description` are the only universal keys. `allowed-tools` (kebab-case!) and
`disable-model-invocation` are the CC-recognized extras; `version`/`owner`/`color` are
framework conventions riding along.

### 2.2 Directory structure

- A skill = a **directory** whose identity is the dir name; `SKILL.md` inside carries
  frontmatter (in all observed cases frontmatter `name` == dir name).
- Resources live as sibling files: e.g.
  `superpowers/5.1.0/skills/systematic-debugging/` holds `SKILL.md` + 10 companion `.md`
  files; swarmery `troubleshooting/` holds 2 extra files. Registry stores `dir_path` and
  versions only `SKILL.md` content (per design: "ресурси — по ref") — that matches
  reality.
- **Only `skills/` is auto-discovered.** The figma plugin also ships
  `workflow-skills/generate-project-plan/SKILL.md`; the 9 skills under its `skills/` dir
  are all loaded in a live session, `generate-project-plan` is **not**. Scanner rule: for
  plugins, scan `<pluginRoot>/skills/*/SKILL.md` only.

### 2.3 Example (user scope — minimal shape)

```yaml
# ~/.claude/skills/gitnexus-guide/SKILL.md
---
name: gitnexus-guide
description: Use when the user asks about GitNexus itself — available tools, ...
---
```

Observed on: 2026-07-13, machine-local.

---

## 3. Hooks — `settings.json` / `settings.local.json`

### 3.1 Exact structure (observed everywhere, no variation)

```
{ "hooks": { "<Event>": [ { "matcher"?: string,
                            "hooks": [ { "type": "command",
                                         "command": string,
                                         "timeout"?: number,       // seconds
                                         "statusMessage"?: string, // user tier, gitnexus
                                         "async"?: bool            // plugin hooks.json only
                                       } ] } ] } }
```

- `matcher` is **optional** (the swarmery `Stop` group has none), can be `"*"`, `""`
  (plugin hooks.json), or a regex-ish alternation (`"Edit|Write"`, `"Grep|Glob|Bash"`,
  and for `SessionStart`: `"startup|clear|compact"` — matcher semantics are per-event).
- `type` is always `"command"` in the corpus.
- Commands reference env vars un-expanded: `$CLAUDE_PROJECT_DIR/...` (project tier, both
  quoted `"$CLAUDE_PROJECT_DIR/..."` and unquoted variants of the *same* script exist —
  string comparison must not assume canonical quoting), `${CLAUDE_PLUGIN_ROOT}/...`
  (plugin tier).

### 3.2 What is installed where (this machine)

| File | Events | Owner |
|---|---|---|
| `~/.claude/settings.json` | `PreToolUse` (matcher `Grep\|Glob\|Bash`), `PostToolUse` (matcher `Bash`) — both `node "<home>/.claude/hooks/gitnexus/gitnexus-hook.cjs"`, `timeout: 10`, with `statusMessage` | gitnexus (foreign) |
| `<project>/.claude/settings.json` (Sergiys, Naomi) | 9 events / 12 entries: `PreToolUse`, `PostToolUse`, `SessionStart`, `SessionEnd`, `SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`, `Notification`; all `$CLAUDE_PROJECT_DIR/.claude/hooks/*.sh` | agentry-era framework (foreign) |
| `<project>/.claude/settings.local.json` (swarmery, Skygor, Sergiys, + others) | `PermissionRequest` (matcher `"*"`, `timeout: 130`) + `Stop` (no matcher) → `<home>/.swarmery/bin/swarmery hook <event>` | **swarmery** |

swarmery-owned entries are recognized by the `"swarmery hook"` command substring — the
exact `marker` in `internal/hookcfg/hookcfg.go`; installed shape matches `Install()`
verbatim (PermissionRequest: matcher `*` + timeout 130; Stop: no matcher, no timeout; an
optional `SWARMERY_PORT=<n> ` prefix is possible for non-default ports — not present in
any current file). `settings.local.json.bak` files exist in Sergiys and Naomi — produced
by `hookcfg.writeSettings` before its first write. Scanner must **ignore `*.bak`**.

Real example (project settings.local.json, swarmery-installed — full file):

```json
{
  "hooks": {
    "PermissionRequest": [
      { "hooks": [ { "command": "<home>/.swarmery/bin/swarmery hook permission-request",
                     "timeout": 130, "type": "command" } ],
        "matcher": "*" } ],
    "Stop": [
      { "hooks": [ { "command": "<home>/.swarmery/bin/swarmery hook stop",
                     "type": "command" } ] } ]
  }
}
```

### 3.3 Native disable mechanism — **verdict: none per-hook**

Investigated in CC `2.1.170` binary strings (JSON supports no comments, so
"comment-out" is impossible by construction):

- `disableAllHooks` exists as a settings key — a **global kill-switch** ("…hooks are
  disabled (workspace not trusted, **disableAllHooks** set, or matcher mismatch)";
  "…remove `disableAllHooks` from settings.json or ask Claude").
- `allowManagedHooksOnly` exists as a **policy** key (managed environments).
- Zero hits for `hookDisabled` / `disabledHooks` / `hook_disabled`; the hook-config
  schema fragment visible in the binary is `{matcher?, hookCallbackIds, timeout?}` —
  **no `enabled`/`disabled` flag on individual entries.**

**Conclusion for step-10:** there is no native way to disable a single hook entry.
Disable = *remove the entry from the JSON array*; enable = *put it back*. Therefore the
disable feature must persist the removed entry itself (in the DB and/or a backup copy of
the file under `~/.swarmery/config-backups/`) — the settings file cannot carry a dormant
copy. `disableAllHooks: true` is NOT usable per-hook (it would silence foreign hooks
too).

### 3.4 Does sysscan read `settings.local.json`? — **decision: yes**

Recorded decision: scan both `settings.json` and `settings.local.json` per project, as
**separate `source_file` rows**. Rationale: `settings.local.json` is machine truth —
swarmery's own hooks exist *only* there; skipping it would make the System page blind to
the hooks swarmery itself installed. `settings.json.bak` / `settings.local.json.bak` are
excluded. User tier: `~/.claude/settings.json` only (no user-level `settings.local.json`
exists here).

### 3.5 Event-name vocabulary (for validation, not invention)

Event keys present as string literals in the CC 2.1.170 binary: `PreToolUse`,
`PostToolUse`, `PermissionRequest`, `Stop`, `UserPromptSubmit`, `SessionStart`,
`SessionEnd`, `SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`,
`Notification`, `TaskCompleted`. Treat unknown keys as data, not errors (forward-compat).

Observed on: 2026-07-13, machine-local.

---

## 4. Commands — `commands/*.md`

- `~/.claude/commands/` **does not exist** on this machine (user scope: 0 commands).
- Plugin cache: 35 command `.md` files. Project scope: Sergiys (30), Skygor +
  `Skygor/scripts` (10 each, mirrored), Naomi School 24 (4).
- **There is no `name:` key** — the command name is the **file stem** (`dashboard.md` →
  `/dashboard`). All observed files start with `---` frontmatter.

Frontmatter keys observed:

| Field | Plugin (35 files) | Project (~44 files) | Notes |
|---|---|---|---|
| `description` | 35 | all | universal |
| `color` | 24 | 20 | |
| `allowed-tools` | 16 | 20 | YAML block list |
| `disable-model-invocation` | 1 | 0 | |

Edge case: Sergiys ships a `README.md` **inside** `.claude/commands/` — a naive scanner
would register a `/README` command. Filter by "has frontmatter with description" or
exclude `README.md` explicitly.

```yaml
# ~/.claude/plugins/cache/swarmery/core/1.2.0/commands/dashboard.md
---
description: Session dashboard — tool usage, active tasks, agent stats, and metrics overview
allowed-tools:
  - Bash
  - Read
  - Glob
  - Grep
---
```

Observed on: 2026-07-13, machine-local.

---

## 5. Plugins & marketplaces

### 5.1 Cache layout — the "belongs to plugin" rule

```
~/.claude/plugins/
  cache/<marketplace>/<plugin>/<version>/     # ← plugin ROOT (note the version level!)
    .claude-plugin/plugin.json                # manifest
    .in_use                                   # empty DIRECTORY marker (observed on active versions)
    agents/  skills/  commands/  hooks/  bin/  templates/   # components at root
  marketplaces/<name>/                        # full git clone of the marketplace repo
    .claude-plugin/marketplace.json
    plugins/<plugin>/...                      # ← plugin ROOT for relative sources
  installed_plugins.json  known_marketplaces.json  blocklist.json  data/<plugin>-<mp>/
```

`<version>` directory names observed: semver (`1.2.0`), literal `unknown`, and a
12-char git-sha prefix (`atlassian/9b52fb18e184`) — treat as an opaque string.

**A file belongs to plugin P iff its path has prefix** either
`cache/<mp>/<P>/<version>/` **or** `marketplaces/<mp>/plugins/<P>/`. Both roots are live:
`installed_plugins.json` points `core@swarmery` at
`cache/swarmery/core/1.2.0` (scope `project`, projectPath `/Volumes/Work/Skygor`), while
a live session in the swarmery repo itself resolves the *same* plugin's skills from
`marketplaces/swarmery/plugins/core/skills/…` (observed base-dir of a running skill).

### 5.2 Metadata files (all under `~/.claude/plugins/`)

`installed_plugins.json` — `{"version": 2, "plugins": { "<plugin>@<marketplace>": [ {`
`"scope": "user"|"project", "projectPath"?: string, "installPath": string,`
`"version": string, "installedAt"/"lastUpdated": ISO, "gitCommitSha"?: string } ] }}`.
Note the array — multiple installs per plugin key are possible in the schema (only
singletons observed).

`known_marketplaces.json` — `{ "<name>": { "source": {"source":"github","repo":"owner/repo"},`
`"installLocation": string, "lastUpdated": ISO } }`.

`blocklist.json` — fetched denylist (`{plugin, added_at, reason, text}` entries).

**Staleness edge cases (all real, must not crash the scanner):**

- cache/`agentry/*` exists (45 agent files, `.in_use` markers) but `agentry` is in
  **neither** `known_marketplaces.json` **nor** `installed_plugins.json` — orphaned cache
  of a removed marketplace.
- `installed_plugins.json` lists `iot-pack@swarmery` / `uav-pack@swarmery` whose
  `installPath` dirs **do not exist** in the cache — dangling install records.
- marketplace `caveman` is registered with a clone but has **no cache** entries.
- `enabledPlugins` (settings.json, `{"<plugin>@<marketplace>": true}`) — value `false`
  never observed here; enabled-set is per-file (user tier lists 10 official plugins;
  each consumer project lists its swarmery packs).

### 5.3 Manifests

`plugin.json` (at `<root>/.claude-plugin/plugin.json`):
`{ "name", "description", "version", "author": {"name", "email"?} }`.
Superpowers additionally ships `.cursor-plugin/` and `.codex-plugin/` manifest dirs —
scan `.claude-plugin/` only.

`marketplace.json` (repo root `/Volumes/Work/swarmery/.claude-plugin/marketplace.json`):
`{ "name", "owner": {name,email}, "metadata": {description, version},`
`"plugins": [ {name, source: "./plugins/<name>", description} ] }`. The clone under
`marketplaces/swarmery/` carries the same file pinned at the last-update commit (it
currently differs from repo HEAD — expected).

### 5.4 Plugins DO ship `hooks/hooks.json` — **open question for Stage 1.5**

3 of 15 cached plugins ship one: `swarmery/core` (10 events, `${CLAUDE_PLUGIN_ROOT}`
commands), `agentry/core`, `superpowers` (1 event, `"async": false` observed). Format is
the settings-hooks shape wrapped in a root `"hooks"` object:

```json
{ "hooks": { "SessionStart": [ { "matcher": "startup|clear|compact",
    "hooks": [ { "type": "command",
                 "command": "\"${CLAUDE_PLUGIN_ROOT}/hooks/run-hook.cmd\" session-start",
                 "async": false } ] } ] } }
```

**Stage 1 hooks-scan reads settings files only.** Whether/how to surface plugin-shipped
hooks (they are active whenever the plugin is enabled, but live in the cache, not in any
settings file) is deferred to Stage 1.5 — flagged as an open question.

Observed on: 2026-07-13, machine-local.

---

## 6. Templates (browsing only)

Sources for the Templates tab:

- `overlays/_schema/project.schema.json` (repo) — JSON Schema draft-07 for consumers'
  `.claude/project.json`; `required: [name, codePath, enabledPacks]`,
  `additionalProperties: false`; notable properties: `mainApp`, `apps[]`, `repos[]`,
  `device`, `domainTerms{}`, `cloud{provider,region,runtime,envAlias}`, `stack{}`,
  `commitScopes[]`, `enabledPacks[]` (enum of domain packs).
- `overlays/example/` — exactly 2 files: `project.json` (reference overlay, fully
  populated with neutral placeholders) and `settings.snippet.json`. The snippet uses a
  `"//": "…"` **comment-key convention** — a real pattern the renderer should display,
  not strip.
- Plugin `templates/` dirs — core ships `templates/*.md` (`agent-template.md`,
  `adr-template.md`, commit/summary/handoff templates, an `evaluation/` subdir).
  Resolution order (per repo CLAUDE.md): project `.claude/templates/` first, then
  `${CLAUDE_PLUGIN_ROOT}/templates/`. No project on this machine currently has a
  `.claude/templates/` override — show plugin templates as the effective set.

Tab scope: render schema + example + template files read-only; no editing, no validation
UI in Stage 1.

Observed on: 2026-07-13, machine-local.

---

## 7. Naming rule for plugin components — **CONFIRMED: `plugin:name`**

Claim under test: CC addresses plugin-owned components as `"<plugin>:<name>"`, which
justifies storing plugin items under that composite name to survive
`UNIQUE(name, scope, project_id)` alongside a same-named project-local item.

Four independent evidence classes, all from this machine:

1. **Skill invocations in transcripts** — `"skill":"superpowers:writing-plans"`,
   `"skill":"superpowers:executing-plans"` (plugin-qualified) vs `"skill":"gitnexus-exploring"`
   (user-scope skill, bare name).
2. **Subagent dispatch** — `"subagent_type":"core:implementation-planner"`,
   `"core:debugger"`, `"core:implementation-agent"` for plugin agents vs bare
   `"fleet-sync"` / `"ui-designer"` for project-local agents (built-ins `general-purpose`
   and `Explore` are also bare).
3. **User-typed commands** — `history.jsonl` contains `/commit-commands:commit`.
4. **Live session skill roster** — plugin skills are listed as `core:code-quality`,
   `figma:figma-use`, … and CC's own instruction text says to use "the fully qualified
   `plugin:skill` form".

Caveats recorded:

- The qualifier is the **plugin name only** — never the marketplace
  (`core:tech-lead`, not `core@swarmery:tech-lead`). Two marketplaces can ship
  same-named plugins (cache holds **both** `agentry/core` and `swarmery/core` right
  now), so `plugin:name` is not globally unique across *all* cached plugins — it is
  unique within the *enabled* set, which is what the registry models. If a collision
  ever matters, disambiguate in a separate `marketplace` column, not in the name.
- `subagent_type` reflects the **caller's** string: bare `"tech-lead"` (6×) also appears
  in transcripts and resolved fine, so CC accepts both forms on input. The canonical
  system-side representation is the qualified form, which is what the registry stores.

**Verdict: store plugin items under `"plugin:name"` — confirmed sound for Stage 1.**

Observed on: 2026-07-13, machine-local.

---

## 8. Redaction survey (input for step-05 API filter)

Scan: 21 settings files (user + 10 projects × up to 2 tiers) → 48 hook command strings,
17 `env` entries, 210 permission rules.

**Result: 0 token-pattern hits.** This machine's hook commands carry only: absolute
home paths (`/Users/<user>/…` — a *username* leak, relevant if reports ever leave the
machine), `$CLAUDE_PROJECT_DIR` / `${CLAUDE_PLUGIN_ROOT}` references, `node`/`bash`
wrappers, and (potentially, per `hookcfg.command()`) a `SWARMERY_PORT=<n>` env prefix.
`env` blocks hold only `AGENT_PROJECT`, `AGENT_WORKSPACE_ROOT`, `NODE_ENV`,
`CLAUDE_CODE_ALWAYS_ENABLE_EFFORT`. Permission rules are tool patterns
(`Bash(docker build:*)`) with no embedded credentials.

Clean **today** is not clean **forever** — hook commands are arbitrary shell strings and
the API serves them verbatim. Pattern classes the step-05 redaction filter must catch
(class list, not observed instances):

| Class | Pattern (case-sensitive unless noted) |
|---|---|
| Anthropic keys | `sk-ant-[A-Za-z0-9_-]+` |
| GitHub tokens | `gh[pousr]_[A-Za-z0-9]+`, `github_pat_[A-Za-z0-9_]+` |
| GitLab tokens | `glpat-[A-Za-z0-9_-]+` |
| AWS access keys | `(AKIA\|ASIA)[A-Z0-9]{16}` |
| Slack tokens | `xox[baprs]-[A-Za-z0-9-]+` |
| Google API keys | `AIza[A-Za-z0-9_-]{35}` |
| npm tokens | `npm_[A-Za-z0-9]{36}` |
| JWTs | `eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+` |
| Bearer headers | `(?i)bearer\s+[A-Za-z0-9._-]+` |
| Generic assignments | `(?i)(token\|secret\|passwd\|password\|api[_-]?key\|credential)\s*[=:]\s*\S+` |
| URL userinfo creds | `[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@` |

Recommendation: redact the **value** side only (keep key names visible so the UI stays
diagnosable), and apply the filter at the API layer so the DB keeps ground truth.

Observed on: 2026-07-13, machine-local.

---

## 9. Stage 2 write safety (`internal/sysedit`, step-08)

Every config write goes through **one** code path — `sysedit.Editor.WriteFile` /
`DeleteFile`. Guarantees, in pipeline order: kill-switch → DB-resolved path fenced
into known roots → plugin-managed refusal → sha256 conflict detection (409, no
overwrite) → verified backup **before** any change → atomic tmp+fsync+rename →
forced rescan (new `*_versions` row).

### 9.1 Kill-switch — `SWARMERY_SYSTEM_READONLY`

Set `SWARMERY_SYSTEM_READONLY=1` (or `true`) in the daemon's environment to refuse
**every** write with `ErrReadOnly` (API: `403` "readonly mode"). Read per call —
flipping it on a live daemon takes effect on the next write, no restart needed.
Same env-override pattern as `SWARMERY_PRICING` / `SWARMERY_LINT_*`.

### 9.2 Backups

Before any modification the original is copied (byte-verified) to
`~/.swarmery/config-backups/<timestamp>/<full original path>`, e.g.
`…/config-backups/2026-07-14T10-22-33Z/Users/me/.claude/agents/x.md`
(RFC3339 UTC, `-` instead of `:` in the time part — colons break some tools).
Rotation keeps the newest **50** timestamp directories; deletion is fenced with a
prefix assertion so `RemoveAll` can never escape `config-backups`. Soft deletes
**move** the file into the same layout — originals are never destroyed.

### 9.3 Optional: git history in `~/.claude` (user opt-in only)

swarmery never initializes or touches a git repo in `~/.claude` — the
`*_versions` tables plus `config-backups` already cover rollback. If you want
full history with your own tooling, you can `git init ~/.claude` yourself
(mind secrets in `settings.local.json` — add a `.gitignore` first); sysedit's
atomic renames are ordinary file replacements, so external git tracking works
unchanged. This stays a user decision; no swarmery component will ever create
commits there.

---

## 10. Hook disable mechanism (`_swarmery_disabled_hooks`, step-10)

There is no native per-hook disable in Claude Code (§3.3) and JSON carries no
comments, so a dormant entry cannot stay in place. Disable therefore **moves**
the entry into a service top-level key `_swarmery_disabled_hooks` (same entry
shape + its original event and position); enable is the exact reverse move.
CC tolerates unknown top-level settings keys (§3.5: unknown keys are data, not
errors), so the parked entries are inert. `sysscan` recognizes the section and
lists such entries with `enabled=0`.

Before:

```json
{ "hooks": { "PreToolUse": [
    { "matcher": "Bash", "hooks": [ { "type": "command", "command": "./check.sh" } ] } ] } }
```

After `toggle {enabled:false}` (the emptied matcher group is dropped; the
record keeps the original matcher plus group/hook indices so enable restores
the exact position):

```json
{ "_swarmery_disabled_hooks": [
    { "event": "PreToolUse", "groupIndex": 0, "hookIndex": 0, "matcher": "Bash",
      "hook": { "type": "command", "command": "./check.sh" } } ],
  "hooks": {} }
```

Serialization standard (recorded, not silently downgraded): the canonical
stdlib form hookcfg has always written — `json.MarshalIndent`, 2-space indent,
sorted object keys, trailing newline. A file already in canonical form
roundtrips **byte-for-byte** (golden test); a non-canonical file is normalized
(semantically identical, stable ordering) on its first edit, after which every
edit is byte-surgical (single-hunk diff, test-asserted). Hooks with
`managed=swarmery` are refused (403) — they are the daemon's own data-collection
channel and are managed only via `swarmery hooks`.

Observed on: 2026-07-13, machine-local.

---

## Open questions

1. **Plugin-shipped `hooks/hooks.json`** (§5.4): active-but-not-in-settings hooks are
   invisible to the Stage 1 settings-only scan. Decide in Stage 1.5 whether to merge them
   into the hooks registry (source = plugin root path) — format is already documented.
2. **`tools:` frontmatter** (§1.2): documented CC key, 0 observations here. Keep
   `agents.tools_json` nullable; revisit if a corpus with real `tools:` shows up.
3. **Dual plugin roots** (§5.1): when both a cache version dir and a marketplace-clone
   root exist for the same plugin, which one is "current" for editing/versioning? Stage 1
   is read-only browsing, so both can be listed; write flows (agent editor) must pick one.
4. **`plugin:name` cross-marketplace collision** (§7): `agentry/core` vs `swarmery/core`
   both cached. Only enabled plugins enter the registry in Stage 1, which sidesteps it —
   but the uninstalled-cache browser (if ever built) needs the marketplace qualifier.
5. **`(unknown)` project row + paths with spaces** (§0): scanner iterates DB projects —
   must skip non-absolute paths and handle spaces without shelling out unquoted.
