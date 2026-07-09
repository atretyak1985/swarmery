---
name: refactor-plan
description: "PRODUCES a refactoring plan ONLY -- impact analysis, step ordering, risk assessment, rollback strategy -- with NO code changes made. NOT for executing refactors: pure-function/immutability refactoring is executed by functional-design. NOT for deployment rollback (use release-promotion) or dependency upgrades (use deps-check)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Grep, Glob
disable-model-invocation: true
---

# Purpose

Generate a structured refactoring plan for a proposed code change in the project's codebase. The plan includes current state analysis, impact analysis across repos, step-by-step migration, risk assessment, and a rollback strategy. The output is a markdown document that can be reviewed before execution begins. This skill plans only -- it does not execute code changes.

Success criteria: every file cited in the plan was found via Grep/Glob (not guessed), the plan covers all affected repos in every language of the project's stack (see `.claude/project.json` → `stack`), and the rollback strategy is specific enough to execute without interpretation.

Placeholders: `<mainApp>` = `project.json → mainApp`; `<device>` = `project.json → device` (the device/edge repo, if the project has one).

# When to use

- Renaming a type, table, or module across the codebase (e.g., renaming a legacy entity name to its new domain name).
- Restructuring file layout or module boundaries within the main app or the device repo.
- Extracting shared logic into a library or consolidating duplicated code.
- Changing a WebSocket/SSE message format between a producer service (e.g., the device repo) and a consumer (e.g., the main app).
- Migrating from one pattern to another (e.g., class-based to functional, eager init to lazy init).

# When NOT to use

- **Executing pure-function refactoring directly** -- use `functional-design` which applies changes via Edit.
- **Deployment or environment rollback** -- use `release-promotion` for release/image rollback flows.
- **Dependency-only upgrades** (e.g., bumping a library version) -- use `deps-check`.
- **One-line bug fixes** -- no plan needed; fix directly.
- **Single-file cosmetic changes** (formatting, import reordering) -- too small for a plan.

# Required environment

- Runtime: `.claude/skills/refactor-plan/SKILL.md`
- Read access to all potentially affected repos: the project's repos (see `.claude/project.json` → `repos`).
- Tools: Read, Grep, Glob (search for references across the codebase).

# Inputs

- **Refactoring goal**: what to change and why (e.g., "rename the `legacy_entity` table and all references to `new_entity`").
- **Scope boundary**: which repos are in scope. Defaults to all project repos (`project.json → repos`).
- **Constraints**: deadlines, feature-freeze windows, or areas that must not be touched.

# Outputs

- Format: markdown refactoring plan document with the sections shown in the template below.
- Length budget: max 200 lines for plans under 20 files affected; request phase breakdown for larger blast radii.

<output-template>
```markdown
## Refactoring Plan: [Title]

### Current State
[What exists now -- file paths, type names, usage counts. Every file cited was found via Grep/Glob.]

### Target State
[What it should look like after the refactor]

### Impact Analysis
| Repo | Files Affected | Type of Change | Risk |
|------|---------------|----------------|------|
| apps/<mainApp> | src/lib/db/schema.ts, 12 more | Type rename | Medium |

### Step-by-Step Plan
1. [File path]: [What to change] -- [Why this order]
2. ...

### Risk Assessment
| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| WebSocket format mismatch | Medium | High | Version the message format; deploy the producer service first |

### Rollback Plan
[Steps to undo safely -- git revert sequence, undo migration if applicable. Specific enough to execute without interpretation.]

### Effort Estimate
| Phase | Estimated Time | Risk |
|-------|----------------|------|
| 1. [Phase name] | [hours/days] | Low/Medium/High |

### Cross-Repo Coordination
[If changes span multiple repos, reference the monorepo-coordination skill for merge ordering]
```
</output-template>

# Procedure

1. **Reason through the blast radius** -- Before running any Grep/Glob searches, reason through which repos are likely affected based on the refactoring goal. Identify the entity type (type, table, message format, file layout) and its likely cross-repo footprint. This prevents unnecessary broad searches and focuses tool calls.
   **Checkpoint:** Entity type identified; likely affected repos listed before any search runs.

2. **Analyze current state** -- Read the target code. Use Grep to find all references to the symbol/pattern being refactored across all project repos (application, device, and infrastructure). Record file paths and usage counts.
   **Checkpoint:** Grep/Glob results reviewed; file count confirmed. Every cited file is a real result, not a guess.

