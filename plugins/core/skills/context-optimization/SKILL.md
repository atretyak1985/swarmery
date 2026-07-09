---
name: context-optimization
version: "1.0.0"
owner: "agentry-core"
description: "Use this skill when a task spans 3+ files or crosses repo boundaries and context usage must be managed. Don't use it for single-file edits or read-only queries."
disable-model-invocation: true
allowed-tools: Read, Grep, Glob
color: teal
---

# Purpose

Minimize context window consumption during multi-file and cross-repo tasks by using targeted reads, codebase-retrieval-first discovery, and incremental loading. This skill does not perform code changes itself; it governs how the agent loads information before skills like `code-quality`, `api-integration`, or `deployment` do their work.

# When to use this skill

- Trigger A -- Task touches 3 or more files across any of the project's repos (see `.claude/project.json` → `repos`)
- Trigger B -- Task crosses repo boundaries (e.g., the device/edge repo + the main app)
- Trigger C -- Context window usage exceeds 40% of the model's limit and more reads are planned (this is also the threshold at which step 7 delegates isolatable reads to a leaf)

# When NOT to use this skill

- Anti-trigger A -- Single-file edit (renaming a variable, fixing a typo)
- Anti-trigger B -- Read-only query about one function or type definition
- Anti-trigger C -- Task estimated under 5 minutes with fewer than 3 files involved
- Anti-trigger D -- Agent is already in a subagent with scoped context (context is already isolated)

# Required environment (Runtime: .claude/skills/context-optimization/SKILL.md)

- Tools/libraries: `codebase-retrieval`, `Grep`, `Read` (with offset/limit parameters)

# Inputs

- `task_description: string` -- The user's request or the delegated task summary
- `repos_involved: string[]` -- List of repos the task may touch (e.g., `["apps/<mainApp>", "<device>"]`)

# Outputs

**Format:** A structured context plan listing which files to load, in what order, and which sections (line ranges) to target.

**Length budget:** Max 30 lines for the context plan. The plan is a lightweight index, not a narrative.

**Output template:**

```markdown
## Context Plan

**Task:** {one-line summary}
**Repos:** {repo list}
**Estimated files to edit:** {N}

### Phase 1 -- {description}
| # | Repo | File | Offset | Limit | Reason |
|---|------|------|--------|-------|--------|
| 1 | apps/<mainApp> | src/lib/telemetry/ws-client.ts | 12 | 35 | WebSocket reconnect logic |
| 2 | apps/<mainApp> | src/app/api/telemetry/stream/route.ts | 1 | 35 | SSE endpoint handler |

/clear before Phase 2: {yes|no} -- {reason}

### Phase 2 -- {description}
| # | Repo | File | Offset | Limit | Reason |
|---|------|------|--------|-------|--------|
| 3 | <device> | src/telemetry/sender.py | 20 | 40 | Telemetry sender |

### Budget
Files loaded: {N} / Files to edit: {M} (ratio: {N}:{M})
Confidence: {HIGH|MEDIUM|LOW} -- {rationale}
```

# Procedure

<procedure>

1. **Identify repos** -- From the task description, determine which of the project's repos are involved (list them from `.claude/project.json` → `repos` and `device`). Map each repo to its language and search glob, e.g.:

   | Repo (example) | Language | Search glob |
   |------|----------|-------------|
   | `apps/<mainApp>` | TypeScript | `*.ts`, `*.tsx` |
   | `<device>` (device/edge repo) | Python | `*.py` |
   | infrastructure repo | YAML/service config | `*.yaml`, `*.yml`, `*.tpl` |
   | CI/CD config | YAML/Shell | `*.yaml`, `*.sh` |
   | versions/config repo | YAML/JSON | `*.yaml`, `*.json` |

   Checkpoint: Repos identified; proceed only if task touches files in at least one repo.

2. **Discovery via codebase-retrieval** -- Run `codebase-retrieval` with a focused query describing the symbols, patterns, or data flow relevant to the task. Do NOT read full files first.

   **Confidence gate:** If the returned results do not reference any of the repos or symbols mentioned in `task_description`, rate confidence as LOW and stop: "codebase-retrieval returned results that do not appear relevant to [task]. Narrow the task scope or specify which repo to search."

   Checkpoint: codebase-retrieval returned at least one relevant result with HIGH or MEDIUM confidence.

3. **Targeted reads** -- For each file identified, read only the relevant section using offset/limit (read at most 200 lines per file unless targeting a known function that spans more). Prefer reading imports + the specific function over loading the entire module.

   Checkpoint: Each read used offset/limit, not full-file reads.

4. **Incremental loading** -- If the task spans multiple phases (e.g., understand structure, then implement, then test), load only the files needed for the current phase. After completing a phase that loaded files no longer needed, suggest `/clear` before the next phase.

   Checkpoint: Phase boundary identified (if applicable).

5. **Cross-repo boundary** -- When switching between repos (e.g., from the main app to the device/edge repo), suggest `/clear` to release the previous repo's context before loading the next repo's files.

   Checkpoint: `/clear` suggestion made with reason.

6. **Track context budget** -- Keep a running count of files loaded vs. files edited. Target: loaded files should be no more than 3x the number of files actually edited.

   Checkpoint: Budget ratio calculated and logged in the context plan.

