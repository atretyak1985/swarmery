---
description: Find feature flags, env vars, and config keys referenced in code but absent from env-check / settings — dead toggles that pretend to gate behaviour
color: yellow
---

# Sweep Stale Flags (Dynamic Workflow)

Scope hint: $ARGUMENTS (optional — restrict to one repo, e.g. the main app or the device repo; see `project.json → mainApp` / `device`)

## When to use

Periodic hygiene sweep across the codebase to find:
- Feature flags read in code but never set in any deployment environment
- Env vars referenced in `process.env.X` / `os.getenv('X')` but absent from `env.example` / `values.*.yaml`
- Config keys that exist in `values.yaml` but no code reads them (rot)

This is breadth-first work — independent files, no mid-run user input needed. Perfect for Dynamic Workflow substrate.

For a targeted check on one variable, use the `env-check` skill directly.

## Pre-flight (mandatory)

- [ ] Phase 1 user-only gaps RESOLVED
- [ ] No code changes intended — discovery + report only
- [ ] Scope is closed (single repo or "all project repos" — `project.json → repos`)

## Instructions to @tech-lead

Generate a Dynamic Workflow that:

1. **Discovery stage** (parallel per repo) — extract:
   - Code references: `grep -rE "process\.env\.[A-Z_]+|os\.getenv\(['\"]?[A-Z_]+"` per repo
   - Code references for feature flags: `grep -rE "growthbook\.feature\(|isEnabled\(['\"]"` (or whatever flag library is in use)
   - Declared env vars: parse `env.example`, `.env.example`, all `values*.yaml`, all `Chart.yaml` defaults
   - Declared flags: parse `growthbook.json` or equivalent
2. **Cross-reference stage** (sequential reduce) — compute three sets:
   - **Stale reads** = used in code, absent everywhere → likely dead code
   - **Orphan declarations** = declared, never read → likely dead config
   - **Schema drift** = declared in some envs but missing in others
3. **Artifact** — `sweep-stale-flags-{date}.md` in `.claude-workspace/working/`.

## Categories

1. **Stale env-var reads** — code reads `process.env.X` / `os.getenv('X')` but no env declares X
2. **Stale flag reads** — code calls `isEnabled('foo')` but no flag config declares `foo`
3. **Orphan declarations** — declared in `values.yaml` / `env.example` but no code reads
4. **Schema drift** — declared in `values.<envAlias>.yaml` but missing in `values.prod.yaml` (or vice versa) — high incident risk
5. **Sensitive-name without secret** — variable name contains `KEY|SECRET|TOKEN|PASSWORD` but declared with a plaintext default (should source from the cloud provider's secret manager)

## Stop conditions

- All repos in scope discovered → emit reduced report
- Discovery returned <10 references → ESCALATE (likely scoping error)
- A repo failed checkout / read → continue with remaining, mark in report

## Output format

```markdown
# Stale Flag/Env Sweep — {date}

**Repos scanned:** N
**Stale reads:** X (high incident risk)
**Orphan declarations:** Y (config rot)
**Schema drift:** Z (deploy risk)

## Stale reads (dead code or undeclared env)
- `<mainApp>/src/.../file.ts:line` — reads `X_FLAG`, declared nowhere

## Orphan declarations (delete from values?)
- `values.<envAlias>.yaml:line` — `X_KEY`, no code reads

## Schema drift (urgent)
- `Y_TOKEN` declared in <envAlias> only; prod will fail

## Action plan
- Immediate: schema drift (deploy blockers)
- This week: stale reads in hot paths
- This month: orphan declarations
```

---

Now sweep: $ARGUMENTS
