---
name: deployment
description: "Use this skill for a Helm-upgrade deploy of an app service (the web portal or the edge service) to an EXISTING Kubernetes cluster, plus post-deploy verification. NOT for cluster-level ops -- Minikube/k3s lifecycle, GCP firewall, tunnels, bootstrap secrets (use kubernetes-deployment); NOT for cross-environment promotion (use release-promotion)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Orchestrate the project's service deployments across local-dev (Minikube), the staging environment (project.json → cloud.envAlias; typically a cloud VM + Minikube), and production (e.g. edge device + k3s) using Helm, producing a deploy status report with pod health and endpoint verification. This skill covers the single-environment deploy-and-verify cycle; it defers image building to `docker-build`, chart template authoring to `helm-chart-expert`, and cross-environment promotion to `release-promotion`.

# When to use this skill

- Trigger A -- Deploying a Helm release to any single project environment
- Trigger B -- Running post-deployment verification (pod health, endpoint checks, ingress validation)
- Trigger C -- Rolling back a failed Helm release within the same environment
- Trigger D -- Inspecting current deploy state (helm list, helm history, pod status)

# When NOT to use this skill

- Anti-trigger A -- Building or pushing Docker images -> use `docker-build` instead
- Anti-trigger B -- Writing or modifying Helm chart templates -> use `helm-chart-expert` instead
- Anti-trigger C -- CI/CD pipeline configuration -> use `gitlab-ci-cd` instead
- Anti-trigger D -- Production deploy without an approved merge request -> stop and escalate
- Anti-trigger E -- Staging-environment operational recovery (SSH debugging, secret rotation, VM recovery) -> follow the project's environment runbooks; cluster-level pieces belong to `kubernetes-deployment`
- Anti-trigger F -- Promoting an image across environments (dev -> staging -> production) -> use `release-promotion` instead
- Anti-trigger G -- Cluster-level infrastructure ops (Minikube management, GCP firewall, tunnel debugging) -> use `kubernetes-deployment` instead

# Required environment

- Runtime mount: `.claude/skills/deployment/SKILL.md`
- Tools / libraries: `helm` (v3+), `kubectl`, `gcloud` CLI, SSH access (for staging/production)
- Bundled resources:
  - `.claude/skills/deployment/scripts/deploy-checklist.sh` -- pre-deploy validation
  - `.claude/skills/deployment/resources/environments.md` -- environment coordinates
- Reversibility profile: hard-to-reverse -- confirm before applying to shared clusters; `helm rollback` can revert but causes pod disruption

# Inputs

- `environment: "localdev" | "<envAlias>" | "production"` -- target environment (staging alias from project.json → cloud.envAlias)
- `chart_path: string` -- path to the Helm chart (e.g., `charts/<app>/`)
- `values_file: string` -- environment-specific values file (e.g., `values.localdev.yaml`)
- `image_tag: string` -- the image tag to deploy (git short hash or semver)

# Outputs

- Format: Deploy status report inlined in agent response
- Length budget: max 30 lines for the status report; verbose pod logs or dry-run diffs go in a collapsible block
- Output template:
  ```
  Deploy: {chart} -> {environment}
  Image: {registry}/{image}:{tag}
  Helm revision: {N}
  Pod status: {count} Running, {count} other
  Health endpoint: {status code}
  Audit trail: {updated | skipped (localdev)}
  ```

# Procedure

1. **Validate prerequisites** -- Confirm GCP auth (`gcloud auth print-access-token`), kubectl cluster access (`kubectl get nodes`), and image existence in registry (`gcloud artifacts docker images describe $IMAGE_REGISTRY/$IMAGE:$TAG`). Use `$IMAGE_REGISTRY` environment variable for the registry URL (do not hardcode GCP project IDs). Checkpoint: All three checks pass. If any fails, stop and report which prerequisite is missing.

2. **Run pre-deploy checklist** -- Execute `.claude/skills/deployment/scripts/deploy-checklist.sh <image-name> <image-tag> <chart-path>` to validate chart lint, template render, and image availability. Checkpoint: Script exits 0.

