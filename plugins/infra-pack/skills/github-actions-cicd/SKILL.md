---
name: github-actions-cicd
description: "Design, review, or debug GitHub Actions workflows — CI jobs (type-check, lint, test, build, security, summary), AWS ECR/ECS deploys, and a standardized per-repo workflow set. NOT for cloud-auth role/OIDC trust details (use aws-cicd-auth) or Dockerfile/image-build mechanics (use docker-build)."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Author, review, and debug the GitHub Actions workflows that drive the project's repositories. Produce workflow YAML that matches the established CI job graph, the AWS ECR/ECS deploy contract, and the standardized workflow set each repo ships — so a workflow in any repo is recognizable from any other.

Read the project's shape from `project.json` (repos/monorepo) — this skill covers both; the examples below use a multi-repo layout. Each repository owns its own `.github/workflows/` directory and runs the **same standardized set** of workflows. The application repos (e.g. the API repo — `project.json` → `mainApp` — a NestJS service, plus the client/admin/vendor/mobile repos) build container images; the infrastructure repo carries its own `ci.yml` + `deploy.yml`.

Cloud is **AWS** — images go to ECR, services run on ECS with a rolling, force-new-deployment update. There is **no GCP, no Kubernetes, no Helm, and no GitOps controller** anywhere in this pipeline. Deploys are imperative `aws ecs update-service` calls followed by a stability poll.

# When to use

- Adding or modifying a job in a repo's `ci.yml` (type-check, lint, test, build, security, summary).
- Writing or fixing the ECR build/push + ECS force-new-deployment flow in `deploy-prod.yml`.
- Bringing a repo's workflow set in line with the standard (`ci.yml`, `deploy-prod.yml`, `nightly.yml`, `version-bump.yml`, `secret-scan.yml`, `dependabot-lockfix.yml`, `branch-protection.yml`).
- Debugging a failing Actions run — a red CI job, a deploy that never reaches ECS stability, a `workflow_run` trigger that didn't fire.
- Tuning concurrency, `permissions`, the Node/service-container matrix, or the `$GITHUB_STEP_SUMMARY` aggregation.
- Reviewing a PR that touches any file under `.github/workflows/`.

# When NOT to use

- **AWS OIDC trust, the assumed IAM role, ECR repository permissions, or the `AWS_ROLE_ARN` federation setup** — use `aws-cicd-auth`. This skill consumes those secrets; it does not define the cloud-side trust.
- **Dockerfile authoring or image-build mechanics** (layer caching, build args, multi-stage, image size) — use `docker-build`. This skill only invokes `docker build`/`docker push`; it does not own the Dockerfile.
- **ECS task definition / service / cluster provisioning** (infrastructure-as-code) — out of scope; the workflow assumes the cluster and service already exist.
- **Application code, tests, or lint rules themselves** — this skill wires the commands (`npm run typecheck`, `npm run test:cov`), it does not write them.
- **Prometheus/Grafana CI metrics** — use `monitoring`.

# Required environment

- Read/write access to the target repo's `.github/workflows/` directory.
- Node 20 toolchain assumptions: `package.json` exposes `typecheck`, `lint:check`, `format:check`, `test:cov`, `build` scripts. (`npm ci` requires a committed `package-lock.json`.)
- For the `test` job: ability to run service containers `mongo:6.0` and `redis:7-alpine` on the runner.
- For `deploy-prod.yml`: repository secrets `AWS_ROLE_ARN`, `AWS_REGION`, `ECR_REPOSITORY`, `ECS_CLUSTER`, `ECS_SERVICE` set, and the OIDC trust already established (owned by `aws-cicd-auth`).
- `gh` CLI for inspecting runs (`gh run list`, `gh run view --log-failed`).

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Target repo | Yes | Which repo's workflows to touch (the API repo — `project.json` → `mainApp` —, a client repo, the infrastructure repo, …) |
| Change intent | Yes | New job, deploy fix, workflow-set alignment, or run-failure diagnosis |
| Failing run reference | No | Run ID / job name when debugging |
| Secret/var availability | No | Confirmation that required `secrets.*` exist (deploy work) |

