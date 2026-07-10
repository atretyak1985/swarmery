---
name: helm-chart-expert
description: "Use this skill when a task involves Helm chart templating, values file structure, subchart dependency management, or chart validation (lint/template/dry-run) for the project's charts. Don't use it for Helm deploy orchestration (use deployment), Keycloak config (use keycloak), IaC drift (use infrastructure-as-code), or Docker builds (use docker-build)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Author and validate Helm charts for the project's infrastructure and chart repos (project.json → repos). This skill covers chart structure, Go template patterns, values management, subchart dependencies, and validation workflows (lint, template render, dry-run). It does not cover end-to-end deploy orchestration (use `deployment`), Keycloak realm/client configuration (use `keycloak`), IaC drift detection (use `infrastructure-as-code`), or Docker image builds (use `docker-build`).

# When to use this skill (triggers)

- Creating or modifying Helm chart templates (deployment, service, ingress, networkpolicy, etc.)
- Structuring or reviewing `values.yaml` / `values.<env>.yaml` files
- Managing subchart dependencies in `Chart.yaml` and `Chart.lock`
- Running chart validation: `helm lint`, `helm template`, `helm upgrade --dry-run`
- Debugging template rendering errors (nil pointer, label mismatch, values not applied)
- Bumping chart versions and synchronizing umbrella/subchart versions
- Reviewing Helm template correctness with file:line citations

# When NOT to use this skill (anti-triggers)

- End-to-end deploy orchestration (helm upgrade to a live cluster) -- use `deployment`
- Keycloak realm setup, client config, or Auth.js integration -- use `keycloak`
- Terraform drift detection or post-incident IaC capture -- use `infrastructure-as-code`
- Docker image builds or multi-arch buildx -- use `docker-build`
- Migration safety checks or schema alignment -- use `migration-check`
- GCP firewall rules or VM-level k8s debugging -- use `kubernetes-deployment`
- CI/CD pipeline YAML review -- use `gitlab-ci-cd`

# Required environment (Runtime: .claude/skills/helm-chart-expert/SKILL.md)

- Tools/libraries: `helm` (v3.10+), `kubectl` (v1.28+), `bash`
- Repos: the project's infrastructure and chart repos (project.json → repos)
- Validation script: `.claude/skills/helm-chart-expert/scripts/validate-chart.sh`

# Inputs

- `chart_path: string` -- path to the chart directory (e.g., `<infra-repo>/`)
- `values_file: string` (optional) -- path to the values file to validate against
- `operation: enum` -- one of: `author`, `validate`, `review`, `debug`

# Outputs

**Format:** Advice, corrected template YAML, or a shell command sequence.

**Length budget:** Corrected YAML max 60 lines per template file. Shell command sequences max 20 lines. Review findings max 40 lines.

**Output template:**

```
## {operation} Result — {chart_path}

### Findings
| # | File:Line | Issue | Severity | Fix |
|---|-----------|-------|----------|-----|
| {n} | {file}:{line} | {description} | {HIGH|MEDIUM|LOW} | {corrected snippet or instruction} |

### Validation Commands
{ordered command list with pass/fail}

### Confidence
{HIGH|MEDIUM|LOW} — {rationale}
```

For `validate`: ordered command list (lint, template, dry-run) with pass/fail assessment.
For `review`: structured findings table with file:line citations.
For `author`: corrected template YAML with inline comments.

# Procedure (Checkpoint: after each step)

1. **Identify chart and operation** -- Confirm which chart directory and which values file(s) are in scope.
   Checkpoint: chart directory contains `Chart.yaml`.

2. **Run validation first** -- Run `helm lint` and `helm template` before suggesting any upgrade. These two commands are independent and may run in parallel Bash calls.
   Checkpoint: both exit 0.

3. **Apply template patterns** -- Use defensive `| default dict` at ALL nesting levels. One resource per file. Namespaced template names via `_helpers.tpl`.
   Checkpoint: every conditional chain protects all levels.

4. **Dry-run before apply** -- Every upgrade example must show `--dry-run` as the first step.
   Checkpoint: dry-run output reviewed before suggesting live apply.

