---
name: deps-check
version: "1.0.0"
owner: "agentry-core"
description: "Use this skill when auditing dependency versions, checking for outdated packages, or scanning for security vulnerabilities across the project's repos. Don't use it for upgrading packages (that requires a separate implementation task) or for deployment config template issues (use deployment)."
allowed-tools: Read, Bash, Glob, Grep
disable-model-invocation: true
color: teal
---

# Purpose

Audit dependency versions across all of the project's repositories (see `.claude/project.json` → `repos`), producing a structured report of outdated packages, security vulnerabilities, and cross-repo version mismatches. This skill scans and reports only; it does not modify lockfiles or upgrade packages. For acting on findings, hand off to the implementation agent with the report as input.

Placeholders: `<mainApp>` = `project.json → mainApp`; `<device>` = `project.json → device` (the device/edge repo, if the project has one); `<infrastructure-repo>` = the project's infrastructure/chart repo(s).

# When to use this skill

- Trigger A -- Periodic dependency audit requested (e.g., monthly security review)
- Trigger B -- Pre-release check to verify no known vulnerabilities ship
- Trigger C -- Cross-repo version alignment check (e.g., ensuring shared TypeScript packages use the same version in the main app)
- Trigger D -- After a CVE advisory, checking if the project's repos are affected

# When NOT to use this skill

- Anti-trigger A -- Upgrading packages or running `npm update` / `pip install --upgrade` (that is an implementation task, not an audit)
- Anti-trigger B -- Deployment config template lint errors (use `deployment`)
- Anti-trigger C -- Offline environment without network access (package registry queries will fail silently)
- Anti-trigger D -- Repos without a lockfile (`package-lock.json` or `requirements.txt`) -- flag as incomplete scan rather than proceeding
- Anti-trigger E -- Reviewing a single `package.json` for a new feature (not a full audit)

# Required environment (Runtime: .claude/skills/deps-check/SKILL.md)

- Tools/libraries: `npm` (v9+), `pip` / `pip-audit`, `helm` (v3+), network access to package registries
- Allowed tools: Read, Bash, Glob, Grep
- Repos in scope: the project's repos, as listed in `.claude/project.json` → `repos` -- typically:
  1. `apps/<mainApp>` (Node.js)
  2. `<device>` (Python)
  3. the infrastructure/chart repo(s)

# Inputs

- `repos: string[]` -- List of repos to scan. Defaults to all repos from `project.json → repos`.
- `severity_threshold: "critical" | "high" | "moderate" | "low"` -- Minimum severity to include in report. Default: `"moderate"`

# Outputs

**Format:** Structured markdown report returned inline to the caller.

**Length budget:** Max 200 lines per report. Consolidate the outdated-packages table if it exceeds 20 rows (show top 20 by severity, append a "... and N more" note).

**Output template:**

```markdown
## Dependency Audit Report

**Date**: {YYYY-MM-DD}
**Repos scanned**: {N} of {total}
**Scan failures**: {list of repos where scan command failed, or "None"}

### Summary

| Repo | Dependency file | Total deps | Outdated | Vulnerable (>= threshold) |
|------|----------------|------------|----------|---------------------------|
| apps/<mainApp> | package.json | {N} | {N} | {N} |
| <device> | requirements.txt | {N} | {N} | {N} |
| <infrastructure-repo> | Chart.yaml (subcharts) | {N} | {N} | N/A |

### Critical / High Vulnerabilities

| Package | Current | Fixed in | Severity | CVE | Repo |
|---------|---------|----------|----------|-----|------|
| {name} | {ver} | {ver} | {sev} | {id} | {repo} |

### Cross-Repo Version Mismatches

| Package | Version in repo A | Version in repo B | Notes |
|---------|-------------------|-------------------|-------|

### Outdated Packages (non-vulnerable)

| Package | Current | Latest | Type | Repo |
|---------|---------|--------|------|------|

### Recommendations

1. {Prioritized action item with specific version target}
```

