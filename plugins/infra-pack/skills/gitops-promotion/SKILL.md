---
name: gitops-promotion
description: "Use this skill when the project uses a pull-based GitOps controller (Wave B) for cluster reconciliation via a version-pinning repo's desired state. Don't use it during Wave A imperative GitLab deploys (use gitlab-ci-cd) or for CI pipeline structure (use gitlab-ci-cd)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Define and enforce the GitOps promotion flow for the project's Wave B deployment model, where a pull-based controller (Flux or ArgoCD) reconciles the cluster from the version-pinning repo's desired state (project.json → repos; referenced below as `<versions-repo>`). Covers the boundary between Wave A (GitLab-native imperative deploy) and Wave B (GitOps pull-based), promotion sequencing, rollback via Git, and verification gates.

# When to use this skill (triggers)

- Configuring or reviewing the GitOps promotion flow after a pull-based controller is deployed
- Updating `<versions-repo>` desired-state files for candidate, current, or previous digests
- Designing the verify-then-promote gate in a GitOps workflow
- Rolling back a deployment by updating desired state to a previous digest
- Determining whether a task belongs in Wave A (imperative) or Wave B (GitOps)

Only activate if a GitOps controller (Flux v2 or ArgoCD) is confirmed deployed in the target cluster.

# When NOT to use this skill (anti-triggers)

- Wave A imperative deployments where GitLab CI runs `helm upgrade` directly -- use `gitlab-ci-cd`
- CI pipeline structure (stages, rules, artifacts) -- use `gitlab-ci-cd` even in Wave B (CI still builds/scans/publishes)
- Helm chart template authoring or values configuration -- use `helm-chart-expert`
- GCP authentication for image push or secret access -- use `gcp-cicd-auth`
- No GitOps controller is deployed yet -- this skill produces guidance that cannot be acted on without a controller; redirect to `gitlab-ci-cd`

# Required environment (Runtime: .claude/skills/gitops-promotion/SKILL.md)

- Tools/libraries: Read (inspect desired-state files), Grep (search for digest references), Edit (update desired state), Bash (run detection commands)
- Prerequisite: a pull-based GitOps controller (Flux v2 or ArgoCD) is deployed in the target cluster

# Inputs

- `action: string` -- `promote` (advance candidate to current), `rollback` (revert to previous), or `review` (audit the current flow)
- `environment: string` -- target environment (`<envAlias>` staging, `staging`, `production`)
- `digest: string` (optional) -- specific image digest for promote/rollback

# Outputs

**Format:** Updated `<versions-repo>` file content or audit report.

**Length budget:** Desired-state file changes max 30 lines. Audit report max 80 lines including checklist.

**Output template:**

```
## GitOps Promotion Report -- {environment}

### Wave Status
Current: {Wave A (imperative) | Wave B (GitOps) | Transitioning}
Controller: {Flux v2 | ArgoCD | None detected}

### Desired State (<versions-repo>)
| Field | Value |
|-------|-------|
| candidate.digest | {sha256:... | null} |
| current.digest | {sha256:...} |
| previous.digest | {sha256:... | null} |

### Promotion Checklist
- [{x| }] Image built and scanned in GitLab CI
- [{x| }] Immutable digest (sha256:...) captured, not a mutable tag
- [{x| }] <versions-repo> updated with candidate digest
- [{x| }] Controller reconciled cluster to candidate
- [{x| }] helm diff or --dry-run passed before reconciliation
- [{x| }] Verification job confirmed rollout health
- [{x| }] Candidate promoted to current only after verification
- [{x| }] Previous digest preserved for rollback

### Confidence: {HIGH|MEDIUM|LOW} -- {rationale}
```

# Procedure (Checkpoint: after each step)

1. **Determine wave status** -- Check whether a GitOps controller is deployed. If not, redirect to `gitlab-ci-cd` for Wave A.
   Checkpoint: controller presence confirmed.

2. **Read current desired state** -- Read the `<versions-repo>` desired-state file to determine current `candidate`, `current`, and `previous` digest values.
   Checkpoint: all three values documented.

3. **For promote action**:
   a. Verify the candidate digest was built by GitLab CI (not manually pushed).
   b. Verify the controller has reconciled the cluster to the candidate.
   c. Verify the verification job passed (smoke checks, rollout status).
   d. Verify `helm diff` or `--dry-run` was executed before the controller applied changes.
   e. **Show the intended change as a diff and require explicit user confirmation before using Edit.** A mistaken promotion triggers real cluster reconciliation.
   f. Update `<versions-repo>`: move `current` to `previous`, move `candidate` to `current`.
   Checkpoint: desired-state file updated with immutable digests only.