5. **Version sync** -- If a subchart version was bumped, verify umbrella `Chart.yaml` dep matches and `Chart.lock` is regenerated. A `check-chart-sync.sh` script typically lives in the chart repo (e.g. `scripts/check-chart-sync.sh`), not in this skill bundle.
   Checkpoint: all three files (subchart Chart.yaml, umbrella Chart.yaml, Chart.lock) updated together.

# Self-check before returning (anti-hallucination, confidence labels, format match)

- [ ] Every `helm upgrade` command I suggested includes `--dry-run` as a preceding step
- [ ] Template examples protect ALL nesting levels with `| default dict` (not just the body)
- [ ] I used `{{ .Release.Namespace }}` instead of hardcoded namespace names
- [ ] I did not include absolute developer home paths (used `$REPO_ROOT` or relative paths)
- [ ] Chart version bump includes umbrella Chart.yaml + Chart.lock + subchart Chart.yaml
- [ ] I cited which template file and values key are relevant to the recommendation, using file:line format
- [ ] Secret values use `requireRealSecret` helper or `$VARIABLE` placeholders -- no plaintext
- [ ] No mutable image tags (`:latest`, `:main`) appear in any YAML I produced -- only immutable digests or `$VARIABLE` placeholders
- [ ] Output matches the output template format for the given operation
- [ ] Confidence label (HIGH / MEDIUM / LOW) is attached to findings where the template nesting pattern depends on values file structure I have not inspected -- label those LOW

# Common mistakes to avoid (DO NOT patterns)

- DO NOT access nested values without `| default dict` at every level -- causes nil pointer errors when a values file omits a section
- DO NOT hardcode namespaces in examples -- use `$NAMESPACE` or `{{ .Release.Namespace }}`
- DO NOT put multiple K8s resources in one template file -- one resource per file
- DO NOT show `helm upgrade` without a preceding `--dry-run` step -- silent failures in prod
- DO NOT bump a subchart version without updating the umbrella `Chart.yaml` dep and regenerating `Chart.lock`
- DO NOT embed environment-specific values (hostnames, project IDs) in templates -- put them in `values.<env>.yaml`
- DO NOT use mutable image tags (`:latest`, `:main`) in values files or templates -- use immutable digests (`sha256:...`) or `$VARIABLE` placeholders
- DO NOT confuse this skill's scope with `deployment` -- this skill covers template authoring and validation only; `deployment` handles end-to-end orchestration

# Escalation (stop-and-ask conditions)

- Stop and ask when: the chart directory cannot be found or `Chart.yaml` is missing required fields
- Stop and ask when: a subchart dependency cannot be resolved after `helm repo update`
- Stop and ask when: the user is attempting a production upgrade without a values file -- redirect to `deployment` skill for orchestration
- Stop and ask when: the user asks for end-to-end deploy orchestration -- redirect to `deployment`

# Examples

<example name="defensive-template-for-optional-feature">
## Defensive template for optional feature

The infrastructure chart has an optional NetworkPolicy for Keycloak. If the `networkPolicy` section is absent from a values file, unprotected access causes a nil pointer error.

**Wrong -- only body protected:**
```yaml
{{- if .Values.networkPolicy.keycloak.enabled }}
  {{- $config := .Values.networkPolicy.keycloak.ingress | default dict }}
{{- end }}
```

**Correct -- all levels protected:**
```yaml
{{- $networkPolicy := .Values.networkPolicy | default dict }}
{{- $keycloakPolicy := $networkPolicy.keycloak | default dict }}
{{- if (hasKey $keycloakPolicy "enabled") | ternary $keycloakPolicy.enabled false }}
  {{- $ingressConfig := $keycloakPolicy.ingress | default dict }}
  {{- if (hasKey $ingressConfig "allowFromIngressController") | ternary $ingressConfig.allowFromIngressController true }}
    # ... ingress rules
  {{- end }}
{{- end }}
```

