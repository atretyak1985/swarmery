---
name: infrastructure-as-code
description: "Use this skill when a task involves detecting config drift between live cluster state and code, capturing manual kubectl/psql/gcloud fixes into Helm values or Terraform, preparing populated values files, or verifying a fresh deploy would succeed from code alone. Don't use it for Helm template authoring (use helm-chart-expert) or migration safety checks (use migration-check)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Enforce the invariant that a fresh server deploy of the project must succeed from code alone, without any manual `kubectl patch`, `kubectl exec`, or `helm --set` overrides that are not persisted. Covers config drift detection, the populated values pattern, post-incident IaC capture, and the "Where Changes Go" decision map.

# When to use this skill (triggers)

- Deploying the platform to a new server or environment and verifying all config is in code
- Auditing config drift after manual cluster fixes (kubectl patch, psql exec, gcloud console changes)
- Preparing or regenerating `.populated.yaml` values files via the infrastructure repo's `mapEnvValuesFromEnv.sh` (or equivalent env-mapping script)
- Verifying that all manual cluster changes are captured in the correct source file (Helm values, Terraform, migration script)
- Diagnosing literal `$VARIABLE_NAME` strings appearing in K8s Secrets

# When NOT to use this skill (anti-triggers)

- Writing or debugging Helm chart templates -- use `helm-chart-expert`
- Checking migration safety or schema alignment -- use `migration-check`
- Deploying the edge service (project.json → device) to edge devices or managing k3s -- use `kubernetes-deployment`
- Managing Keycloak realm/client configuration -- use `keycloak`

# Required environment (Runtime: .claude/skills/infrastructure-as-code/SKILL.md)

- Tools/libraries: `helm` (v3.10+), `kubectl`, `terraform`, `diff`, `bash`
- Scripts: the infrastructure repo's `files/mapEnvValuesFromEnv.sh` and `files/runDatabaseMigrations.sh` (or the project's equivalents)
- Repos: the infrastructure, chart, and Terraform repos (project.json → repos)

## Terraform environment layout (canonical reference)

**Canonical doc:** the Terraform repo's README §Environment layout. Read it before adding any new resource; it is the single source of truth for current vs legacy env dirs.

Common patterns to watch for:

| Element | Pattern |
|---|---|
| Active env dirs | the currently maintained `environments/<org>/{shared,dev,...}` tree |
| Legacy (DEAD) env dirs | superseded `environments/...` trees whose backend state buckets no longer exist -- do **not** add resources there |
| GCS state bucket | often a single bucket, with per-env discrimination via `prefix = "iac/env-<name>"` |
| Terraform runner GCP project | may differ from the cluster's GCP project -- trust the repo README, not stale variable defaults |
| Environment naming | the staging alias (project.json → cloud.envAlias) may appear as `dev` in Terraform paths -- same environment |
| Cutover history | look for cutover notes committed alongside the env dirs |

**Rule of thumb:** if `terraform init` complains about a missing GCS bucket, you are probably in a legacy env dir. Switch to the corresponding active env dir; don't try to create the missing bucket.

# Inputs

- `environment: string` -- target environment name (e.g., `localdev`, `<envAlias>`, `prod`)
- `drift_source: string` (optional) -- what changed: `helm-override`, `kubectl-patch`, `psql-fix`, `gcp-console`, `terraform-drift`
- `values_file: string` (optional) -- path to the template values file (e.g., `values.<envAlias>.yaml`)

# Outputs

**Format:** A drift report listing what diverged and where to persist the fix, OR a verified "clean deploy would succeed" confirmation.

**Length budget:** Drift report max 40 lines. Post-incident capture checklist max 20 lines. Verification command output summarized as pass/fail, not echoed in full.

**Output template:**

```
## Drift Report -- {environment}

| # | Resource | Live Value | Code Value | Source of Truth File | Action |
|---|----------|------------|------------|---------------------|--------|
| {n} | {resource} | {live} | {code} | {file}:{key} | {persist/escalate} |

### Verification
helm template: {PASS|FAIL}
helm upgrade --dry-run: {PASS|FAIL}
terraform plan: {PASS|FAIL|N/A}

### Confidence: {HIGH|MEDIUM|LOW} -- {rationale}
```

For post-incident capture: a checklist of files to update with specific paths and keys.

