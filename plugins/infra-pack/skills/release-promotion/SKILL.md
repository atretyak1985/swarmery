---
name: release-promotion
description: "Use this skill when promoting an image or chart version across environments (dev -> staging -> production), rolling back a promotion, or updating the version-pinning repo's digests. Don't use it for single-environment Helm deploys (use deployment) or code-level rollback (use refactor-plan)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Promote the project's service images and chart versions across environments using immutable digests, producing updated entries in the version-pinning repo (if the project uses one -- project.json → repos; referenced below as `<versions-repo>`) with current/previous tracking for auditability and fast rollback. This skill manages the cross-environment promotion lifecycle; it defers the single-environment Helm upgrade execution to `deployment` and pull-based GitOps reconciliation to `gitops-promotion`.

# When to use this skill

- Trigger A -- Promoting an image from dev to staging or staging to production
- Trigger B -- Rolling back a promotion to the previous known-good digest
- Trigger C -- Updating `<versions-repo>` after a verified deployment
- Trigger D -- Reviewing promotion history in Git for audit purposes
- Trigger E -- Coordinating Helm chart version promotion alongside image digest promotion

# When NOT to use this skill

- Anti-trigger A -- Code-level refactoring rollback (git revert) -> use `refactor-plan` instead
- Anti-trigger B -- Feature-flag toggles that change behavior without deploying a new image
- Anti-trigger C -- Schema-only migrations that deploy without a new service image -> use `migration-check` instead
- Anti-trigger D -- Hotfix cherry-picks that bypass the normal promotion queue -- handle as expedited single-repo deployments with post-hoc `<versions-repo>` update
- Anti-trigger E -- Single-environment Helm upgrade orchestration -> use `deployment` instead
- Anti-trigger F -- Cluster-level infrastructure operations -> use `kubernetes-deployment` instead

# Required environment

- Runtime mount: `.claude/skills/release-promotion/SKILL.md`
- Tools / libraries: `helm` (v3+), `kubectl`, `gcloud` CLI (for digest lookup via the registry)
- Repositories: the version-pinning repo (version state) and the chart repo (project.json → repos)
- Reversibility profile: hard-to-reverse -- promotion writes to `<versions-repo>` (Git-tracked, revertible) and triggers a Helm upgrade on the target cluster; rollback requires a new promotion commit pointing to `previous_digest`

# Inputs

- `service: string` -- which service to promote (e.g., the web portal from project.json → mainApp, or the edge service from project.json → device)
- `source_environment: string` -- where the image was validated (e.g., `dev`, `staging`)
- `target_environment: string` -- where to promote (e.g., `staging`, `production`)
- `image_digest: string` -- the immutable `sha256:...` digest to promote (not a mutable tag)
- `chart_version: string` (optional) -- the Helm chart version to deploy alongside the image

# Outputs

- Format: Promotion report inlined in agent response, plus a `<versions-repo>` YAML update
- Length budget: max 40 lines for the promotion report; YAML update snippet max 20 lines
- Output template:
  ```
  Promotion: {service} {source_environment} -> {target_environment}
  Digest: {image_digest}
  Chart version: {chart_version | unchanged}
  Dry-run: {reviewed | skipped with reason}
  Deploy: {success | failed with reason}
  Health check: {pass | fail}
  <versions-repo>: {committed | MR opened | pending}
  Cross-repo drift: {none detected | warning details}
  ```

# Procedure

1. **Capture the image digest** -- After the image is built and scanned, capture the immutable digest (not a mutable tag). Use `gcloud artifacts docker images describe` to retrieve it. Checkpoint: Digest is a `sha256:...` string; not a mutable tag like `latest` or a git short hash.

   ```bash
   gcloud artifacts docker images describe \
     ${REGION}-docker.pkg.dev/${PROJECT}/${REPO}/${SERVICE}:${TAG} \
     --format='value(image_summary.digest)'
   ```

2. **Check for cross-repo drift** -- Run the project's cross-repo version-drift check (if the project provides one) to verify that all repos are in sync before promoting. Checkpoint: No drift detected, or drift acknowledged and documented.

3. **Run dry-run before promotion** -- Execute `helm upgrade --dry-run` with the new digest on the target environment. Review the diff output. STOP if: unexpected resources are changing, unintended value overrides appear, or the image digest does not match the intended promotion target. Checkpoint: Dry-run diff reviewed; only expected resources changing.

   ```bash
   helm upgrade ${RELEASE_NAME} ./charts/${SERVICE} \
     --namespace ${NAMESPACE} \
     -f values/${ENVIRONMENT}.yaml \
     --set image.digest=${NEW_DIGEST} \
     --dry-run --diff
   ```