4. **For rollback action**:
   a. Read `previous` digest from `<versions-repo>`.
   b. Show the intended rollback diff and require user confirmation.
   c. Update `current` to the `previous` digest value.
   d. Commit the change to trigger controller reconciliation.
   e. Verify the controller reconciled and the rollback is healthy.
   Checkpoint: rollback commit created with descriptive message.

5. **For review action**:
   a. Audit the full flow: CI builds -> digest captured -> desired state updated -> controller reconciles -> verification -> promotion.
   b. Run `grep -r 'kubectl apply\|helm upgrade' .gitlab-ci.yml <versions-repo>/` to detect direct-apply violations after GitOps adoption.
   c. Check for mutable tags: `grep -rE ':[a-z]+(-[a-z]+)*"' <versions-repo>/ | grep -v 'sha256:'` to find non-digest values.
   d. Compile the promotion checklist.
   Checkpoint: all checklist items assessed.

## Wave A / Wave B boundary

| Responsibility | Wave A (imperative) | Wave B (GitOps) |
|---------------|---------------------|-----------------|
| Build, test, scan | GitLab CI | GitLab CI (unchanged) |
| Publish image | GitLab CI | GitLab CI (unchanged) |
| Update desired state | N/A | GitLab CI updates `<versions-repo>` |
| Apply to cluster | GitLab CI runs `helm upgrade` | Controller reconciles from Git |
| Verify | GitLab CI verify job | GitLab CI verify job (unchanged) |
| Promote | GitLab CI updates `<versions-repo>` | GitLab CI updates `<versions-repo>` after verify |
| Rollback | `helm rollback` or redeploy previous digest | Update `<versions-repo>` to previous digest |

The boundary: in Wave A, CI directly mutates the cluster. In Wave B, CI only mutates Git state; the controller handles cluster mutation. Everything before "apply to cluster" and after "verify" is identical in both waves.

## Environment naming

- Use the staging alias (`project.json → cloud.envAlias`) in CI/CD, promotion, and operational documentation
- Terraform or infrastructure paths may still refer to the same environment as `dev` -- explicitly note this when encountered, do not create separate environment records for the two names

# Self-check before returning (anti-hallucination, confidence labels, format match)

- [ ] All digest values in `<versions-repo>` are immutable SHA256 digests, not mutable tags (`:latest`, `:main`)
- [ ] Promotion only occurs after verification passes (never before)
- [ ] `helm diff` or `--dry-run` was executed before controller reconciliation
- [ ] `previous` digest preserved alongside `current` for rollback
- [ ] No direct `kubectl apply` or `helm upgrade` commands in the flow after GitOps adoption (all changes go through Git)
- [ ] The staging alias and any legacy `dev` naming are not documented as separate environments
- [ ] No hardcoded cluster context, namespace, or endpoint in desired-state files
- [ ] User confirmation was obtained before any Edit to `<versions-repo>` for promote or rollback actions
- [ ] Output matches the promotion report template
- [ ] Confidence label attached -- label LOW when controller reconciliation status cannot be verified from available data

# Common mistakes to avoid (DO NOT patterns)

- DO NOT promote (move candidate to current) before the verification job confirms rollout health -- this is the single most important gate in the flow
- DO NOT use mutable image tags (`:latest`, `:main`) in `<versions-repo>` -- always use immutable digests (`sha256:...`); a mutable tag breaks the entire audit trail
- DO NOT run `kubectl apply` or `helm upgrade` directly after GitOps adoption -- all cluster changes must go through Git state and controller reconciliation
- DO NOT document the staging alias and legacy `dev` naming as separate environments -- they are the same environment with different naming in different tooling layers
- DO NOT commit `*.populated.yaml` secrets to `<versions-repo>` or any desired-state repo
- DO NOT skip `helm diff` or `--dry-run` before reconciliation -- template errors should be caught before the controller applies them
- DO NOT hardcode cluster context, namespace, or server endpoint in desired-state manifests -- use per-environment overlays or variable substitution
- DO NOT edit `<versions-repo>` without showing the diff and getting user confirmation -- a mistaken edit triggers real cluster reconciliation

# Escalation (stop-and-ask conditions)