**Verification -- test all paths (lint and template can run in parallel):**
```bash
helm template <infra-release> . --values values.localdev.yaml       # full values
helm template <infra-release> . --set feature.enabled=true          # minimal --set
helm template <infra-release> .                                     # defaults only
helm template <infra-release> . --values values.init.localdev.yaml  # bootstrap values
```
</example>

<example name="subchart-version-bump-workflow">
## Subchart version bump workflow

When bumping `charts/<app>/Chart.yaml` version in the umbrella chart repo:

```bash
# 1. Bump subchart version
#    Edit charts/<app>/Chart.yaml -> version: 0.2.0

# 2. Update umbrella Chart.yaml dependency to match
#    Edit Chart.yaml -> dependencies.<app>.version: "0.2.0"

# 3. Regenerate Chart.lock
helm dependency update .

# 4. Verify (this script lives in the chart repo, not this skill bundle)
bash scripts/check-chart-sync.sh

# 5. Commit all three together
git add Chart.yaml Chart.lock charts/<app>/Chart.yaml
```
</example>

<example name="canonical-upgrade-with-dry-run">
## Canonical upgrade with dry-run first

```bash
# Step 1: Dry-run (always first)
helm upgrade --install <infra-release> . \
  -f values.<envAlias>.populated.yaml \
  --namespace "$NAMESPACE" \
  --dry-run --debug

# Step 2: Apply (defer to `deployment` skill for production orchestration)
helm upgrade --install <infra-release> . \
  -f values.<envAlias>.populated.yaml \
  --namespace "$NAMESPACE" \
  --wait --atomic --timeout 8m \
  --description "[ci $CI_PIPELINE_ID deploy] infra@$(git rev-parse --short HEAD)"
```
</example>

<example name="requireRealSecret-helper">
## requireRealSecret helper

For secret values that must not ship as `CHANGE_ME` in production:

```yaml
# _helpers.tpl
{{- define "app.requireRealSecret" -}}
{{- $value := index . 0 -}}
{{- $name := index . 1 -}}
{{- if or (eq $value "") (eq $value "CHANGE_ME") -}}
{{- fail (printf "secrets.%s required -- set a real value" $name) -}}
{{- end -}}
{{- $value -}}
{{- end -}}

# secret.yaml
auth-secret: {{ include "app.requireRealSecret" (list .Values.secrets.authSecret "authSecret") | quote }}
```
</example>

# Failure modes (symptom -> detection -> action)

- **nil pointer in template**: symptom: `nil pointer evaluating interface {}.fieldName` -> detect: identify which values key is missing `| default dict` at the conditional level -> fix: add defensive extraction at every nesting level
- **Chart.lock stale after subchart bump**: symptom: `can't get a valid version for dependency <name>` -> detect: compare subchart Chart.yaml version with umbrella Chart.yaml dep version -> fix: update umbrella dep, run `helm dependency update .`, commit both
- **Values not applied**: symptom: default values appear instead of overrides -> detect: `helm get values <release> -n $NAMESPACE` shows missing keys -> fix: verify `--values <file>` flag was passed and check indentation in the values file
- **Label mismatch blocking service discovery**: symptom: service can't find pods -> detect: compare `kubectl get pods --show-labels` with `kubectl get svc -o yaml | grep selector` -> fix: ensure both use the same selector labels from `_helpers.tpl`

# Related skills (compose vs defer)

- `deployment` -- **defer** to it for end-to-end deploy orchestration (helm upgrade to live clusters); this skill covers template authoring and validation only. For end-to-end deploy orchestration, use `deployment`.
- `keycloak` -- **defer** to it for Keycloak realm/client/Auth.js config; this skill only covers the Helm values structure for the keycloakx subchart
- `infrastructure-as-code` -- **defer** to it for IaC drift detection; **compose** with it when a manual helm override needs to be captured in code
- `kubernetes-deployment` -- **defer** to it for k8s cluster operations, GCP firewall, minikube tunnel; **compose** when debugging a deployment that uses this chart
- `migration-check` -- no direct overlap; migration scripts are not Helm-managed
- `docker-build` -- **defer** to it for image builds; this skill only consumes image tags/digests in values files