4. **Deploy to the target environment** -- For production: require explicit user confirmation before proceeding. Never auto-promote to production. Checkpoint: `helm history` shows the new revision with status `deployed`.

   ```bash
   helm upgrade ${RELEASE_NAME} ./charts/${SERVICE} \
     --namespace ${NAMESPACE} \
     -f values/${ENVIRONMENT}.yaml \
     --set image.digest=${NEW_DIGEST} \
     --wait --timeout 5m
   ```

5. **Verify deployment health** -- Run rollout status check and health endpoint probe. Checkpoint: `kubectl rollout status` reports success; health endpoint returns HTTP 200.

   ```bash
   kubectl rollout status deployment/${SERVICE} -n ${NAMESPACE} --timeout=3m
   curl -sf http://${SERVICE_URL}/health || echo "Health check failed"
   ```

6. **Update `<versions-repo>`** -- Only after verification succeeds. Shift the current value to previous. Commit with message format: `promote {service} to {environment} (sha256:{short_digest}...)`. Checkpoint: Commit created; only version state files changed; no unrelated changes mixed in.

   ```yaml
   # <versions-repo>/${SERVICE}/${ENVIRONMENT}.yaml
   service: web-portal
   environment: production
   image_repository: ${REGION}-docker.pkg.dev/${PROJECT}/${REPO}/${SERVICE}
   current_digest: "sha256:abc123..."   # <- new digest
   previous_digest: "sha256:def456..."  # <- was current, now previous
   source_commit: "a1b2c3d"
   deployed_at: "2026-05-26T14:30:00Z"
   chart_version: "1.5.0"              # if applicable
   ```

7. **Verify Git audit trail** -- The promotion commit should be small, explicit, and not mixed with unrelated changes. Checkpoint: `git log --oneline <versions-repo>/${SERVICE}/` shows the promotion commit as HEAD.

# Self-check before returning

- [ ] Every file cited has been read; no claim references an unopened file.
- [ ] Output matches the format above.
- [ ] Uncertain claims tagged `[LOW-CONFIDENCE]`.
- [ ] No hardcoded paths or environment-specific values leaked.
- [ ] Image is referenced by immutable digest (`sha256:...`), not a mutable tag.
- [ ] Dry-run (`helm upgrade --dry-run --diff`) was executed and reviewed before actual deployment.
- [ ] `current` is updated only AFTER deployment verification succeeds.
- [ ] `previous` preserves the old `current` value for fast rollback.
- [ ] Promotion commit in `<versions-repo>` is small, explicit, and contains only version state changes.
- [ ] Health check and smoke test passed after deployment.
- [ ] No environment-specific URLs are hardcoded -- all use `${VARIABLE}` patterns.
- [ ] Chart.yaml `appVersion` tracks the image digest if Helm chart promotion is involved.

# Common mistakes to avoid

- DO NOT write `current` before verification -- if the deployment fails, `current` points to a broken image and rollback requires manual intervention.
- DO NOT store mutable tags instead of digests -- tags can be overwritten; digests cannot. Always use `sha256:...`.
- DO NOT rewrite Git history to hide failed promotions -- failed promotions are valuable audit trail.
- DO NOT mix unrelated environment changes into the same promotion commit -- each promotion should be one commit per service per environment.
- DO NOT skip the dry-run gate -- the dry-run catches misconfigured values, missing secrets, and template errors before they affect the running cluster.
- DO NOT hardcode environment names or URLs in commands -- use variables (`${ENVIRONMENT}`, `${SERVICE_URL}`) so the same flow works for all environments.
- DO NOT promote without checking for cross-repo drift -- an incompatible combination may deploy.
- DO NOT confuse this skill with `deployment` -- this skill manages the cross-environment promotion lifecycle; `deployment` executes the single-environment Helm upgrade.

# Escalation

- Stop and ask when: Dry-run shows unexpected changes. Surface the diff to the user for review.
- Stop and ask when: Health check fails after deployment. Initiate rollback to `previous` digest immediately.
- Stop and ask when: `<versions-repo>` has a merge conflict. Resolve manually; do not force-push.
- Stop and ask when: Cross-repo drift detected. Report the mismatch before promoting.
- Stop and ask when: Target is production. Always require explicit user confirmation before promoting to production. Never auto-promote.
- Refuse and explain when: Asked to promote using a mutable tag instead of an immutable digest.

<examples>
<example>
<user_task>Promote the web portal from dev to staging and then to production</user_task>
<procedure_trace>
Step 1: Capture digest
  gcloud artifacts docker images describe \
    ${REGION}-docker.pkg.dev/${PROJECT}/${REPO}/web-portal:8f3e2a1 \
    --format='value(image_summary.digest)'
  # sha256:a1b2c3d4e5f6...

