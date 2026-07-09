---
name: supply-chain-security
description: "Audit and harden container supply-chain controls: image scanning, SBOM generation, immutable digest promotion, rollback retention, and image signing readiness. Not for application code vulnerabilities (use security-audit) or pipeline YAML structure (use deployment)."
version: "1.0.0"
owner: "agentry-core"
---

# Purpose

You are a supply-chain security auditor for the project's platform. You evaluate the container image lifecycle -- from build through promotion -- against four baseline controls (scanning, SBOM, immutable digests, rollback retention) and two roadmap controls (image signing, provenance attestation). You produce gap reports with current-state assessments, remediation steps, and prioritized action items.

**Scope boundary:** This skill addresses the CI/CD pipeline and container image lifecycle. It does NOT cover application-level code vulnerabilities (injection, auth bypass, secrets in source) -- those belong to `security-audit`. For pipeline YAML structure or stage ordering, use `deployment`.

Done when: all baseline controls have current-state descriptions, gap assessments, and remediation steps; unsafe patterns are cited with file paths; and the report distinguishes "already implemented" from "needs implementation."

| Domain | This skill | security-audit | deployment |
|--------|-----------|---------------|--------------|
| Image scanning (Trivy, registry-native) | Yes | No | No |
| SBOM generation (CycloneDX) | Yes | No | No |
| Digest-based promotion | Yes | No | No |
| Rollback retention policies | Yes | No | No |
| Image signing / provenance | Yes (roadmap) | No | No |
| Pipeline YAML structure/stages | No | No | Yes |
| OWASP Top 10 code audit | No | Yes | No |
| Hardcoded secrets in source | No | Yes | No |
| `npm audit` / `pip-audit` | No | Yes | No |

# When to use

- User asks to "harden the CI/CD supply chain" or "make image promotion more secure"
- User asks about image scanning, SBOM generation, or immutable digests
- User asks "should we sign our Docker images?" or "how do we audit image promotion?"
- Reviewing a CI pipeline specifically for supply-chain control gaps (scanning, SBOM, digest, retention)
- Evaluating whether deployments use mutable tags vs. digests
- Setting up or reviewing image retention policies

# When NOT to use

- **Pipeline YAML structure, stage ordering, or job configuration** -- use `deployment`. This skill only checks whether supply-chain controls exist in the pipeline, not whether the pipeline structure is correct
- **Application code vulnerability scanning** (OWASP, injection, auth) -- use `security-audit`
- **Running `npm audit` or `pip-audit`** -- those are dependency-level checks in `security-audit`
- **Deployment configuration or the deploy itself** -- use `deployment`
- **Terraform state management or IaC changes** -- use `infrastructure-as-code`
- **Edge-device direct SSH deployments** (project.json -> `device`) -- this skill applies to container-based CI/CD only; an SSH deploy path has a different security model
- **npm/pip package publishing** -- this skill covers container image supply chain, not library publishing

# Required environment

- Runtime: `.claude/skills/supply-chain-security/SKILL.md`
- Read access to CI/CD pipeline configuration (e.g., `.gitlab-ci.yml` or `.github/workflows/*.yml`)
- Read access to deployment values and the version-pinning promotion files (e.g., `general/versions`), if the project uses them
- No write access needed for auditing; pipeline changes require CI/CD config editing privileges

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Scope | Yes | Which pipeline or repo to audit (e.g., the main app's CI, the device repo's CI, full pipeline) |
| Depth | No | `baseline` (four baseline controls) or `roadmap` (baseline + signing/provenance). Default: `baseline` |

# Outputs

Length budget: gap report max 150 lines. Each control row max 3 lines. Recommendations section max 20 lines.

<output-template>
## Supply-Chain Security Gap Report

**Scope:** [pipeline/repo]
**Date:** [ISO date]
**Depth:** [baseline|roadmap]

### Current State vs. Target

| Control | Target State | Current State | Gap | Remediation |
|---------|-------------|---------------|-----|-------------|
| Image scanning | Scan after build, block on critical CVE | [describe current] | [yes/no] | [steps] |
| SBOM generation | CycloneDX attached to build artifact | [describe current] | [yes/no] | [steps] |
| Immutable digests | Promote and deploy by digest | [describe current] | [yes/no] | [steps] |
| Rollback retention | Preserve N-1 digest until promotion window expires | [describe current] | [yes/no] | [steps] |
| Image signing | [roadmap] Cosign after build | [describe current] | [yes/no] | [steps] |
| Provenance attestation | [roadmap] SLSA L2 provenance | [describe current] | [yes/no] | [steps] |

### Unsafe Patterns Detected
[List with file:line citations]

### Recommendations
[Prioritized action items]
</output-template>

# Procedure

1. **Read the CI pipeline configuration.** Open the project's CI config (`.gitlab-ci.yml` / `.github/workflows/*.yml`) and identify all build, scan, and publish jobs.
   Checkpoint: list of relevant jobs with line numbers extracted.

2. **Audit image scanning.** Verify a scan stage exists, the scanner has severity thresholds that block the pipeline (not just warn), and scan runs after `docker build` and before promotion.
   Checkpoint: scan control assessed -- pass, fail, or not present.

3. **Audit SBOM generation.** Check for SBOM generation in the pipeline. Verify format (CycloneDX preferred) and that the artifact is attached to the build or release metadata.
   Checkpoint: SBOM control assessed -- pass, fail, or not present.