# Outputs

**Length budget:** Workflow YAML output must not exceed ~120 lines per workflow file. A diagnosis writeup must not exceed 30 lines plus the failing log excerpt.

Deliverables:
- Workflow YAML (full file or a precise diff) matching the conventions below.
- For deploy work: confirmation that the five `secrets.*` are referenced, not hardcoded.
- For debugging: root-cause line, the offending step, and the minimal fix.
- Verification evidence: `actionlint` clean (if available), or the `gh run` that proves green.

# Procedure

## Step 1: Confirm the repo's workflow set

Every repo in the project ships the same standardized set. Verify it exists before adding anything new:

```
.github/workflows/
  ci.yml                  # push/PR gate
  deploy-prod.yml         # ECR build + ECS rolling deploy
  nightly.yml             # scheduled deeper checks
  version-bump.yml        # semver bump automation
  secret-scan.yml         # leaked-credential scan
  dependabot-lockfix.yml  # repair Dependabot lockfile PRs
  branch-protection.yml   # codify required checks
```

Do not invent a one-off workflow when an existing member already owns the concern (e.g. nightly audits belong in `nightly.yml`, not a new file).

**Checkpoint:** The set is complete and you are editing the correct member, not duplicating one.

## Step 2: Match the CI contract (`ci.yml`)

CI is the gate on `dev` and `main`. Triggers, permissions, concurrency, and the Node version header are fixed:

```yaml
name: CI

on:
  push:
    branches: [dev, main]
  pull_request:
    branches: [dev, main]

permissions:
  contents: read
  pull-requests: write
  checks: write
  statuses: write

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

env:
  NODE_VERSION: '20'
```

The job graph is six jobs. Five run in parallel; `summary` aggregates them:

| Job | Name | Command(s) | Notes |
|-----|------|-----------|-------|
| `type-check` | TypeScript | `npm run typecheck` | `NODE_OPTIONS: --max-old-space-size=6144` |
| `lint` | Lint | `npm run lint:check` then `npm run format:check` | ESLint + Prettier |
| `test` | Tests | `npm run test:cov -- --maxWorkers=1` | service containers; uploads coverage artifact |
| `build` | Build | `npm run build` | — |
| `security` | Security | `npm audit --audit-level high` | non-fatal-by-policy; summary marks it ⚠️ not ❌ |
| `summary` | Summary | aggregates results into `$GITHUB_STEP_SUMMARY` | `needs:` all five, `if: always()`, exits 1 if any failed |

Every job uses `actions/checkout@v4` then `actions/setup-node@v4` with `node-version: ${{ env.NODE_VERSION }}` and `cache: 'npm'`, then `npm ci`.

**Checkpoint:** Six jobs present; parallel five + summary; `cache: 'npm'` on every setup-node; `NODE_OPTIONS` 6144 on the memory-heavy jobs.

## Step 3: Wire the `test` job service containers

The `test` job needs MongoDB and Redis as GitHub service containers with healthchecks, and passes connection env to the test run:

```yaml
  test:
    runs-on: ubuntu-latest
    name: Tests
    services:
      mongodb:
        image: mongo:6.0
        ports: [27017:27017]
        options: >-
          --health-cmd "mongosh --eval 'db.runCommand({ping: 1})'"
          --health-interval 10s --health-timeout 5s --health-retries 5
      redis:
        image: redis:7-alpine
        ports: [6379:6379]
        options: >-
          --health-cmd "redis-cli ping"
          --health-interval 10s --health-timeout 5s --health-retries 5
    steps:
      # checkout + setup-node + npm ci ...
      - name: Run tests
        run: npm run test:cov -- --maxWorkers=1
        env:
          NODE_ENV: test
          NODE_OPTIONS: '--max-old-space-size=6144'
          MONGODB_URI: mongodb://localhost:27017/app_test
          REDIS_HOST: localhost
          REDIS_PORT: 6379
          JWT_SECRET: test-secret
          JWT_REFRESH_SECRET: test-refresh-secret
      - name: Upload coverage
        uses: actions/upload-artifact@v4
        if: always()
        with:
          name: coverage-${{ github.run_number }}
          path: coverage/
          retention-days: 7
```

