---
name: security-audit
version: "1.0.0"
owner: "agentry-core"
description: "Use this skill when a task involves reviewing or scanning application code for security vulnerabilities, evaluating OWASP Top 10 compliance for the project's apps, device/edge repo, or cloud service config, or scanning for hardcoded secrets. Don't use it for CI/CD pipeline hardening or SBOM generation (use supply-chain-security instead)."
disable-model-invocation: true
allowed-tools: Read, Grep, Glob, Bash
color: teal
---

# Purpose

Perform a static security audit of the project's application code, configuration, and infrastructure manifests. Produce a structured report listing vulnerabilities with file:line citations, OWASP category mapping, severity classification, and prioritized remediation steps.

This skill covers application-level vulnerabilities: injection, auth bypass, secrets leakage, insecure configuration, and dependency CVEs. It does **not** cover CI/CD pipeline hardening, image scanning, or SBOM generation -- those belong to the `supply-chain-security` skill.

# When to use this skill

- User explicitly asks to "audit," "scan," or "review" code for security vulnerabilities
- User asks about OWASP compliance for any of the project's repositories (see `.claude/project.json` → `repos`)
- User asks to "find vulnerabilities," "harden auth," or "check for secrets" in application code
- A new feature or module needs a security review before merge
- Investigating a suspected auth bypass or injection risk

# When NOT to use this skill

- **CI/CD pipeline hardening, image scanning, SBOM generation** -- use `supply-chain-security`
- **Active penetration testing, fuzzing, or exploit development** -- this skill is passive static analysis only
- **Code quality review without security focus** -- use `code-standards` or `code-quality`
- **Writing tests to reproduce a vulnerability** -- use `testing`
- **Updating Cloud Run values or deploying fixes** -- use `deployment` or `deployment`
- **Generic "check this code" or "review this PR" without security context** -- use `code-standards`

# Required environment (Runtime: .claude/skills/security-audit/SKILL.md)

- Read access to the target repository
- `npm` available for `npm audit` (Node/TypeScript apps)
- `pip-audit` available for Python dependency scanning (Python repos, e.g. the device/edge repo)
- No write access needed -- this is a read-only analysis skill

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Scope | Yes | Repository or module path to audit (e.g., `apps/<mainApp>/src/app/api/`, `<device>/src/`) |
| Depth | No | `quick` (hardcoded secrets + critical injection only) or `full` (all OWASP categories). Default: `full` |

# Outputs

**Format:** Structured markdown security audit report.

**Length budget:** Max 250 lines for the full report. For `quick` depth, max 80 lines. Keep the report concise -- do not include passing checks unless the user requests a full compliance report.

**Output template:**

```markdown
## Security Audit Report

**Scope:** [repo/module path]
**Date:** [ISO date]
**Depth:** [quick|full]

### Summary
| Severity | Count |
|----------|-------|
| Critical | X |
| High     | Y |
| Medium   | Z |
| Low      | W |

### Findings

#### [FINDING-NNN] [Title]
- **Severity:** Critical | High | Medium | Low
- **OWASP:** A01-A10 category
- **CWE:** CWE-NNN (if applicable)
- **Location:** `file/path.ts:42`
- **Description:** What the vulnerability is
- **Evidence:** Code snippet showing the issue
- **Remediation:** Specific fix with code example
- **Confidence:** HIGH | MEDIUM | LOW

### OWASP Top 10 Checklist
| # | Category | Status | Notes |
|---|----------|--------|-------|
| A01 | Broken Access Control | PASS/FAIL/N-A | |
| ... | ... | ... | |

### Action Plan
[Prioritized remediation steps with effort estimates]

### Scope Coverage
[List of files/modules checked vs. skipped, with reasons for skipping]
```

# Procedure

<procedure>

### 1. Load the OWASP checklist
Read `resources/owasp-checklist.md` (bundled with this skill). Use it as the authoritative checklist for all OWASP category checks. Check every item before generating the OWASP table in the report.

Checkpoint: OWASP checklist loaded; all 10 categories queued for review.