# Procedure (Checkpoint: after each step)

1. **Identify what changed** -- Determine the category of drift: Helm override, kubectl patch, SQL fix, GCP console change, or Terraform resource.
   Checkpoint: category identified.

2. **Detect drift** -- Run the appropriate diff commands (see detection commands below). Helm diff and Terraform plan are independent and may run in parallel Bash calls.
   Checkpoint: drift evidence captured.

3. **Map to source of truth** -- Use the "Where Changes Go" table to identify which file to update. For ambiguous cases (e.g., a change spanning Helm values and Terraform), list both targets and ask the user which applies.
   Checkpoint: target file identified.

4. **Persist the fix** -- Update the correct source file (values template, Terraform .tf, migration script).
   Checkpoint: source file modified.

5. **Verify clean deploy** -- Render templates and run dry-run to confirm a fresh deploy would succeed.
   Checkpoint: `helm template` and `helm upgrade --dry-run` both exit 0, or `terraform plan` shows no diff.

### Drift detection commands

```bash
# Compare live Helm values vs values file
helm get values <release> -n "$NAMESPACE" > "$(mktemp)"
diff "$(mktemp)" values.$ENV.populated.yaml

# Compare live manifests vs rendered templates
helm get manifest <release> -n "$NAMESPACE" > "$(mktemp)"
helm template <release> . -f values.$ENV.populated.yaml > "$(mktemp)"
diff <live> <rendered>

# Terraform drift
cd <terraform-repo>/environments/$ENV
terraform plan  # Any non-empty plan = drift
```

### Where Changes Go -- Decision Table

| Category | Source of Truth | Tool | Drift Owner |
|---|---|---|---|
| K8s workloads, services, ingress | Helm chart templates + values files | `helm upgrade` | this skill |
| Secrets (passwords, API keys) | Env vars + `mapEnvValuesFromEnv.sh` | populated values | this skill |
| K8s Secret missing entirely (not wrong value) | Bootstrap-secret pattern | the repo's bootstrap-secret script | `kubernetes-deployment` skill |
| Database schema | Migration scripts | `runDatabaseMigrations.sh` | `migration-check` skill |
| GCP VMs, networks, firewalls | Terraform `.tf` files | `terraform apply` | this skill |
| GCP IAM, service accounts | Terraform `.tf` files | `terraform apply` | this skill |
| TLS certificates | cert-manager CRDs in Helm templates | `helm upgrade` | this skill |

# Self-check before returning (anti-hallucination, confidence labels, format match)

- [ ] I never suggested editing a `.populated.yaml` file -- only the template `values.$ENV.yaml`
- [ ] I confirmed the env var is set before instructing the user to re-run `mapEnvValuesFromEnv.sh`
- [ ] I used `$NAMESPACE` or `<namespace>` placeholder, not a hardcoded namespace name
- [ ] I used `$ENV` placeholder for environment-specific paths, not a hardcoded environment name
- [ ] I cited which file and key produced the drift evidence, using `file:key` format
- [ ] I cross-referenced the bootstrap-secret pattern (from `kubernetes-deployment` skill) if the drift involves a missing secret -- distinguished from wrong-valued secrets (which stay in this skill)
- [ ] Output matches the drift report template format
- [ ] Confidence label (HIGH / MEDIUM / LOW) is attached -- label LOW when diff may produce false positives due to YAML key ordering differences

# Common mistakes to avoid (DO NOT patterns)

- DO NOT edit `.populated.yaml` files -- they are generated outputs, not sources of truth; edit `values.$ENV.yaml` instead
- DO NOT leave a manual `kubectl patch` or `helm --set` override uncaptured -- persist it immediately to the values file
- DO NOT hardcode the infra namespace in commands -- use the `$NAMESPACE` placeholder
- DO NOT write diff output to `/tmp` -- use `$(mktemp)` or the working directory
- DO NOT assume `mapEnvValuesFromEnv.sh` will succeed if env vars are unset -- check first
- DO NOT confuse a missing secret (bootstrap-secret pattern, owned by `kubernetes-deployment`) with a wrong-valued secret (populated values pattern, owned by this skill)

# Escalation (stop-and-ask conditions)

