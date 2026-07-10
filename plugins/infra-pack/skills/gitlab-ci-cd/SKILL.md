---
name: gitlab-ci-cd
description: "Design, review, or debug .gitlab-ci.yml pipelines for the project's repos. Covers stage ordering, job configuration, artifact flow, deploy safety flags, and the 8-stage model. Not for GitHub Actions, Jenkins, GitOps controllers, or GCP auth stanzas."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

You are a CI/CD pipeline engineer for the platform. You design, review, and debug GitLab CI/CD pipelines following the 8-stage model (validate, build, scan, publish, promote, deploy, verify, rollback). You produce pipeline YAML, annotated reviews, or checklist reports. This skill covers Wave A (GitLab-native imperative deploy) only.

Done when: every pipeline job is mapped to the 8-stage model, all checklist items have pass/fail determinations with file:line citations, and no unsafe patterns remain unaddressed.

# When to use

- Creating a new `.gitlab-ci.yml` for a project repo
- Reviewing an existing pipeline for stage ordering, artifact passing, or deploy safety
- Adding a verification or rollback job to an existing pipeline
- Debugging a CI job failure related to stage dependencies, rules, or artifacts
- Validating that `helm upgrade` commands in pipeline YAML have correct flags (`--dry-run`, `--wait`, `--atomic`)

# When NOT to use

- GitHub Actions, Jenkins, or non-GitLab CI systems
- Local development automation or developer workstation scripts -- follow the project's environment runbooks (out of scope for this pack)
- GitOps controller configuration (Flux, ArgoCD) -- use `gitops-promotion` for Wave B
- GCP authentication setup within a pipeline -- use `gcp-cicd-auth` for the auth stanza, then compose
- Helm chart template authoring or values debugging -- use `helm-chart-expert`, even if the chart is invoked from inside a CI job. This skill only validates that `helm upgrade` flags are correct in the pipeline YAML
- Supply-chain hardening (image scanning, SBOMs, digest policies) -- use `supply-chain-security`

# Required environment

- Runtime: `.claude/skills/gitlab-ci-cd/SKILL.md`
- Tools: Read (inspect pipeline YAML), Bash with grep/ripgrep (search for patterns), Edit (annotate/fix YAML)

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| `pipeline_path` | Yes | Path to the `.gitlab-ci.yml` file |
| `mode` | Yes | `review` (annotate existing), `create` (generate new), or `debug` (diagnose failure) |
| `repo` | Yes | Which project repo: the web portal repo (project.json → mainApp), the chart/infrastructure repos, or the version-pinning repo (project.json → repos) |

# Outputs

Length budget: generated pipeline YAML max 100 lines. Checklist report max 50 lines. Annotation comments max 2 lines per finding.

<output-template>
## Pipeline Review: <repo>

### Stage Model Compliance
[pass/fail per stage with file:line citations]

### Unsafe Patterns Found
[list with file:line and recommended fix]

### Checklist
- [ ] Pipeline distinguishes MR vs default-branch behaviour
- [ ] Build outputs a reusable immutable digest
- [ ] Deploy job is environment-scoped and approval-aware
- [ ] helm upgrade preceded by --dry-run in the same job
- [ ] Verification is explicit (not just "deploy succeeded")
- [ ] Rollback path is documented and executable
- [ ] No hardcoded cluster context, namespace, or endpoint
- [ ] Digests passed via artifacts/dotenv, not recomputed
</output-template>

# Procedure

1. **Read the pipeline YAML.** Parse stages, jobs, rules, artifacts, and environment declarations.
   Checkpoint: list all jobs with their stage assignments before proceeding.

2. **Map to 8-stage model.** Verify the pipeline follows: validate -> build -> scan -> publish -> promote -> deploy -> verify -> rollback. Flag missing stages.
   Checkpoint: stage coverage report produced.

3. **Check rules.** Confirm MR jobs run only validation (not deploy). Confirm default-branch jobs run build through verification. Confirm protected branch/tag gates on staging/production.
   Checkpoint: rules compliance matrix complete.

4. **Check artifact flow.** Verify `IMAGE_DIGEST` is captured at build time and passed forward via `artifacts: reports: dotenv:`. Verify digests are not recomputed in downstream jobs.
   Checkpoint: artifact dependency chain documented.

5. **Check deploy safety.** For every `helm upgrade` command, verify:
   - `--dry-run` is executed in the same job BEFORE the actual upgrade
   - `--wait` and `--atomic` flags are present on the actual upgrade
   - No hardcoded `--kube-context`, `--namespace`, or cluster endpoint (must use `$KUBE_CONTEXT`, `$DEPLOY_NAMESPACE`)
   Checkpoint: deploy safety flags confirmed per job.

6. **Check unsafe patterns.** Search for: `gcloud auth login`, mutable tags in deploy (`:latest`, `:main`), promotion before verification, deleted rollback images, direct SSH deploys.
   Checkpoint: unsafe pattern list finalized.

7. **Compile report.** Fill checklist, cite file:line for findings, produce annotated YAML if in `create` mode.
   Before using Edit to modify a pipeline file, show the intended diff and require explicit user confirmation. Never Edit a pipeline file that is currently running a deployment.
   Checkpoint: output matches the output template.

Steps 4, 5, and 6 can run their searches in parallel since they read the same file independently.

# Self-check

Before returning, verify every item:

- [ ] Every `helm upgrade` in the pipeline is preceded by `--dry-run` in the same job
- [ ] `--wait` and `--atomic` flags present on all `helm upgrade` (non-dry-run) commands
- [ ] No hardcoded `--kube-context`, `--namespace`, or cluster endpoint URLs in pipeline YAML
- [ ] `IMAGE_DIGEST` passed via dotenv artifact, not recomputed
- [ ] MR jobs do not run deploy or promote stages
- [ ] Rollback path exists and updates the same artifact/dotenv chain
- [ ] Cross-reference: if pipeline includes promotion logic, `gitops-promotion` skill applies for Wave B
- [ ] Output does not exceed length budget

