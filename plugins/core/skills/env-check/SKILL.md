---
name: env-check
version: "1.0.0"
owner: "agentry-core"
description: "Use this skill when a task involves adding, removing, or renaming environment variables across the project's repos OR verifying env var documentation before a release. Don't use it for runtime env introspection in a live cluster (that requires exec access to the running service)."
disable-model-invocation: true
allowed-tools: Read, Grep, Glob
color: teal
---

# Purpose

Audit environment variables across all of the project's repositories (see `.claude/project.json` → `repos`) to find missing, unused, undocumented, or inconsistent env vars and flag security issues. Produce a structured markdown report with file:line citations. Related skills: `gcp-cicd-auth` (for cloud credential variables), `deployment` (for CI/CD variable plumbing and runtime values injection).

# When to use this skill

- Add or remove an env var in any of the project's repos and verify cross-repo consistency
- Prepare a release and confirm all required env vars are documented in `env.example`
- Review a PR that touches `values.yaml`, `env.example`, `getServerEnv()`, or `clientEnv`
- Investigate a deployment failure caused by a missing or misspelled env var

# When NOT to use this skill

- Checking which env vars are populated in a running cluster or pod -- requires exec access to the running service, not static analysis
- Adding a new secret to the cloud provider's secret manager -- use `gcp-cicd-auth`
- Reviewing Prisma schema, API routes, or any non-env-var code
- Writing unit tests that use env var fixtures -- test fixtures intentionally use placeholder values

# Required environment (Runtime: .claude/skills/env-check/SKILL.md)

- Tools/libraries: Read, Grep, Glob (no write access)
- Repos in scope: the project's canonical repo list from `.claude/project.json` → `repos`, plus the device/edge repo (`device`) if the project has one. Typical shape: the main app, other apps, the infrastructure repo, and the device repo.

# Inputs

