---
name: kubernetes-deployment
description: "Use this skill for CLUSTER-LEVEL Kubernetes operations: Minikube/k3s lifecycle, GCP firewall rules, minikube tunnel debugging, and bootstrap-secret patterns. NOT for app-service Helm deploys or upgrade orchestration (use deployment) or Helm chart template authoring (use helm-chart-expert)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Provides cluster-level Kubernetes infrastructure operations for the platform, producing diagnostic reports or verified shell command sequences for Minikube/k3s cluster management, GCP firewall rules, minikube tunnel debugging, bootstrap-secret patterns, and NetworkPolicy configuration. This skill handles the infrastructure layer beneath Helm deploys; it defers Helm upgrade orchestration to `deployment` and chart template authoring to `helm-chart-expert`.

# When to use this skill

- Trigger A -- Managing Minikube (local), k3s (edge device), or staging (cloud VM + Minikube; project.json → cloud.envAlias) cluster infrastructure
- Trigger B -- Creating or debugging GCP firewall rules for the staging VM
- Trigger C -- Diagnosing minikube tunnel or ingress-nginx connectivity issues
- Trigger D -- Running bootstrap-secret scripts before first deploy
- Trigger E -- Configuring NetworkPolicy namespace selectors
- Trigger F -- Diagnosing cluster/versions drift after a failed verify step

# When NOT to use this skill

- Anti-trigger A -- Helm upgrade orchestration (deploy-and-verify cycle) -> use `deployment` instead
- Anti-trigger B -- Writing or debugging Helm chart templates or `_helpers.tpl` -> use `helm-chart-expert` instead
- Anti-trigger C -- Building or pushing Docker images -> use `docker-build` instead
- Anti-trigger D -- Configuring Keycloak realm, clients, or Auth.js -> use `keycloak` instead
- Anti-trigger E -- Detecting IaC config drift -> use `infrastructure-as-code` instead
- Anti-trigger F -- Checking migration safety -> use `migration-check` instead
- Anti-trigger G -- Staging-environment operational recovery (SSH runbooks, secret rotation, VM troubleshooting) -> follow the project's environment runbooks
- Anti-trigger H -- Promoting an image across environments (dev -> staging -> production) -> use `release-promotion` instead

# Required environment

- Runtime mount: `.claude/skills/kubernetes-deployment/SKILL.md`
- Tools / libraries: `helm` (v3.10+), `kubectl` (v1.28+), `gcloud` CLI, `bash`
- Scripts: the infrastructure repo's `files/initEnv.sh`, `files/updateEnv.sh`, and bootstrap-secret script (project.json → repos)
- KUBECONFIG: `$REPO_ROOT/<terraform-repo>/environments/$ENV/.minikube/remote-minikube-config.yaml` (for the staging cluster)
- Reversibility profile: hard-to-reverse -- GCP firewall changes and cluster reconfiguration affect shared infrastructure; confirm before applying

# Inputs

- `operation: "cluster-mgmt" | "firewall" | "tunnel-debug" | "bootstrap-secrets" | "networkpolicy" | "drift-check"` -- the infrastructure operation needed
- `environment: string` -- target environment (localdev, `<envAlias>`, prod)
- `symptom: string` (optional) -- error message or behavior being debugged

# Outputs

- Format: shell command sequence with dry-run shown first, or ordered diagnostic steps with expected output
- Length budget: max 80 lines for cluster management commands; max 120 lines for connectivity diagnosis
- Output template:
  ```
  Operation: {operation} on {environment}
  Cluster: {cluster-type} ({status})
  Pre-flight: {pass | fail with details}
  Commands: {numbered list of commands with expected output}
  Verification: {verification command and expected result}
  ```

# Procedure

1. **Identify environment and operation** -- Determine target cluster and what infrastructure operation is needed. Set KUBECONFIG and verify cluster connectivity. Checkpoint: `kubectl cluster-info` succeeds and returns the expected cluster URL.

2. **Pre-flight checks** -- For bootstrap-secrets: verify which secrets exist using the bootstrap-secret probe pattern. For connectivity debugging: check minikube-tunnel status (`sudo systemctl status minikube-tunnel`). For firewall: list existing rules on the correct network. Checkpoint: Pre-flight data collected; current state documented before any changes.