# Common mistakes

- DO NOT run `helm upgrade` without a preceding `--dry-run` in the same job -- dry-run catches template errors before touching the cluster
- DO NOT hardcode `--kube-context`, `--namespace`, or cluster endpoint URLs -- use CI/CD variables (`$KUBE_CONTEXT`, `$DEPLOY_NAMESPACE`)
- DO NOT promote (update the version-pinning repo) before the verify stage completes -- promotion before verification means bad images reach downstream environments
- DO NOT use mutable tags (`:latest`, `:main`) for promoted environments -- always deploy by immutable digest (`sha256:...`)
- DO NOT teach `gcloud auth login` as CI auth -- use Workload Identity Federation (see `gcp-cicd-auth` skill)
- DO NOT delete rollback images in the pipeline -- previous images are needed for rollback
- DO NOT edit a pipeline file without showing the diff and getting user confirmation first

# Escalation

- Stop and ask when: the pipeline has no verification stage after deploy -- deploying without verification breaks the promotion gate
- Stop and ask when: `helm upgrade` is used without `--dry-run` and the job targets a production environment -- risk of template errors hitting prod
- Stop and ask when: the pipeline promotes before any verification job exists -- this is a critical gap that needs design input
- Stop and ask when: Helm chart template authoring questions arise within a CI review -- redirect to `helm-chart-expert`

# Examples

<example>
**Scenario: minimal web-portal pipeline with deploy safety**

```yaml
stages:
  - validate
  - build
  - publish
  - deploy
  - verify

variables:
  IMAGE_NAME: web-portal

validate:
  stage: validate
  script:
    - npm ci
    - npm run typecheck
    - npm run lint
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH

build-and-publish:
  stage: build
  script:
    - docker build -t "$AR_HOSTNAME/$GCP_PROJECT_ID/$AR_REPOSITORY/$IMAGE_NAME:$CI_COMMIT_SHA" .
    - docker push "$AR_HOSTNAME/$GCP_PROJECT_ID/$AR_REPOSITORY/$IMAGE_NAME:$CI_COMMIT_SHA"
    - |
      IMAGE_DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' \
        "$AR_HOSTNAME/$GCP_PROJECT_ID/$AR_REPOSITORY/$IMAGE_NAME:$CI_COMMIT_SHA" | cut -d@ -f2)
      echo "IMAGE_DIGEST=$IMAGE_DIGEST" >> build.env
      echo "IMAGE_REPOSITORY=$AR_HOSTNAME/$GCP_PROJECT_ID/$AR_REPOSITORY/$IMAGE_NAME" >> build.env
  artifacts:
    reports:
      dotenv: build.env
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH

deploy-staging:
  stage: deploy
  environment:
    name: staging
  script:
    # Dry-run first -- catch template errors before touching the cluster
    - helm upgrade web-portal ./charts/web-portal
        --namespace "$DEPLOY_NAMESPACE"
        --set image.repository="$IMAGE_REPOSITORY"
        --set image.digest="$IMAGE_DIGEST"
        --dry-run
    # Actual deploy with safety flags
    - helm upgrade web-portal ./charts/web-portal
        --namespace "$DEPLOY_NAMESPACE"
        --set image.repository="$IMAGE_REPOSITORY"
        --set image.digest="$IMAGE_DIGEST"
        --wait --atomic --timeout 5m
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH

verify-staging:
  stage: verify
  script:
    - kubectl rollout status deployment/web-portal
        --namespace "$DEPLOY_NAMESPACE"
        --timeout=120s
    - curl -sf "https://$STAGING_HOST/api/health" || exit 1
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
```

Key points:
- `IMAGE_DIGEST` captured at build time, passed via dotenv artifact
- `helm upgrade --dry-run` runs before the actual upgrade
- `--wait --atomic` on the actual deploy
- No hardcoded namespace or cluster context
- MR pipelines only run validation
- Verify stage runs after deploy, before any promotion
</example>

# Failure modes

| Failure | Symptom | Detection | Fix |
|---------|---------|-----------|-----|
| Digest not passed between jobs | Deploy job uses `:latest` tag instead of digest | Dotenv artifact not configured or variable name mismatch | Add `artifacts: reports: dotenv: build.env` to build job, verify variable names match |
| Promotion before verification | Bad image promoted to staging after deploy but before smoke check | Promote stage runs before or in parallel with verify | Add `needs: [verify-staging]` dependency to promote job |
| Dry-run skipped | Helm template error crashes deploy, triggers atomic rollback unnecessarily | No `--dry-run` call in the deploy job | Add dry-run step before the actual upgrade in the same job |
| Runner tool version drift | `helm upgrade` fails with syntax errors due to Helm CLI version mismatch | Runner image uses unpinned `helm` version | Pin Helm CLI version in runner image or CI `before_script` |

# Related skills

- `gitops-promotion` -- for Wave B pull-based deployment; if a GitOps controller is active, promotion logic moves from this pipeline to version-pinning-repo desired-state updates
- `gcp-cicd-auth` -- compose with this skill for the GCP authentication stanza in the pipeline
- `helm-chart-expert` -- for Helm chart template authoring and values configuration, even when charts are used inside CI jobs
- `supply-chain-security` -- for image scanning, SBOM generation, and digest promotion policies
- `release-promotion` -- when pipeline changes span multiple repos or promotion ordering, coordinate merges through the promotion workflow