- `repos: string[]` -- list of repository root paths to audit (default: the project's full repo list above)
- `focus: string` (optional) -- specific env var name or prefix to audit (e.g., `NEXT_PUBLIC_`, `MOCK_MODE`)

# Outputs

**Format:** Markdown report saved to the path specified by the caller.

**Length budget:** Max 150 lines for the report body. Consolidate tables with more than 20 rows into a top-20 list sorted by severity, with a count of omitted items.

**Output template:**

```markdown
## Environment Variables Report

**Repositories Checked:** N
**Total Variables Found:** N
**Documented in env.example:** N
**Missing from examples:** N
**Confidence:** HIGH | MEDIUM (if dynamic env access patterns detected)

### By Repository
| Repo | Defined | Used | Documented | Issues |
|------|---------|------|------------|--------|
| apps/<mainApp> | 12 | 14 | 10 | 4 |

### Missing Variables
| Variable | Used at | Expected in |
|----------|---------|-------------|
| `DATABASE_URL` | `src/lib/db/index.ts:8` | `env.example` |

### Unused Variables
| Variable | Declared in | Last referenced |
|----------|-------------|-----------------|
| `OLD_API_URL` | `env.example:3` | nowhere |

### Security Issues
| Issue | Location | Severity |
|-------|----------|----------|
| Hardcoded API key | `src/lib/maps.ts:4` | critical |

### Low-Confidence Findings
[Dynamic env access patterns that could not be resolved statically]
```

# Procedure

<procedure>

1. **Glob for env-related files** -- Use patterns `**/.env*`, `**/env.example`, `**/values*.yaml`, `**/*.populated.yaml`, `**/env/server.ts`, `**/env/client.ts`.
   Glob operations across different repos are independent -- run all repo scans as parallel tool calls.
   Checkpoint: at least one env file found per repo.

2. **Grep for env var usage per repo** (run all repo greps in parallel):
   - Python repos (e.g., the device/edge repo): `os.environ.get(`, `os.getenv(` in `src/`
   - Node/Next.js apps (e.g., the main app): `process.env.` in `src/`, `NEXT_PUBLIC_` in `src/` (incl. access via `getServerEnv()` / `clientEnv` helpers)
   - Infrastructure / service config repos: `env:` sections and `{{ .Values.* }}` references in `templates/`, keys in `values.yaml`
   - Exclude `node_modules/`, `.next/`, `__pycache__/`, `venv/`
   Checkpoint: count of unique var names per repo.

3. **Cross-reference** -- Compare defined vars vs used vars vs documented vars. Documentation sources: `env.example`, plus README.md / CLAUDE.md / setup guides mentions. Flag: (a) used but not in `env.example`, (b) in `env.example` but never used, (c) inconsistent naming across repos for the same logical var.
   Checkpoint: cross-reference table populated.

4. **Security check** -- Flag hardcoded strings that look like secrets (API keys, passwords, tokens) in source code. Verify sensitive vars use the runtime's secret manager (not committed `values.yaml`). Verify `*.populated.yaml` files are in `.gitignore`. NEVER print actual secret values in the report -- only flag presence and file:line location.
   Checkpoint: security findings logged.

5. **Detect dynamic access** -- Grep for `process.env[` (bracket notation) and `os.environ[` patterns. Mark these as low-confidence findings since the var name cannot be resolved statically.
   Checkpoint: dynamic access patterns counted.

6. **Compile report** -- Assemble findings into the output template above. Every finding must include `file:line` citation. Respect the 150-line length budget.
   Checkpoint: report complete, all sections present.

</procedure>

# Known variables (reference baseline)

Build the expectation set from the project itself -- the `env.example` files in each repo, the project's `CLAUDE.md`, and any setup guides. Typical shapes to expect (always verify against current code, never assume exhaustive):

- **Device/edge repo:** mock/simulation toggles, device identity, backend API URL, WebSocket/HTTP ports
- **Main web app:** `NEXT_PUBLIC_*` client keys, `DATABASE_URL`, auth provider vars (`NEXTAUTH_*` / `AUTH_*`), cache/queue URLs
- **Service-config values keys:** replica counts, `image.tag`, ingress settings, DNS suffixes

# Self-check before returning

- [ ] Every finding includes a `file:line` citation
- [ ] No actual secret values appear anywhere in the report
- [ ] Dynamic env access patterns flagged as low-confidence, not reported as definitive
- [ ] `node_modules/`, `.next/`, `__pycache__/`, test fixtures excluded from scan
- [ ] Cross-repo consistency checked (same logical var has same name across repos)
- [ ] Report includes confidence level (HIGH if no dynamic patterns, MEDIUM otherwise)
- [ ] Report stays within the 150-line length budget

# Common mistakes to avoid

- DO NOT print secret values in the report -- only flag file:line locations where secrets appear
- DO NOT report env vars found only in test fixtures or mock files as "missing" -- test code intentionally uses placeholders
- DO NOT scan `node_modules/`, `.next/`, or other generated directories -- they contain false positives
- DO NOT treat `process.env[dynamicKey]` as a specific missing var -- flag as low-confidence dynamic access
- DO NOT assume `NEXT_PUBLIC_*` vars are server-side -- they are client-exposed and have different security implications

# What to surface to the user

- File paths and line numbers for every finding
- Which repos were scanned and which were skipped (with reason)
- Any env vars that exist in one repo but are missing from the corresponding service-config `values.yaml`
- Any `*.populated.yaml` files that are NOT in `.gitignore`

# Escalation

- Stop and ask when: a `*.populated.yaml` file containing apparent secrets is NOT in `.gitignore` (potential secret leak)
- Stop and ask when: more than 5 env vars are used in code but absent from all documentation (may indicate a documentation backlog vs actual bugs)
- Stop and ask when: dynamic env access patterns account for >30% of env var usage (static analysis unreliable)

# Examples

<example title="Cross-repo env var audit for BACKEND_API_URL">

**Task:** Verify `BACKEND_API_URL` is consistent across the device/edge repo and the service config in the infrastructure repo.

**Step 1 -- Grep the device/edge repo:**
```
Grep: os.environ.get("BACKEND_API_URL" in <device>/src/
Found: src/agents/main_agent.py:12  BACKEND_API_URL = os.environ.get("BACKEND_API_URL", "http://localhost:3000")
```

**Step 2 -- Grep the service config (run in parallel with Step 1):**
```
Grep: BACKEND_API_URL in the infrastructure repo
Found: charts/<device>/values.yaml:18  BACKEND_API_URL: "http://<mainApp>:3000"
Found: charts/<device>/templates/deployment.yaml:42  - name: BACKEND_API_URL
```

**Step 3 -- Cross-reference:**
- Device repo default: `http://localhost:3000`
- Production value: `http://<mainApp>:3000`
- Consistent naming, different defaults (expected: local dev vs in-cluster service DNS)

**Step 4 -- Check env.example:**
```
Grep: BACKEND_API_URL in <device>/env.example
Not found -- MISSING from env.example
```

**Report entry:**
```
| `BACKEND_API_URL` | `src/agents/main_agent.py:12` | `env.example` |
```

</example>

# Failure modes

- **False positives from generated code**: symptom: hundreds of env var "findings" from `.next/` or `node_modules/` -> detect: finding count >50 for a single repo -> fix: verify exclusion patterns are applied, re-run scan
- **Dynamic access missed**: symptom: report says "all vars documented" but deployment fails with missing var -> detect: check for `process.env[` bracket patterns -> fix: grep for bracket notation, add to low-confidence section
- **Secret value leaked in report**: symptom: actual API key visible in report output -> detect: grep report text for patterns like `sk-`, `AIza`, base64 strings -> fix: immediately delete the report, re-run with value-scrubbing check, notify user

# Related skills

- `gcp-cicd-auth` -- defer to this skill for GCP-specific credential variables and Workload Identity Federation setup
- `deployment` -- compose with this skill when CI/CD pipeline variables need to match application env vars
- `deployment` -- compose with this skill when verifying that Cloud Run values match application expectations
- `deps-check` -- shares the same canonical five-repo scope; align repo lists when auditing both env vars and dependencies