# Procedure

<procedure>

1. **Locate dependency files** -- For each repo in the input list, find the dependency manifest:
   - `apps/<mainApp>`: `package.json` + `package-lock.json`
   - `<device>`: `requirements.txt`, `requirements-dev.txt`, and `pyproject.toml` (if present)
   - infrastructure/chart repo(s): `charts/*/Chart.yaml` (subchart dependencies)

   Checkpoint: At least one dependency file found per repo. If a repo has no dependency file, log it as a scan gap.

2. **Run outdated checks** -- Execute scan commands with error handling. Scans for the Node.js and Python repos are independent -- run them as parallel Bash calls. The chart-repo update must complete before chart searches.
   ```bash
   # Node.js (main app) -- can run in parallel with Python scan
   cd apps/<mainApp> && npm outdated --json 2>/dev/null || echo '{"error": "npm outdated failed"}'

   # Python (device repo) -- can run in parallel with Node scan
   cd <device> && pip list --outdated --format=json 2>/dev/null || echo '[]'

   # Charts (all chart repos) -- must run after helm repo update
   helm repo update 2>/dev/null  # Side effect: updates local chart repo cache; requires network
   helm search repo <chart-name> --versions
   ```

   Checkpoint: Each scan command either produced output or an error message was captured.

3. **Run security scans** -- Execute vulnerability checks (Node.js and Python scans can run in parallel):
   ```bash
   # Node.js
   cd apps/<mainApp> && npm audit --json 2>/dev/null

   # Python
   cd <device> && pip-audit --format=json 2>/dev/null || echo '{"error": "pip-audit not installed or failed"}'
   ```

   Checkpoint: Security scan output captured for each repo.

4. **Check cross-repo alignment** -- Compare shared package versions across repos. For TypeScript packages used in multiple places, verify version consistency.

   Checkpoint: Any version mismatches logged.

5. **Compile report** -- Assemble findings into the output template above. Sort vulnerabilities by severity (critical first). Include scan failures in the report header. Respect the 200-line length budget.

   Checkpoint: Report assembled with all sections populated (or marked "None found").

6. **Triage recommendations** -- For each critical/high vulnerability, check if a patched version exists and note the upgrade path. When recommending an upgrade, consult the package CHANGELOG or migration guide for breaking changes between the current and target versions, and include a post-upgrade testing checklist (typecheck, unit tests, build) plus a rough effort estimate. Do NOT run `npm audit fix` or `pip install --upgrade` automatically.

   Checkpoint: Top 3 recommendations prioritized by severity and fix availability.

</procedure>

# Self-check before returning

- [ ] Every repo in the input list was scanned or its scan failure was reported
- [ ] `helm repo update` side effect was explicitly noted (it modifies the local chart repo cache)
- [ ] No `npm audit fix`, `npm update`, or `pip install --upgrade` was executed (this skill is read-only)
- [ ] Vulnerability severity is reported using the registry's severity level, not a custom scale
- [ ] Cross-repo version mismatches section is populated (even if empty with "None found")
- [ ] Scan failures (command not found, no network, missing lockfile) are listed in the report header
- [ ] Report stays within the 200-line length budget

# Common mistakes to avoid

- DO NOT run `npm audit fix` or `pip install --upgrade` -- this skill audits only; upgrades require a separate task
- DO NOT assume `pip-audit` is installed -- check first and report if missing
- DO NOT ignore `helm repo update` as a side effect -- it mutates the local chart repo cache and requires network access
- DO NOT skip `pyproject.toml` -- modern Python projects may declare dependencies there instead of `requirements.txt`
- DO NOT report `npm outdated` warnings as vulnerabilities -- outdated is not the same as vulnerable
- DO NOT run scans without capturing stderr -- silent failures produce incomplete reports

# What to surface to the user

- Total vulnerability count by severity level
- Any scan that failed (missing tool, no network, no lockfile)
- Cross-repo version mismatches that could cause runtime issues
- The top 3 recommended actions, prioritized by severity and fix availability