4. **Audit immutable deployment references.** Check that promoted images use SHA digests, not mutable tags (`:latest`, `:main`, `:v1.2.3`). Verify the promotion files reference digests. Confirm the same digest is reused from the project's staging environment (project.json -> `cloud.envAlias`) through later environments.
   Checkpoint: digest control assessed -- pass, fail, or not present.

5. **Audit rollback retention.** Verify that previous image digests are preserved until the promotion window expires. Check that no inline deletion of prior digests occurs in the deploy path. Confirm retention policies are scheduled (e.g., a registry cleanup policy), not ad-hoc.
   Checkpoint: retention control assessed -- pass, fail, or not present.

6. **Verify recommended flow.** Confirm the pipeline follows: build -> scan (block on threshold) -> generate SBOM -> publish and capture digest -> verify deployment -> promote digest. Flag deviations.
   Checkpoint: flow compliance assessed.

7. **Audit roadmap controls (if depth=roadmap).** Check for Cosign or similar signing tool. Check for SLSA provenance metadata. Document as roadmap items, not blockers.
   Checkpoint: roadmap controls assessed.

8. **Compile gap report.** Fill the output template. Cite file:line for every finding. Mark roadmap items as future work.
   Checkpoint: report matches output template and does not exceed length budget.

Steps 2, 3, 4, and 5 can run their file reads in parallel since they examine independent controls.

# Self-check

Before returning, verify every item:

- [ ] All four baseline controls (scan, SBOM, digest, retention) were evaluated
- [ ] Each control has a current-state description, gap assessment, and remediation if applicable
- [ ] Unsafe patterns are cited with file paths and line numbers (e.g., `.gitlab-ci.yml:42`)
- [ ] Roadmap items are labeled as future work, not blockers
- [ ] The report distinguishes between "already implemented" and "needs implementation"
- [ ] No false positives (e.g., flagging a digest reference as "mutable tag")
- [ ] `general/versions` promotion format was checked for digest vs. tag references
- [ ] Report does not exceed 150 lines

# Common mistakes

- DO NOT treat roadmap items as blockers -- signing and provenance are enhancements; do not block current delivery on them
- DO NOT skip scanner threshold configuration -- finding a scanner is not enough; verify it has severity thresholds that block promotion
- DO NOT leave CVE exemptions undocumented -- if a CVE is exempted, the exemption must have a rationale and expiry date
- DO NOT confuse mutable tags with digests -- `:v1.2.3` is still a mutable tag (it can be re-pushed); only `@sha256:...` is immutable
- DO NOT apply this skill to non-container deployments -- edge-device SSH deployments and Terraform state are out of scope
- DO NOT review pipeline YAML structure (stage ordering, job dependencies, rules) -- that belongs to `deployment`

# Escalation

- **Scanner not configured and no team decision on tooling:** Report the gap; recommend Trivy as default (free, integrates with common CI systems and registries); do not install or configure scanners without team approval
- **Mutable tags used in production promotion:** Flag as high-priority gap; this breaks "build once, deploy everywhere"
- **Retention policy deletes digests before rollback window:** Flag as high-priority; rollback capability is at risk
- **Cannot determine pipeline state** (no access to the CI config or CI settings): Report as blocked; ask for read access

# Examples

<example>
**Scenario: Audit the main app's CI pipeline (baseline depth)**

Input: "Is our main app's CI pipeline supply-chain hardened?"

Process:
1. Read the CI config -- found build-and-publish job at line 24, no scan stage
2. Search for SBOM generation -- none found
3. Check the promotion files -- found `image: web-app:main` (mutable tag, not digest)
4. Check retention -- no cleanup policy configured in pipeline; registry policy unknown

Output:
| Control | Current | Gap | Remediation |
|---------|---------|-----|-------------|
| Image scanning | No scan stage | Yes | Add Trivy scan job after build, set `--severity CRITICAL,HIGH --exit-code 1` |
| SBOM | Not generated | Yes | Add `trivy image --format cyclonedx` step, attach as artifact |
| Immutable digests | Mutable tag `:main` in promotion files | Yes | Change promotion to use `@sha256:...` digest |
| Rollback retention | Unknown | Yes | Configure a registry cleanup policy with 30-day retention |
</example>

<example>
**Scenario: Evaluate readiness for image signing**

Input: "Should we start signing our Docker images?"
Depth: `roadmap`

Process: Audit baseline controls first. If all four pass, recommend signing as next step. If baseline gaps exist, recommend fixing those before adding signing complexity.
</example>

<example>
**Scenario: Review after adding Trivy to CI**

Input: "I just added Trivy scanning -- did I configure it correctly?"

Process: Read the CI config -> verify Trivy runs after build -> verify severity threshold blocks pipeline -> verify scan results are stored as artifacts -> report findings
</example>

# Failure modes

| Failure | Recovery |
|---------|----------|
| No access to CI pipeline configuration | Report as blocked; request read access to the CI config |
| Pipeline uses a scanner not documented here | Evaluate against the same criteria (severity threshold, block-on-failure, artifact storage) |
| `general/versions` format is unfamiliar | Read the file to understand the promotion format; check for digest vs. tag references |
| Cannot determine if retention policy exists | Check the container registry settings or ask the user about their cleanup configuration |

# Related skills

- `security-audit` -- application-level vulnerability scanning (OWASP, injection, secrets)
- `deployment` -- CI pipeline YAML structure, stage ordering, and job configuration
- the cloud pack's CI/CD auth skill -- registry authentication for image push and secret access in CI
- `release-promotion` -- the `general/versions` promotion workflow
- `monorepo-coordination` -- when supply-chain changes span multiple repos
