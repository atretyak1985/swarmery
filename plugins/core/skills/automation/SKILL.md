---
name: automation
description: "Convert a repeatable operational runbook for the project's infrastructure (pod restarts, cache flushes, scaling) into a parameterized, idempotent script with safety gates, or design a chaos experiment to test system resilience. Do not use for CI/CD pipeline changes, deployment-manifest authoring, application code changes, or ad-hoc debugging."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Write, Bash
---

# Purpose

You convert manual operational procedures (runbooks) into parameterized, idempotent scripts with safety gates, and optionally graduate them to self-healing controllers. You also design and produce chaos experiments against the project's cloud infrastructure (see `.claude/project.json` -> `cloud.*`) with mandatory environment guards. All output is executable shell scripts or Python automation code with dry-run, confirmation, and rollback steps. The examples below use Kubernetes/kubectl -- adapt commands to the project's runtime (`cloud.runtime`). Related skills: `code-standards` (for script quality), `api-integration` (if the runbook touches the main app's APIs).

# When to use

- Trigger A -- A manual runbook exists (documented or ad-hoc) that is executed more than twice per week
- Trigger B -- An operational task involves restarting pods, flushing caches, scaling deployments, or rotating secrets
- Trigger C -- A chaos experiment is needed to validate system resilience (pod kill, network partition, resource exhaustion)
- Trigger D -- A toil reduction analysis is requested for a specific operational area

# When NOT to use

- Writing or modifying CI/CD pipelines (GitLab CI, GitHub Actions) -- use `deployment`
- Authoring or updating deployment manifests or infrastructure templates -- use `deployment`
- Writing application code (TypeScript, Python) for the main app or the device/edge repo -- use appropriate application skills
- Ad-hoc debugging of a production incident -- use `troubleshooting`
- Updating Prometheus alerting rules or Grafana dashboards -- use `monitoring`

# Required environment

- Runtime: `.claude/skills/automation/SKILL.md`
- Tools: Read, Write, Bash
- File system assumptions:
  - `kubectl` (or the runtime's equivalent CLI -- `.claude/project.json` -> `cloud.runtime`) is configured with the appropriate context
  - The target namespace is passed as a parameter (never hardcoded)
  - Chaos experiments require explicit `ALLOW_CHAOS=true` environment variable
- Canonical script storage path: `devops/scripts/<runbook-name>.sh` (or `.py`). All produced scripts are saved here and referenced in a pull request for review before cluster execution.

# Inputs

- `runbook_name: string` -- name of the runbook to automate (e.g., "restart-device-gateway", "flush-redis")
- `target_namespace: string` -- target namespace (e.g., "app", "edge")
- `target_deployment: string` -- deployment name (e.g., "device-gateway", "web-app")
- `automation_level: "script" | "self-healing" | "chaos"` -- what to produce

# Outputs

- Format: executable shell script (`.sh`) or Python module (`.py`) with inline documentation
- Length budget: scripts under 100 lines; self-healing modules under 200 lines
- Storage: saved to `devops/scripts/<runbook-name>.sh` (or `.py`) using the Write tool

# Procedure

1. **Review the manual runbook** -- Read the existing documentation or user description of the manual steps using the Read tool.
   **Checkpoint:** Manual steps are understood and can be listed sequentially.

2. **Confidence gate** -- If the runbook steps are ambiguous, contradictory, or incomplete, STOP and ask the user for clarification before proceeding. Do not write or execute scripts against a live cluster without at least one human confirmation step.
   **Checkpoint:** Runbook steps are unambiguous and complete.

3. **Identify safety requirements** -- For each step, determine:
   - Is it destructive (deletes data, restarts services, modifies state)?
   - Does it require confirmation?
   - Can it be rolled back?
   - Is a dry-run possible?
   **Checkpoint:** Every destructive step has a safety gate identified.

4. **Parameterize all environment-specific values** -- Replace hardcoded namespaces, deployment names, hostnames, and credentials with script parameters or environment variables.
   **Checkpoint:** Zero hardcoded cluster-specific values remain.

5. **Write the automated script** -- Use the Write tool to save the script to `devops/scripts/<runbook-name>.sh` (or `.py`). Apply these mandatory rules:
   - Every destructive kubectl command must be preceded by a `--dry-run=client` step
   - Every script must accept `--dry-run` flag that skips all destructive operations
   - Every script must log what it does with timestamps
   - Every script must use `set -euo pipefail` (bash) or equivalent error handling
   - Chaos experiments must check `ALLOW_CHAOS=true` before executing
   - Chaos experiments must check that the target namespace is NOT a production namespace
   - All parameters must have defaults documented in usage text
   **Checkpoint:** Script saved to canonical path.

6. **Add rollback procedure** -- Document or script the rollback for each destructive step.
   **Checkpoint:** Rollback is either scripted or documented with exact commands.

7. **Test with dry-run** -- Use the Bash tool to execute the script with `--dry-run` flag and verify output. Include the dry-run output in your response.
   **Checkpoint:** Dry-run completes without errors and shows what would happen.

8. **Final acceptance check** -- Script is parameterized, has safety gates, has rollback, and dry-run passes.
   **Checkpoint:** All self-check items pass.

# Self-check before returning

- [ ] Zero hardcoded namespaces -- all namespace values come from parameters or environment variables
- [ ] Zero hardcoded deployment names -- all deployment names come from parameters
- [ ] Every destructive command has a preceding `--dry-run=client` step or equivalent
- [ ] Script accepts a `--dry-run` flag that prevents all destructive operations
- [ ] Chaos experiments check `ALLOW_CHAOS=true` environment variable before executing
- [ ] Chaos experiments reject production namespaces (any namespace containing "prod")
- [ ] Script uses `set -euo pipefail` or equivalent error handling
- [ ] Every operation is logged with a timestamp
- [ ] Rollback procedure is documented or scripted for every destructive step
- [ ] Script is saved to `devops/scripts/` using the Write tool

# Common mistakes to avoid

- DO NOT hardcode `-n app` or any namespace -- always use a `$NAMESPACE` variable with the namespace passed as a required parameter
- DO NOT hardcode deployment names like `deployment/device-gateway` -- accept them as parameters
- DO NOT run chaos experiments without an environment guard that blocks production
- DO NOT use `kubectl delete pod` without a preceding dry-run and confirmation prompt
- DO NOT use `asyncio.sleep()` without a cancellation mechanism in self-healing code -- use `asyncio.wait_for()` with a timeout
- DO NOT reference non-existent Python modules (e.g., `import disconnect_device`) -- only use modules that exist in the repository
- DO NOT schedule recurring chaos experiments (cron-based Chaos Mesh) without explicit human approval and a documented kill switch
- DO NOT write scripts by echoing content through Bash -- always use the Write tool to create script files

# What to surface to the user

- The complete script with inline comments explaining each step
- Dry-run output showing what the script would do
- Rollback procedure (scripted or documented)
- Estimated toil reduction (time saved per execution * frequency)
- Any assumptions about cluster state or prerequisites

# Escalation

- Stop and ask when: the runbook involves deleting PersistentVolumeClaims or StatefulSet data
- Stop and ask when: the chaos experiment targets a namespace that might be production (any namespace not explicitly marked as non-prod)
- Stop and ask when: the self-healing controller would automatically restart a deployment more than 3 times in 10 minutes (restart loop risk)
- Refuse and explain when: asked to automate a runbook that includes credential rotation without a secrets manager integration
- Refuse and explain when: asked to run chaos experiments against a production cluster without explicit written confirmation

# Examples

<example name="restart-device-connection-runbook">
**Manual runbook:**
1. Check device-gateway logs for the disconnected device
2. Restart the device-gateway pod
3. Verify the device reconnects

**Automated script (saved to `devops/scripts/restart-device-connection.sh`):**

```bash
#!/usr/bin/env bash
# restart-device-connection.sh
# Restarts the device-gateway deployment to recover a disconnected device.
#
# Usage: ./restart-device-connection.sh --namespace <ns> --deployment <deploy> [--dry-run]
# Example: ./restart-device-connection.sh --namespace app --deployment device-gateway

set -euo pipefail

NAMESPACE=""
DEPLOYMENT=""
DRY_RUN=false

usage() {
  echo "Usage: $0 --namespace <ns> --deployment <deploy> [--dry-run]"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case $1 in
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --deployment) DEPLOYMENT="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    *) usage ;;
  esac
done

[[ -z "$NAMESPACE" || -z "$DEPLOYMENT" ]] && usage

log() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] $1"; }

log "Checking pods for deployment/$DEPLOYMENT in namespace $NAMESPACE..."
kubectl get pods -n "$NAMESPACE" -l "app=$DEPLOYMENT" --no-headers

log "Dry-run: rollout restart deployment/$DEPLOYMENT -n $NAMESPACE"
kubectl rollout restart "deployment/$DEPLOYMENT" -n "$NAMESPACE" --dry-run=client

if $DRY_RUN; then
  log "DRY RUN complete. No changes made."
  exit 0
fi

read -rp "Proceed with restart? (y/N): " confirm
[[ "$confirm" != "y" && "$confirm" != "Y" ]] && { log "Aborted."; exit 0; }

log "Restarting deployment/$DEPLOYMENT in namespace $NAMESPACE..."
kubectl rollout restart "deployment/$DEPLOYMENT" -n "$NAMESPACE"

log "Waiting for rollout to complete..."
kubectl rollout status "deployment/$DEPLOYMENT" -n "$NAMESPACE" --timeout=120s

log "Post-restart pod status:"
kubectl get pods -n "$NAMESPACE" -l "app=$DEPLOYMENT" --no-headers

log "Restart complete."

# Rollback: kubectl rollout undo deployment/$DEPLOYMENT -n $NAMESPACE
```
</example>

<example name="chaos-kill-pod">
```bash
#!/usr/bin/env bash
# chaos-kill-pod.sh
# Kills a random pod in the target deployment to test resilience.
#
# Usage: ALLOW_CHAOS=true ./chaos-kill-pod.sh --namespace <ns> --deployment <deploy>
# Safety: blocked in production namespaces; requires ALLOW_CHAOS=true

set -euo pipefail

NAMESPACE=""
DEPLOYMENT=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --deployment) DEPLOYMENT="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

[[ -z "$NAMESPACE" || -z "$DEPLOYMENT" ]] && { echo "Missing required args"; exit 1; }

if [[ "${ALLOW_CHAOS:-}" != "true" ]]; then
  echo "ERROR: ALLOW_CHAOS=true is required."
  exit 1
fi

if [[ "$NAMESPACE" == *prod* || "$NAMESPACE" == *production* ]]; then
  echo "ERROR: Chaos experiments are blocked in production namespaces ('$NAMESPACE')."
  exit 1
fi

log() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] $1"; }

POD=$(kubectl get pods -n "$NAMESPACE" -l "app=$DEPLOYMENT" \
  --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')

if [[ -z "$POD" ]]; then
  log "No running pods found for deployment/$DEPLOYMENT in $NAMESPACE"
  exit 1
fi

log "Target pod: $POD"
log "Deleting pod $POD in namespace $NAMESPACE..."
kubectl delete pod "$POD" -n "$NAMESPACE"

log "Waiting for replacement pod..."
kubectl rollout status "deployment/$DEPLOYMENT" -n "$NAMESPACE" --timeout=60s

log "Post-chaos pod status:"
kubectl get pods -n "$NAMESPACE" -l "app=$DEPLOYMENT" --no-headers

log "Chaos experiment complete. Verify telemetry and connectivity."
```
</example>

# Failure modes

| Failure | Detect | Fix |
|---------|--------|-----|
| Script fails because kubectl context is wrong | "error: context not found" in output | Add `kubectl config current-context` check at script start |
| Rollout restart hangs because new pod fails health checks | `kubectl rollout status` times out | Script catches the timeout and suggests `kubectl rollout undo` |
| Chaos experiment accidentally targets production | Namespace contains "prod" | Environment guard blocks execution and exits with error |

# Toil measurement

When automating a runbook, document the toil reduction:

```
Manual time per execution: X minutes
Automated time per execution: Y minutes
Frequency: Z times per week
Weekly savings: (X - Y) * Z minutes
```

# Related skills

- `code-standards` -- defer to for script quality and style conventions
- `api-integration` -- compose when a runbook step involves calling the main app's API endpoints
- `code-quality` -- defer to for complexity and maintainability analysis of automation scripts