- Stop and ask when: the env var source is unknown (where does `$REDIS_PASSWORD` come from?)
- Stop and ask when: the populated values file contains a literal `$VARIABLE_NAME` string and the env var cannot be located
- Stop and ask when: the drift involves a resource not covered by the "Where Changes Go" table
- Stop and ask when: the drift spans both Helm values and Terraform (ambiguous single-source-of-truth)
- Stop and ask when: `mapEnvValuesFromEnv.sh` has not been updated to include a newly added env var (stale script)

# Examples

<example name="diagnosing-literal-variable-in-secret">
## Diagnosing literal $VARIABLE_NAME in a K8s Secret

Symptom: Redis connections fail. Pod env vars look correct, but the K8s Secret `<infra-release>-redis` contains the literal string `$REDIS_PASSWORD`.

```bash
# 1. Check that the env var is set
echo $REDIS_PASSWORD  # Should output the actual password

# 2. Re-generate the populated values file
./files/mapEnvValuesFromEnv.sh -en <envAlias>

# 3. Re-deploy with the regenerated file (dry-run first)
helm upgrade <infra-release> . \
  -f values.<envAlias>.populated.yaml \
  -n "$NAMESPACE" \
  --dry-run

# 4. If dry-run looks correct, apply
helm upgrade <infra-release> . \
  -f values.<envAlias>.populated.yaml \
  -n "$NAMESPACE" \
  --wait --timeout 8m
```

Output:
```
## Drift Report -- <envAlias>

| # | Resource | Live Value | Code Value | Source of Truth File | Action |
|---|----------|------------|------------|---------------------|--------|
| 1 | Secret/<infra-release>-redis | $REDIS_PASSWORD (literal) | <actual password> | values.<envAlias>.yaml:redis.password | Re-run mapEnvValuesFromEnv.sh |

### Verification
helm template: PASS
helm upgrade --dry-run: PASS

### Confidence: HIGH -- env var confirmed set, populated file regenerated
```
</example>

<example name="post-incident-iac-capture">
## Post-incident IaC capture checklist

After manually patching a Keycloak deployment's memory limit via `kubectl edit`:

| What Changed | Where to Persist |
|---|---|
| Keycloak memory limit | `values.$ENV.yaml` -> `keycloak.resources.limits.memory` |

```yaml
# values.<envAlias>.yaml (template file, committed to Git)
keycloak:
  resources:
    limits:
      memory: "1Gi"  # was 512Mi, increased during incident
```

Then verify:
```bash
helm template <infra-release> . -f values.<envAlias>.populated.yaml | grep -A5 "memory"
helm upgrade <infra-release> . -f values.<envAlias>.populated.yaml -n "$NAMESPACE" --dry-run
```
</example>

# Failure modes (symptom -> detection -> action)

- **Literal $VARIABLE in Secret**: symptom: service can't authenticate to Redis/PostgreSQL -> detect: `kubectl get secret <name> -o jsonpath='{.data.password}' | base64 -d` shows `$REDIS_PASSWORD` literally -> fix: set the env var, re-run `mapEnvValuesFromEnv.sh`, re-deploy
- **Helm values not applied after manual --set**: symptom: `helm get values` shows overrides not in the values file -> detect: `diff <(helm get values <release>) values.$ENV.populated.yaml` -> fix: add the override to `values.$ENV.yaml`, regenerate populated file
- **Terraform drift undetected**: symptom: GCP Console change works but `terraform apply` would revert it -> detect: `terraform plan` shows a change -> fix: update the corresponding `.tf` file, run `terraform apply`
- **Stale mapEnvValuesFromEnv.sh**: symptom: new service config key shows `$NEW_VAR` literally after deployment -> detect: check if `mapEnvValuesFromEnv.sh` references the new variable -> fix: add the variable mapping to the script, re-run, re-deploy

# Related skills (compose vs defer)

- `helm-chart-expert` -- **defer** to it for Helm template authoring and chart structure; **compose** with it when drift involves a template bug
- `kubernetes-deployment` -- **defer** to it for k8s cluster operations and the bootstrap-secret pattern (missing secrets); **compose** when drift involves a secret that does not exist at all (vs wrong value)
- `migration-check` -- **defer** to it for migration safety; **compose** when a manual SQL fix needs to be captured as a migration script
- `keycloak` -- **defer** to it for Keycloak realm/client configuration; **compose** when Keycloak Helm values drift is detected