3. **Dry-run or read-only diagnosis first** -- For firewall rules: show the `gcloud compute firewall-rules create` command with `--dry-run` equivalent (describe what will be created). For tunnel issues: run diagnostic commands (`ss -tlnp`, `kubectl get svc -n ingress-nginx`). Always show what will change before changing it. Checkpoint: Operator has reviewed the planned change or diagnosis output.

4. **Execute** -- Run the infrastructure operation. For cluster management: use `--wait --atomic --timeout` contract where Helm is involved. For firewall: always include `--network` flag explicitly. Checkpoint: Operation completed without error; `gcloud`/`kubectl` exit code 0.

5. **Verify** -- Check that the infrastructure change took effect. For firewall: `gcloud compute firewall-rules list` confirms the rule on the correct network. For tunnel: ports 80/443 are bound. For bootstrap-secrets: `kubectl get secret` confirms presence. Checkpoint: Verification command confirms the expected state.

# Self-check before returning

- [ ] Every file cited has been read; no claim references an unopened file.
- [ ] Output matches the format above.
- [ ] Uncertain claims tagged `[LOW-CONFIDENCE]`.
- [ ] No hardcoded paths or environment-specific values leaked.
- [ ] Every `helm upgrade` I suggested shows `--dry-run` as a preceding step.
- [ ] I used `$NAMESPACE` placeholder, not hardcoded namespace names.
- [ ] I used `$GCP_PROJECT_ID` placeholder, not hardcoded GCP project IDs.
- [ ] I used `$REPO_ROOT` or relative paths, not absolute developer home paths.
- [ ] GCP firewall rules include explicit `--network` flag (not relying on default VPC).
- [ ] NetworkPolicy selectors use `kubernetes.io/metadata.name`, not custom `name:` labels.
- [ ] I cited the specific pod/service/deployment status that led to my diagnosis.

# Common mistakes to avoid

- DO NOT create GCP firewall rules without `--network` flag -- gcloud silently defaults to the `default` VPC; the staging VM may be on a different network.
- DO NOT use the minikube ingress addon -- it creates a NodePort service; use Helm-managed ingress-nginx with `type: LoadBalancer` for minikube tunnel to work.
- DO NOT hardcode GCP project IDs in commands -- use `$GCP_PROJECT_ID`.
- DO NOT hardcode absolute developer paths -- use `$REPO_ROOT` or relative paths.
- DO NOT assume ingress-nginx ClusterIP is stable after Helm upgrade -- restart minikube-tunnel to clear stale SSH tunnel processes.
- DO NOT skip `--atomic` on shared-cluster deploys -- without it, a failed upgrade leaves the cluster in a broken state.
- DO NOT confuse this skill with `deployment` -- this skill handles infrastructure below the Helm upgrade; `deployment` handles the deploy-and-verify cycle.

# Escalation

- Stop and ask when: bootstrap secrets are missing and the user has not run the repo's bootstrap-secret script.
- Stop and ask when: the KUBECONFIG cannot reach the cluster.
- Stop and ask when: a rollback would target a digest two versions back (cluster/versions drift).
- Stop and ask when: the user requests a destructive operation (`helm uninstall`, `kubectl delete namespace`).
- Refuse and explain when: asked to create firewall rules that open all ports (0-65535) to `0.0.0.0/0`.

<examples>
<example>
<user_task>Create a GCP firewall rule to allow HTTPS to the staging VM</user_task>
<procedure_trace>
Step 1: Identify — target is the staging environment, operation is firewall.
Step 2: Pre-flight — list existing rules on minikube-network.
  gcloud compute firewall-rules list --project="$GCP_PROJECT_ID" \
    --format="table(name,network.basename(),allowed[].map().firewall_rule().list())"
Step 3: Dry-run — show planned command.
Step 4: Execute:
  gcloud compute firewall-rules create allow-https \
    --project="$GCP_PROJECT_ID" \
    --network=minikube-network \
    --direction=INGRESS \
    --priority=1000 \
    --action=ALLOW \
    --rules=tcp:443 \
    --source-ranges=0.0.0.0/0