`--maxWorkers=1` is intentional — Jest sharing one Mongo/Redis instance must run serially. Test-only secrets are inline literals (`test-secret`), never real secrets.

**Checkpoint:** Both services have healthchecks; test env points at `localhost`; coverage uploaded with `if: always()`.

## Step 4: Build the deploy contract (`deploy-prod.yml`)

Deploy is triggered by CI succeeding on `main` (via `workflow_run`), plus a manual escape hatch. It uses AWS OIDC — no static AWS keys:

```yaml
name: Build & Deploy image to ECS (prod)

on:
  workflow_dispatch:
  workflow_run:
    workflows: ['CI']
    types: [completed]
    branches: [main]

permissions:
  id-token: write    # required for AWS OIDC
  contents: read

concurrency:
  group: api-ecr-prod   # per-repo group — serialize deploys
  cancel-in-progress: true

jobs:
  build-and-deploy:
    if: ${{ github.event_name == 'workflow_dispatch' || github.event.workflow_run.conclusion == 'success' }}
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.workflow_run.head_sha || github.sha }}
      - name: Configure AWS credentials (OIDC)
        uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: ${{ secrets.AWS_ROLE_ARN }}
          aws-region: ${{ secrets.AWS_REGION }}
      - name: Login to Amazon ECR
        id: login-ecr
        uses: aws-actions/amazon-ecr-login@v2
```

The `if:` guard is mandatory — a `workflow_run` fires on **completed**, including failed CI, so the job must check `conclusion == 'success'`. The checkout `ref` must be `workflow_run.head_sha` so you deploy the exact commit CI tested, falling back to `github.sha` for manual dispatch.

**Checkpoint:** `id-token: write` present; `if:` guards on conclusion; checkout pins `head_sha`.

## Step 5: Build, tag, and push to ECR

Tag with both the commit SHA and `latest`. Image-build mechanics belong to `docker-build`; this is only the invocation:

```yaml
      - name: Build, tag, and push image to ECR
        env:
          ECR_REGISTRY: ${{ steps.login-ecr.outputs.registry }}
          ECR_REPOSITORY: ${{ secrets.ECR_REPOSITORY }}
        run: |
          set -euo pipefail
          IMAGE_URI="$ECR_REGISTRY/$ECR_REPOSITORY"
          docker build -t "$IMAGE_URI:${GITHUB_SHA}" -t "$IMAGE_URI:latest" .
          docker push "$IMAGE_URI:${GITHUB_SHA}"
          docker push "$IMAGE_URI:latest"
```

The `${GITHUB_SHA}` tag is the immutable, traceable identity; `latest` is what the ECS task definition pulls on force-new-deployment.

**Checkpoint:** Both tags pushed; `set -euo pipefail` guards the script; repo name comes from `secrets.ECR_REPOSITORY`.

## Step 6: Force the ECS deployment and poll for stability

ECS is updated imperatively, then a **custom** poll waits for stability (the built-in `aws ecs wait services-stable` tops out near 10 minutes — too short for grace periods):