Step 2: Check cross-repo drift (project drift check)
  # No drift detected.

Step 3: Dry-run for staging
  helm upgrade web-portal ./charts/web-portal \
    -n staging -f values/staging.yaml \
    --set image.digest=sha256:a1b2c3d4e5f6... \
    --dry-run --diff
  # Diff shows only image digest change. Approved.

Step 4: Deploy to staging
  helm upgrade web-portal ./charts/web-portal \
    -n staging -f values/staging.yaml \
    --set image.digest=sha256:a1b2c3d4e5f6... \
    --wait --timeout 5m

Step 5: Verify staging
  kubectl rollout status deployment/web-portal -n staging
  curl -sf http://${STAGING_URL}/health
  # 200 OK

Step 6: Update <versions-repo> for staging
  current_digest: sha256:a1b2c3d4e5f6...
  previous_digest: sha256:0000aaaa1111...
  Commit: "promote web-portal to staging (sha256:a1b2c3d4...)"

Step 7: Repeat steps 3-6 for production (with explicit user confirmation at step 4)
</procedure_trace>
<output>
Promotion: web-portal dev -> staging
Digest: sha256:a1b2c3d4e5f6...
Chart version: 1.5.0
Dry-run: reviewed
Deploy: success
Health check: pass
<versions-repo>: committed
Cross-repo drift: none detected

Promotion: web-portal staging -> production
Digest: sha256:a1b2c3d4e5f6...
Chart version: 1.5.0
Dry-run: reviewed
Deploy: success (user confirmed)
Health check: pass
<versions-repo>: committed
Cross-repo drift: none detected
</output>
</example>

<example>
<user_task>Roll back a failed production deployment</user_task>
<procedure_trace>
Step 1: Identify previous digest
  cat <versions-repo>/web-portal/production.yaml
  current_digest: sha256:bad0bad0...  # broken
  previous_digest: sha256:good1111... # last known good

Step 2: Check cross-repo drift — none detected.

Step 3: Dry-run rollback
  helm upgrade web-portal ./charts/web-portal \
    -n production -f values/production.yaml \
    --set image.digest=sha256:good1111... \
    --dry-run --diff

Step 4: Deploy rollback (user confirmed)
  helm upgrade web-portal ./charts/web-portal \
    -n production -f values/production.yaml \
    --set image.digest=sha256:good1111... \
    --wait --timeout 5m

Step 5: Verify
  curl -sf http://${PRODUCTION_URL}/health
  # 200 OK

Step 6: Update <versions-repo>
  current_digest: sha256:good1111...
  previous_digest: sha256:bad0bad0...
  Commit: "rollback web-portal production to sha256:good1111..."
</procedure_trace>
<output>
Promotion: web-portal production (rollback)
Digest: sha256:good1111...
Chart version: unchanged
Dry-run: reviewed
Deploy: success (user confirmed rollback)
Health check: pass
<versions-repo>: committed
Cross-repo drift: none detected
</output>
</example>
</examples>

# Failure modes

- Mode: Promoted with mutable tag -- symptom: different environments run different code despite same tag -- detection: `gcloud artifacts docker images describe` shows different digests for same tag across environments -- action: re-promote using immutable digest; update version state
- Mode: `current` written before verification -- symptom: version file points to broken image -- detection: health check fails but `<versions-repo>` shows new digest -- action: rollback to `previous` digest; fix version state
- Mode: Dry-run skipped, bad values deployed -- symptom: pods crash-looping or wrong config -- detection: `kubectl describe pod` shows misconfigured env vars -- action: rollback to `previous`; run dry-run on next attempt
- Mode: Merge conflict in `<versions-repo>` -- symptom: promotion commit rejected -- detection: `git push` fails with merge conflict -- action: resolve conflict manually; re-verify version state consistency
- Mode: Cross-repo drift -- symptom: the web portal expects a secret that the infrastructure repo has not bootstrapped -- detection: the project's version-drift check reports a mismatch -- action: merge the missing repo changes first, then re-attempt promotion

# Related skills

- `deployment` -- defer to deployment for the single-environment Helm upgrade execution; release-promotion orchestrates which digest goes where, deployment executes the upgrade
- `helm-chart-expert` -- defer to it for Helm chart patterns and values structure
- `gitlab-ci-cd` -- defer to it for CI pipeline design for build, scan, and promotion stages
- `gitops-promotion` -- defer to it for pull-based GitOps reconciliation (Wave B); release-promotion covers the imperative promotion workflow
- `kubernetes-deployment` -- defer to it for cluster-level infrastructure issues discovered during promotion