Step 5: Verify — list rules and confirm allow-https is on minikube-network.
</procedure_trace>
<output>
Operation: firewall on staging
Cluster: Minikube on cloud VM (connected)
Pre-flight: No existing HTTPS rule on minikube-network
Commands:
  1. gcloud compute firewall-rules create allow-https --network=minikube-network ...
Verification: gcloud compute firewall-rules list shows allow-https on minikube-network
</output>
</example>

<example>
<user_task>Bootstrap secrets are missing on a fresh staging cluster</user_task>
<procedure_trace>
Step 1: Identify — target is the staging environment, operation is bootstrap-secrets.
Step 2: Pre-flight — run the bootstrap-secret probe:
  ssh $SSH_OPTS "$SSH_TARGET" bash &lt;&lt;'PROBE_EOF'
  MISSING=()
  for s in <app>-auth-secret <app>-ws-api-secret <app>-maps-api-key; do
    kubectl get secret "$s" -n "$NAMESPACE" >/dev/null 2>&1 || MISSING+=("$s")
  done
  if [ ${#MISSING[@]} -gt 0 ]; then
    echo "ERROR: missing bootstrap secrets: ${MISSING[*]}" >&2
    exit 10
  fi
  PROBE_EOF
  # Result: 3 secrets missing
Step 3: Show recovery plan.
Step 4: Execute the bootstrap-secret script on the VM.
Step 5: Verify all 3 secrets exist.
</procedure_trace>
<output>
Operation: bootstrap-secrets on staging
Cluster: Minikube on cloud VM (connected)
Pre-flight: 3 secrets missing (<app>-auth-secret, <app>-ws-api-secret, <app>-maps-api-key)
Commands:
  1. Run the bootstrap-secret script with --maps-api-key from GCP Secret Manager
Verification: kubectl get secret confirms all 3 secrets in the $NAMESPACE namespace
</output>
</example>
</examples>

# Failure modes

- Mode: GCP firewall on wrong network -- symptom: HTTPS times out even though rule exists -- detection: `gcloud compute firewall-rules list` shows rule on `default` not `minikube-network` -- action: recreate with explicit `--network=minikube-network`
- Mode: Stale SSH tunnel after ingress upgrade -- symptom: ports 80/443 unreachable after Helm upgrade -- detection: `sudo ss -tlnp | grep -E ':(80|443)'` shows old PID -- action: `sudo systemctl restart minikube-tunnel`
- Mode: ingress-nginx is NodePort (not LoadBalancer) -- symptom: minikube tunnel does nothing for ports 80/443 -- detection: `kubectl get svc -n ingress-nginx` shows type NodePort -- action: disable minikube addon, deploy ingress-nginx via Helm with `type: LoadBalancer`
- Mode: Missing bootstrap secrets -- symptom: deploy fails with `envsubst` producing empty values -- detection: `kubectl get secret <name> -n $NAMESPACE` returns NotFound -- action: run the repo's bootstrap-secret script on the VM
- Mode: Cluster/versions drift after failed verify -- symptom: cluster state does not match the version-pinning repo's `current_digest` -- detection: SSH to cluster, read Deployment image digest, compare to `current_digest` -- action: `helm rollback <release> -n $NAMESPACE` (reverts one helm revision), then rerun the rollback pipeline

# Related skills

- `deployment` -- defer to deployment for Helm upgrade orchestration (the deploy-and-verify cycle); compose when a cluster issue blocks a deploy
- `helm-chart-expert` -- defer to it for chart template authoring; compose when a deployment issue traces back to a template bug
- `infrastructure-as-code` -- defer to it for IaC drift detection; compose when a manual kubectl fix needs to be captured in code
- `keycloak` -- defer to it for Keycloak realm/client config; compose when Keycloak pod issues involve ingress or firewall
- `docker-build` -- defer to it for image builds; this skill only consumes image tags/digests
- `release-promotion` -- defer to it for cross-environment promotion; compose when a drift check reveals promotion state mismatch
- `gitops-promotion` -- defer to it for GitOps pull-based reconciliation patterns