```yaml
      - name: Force new deployment (ECS pulls latest)
        run: |
          set -euo pipefail
          aws ecs update-service \
            --cluster "${{ secrets.ECS_CLUSTER }}" \
            --service "${{ secrets.ECS_SERVICE }}" \
            --force-new-deployment

      - name: Wait for ECS service stability
        run: |
          set -euo pipefail
          MAX_ATTEMPTS=80; DELAY=15; ATTEMPT=0   # up to 20 min
          while [ $ATTEMPT -lt $MAX_ATTEMPTS ]; do
            ATTEMPT=$((ATTEMPT + 1))
            STATUS=$(aws ecs describe-services \
              --cluster "${{ secrets.ECS_CLUSTER }}" \
              --services "${{ secrets.ECS_SERVICE }}" \
              --query 'services[0].deployments' --output json)
            PRIMARY_COUNT=$(echo "$STATUS" | jq '[.[] | select(.status=="PRIMARY")] | .[0].runningCount // 0')
            PRIMARY_DESIRED=$(echo "$STATUS" | jq '[.[] | select(.status=="PRIMARY")] | .[0].desiredCount // 0')
            # ACTIVE (not DRAINING) non-primary deployments still serving traffic block stability
            ACTIVE_COMPETING=$(echo "$STATUS" | jq '[.[] | select(.status=="ACTIVE" and .runningCount > 0)] | length')
            echo "Attempt $ATTEMPT/$MAX_ATTEMPTS: running=$PRIMARY_COUNT/$PRIMARY_DESIRED competing=$ACTIVE_COMPETING"
            if [ "$PRIMARY_COUNT" -eq "$PRIMARY_DESIRED" ] && [ "$PRIMARY_DESIRED" -gt 0 ] && [ "$ACTIVE_COMPETING" -eq 0 ]; then
              echo "Service is stable!"; exit 0
            fi
            sleep $DELAY
          done
          echo "Service did not stabilize within $((MAX_ATTEMPTS * DELAY))s"; exit 1
```

Stability is defined as: the `PRIMARY` deployment has `runningCount == desiredCount` (and desired > 0) **and** zero `ACTIVE` non-primary deployments are still running tasks. `DRAINING` deployments are deliberately ignored — ECS is already shutting them down and they no longer take traffic.

**Checkpoint:** Poll is 80 × 15s; PRIMARY count matches desired; DRAINING ignored; ACTIVE-competing must reach 0.

## Step 7: Validate

- Run `actionlint` on the changed workflow if available; otherwise lint syntax with a YAML parser.
- For CI changes: confirm a `gh run` on a throwaway branch goes green and `summary` renders the table.
- For deploy changes: confirm the `if:` guard, the five secrets, and the poll exit codes. Never run a real deploy to validate — trace the logic.

**Checkpoint:** Lint clean; verification evidence captured; ready to return.

# Self-check

- [ ] Editing a member of the standardized set, not duplicating an existing concern into a new file.
- [ ] `ci.yml` has all six jobs; `summary` `needs:` all five and `exit 1`s on any failure.
- [ ] Every `setup-node@v4` uses `node-version: ${{ env.NODE_VERSION }}` (20) and `cache: 'npm'`; install is `npm ci`.
- [ ] `test` job has `mongo:6.0` + `redis:7-alpine` with healthchecks; `--maxWorkers=1`; coverage uploaded `if: always()`.
- [ ] `deploy-prod.yml` has `permissions: id-token: write`; the `workflow_run` job guards on `conclusion == 'success'`.
- [ ] Checkout in deploy pins `github.event.workflow_run.head_sha || github.sha`.
- [ ] Image pushed with both `${GITHUB_SHA}` and `latest`; registry/repo from `secrets.*`, not literals.
- [ ] Deploy uses `aws ecs update-service --force-new-deployment` + the 80×15s custom stability poll; DRAINING ignored.
- [ ] All five deploy secrets referenced: `AWS_ROLE_ARN`, `AWS_REGION`, `ECR_REPOSITORY`, `ECS_CLUSTER`, `ECS_SERVICE`.
- [ ] `concurrency` set with `cancel-in-progress` (per-ref for CI, per-repo group for deploy).
- [ ] No secret value hardcoded; only test-only literals inline in the `test` job.

# Common mistakes

- **Missing the `if: conclusion == 'success'` guard.** `workflow_run` fires on *completed* CI runs — including failures. Without the guard a red CI still triggers a prod deploy.
- **Deploying `github.sha` instead of `workflow_run.head_sha`.** On a `workflow_run` event `github.sha` points at the default branch tip, not the commit CI actually tested — you ship the wrong commit.
- **Forgetting `permissions: id-token: write`.** `aws-actions/configure-aws-credentials@v4` OIDC silently fails to assume the role without it.
- **Relying on `aws ecs wait services-stable`.** It times out around 10 minutes; rolling deploys with health-check grace periods need the custom 20-minute poll.
- **Treating DRAINING deployments as competing.** Counting DRAINING deployments in the stability check makes the poll never converge — only `ACTIVE` non-primary deployments with running tasks block stability.
- **Dropping `--maxWorkers=1` in the test job.** Parallel Jest workers share one Mongo/Redis service container and corrupt each other's state.
- **Omitting `cache: 'npm'` or using `npm install`.** Slows every job and breaks reproducibility; CI must use `npm ci` against the committed lockfile.
- **Hardcoding ECR/ECS identifiers.** Cluster, service, repo, region, and role must come from `secrets.*` so the same workflow ports across repos.
- **Inventing a new workflow file.** If the concern is nightly/secret-scan/version-bump, extend the existing member.