7. **Isolate heavy reads behind a subagent summary** -- When a phase must load a large code slice (e.g. an entire module tree) only to extract a verdict or a short list, delegate that read to a **leaf** subagent (`@context-gatherer` for search-and-summarize, `@code-auditor` for review-and-score). The leaf burns its own context window; `main` receives only the summary artifact.

   **Decision rule (single 40% threshold -- apply in order):**

   | Current `main` window usage | Output is a digest (summary/verdict/list)? | Action |
   |-----------------------------|--------------------------------------------|--------|
   | < 40% | any | Read inline with offset/limit (steps 3-4). No delegation. |
   | >= 40% | **yes** | **Delegate to a leaf** -- `main` must not absorb the raw read. |
   | >= 40% | no (you need the code itself in `main` to edit it) | Load-then-`/clear` (steps 4-5); delegation would not help. |
   | >= 60% | any | Stop and ask (see Escalation) before any further load. |

   The 40% line is the same `Trigger C` that activated this skill: once `main` crosses it, an isolatable read is delegated by default rather than loaded. "Isolatable" = the read's product is a digest, not source you must edit.

   **Depth constraint:** keep this at one level (orchestrator -> leaf). Do NOT chain leaf -> leaf delegation; per ARCHITECTURE.md the fleet is depth-1 and a leaf that needs more work escalates back to its orchestrator, it does not spawn helpers. A leaf invoking this skill therefore skips this step entirely (Anti-trigger D).

   Checkpoint: At >=40% window, every isolatable heavy read was delegated; `main` holds a summary, not raw code.

</procedure>

# Self-check before returning

- [ ] codebase-retrieval was called before any full-file reads
- [ ] No file was loaded in its entirety when only a specific function was needed (used offset/limit)
- [ ] Files loaded count is no more than 3x files actually edited
- [ ] `/clear` was suggested if switching between repos
- [ ] Context window usage stayed below 50% of model limit throughout the task
- [ ] Context plan follows the structured output template (not free-form reasoning)
- [ ] Confidence gate was applied after codebase-retrieval
- [ ] At >=40% window usage, every isolatable read (digest output) was delegated to a leaf subagent per the step-7 decision rule -- not loaded into `main` -- and kept at depth-1

# Common mistakes to avoid

- DO NOT read 4+ full files upfront before understanding the task structure -- use codebase-retrieval first
- DO NOT keep files from a previous repo loaded when switching to a different repo
- DO NOT `/clear` mid-task without ensuring the agent has saved its plan or partial progress
- DO NOT assume context window is 200K tokens -- it varies by model; check the model's actual limit from the system prompt
- DO NOT load test files alongside implementation files unless the task specifically involves writing tests

# What to surface to the user

- The structured context plan listing files to load and why each is needed
- A `/clear` suggestion with rationale when switching repos or when context exceeds 40%
- Subagent recommendation when task spans 3+ repos with independent workstreams
- A leaf-subagent-isolation recommendation when `main` is at >=40% window and a phase needs a large read whose output is only a summary (step-7 decision rule; keep at depth-1: orchestrator -> leaf)
- The confidence level from codebase-retrieval results

# Escalation

- Stop and ask when: Task scope is ambiguous and it is unclear which repos are involved
- Stop and ask when: codebase-retrieval returns no relevant results for the described task (the task may be mis-scoped)
- Stop and ask when: Context window exceeds 60% and more files still need to be loaded
- Stop and ask when: Confidence is LOW after codebase-retrieval -- do not proceed with an unreliable context plan

# Examples

<example title="Fix telemetry latency between the device/edge repo and the main app">

**Context plan produced:**

```markdown
## Context Plan

**Task:** Fix telemetry latency between the device/edge repo and the main app
**Repos:** apps/<mainApp>, <device>
**Estimated files to edit:** 1

### Phase 1 -- Understand the data flow (main-app side)
| # | Repo | File | Offset | Limit | Reason |
|---|------|------|--------|-------|--------|
| 1 | apps/<mainApp> | src/lib/telemetry/ws-client.ts | 12 | 35 | WebSocket reconnect logic |
| 2 | apps/<mainApp> | src/app/api/telemetry/stream/route.ts | 1 | 35 | SSE endpoint handler |

/clear before Phase 2: yes -- switching from the main app to the device/edge repo

### Phase 2 -- Check the device firmware side
| # | Repo | File | Offset | Limit | Reason |
|---|------|------|--------|-------|--------|
| 3 | <device> | src/telemetry/sender.py | 20 | 40 | Telemetry sender |

### Budget
Files loaded: 3 / Files to edit: 1 (ratio: 3:1)
Confidence: HIGH -- codebase-retrieval returned exact telemetry files
```

**Result:** 3 files loaded, 1 file edited. Ratio 3:1 (within budget).

</example>

# Failure modes

- Mode: codebase-retrieval returns irrelevant results -- symptom: loaded files do not relate to the task -- detect: agent realizes after reading that the content is unrelated -- fix: refine the query with more specific symbols or file paths; rate confidence as LOW
- Mode: `/clear` suggested at wrong time -- symptom: agent loses context it still needs -- detect: agent cannot recall previously loaded information -- fix: before `/clear`, write a brief summary of findings to preserve across the context reset
- Mode: context budget exceeded -- symptom: model starts truncating or losing earlier context -- detect: agent gets confused about previously loaded content -- fix: `/clear` and reload only the files needed for the remaining work

# Related skills

- `code-search` -- defer to code-search for finding all references to a known identifier; context-optimization governs when and how much to load
- `api-integration` -- compose with api-integration when the task involves understanding the project's API flows across repos
- `code-quality` -- compose with code-quality after context-optimization has identified the minimal set of files to review