3. **Map the blast radius** -- For each reference found:
   - Is it a type definition, usage, import, or test?
   - Does it cross a service boundary (e.g., a WebSocket message field name used by both the producer service and the consumer app)?
   - Does it affect database schema (requires a migration in the project's migrations directory)?
   - Does it affect deployment values or chart templates?
   **Checkpoint:** Blast radius under 50 files. If over 50, escalate to the user before continuing.

4. **Determine execution order** -- Apply these rules:
   - Types and interfaces first, then implementations, then tests.
   - If the refactor spans repos, follow the monorepo coordination phase model (foundation -> wire -> consume).
   - Database schema changes (migrations) land before application code that depends on the new schema.
   - Prefer a gradual, backward-compatible strategy (Strangler Fig: new implementation alongside old, deprecation warnings, feature flag, then cleanup) over a big-bang rewrite whenever the change crosses a service boundary or API/WebSocket contract.
   **Checkpoint:** Execution order documented with dependency justification for each ordering decision.

5. **Assess risks** -- For each identified risk:
   - What is the failure mode if this step goes wrong?
   - What is the mitigation (feature flag, backward-compatible intermediate step, versioned message format)?
   **Checkpoint:** At least one risk entry per affected repo.

6. **Define rollback** -- For each step, describe how to undo it:
   - Single-repo changes: `git revert` the commit (cite the step number to revert).
   - Database changes: an undo migration or forward migration that restores the old state (provide the SQL).
   - Cross-repo changes: revert in reverse merge order.
   **Checkpoint:** Rollback plan is specific -- includes git commands, migration file names, or revert order.

7. **Write the plan document** -- Use the output template above. Include file paths with line numbers where possible. Cite every Grep/Glob result.
   **Checkpoint:** Plan follows the template; all sections present.

# Self-check

<self-check>
- [ ] Every file in the impact analysis was found via Grep/Glob, not guessed
- [ ] The plan covers all project repos in every language of the stack, not just one language
- [ ] No references to languages or frameworks the project does not use (check `project.json → stack`)
- [ ] Database schema changes include a migration step with the path to the project's migrations directory
- [ ] Cross-repo refactors reference the `monorepo-coordination` skill for merge ordering
- [ ] Deployment config changes include a chart/config version bump step
- [ ] The rollback plan is specific (not just "revert the changes" -- includes commit references or migration file names)
- [ ] Risk assessment includes at least one entry per repo affected
- [ ] Blast radius was confirmed under 50 files, or user was consulted for larger scopes
- [ ] Reasoning about likely affected repos preceded the first Grep/Glob call
</self-check>

# Common mistakes

- **Assuming the wrong stack.** Check `project.json → stack` and the project's `CLAUDE.md` for the actual languages and frameworks; do not plan for services or languages the project does not have.
- **Skipping database migration steps.** Schema changes require proper migrations, never manual DDL. Place migration files in the project's migrations directory (see the project's `CLAUDE.md`).
- **Refactoring across repos without a merge order.** Cross-repo changes follow the monorepo coordination protocol. A rename that touches the producer and consumer services simultaneously risks breaking the message contract between them.
- **Breaking the WebSocket/SSE message format without versioning.** If a field name changes in a streamed message, both producer and consumer must be updated. Use a versioned message format or backward-compatible additive changes.
- **Forgetting deployment config version bumps.** Any change to chart templates requires bumping the chart version and refreshing dependencies.
- **Planning a refactor without checking test coverage first.** If the target code has no tests, the plan includes a "write tests for current behavior" step before refactoring.
- **Writing vague rollback plans.** "Revert the changes" is not a rollback plan. Specify which commits to revert, in which order, and which migrations to undo.

# Escalation

- **Blast radius exceeds 50 files**: the refactor may need to be broken into phases. Surface the scope to the user for approval before continuing.
- **Database schema change with no existing migration pattern**: flag it -- the user may need to create the migration manually or consult the infrastructure repo maintainer.
- **WebSocket/SSE format change without a versioning mechanism**: escalate -- breaking a real-time contract requires coordination between the producer and consumer teams.
- **Refactor touches code with zero test coverage**: recommend writing characterization tests before refactoring; flag as a risk.

# What to surface

- Total blast radius: N files across M repos.
- The highest-risk step in the plan and its mitigation.
- Whether the refactor requires a database migration.
- Whether the refactor spans repos and requires monorepo coordination.
- Estimated execution order and any steps that block others.

# Examples

<example name="rename-entity-across-codebase">
**Example: Renaming the `legacy_entity` table and type to `device` across the codebase**

Current state:
- `apps/<mainApp>/src/lib/db/schema.ts`: table `legacy_entity`, type `LegacyEntity`, 47 references across 23 files.
- `<device>/agents/skills/`: 8 references to `legacy_entity` in Python code.
- `<infrastructure-repo>/charts/<mainApp>/values.yaml`: no references (uses generic service names).
- `<infrastructure-repo>/files/backendMigration/`: table `legacy_entity` defined in migration V003.

Step-by-step plan:
1. `<infrastructure-repo>/files/backendMigration/V047__rename_legacy_entity_to_device.sql`: `ALTER TABLE backend.legacy_entity RENAME TO device;` with column renames.
2. `apps/<mainApp>/src/lib/db/schema.ts`: update the ORM schema -- rename table and type.
3. `apps/<mainApp>/src/app/api/legacy-entities/`: rename route directory to `devices`, update handlers.
4. `apps/<mainApp>/src/**/*.ts`: update all imports and references (23 files).
5. `<device>/agents/skills/`: update Python references (8 files).
6. Update tests in both repos.

Risk: WebSocket messages use `device_id` label (already correct). API route rename breaks any external consumers of `/api/legacy-entities` -- add redirect or alias for backward compatibility.

Rollback: revert steps 6-1 in reverse order. For step 1, apply `V048__revert_device_to_legacy_entity.sql` with `ALTER TABLE backend.device RENAME TO legacy_entity;`.
</example>

# Failure modes

| Mode | Symptom | Detection | Fix |
|------|---------|-----------|-----|
| Missed reference in Grep | Type or import error after refactor | Run Grep again with broader pattern | Fix missed reference; add a broader search pattern to the plan |
| Migration conflicts with another branch | Migration version collision | Migration validation fails | Renumber the migration; coordinate with team |
| WebSocket format mismatch | Consumer receives messages with old field names | Integration test failure | Deploy producer changes first (additive); then update the consumer |
| Chart version not bumped | Dependency refresh fails or uses stale chart | The infrastructure repo's chart-sync check script fails | Bump version; run sync check |

# Related skills

- `monorepo-coordination` -- merge ordering when the refactor spans repos.
- `functional-design` -- execute pure-function refactoring directly (this skill plans, that skill executes).
- `code-standards` -- coding standards to apply during the refactor.
- `migration-check` -- verifying database migration compatibility.
- `deployment` -- deployment config changes required by the refactor.