# Escalation

- **AWS role won't assume / ECR push denied / OIDC trust errors** — hand to `aws-cicd-auth`; the IAM/federation side is theirs.
- **Docker build itself fails** (layer error, build-arg, base image) — hand to `docker-build`.
- **ECS service or task definition missing** — that is infrastructure provisioning, not a workflow bug; surface to the platform owner.
- **A required secret is absent** — do not invent or inline it; ask the user to add it to repo secrets and confirm before proceeding.
- **Stability poll always times out but ECS shows healthy tasks** — likely a desired-count or health-grace mismatch on the ECS side; surface metrics rather than padding `MAX_ATTEMPTS`.

# Examples

<example title="Add an e2e job to ci.yml gated into the summary">
Input: "Add an `e2e` job to CI and include it in the pass/fail summary."

Add the job mirroring `build` (checkout → setup-node@v4 with `cache: 'npm'` → `npm ci` → `npm run test:e2e`), then extend `summary`:

```yaml
  summary:
    needs: [type-check, lint, test, build, security, e2e]
    if: always()
    # ...add E2E_RESULT: ${{ needs.e2e.result }} to env, a table row,
    # and include it in the final all-success conjunction (exit 1 on fail).
```
The summary's final `if [[ ... ]]` must include the new result or a failing e2e won't fail CI.
</example>

<example title="Diagnose: deploy ran after a failed CI">
Symptom: a red `ci.yml` on `main` still kicked off `deploy-prod.yml`.

Root cause: the `build-and-deploy` job is missing (or has a malformed) `if:` guard. `workflow_run` fires on every completed run. Fix:

```yaml
    if: ${{ github.event_name == 'workflow_dispatch' || github.event.workflow_run.conclusion == 'success' }}
```
Verify with `gh run view <id>` that the deploy job is now skipped when CI concludes `failure`.
</example>

# Failure modes

| Failure | Symptom | Recovery |
|---------|---------|----------|
| Deploy on failed CI | Prod redeploys despite red CI | Add/repair `if: ...conclusion == 'success'` guard on the job |
| Wrong commit deployed | Shipped HEAD of `main`, not the tested SHA | Checkout `${{ github.event.workflow_run.head_sha || github.sha }}` |
| OIDC role assume fails | `Error: Could not assume role` in deploy | Add `permissions: id-token: write`; auth trust → `aws-cicd-auth` |
| Stability poll never converges | Job runs full 20 min then exits 1 | Confirm DRAINING is ignored and only ACTIVE-competing blocks; check ECS desired count |
| Test job flaky/corrupt state | Intermittent Mongo/Redis test failures | Restore `--maxWorkers=1`; verify service healthchecks pass before tests run |
| Summary green despite a failure | CI reports success but a job failed | A new job wasn't added to `summary` `needs:` + final conjunction |
| Slow / non-reproducible installs | Long jobs, lockfile drift | Use `npm ci` + `cache: 'npm'` on every `setup-node@v4` |

# Related skills

- `aws-cicd-auth` — AWS OIDC trust, the assumed IAM role, ECR/ECS permissions, and the `AWS_ROLE_ARN` federation this workflow consumes.
- `docker-build` — Dockerfile authoring, build args, layer caching, and image-size mechanics behind the `docker build`/`push` steps.
- `monitoring` — Prometheus/Grafana for the services these workflows deploy; use it when a deploy needs post-rollout metric verification.