# Escalation

- Stop and ask when: A critical CVE is found with no patched version available (requires security team decision)
- Stop and ask when: `npm audit` or `pip-audit` command is not available and cannot be installed
- Stop and ask when: Network access is unavailable (all registry-based scans will fail)
- Stop and ask when: A dependency file is missing from an expected repo (may indicate repo restructuring)

# Examples

<example title="Monthly dependency audit across all project repos">

```bash
# Step 1: Scan the main app (runs in parallel with Step 2)
cd apps/<mainApp>
npm outdated --json
# {
#   "next": {"current": "16.1.6", "wanted": "16.1.8", "latest": "16.2.0"},
#   "some-orm": {"current": "0.45.1", "wanted": "0.45.1", "latest": "0.46.0"}
# }

npm audit --json
# {
#   "vulnerabilities": {
#     "got": {"severity": "moderate", "via": ["CVE-2025-XXXX"], "fixAvailable": true}
#   }
# }

# Step 2: Scan the device repo (runs in parallel with Step 1)
cd <device>
pip list --outdated --format=json
# [{"name": "device-protocol-lib", "version": "2.4.41", "latest_version": "2.4.43"}]

pip-audit --format=json
# {"dependencies": [], "vulnerabilities": []}

# Step 3: Scan chart dependencies (after helm repo update)
helm repo update
helm search repo bitnami/postgresql --versions | head -5
```

**Report output:**
```markdown
## Dependency Audit Report

**Date**: 2026-05-24
**Repos scanned**: 3 of 3
**Scan failures**: None

### Summary

| Repo | Dependency file | Total deps | Outdated | Vulnerable (>= moderate) |
|------|----------------|------------|----------|--------------------------|
| apps/<mainApp> | package.json | 42 | 2 | 1 |
| <device> | requirements.txt | 15 | 1 | 0 |
| <infrastructure-repo> | Chart.yaml | 3 | 0 | N/A |

### Critical / High Vulnerabilities

None at this severity level.

### Outdated Packages (non-vulnerable)

| Package | Current | Latest | Type | Repo |
|---------|---------|--------|------|------|
| next | 16.1.6 | 16.2.0 | npm | apps/<mainApp> |
| some-orm | 0.45.1 | 0.46.0 | npm | apps/<mainApp> |
| device-protocol-lib | 2.4.41 | 2.4.43 | pip | <device> |

### Recommendations

1. Upgrade `got` in the main app to fix moderate CVE-2025-XXXX (fix available via `npm audit fix`)
2. Evaluate the Next.js minor upgrade for bug fixes (test build + typecheck before merging)
3. Upgrade device-protocol-lib 2.4.41 -> 2.4.43 for protocol updates
```

</example>

# Failure modes

- Mode: `npm outdated` hangs -- symptom: command does not return within 60 seconds -- detect: timeout on Bash execution -- fix: check network connectivity; run with `--json` flag for faster output
- Mode: `pip-audit` not installed -- symptom: `command not found` error -- detect: stderr captured -- fix: report as scan gap in the audit report; suggest `pip install pip-audit`
- Mode: `helm repo update` fails -- symptom: cannot fetch latest chart versions -- detect: non-zero exit code -- fix: check chart repo configuration (`helm repo list`) and network access
- Mode: Incomplete scan due to missing lockfile -- symptom: `npm outdated` cannot determine wanted versions -- detect: warning in npm output -- fix: report as scan gap; recommend running `npm install` to generate lockfile

# Related skills

- `code-quality` -- after deps-check identifies outdated packages, code-quality may review the upgrade PR
- `deployment` -- deps-check should run before deployment to catch vulnerable dependencies
- `deployment` -- defer deployment config dependency version authoring to deployment; deps-check only reports current state
- `env-check` -- shares the same repo scope (`project.json → repos`); align repo lists when auditing both dependencies and env vars