3. **Dry-run first** -- Always run `helm upgrade --install --dry-run` (or the chart repo's environment wrapper with its dry-run flag, if one exists) before the live deploy. Review the rendered manifests for stop conditions.

   ```bash
   # Local dev
   helm upgrade --install <release> charts/<app>/ -f charts/<app>/values.localdev.yaml --dry-run

   # Staging (<envAlias>)
   ssh <staging-host> "cd <chart-repo> && helm upgrade --install <release> charts/<app>/ -f charts/<app>/values.<envAlias>.yaml --dry-run"
   ```

   Checkpoint: Dry-run output reviewed. STOP if any of these appear: `kind: PersistentVolumeClaim` being deleted, replica count decrease >50%, resource limits removed entirely, namespace change, or `kind: ServiceAccount` deleted. Otherwise proceed.

4. **Run helm diff** -- If `helm-diff` plugin is available, run `helm diff upgrade` to see a colored diff of what will change. Checkpoint: Diff reviewed and accepted by the operator.

5. **Deploy** -- Execute the live deploy command. Side effect: This modifies the target Kubernetes cluster. The Helm release revision increments.

   ```bash
   # Local dev
   helm upgrade --install <release> charts/<app>/ -f charts/<app>/values.localdev.yaml

   # Staging (<envAlias>)
   ssh <staging-host> "cd <chart-repo> && helm upgrade --install <release> charts/<app>/ -f charts/<app>/values.<envAlias>.yaml"
   ```

   Checkpoint: `helm history` shows the new revision with status `deployed`.

6. **Post-deploy verification** -- Run all checks:
   - `kubectl get pods -n <namespace>` -- all pods in Running state within 120 seconds
   - `curl http://<endpoint>/api/health` -- returns HTTP 200
   - `kubectl get ingress -A` -- ingress routes match expected hostnames

   Checkpoint: All pods Running, health endpoint returns 200, ingress routes correct.

7. **Update audit trail** -- After a successful staging or production deploy, update the version-pinning repo, if the project uses one (project.json → repos), via a commit or MR. Fields to update: `image_tag`, `chart_version`, `deployed_at` (ISO 8601 timestamp), `helm_revision`. This is not optional for shared environments. Checkpoint: Version file committed or MR opened.

### Environments Reference

| Environment | Infra | Namespace | Access |
|-------------|-------|-----------|--------|
| localdev | Minikube | `<app-namespace>` | `*.<localdev-host>` via /etc/hosts |
| `<envAlias>` (staging) | Cloud VM + Minikube | `<app-namespace>` | SSH tunnel |
| production | Edge device + k3s (or managed cluster) | `<app-namespace>` | Direct |

### Service Ports (localdev)

| Service | Domain | Port |
|---------|--------|------|
| Web portal | `<localdev-host>` | 80 |
| Device N | `dN.<localdev-host>` | 80 |
| Keycloak | `keycloak.<localdev-host>` | 80 |
| PgAdmin | `pgadmin.<localdev-host>` | 80 |

# Self-check before returning

- [ ] Every file cited has been read; no claim references an unopened file.
- [ ] Output matches the format above.
- [ ] Uncertain claims tagged `[LOW-CONFIDENCE]`.
- [ ] No hardcoded paths or environment-specific values leaked.
- [ ] Pre-deploy checklist script was run and exited 0.
- [ ] `helm upgrade --dry-run` was executed before the live deploy.
- [ ] No `NEXT_PUBLIC_*` variables were passed as `--build-arg` (build-time bake violates 12-factor; use `window.__ENV__` runtime bridge).
- [ ] Registry URL used `$IMAGE_REGISTRY` variable, not a hardcoded GCP project ID.
- [ ] All pods in target namespace are Running.
- [ ] Health endpoint returned HTTP 200.
- [ ] Version-pinning audit trail was updated (staging/production only, if the project uses one).
- [ ] No `--push` command was run without the operator understanding it writes to the shared registry.

# Common mistakes to avoid

- DO NOT use `NEXT_PUBLIC_*` as Docker `--build-arg` -- this bakes environment-specific values into the image, violating 12-factor build-once/deploy-anywhere. Use the `window.__ENV__` runtime bridge pattern instead (server renders `<script>window.__ENV__={...}</script>`, client reads from `window.__ENV__`).
- DO NOT hardcode GCP project IDs in commands -- use the `$IMAGE_REGISTRY` variable.
- DO NOT run the live deploy before the dry-run -- always `--dry-run` first.
- DO NOT skip `helm dependency update` after modifying subchart Chart.yaml versions.
- DO NOT run `--push` to the shared registry without confirming the intended tag -- pushes are not easily reversible.
- DO NOT deploy to production without an approved merge request.
- DO NOT use `latest` tag in any environment.
- DO NOT confuse this skill with `release-promotion` -- this skill deploys to one environment; `release-promotion` manages the digest handoff across environments.

# Escalation

- Stop and ask when: Target is production and there is no approved MR.
- Stop and ask when: Dry-run shows any stop condition (PVC deletion, replica decrease >50%, resource limits removed, namespace change).
- Stop and ask when: Post-deploy verification fails after 2 retry cycles (pods not Running, health endpoint not responding).
- Stop and ask when: The image tag does not exist in the registry.
- Refuse and explain when: Asked to deploy to production without confirmed approved MR URL. Required: operator must provide the MR URL before the skill proceeds with production deploy commands.

<examples>
<example>
<user_task>Deploy the web portal to localdev after a new feature merge</user_task>
<procedure_trace>
Step 1: Validate prerequisites
  gcloud auth print-access-token > /dev/null 2>&1  # Auth valid
  kubectl get nodes                                  # Cluster accessible

Step 2: Pre-deploy checklist
  .claude/skills/deployment/scripts/deploy-checklist.sh <main-app> abc1234 charts/<app>/

Step 3: Dry-run
  helm upgrade --install <release> charts/<app>/ -f charts/<app>/values.localdev.yaml --dry-run
  # Reviewed: no PVC deletions, no replica decreases, no namespace changes.

Step 4: Deploy
  helm upgrade --install <release> charts/<app>/ -f charts/<app>/values.localdev.yaml

Step 5: Verify
  kubectl get pods -n <app-namespace>
  # NAME                                READY   STATUS    RESTARTS
  # <release>-<app>-7b9f8c6d4-x2k9m    1/1     Running   0

  curl http://<localdev-host>/api/health
  # {"status":"ok"}
</procedure_trace>
<output>
Deploy: <app> -> localdev
Image: $IMAGE_REGISTRY/<main-app>:abc1234
Helm revision: 12
Pod status: 1 Running
Health endpoint: 200 OK
Audit trail: skipped (localdev)
</output>
</example>
</examples>

# Failure modes

- Mode: ImagePullBackOff -- symptom: pod stuck in ImagePullBackOff -- detection: `kubectl describe pod` shows auth error -- action: re-authenticate with `gcloud auth login && gcloud auth configure-docker <region>-docker.pkg.dev`, then regenerate the registry pull secret via the chart repo's docker-secret helper script
- Mode: Stale subchart -- symptom: Helm chart changes not applied after deploy -- detection: `helm template` output does not include expected changes -- action: bump version in subchart `Chart.yaml`, bump dependency version in parent `Chart.yaml`, run `helm dependency update charts/<app>/`
- Mode: CrashLoopBackOff -- symptom: pod restarts repeatedly -- detection: `kubectl logs -n <ns> <pod> --previous` shows error -- action: check for missing env vars in values.yaml, wrong image tag, or failing health probe
- Mode: Rollback needed -- symptom: deploy succeeded but application is broken -- detection: health endpoint returns non-200 or telemetry not flowing -- action: `helm rollback <release> <revision> -n <namespace>`, then update the version-pinning repo (if the project uses one)

# Related skills

- `docker-build` -- defer to docker-build for image creation; deployment consumes the built image
- `helm-chart-expert` -- defer to helm-chart-expert for chart template modifications; compose when a deploy failure traces to a template bug
- `gitlab-ci-cd` -- defer to gitlab-ci-cd for pipeline configuration; deployment is the runtime execution
- `release-promotion` -- defer to release-promotion for cross-environment digest promotion; deployment handles the single-environment Helm upgrade that release-promotion orchestrates
- `kubernetes-deployment` -- defer to kubernetes-deployment for cluster-level infrastructure (Minikube management, firewall rules, tunnel debugging)