- Stop and ask when: a mutable tag (`:latest`, `:main`) is found in `<versions-repo>` -- this needs immediate replacement with an immutable digest
- Stop and ask when: no GitOps controller is deployed but the task assumes Wave B -- redirect to `gitlab-ci-cd` for Wave A
- Stop and ask when: an incident requires a hotfix that bypasses Git -- document the exception, apply the hotfix, then immediately create a remediation commit to re-sync desired state with cluster state
- Stop and ask when: `previous` digest is missing from `<versions-repo>` -- rollback is not possible without it
- Stop and ask when: Wave A/B determination is ambiguous -- consult with the user about which wave the target cluster uses

# Examples

<example name="promote-web-portal-to-staging">
## Promoting the web portal to staging after successful verification

Current state of `<versions-repo>/web-portal.yaml`:
```yaml
web-portal:
  staging:
    candidate:
      digest: "sha256:a1b2c3d4e5f6..."
      commit: "abc1234"
      built_at: "2026-05-24T10:30:00Z"
    current:
      digest: "sha256:f6e5d4c3b2a1..."
      commit: "def5678"
      promoted_at: "2026-05-23T14:00:00Z"
    previous:
      digest: "sha256:9876543210ab..."
      commit: "ghi9012"
      promoted_at: "2026-05-22T09:15:00Z"
```

Promotion sequence:
1. GitLab CI built image `sha256:a1b2c3d4e5f6...` from commit `abc1234`.
2. CI updated `candidate.digest` in `<versions-repo>/web-portal.yaml`.
3. GitOps controller detected the change and reconciled the cluster.
4. `helm diff` showed only the expected image change (no template errors).
5. Verification job ran: `kubectl rollout status` succeeded, health endpoint returned 200.
6. Promotion commit updates the file:

```yaml
web-portal:
  staging:
    candidate: null  # cleared after promotion
    current:
      digest: "sha256:a1b2c3d4e5f6..."
      commit: "abc1234"
      promoted_at: "2026-05-24T11:00:00Z"
    previous:
      digest: "sha256:f6e5d4c3b2a1..."
      commit: "def5678"
      promoted_at: "2026-05-23T14:00:00Z"
```

Commit message: `chore(versions): promote web-portal sha256:a1b2c3 to staging current`
</example>

<example name="rollback-after-failed-deployment">
## Rolling back after a failed deployment

Symptom: health endpoint returns 500 after controller reconciled the new candidate.

Rollback steps:
1. Read `previous.digest` from `<versions-repo>/web-portal.yaml`: `sha256:f6e5d4c3b2a1...`
2. Show diff to user: current -> previous digest swap. Wait for confirmation.
3. Update `current.digest` to the previous value.
4. Commit: `fix(versions): rollback web-portal staging to sha256:f6e5d4 after health check failure`
5. Controller reconciles cluster to the previous image.
6. Verify: `kubectl rollout status` and health endpoint confirm recovery.
</example>

# Failure modes (symptom -> detection -> action)

- **Promoted before verification**: symptom: bad image is recorded as `current`, downstream environments may pick it up -> detect: `promoted_at` timestamp is before or equal to the verification job completion time -> fix: revert the promotion commit, re-run verification, only then promote
- **Mutable tag in desired state**: symptom: controller deploys a different image than expected because the tag was overwritten -> detect: grep `<versions-repo>` for values not matching `sha256:` pattern -> fix: replace mutable tag with the immutable digest from the registry
- **Rollback impossible (no previous digest)**: symptom: `previous` field is null or missing in `<versions-repo>` -> detect: read the file, check for `previous.digest` -> fix: query the registry or `git log` for the last known-good digest, populate `previous` manually, then proceed with rollback
- **Hotfix drift**: symptom: `kubectl apply` was run during an incident and cluster state no longer matches Git -> detect: `helm diff` shows unexpected differences -> fix: create a remediation commit that updates `<versions-repo>` to match the live cluster state, then proceed with normal GitOps flow
- **Direct-apply leak after GitOps adoption**: symptom: cluster state changes without a corresponding Git commit -> detect: `grep -r 'kubectl apply\|helm upgrade' .gitlab-ci.yml` finds imperative commands still active -> fix: remove direct-apply steps from CI, route through desired-state updates

# Related skills (compose vs defer)

- `gitlab-ci-cd` -- **compose**: CI pipeline still runs in Wave B (build, scan, publish, verify). If no GitOps controller is deployed, use `gitlab-ci-cd` exclusively for Wave A
- `gcp-cicd-auth` -- **compose**: GCP auth required to push images to the registry
- `helm-chart-expert` -- **defer**: for Helm chart template authoring and values configuration
- `deployment` -- **defer**: for end-to-end deploy orchestration in Wave A