### 2. Hardcoded secrets scan
Search for API keys, passwords, tokens, private keys in source code:
```bash
grep -rn --include='*.ts' --include='*.tsx' --include='*.py' --include='*.yaml' --include='*.yml' --include='*.env*' \
  -E '(password|secret|api_key|token|private_key)\s*[:=]' <scope>
```
Check Cloud Run `values.yaml` files -- secrets must be in `*.populated.yaml`, never in committed values files.

Checkpoint: Hardcoded secrets scan complete; any Critical findings flagged immediately.

### 3. Injection risks

**SQL Injection (web app / ORM code):** Check ORM queries (e.g., Prisma) for raw string concatenation. A typed query builder is safe; flag any raw-SQL escape hatch (e.g., `sql.raw()`) with user input.

**Command Injection (Python / device code):** Check `subprocess.run()`, `os.system()`, `Popen()` calls for user-controlled arguments. Flag any `shell=True` usage or string-interpolated commands; require list-form args with validated inputs.

**XSS (React/Next.js apps):** Search for `dangerouslySetInnerHTML`. If found, verify input is sanitized.

Checkpoint: Injection scan complete for all applicable categories.

### 4. Authentication and authorization

**Auth.js v5 OIDC (Next.js apps, where applicable):**
- Verify middleware protects routes under `/dashboard`, `/api/*`
- Verify `export const dynamic = 'force-dynamic'` on pages importing from next-auth
- Verify PKCE flow configuration is intact
- Check that session checks use `await auth()` not a stale pattern

Checkpoint: Auth checks verified or findings logged.

### 4b. Device & realtime transport security (skip if the project has no device/realtime surface — see `.claude/project.json` → `device`, `domainTerms`)

- **WebSocket auth:** telemetry/command WebSocket and SSE channels must enforce authentication (e.g., the main app's WebSocket auth middleware); flag any unauthenticated channel
- **Transport encryption:** flag `ws://` where `wss://` is expected outside local dev; check CORS configuration on API and realtime endpoints
- **Telemetry input validation:** message fields consumed from the wire must be validated before use; flag command-injection paths from telemetry into device firmware subprocess/exec calls
- **Device control authorization:** verify device command endpoints check role/ownership before issuing commands; flag unauthorized-control paths
- **Media stream access:** image/camera/audio stream endpoints must not be reachable without auth
- **Edge hardening:** BLE/serial access controls on the edge device; rate limiting on command endpoints

Checkpoint: device/realtime surface checks complete.

### 5. Dependency vulnerabilities
```bash
# Node/TypeScript app -- read-only audit, JSON output
cd apps/<mainApp> && npm audit --audit-level=moderate --json

# Python repo (e.g., the device/edge repo) -- read-only audit
cd <device> && pip-audit --output json
```
**Warning:** `npm audit` may write to `package-lock.json` if run without `--json`. Always use `--json` flag to ensure read-only operation. `pip-audit` performs network lookups to check vulnerability databases.

Checkpoint: Dependency scan output captured or scan failure logged.

### 6. Infrastructure security (deployment manifests / service config — see `.claude/project.json` → `cloud.runtime`)
- SecurityContext: `runAsNonRoot: true`, `readOnlyRootFilesystem: true` (where the runtime supports it)
- Network policies: present and correctly scoped (use standard well-known labels, not custom labels)
- Resource limits: CPU and memory limits defined for all containers
- TLS: ingress uses TLS termination; no plain HTTP in production
- Secrets: no secrets in committed values/config files (use the runtime's secret manager or untracked populated files)

Checkpoint: Infrastructure security checks complete.

### 7. Compile report
Assemble findings into the output template. Sort findings by severity (Critical first). Respect the length budget (250 lines full, 80 lines quick).

Checkpoint: Report complete with all sections populated.

</procedure>

# Self-check before returning

- [ ] `resources/owasp-checklist.md` was loaded and every item was checked
- [ ] Every finding has a file:line citation
- [ ] Every finding has a severity classification (Critical/High/Medium/Low)
- [ ] Every finding maps to an OWASP category (A01-A10)
- [ ] The report lists which files/modules were checked and which were skipped
- [ ] No secrets found during the audit are printed in plain text in the report -- redact with `***`
- [ ] False positives are excluded or marked with LOW confidence
- [ ] Action plan items are ordered by severity then effort
- [ ] Report stays within the length budget

# Common mistakes to avoid

- **Printing found secrets in plain text** -- always redact credentials, API keys, and tokens with `***` in the report
- **Running `npm install` or `pip install` during audit** -- this skill is read-only; do not install packages
- **Modifying source files** -- never fix vulnerabilities during the audit; report them for the developer to fix
- **Counting Prisma query builder usage as SQL injection** -- Prisma's typed API is parameterized by default; only flag `sql.raw()` with user input
- **Flagging test fixtures as secrets** -- mock API keys in test files are not real secrets; verify before reporting
- **Running `make test-all`** -- this executes integration tests requiring live services; not needed for security audit

# What to surface to the user

- Total finding count by severity
- Any Critical or High findings with remediation steps
- OWASP categories that failed or could not be assessed
- Dependencies with known CVEs and their fix versions
- Which files/modules were checked and which were skipped

# Escalation

- **Critical findings (auth bypass, RCE, secrets in git):** Flag immediately to the user; do not wait for the full report
- **Findings that require infrastructure changes (NetworkPolicy, TLS):** Note that remediation requires Cloud Run service config updates and cluster access
- **Uncertain findings (low confidence):** Mark as `[LOW-CONFIDENCE]` and recommend manual verification
- **Scope too large to audit in one session:** Ask the user to narrow scope or prioritize specific OWASP categories

# Examples

<example title="Audit a new API route handler">

**Input:** "Check the new orders API for security issues"
**Scope:** `apps/<mainApp>/src/app/api/orders/`
**Process:** Load OWASP checklist -> check auth middleware -> check for injection in query params -> check Zod validation -> verify session checks -> report findings

</example>

<example title="Quick secrets scan before merge">

**Input:** "Any hardcoded secrets in this branch?"
**Scope:** Changed files in current branch
**Depth:** `quick`
**Process:** `git diff --name-only main` -> grep for secret patterns -> check Cloud Run values -> report

</example>

<example title="Full OWASP audit of the device/edge repo">

**Input:** "Run a full security audit on the device firmware"
**Scope:** `<device>/src/` (project.json → device)
**Process:** Load OWASP checklist -> check subprocess calls -> check asyncio patterns for auth -> check pip-audit -> check Dockerfile for runAsNonRoot -> report all 10 OWASP categories

</example>

# Failure modes

| Failure | Recovery |
|---------|----------|
| `npm audit` fails (no network) | Report as "dependency scan skipped -- no network"; proceed with static analysis |
| `pip-audit` not installed | Report as "Python dependency scan skipped -- pip-audit not available" |
| Scope too broad (entire monorepo) | Ask user to narrow to a specific module or OWASP category |
| Cannot determine if a pattern is a real vulnerability | Mark as `[LOW-CONFIDENCE]` with evidence; recommend manual review |
| Found a real secret committed to git | Redact in report; flag as Critical; recommend immediate rotation |

# Bundled resources

- `resources/owasp-checklist.md` -- OWASP Top 10 checklist with per-category items, written for a typical modern stack (ORM for SQL, schema validation, OIDC auth). Map each item onto the project's actual stack (see `.claude/project.json` → `stack` and the project's `CLAUDE.md`). Load this file at the start of every audit.

# Related skills

- **supply-chain-security** -- CI/CD pipeline hardening, image scanning, SBOM generation (not application code vulnerabilities)
- **testing** -- writing tests to reproduce or prevent security issues
- **deployment** -- fixing SecurityContext, NetworkPolicy, and TLS configuration
- **code-standards** -- general code quality checks (not security-focused)
- **troubleshooting** -- investigating live incidents (not preventive auditing)
