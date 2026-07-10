---
name: aws-cicd-auth
description: "Configure and review AWS auth for GitHub Actions CI/CD — OIDC role assumption, ECR push perms, ECS deploy perms, least-privilege IAM. Not for application IAM/Cognito, not for GCP."
version: "1.0.0"
owner: "swarmery-infra"
---

# Purpose

Define, configure, and review how the project's GitHub Actions workflows authenticate to AWS to push container images and trigger deployments. The pattern is **keyless OIDC** — GitHub Actions exchanges a short-lived OIDC token for temporary AWS STS credentials by assuming an IAM role. **No long-lived AWS access keys are stored anywhere** (not in repo secrets, not in `~/.aws`). The only secrets stored are non-sensitive identifiers (role ARN, region, repo/cluster/service names).

The reference implementation is the API repo's (`project.json` → `mainApp`) `.github/workflows/deploy-prod.yml`: it assumes an IAM role via `aws-actions/configure-aws-credentials@v4`, logs into **Amazon ECR**, builds/pushes the image, and forces a new **Amazon ECS** deployment.

# When to use

- Setting up AWS auth for a new repo or service in the project that needs to push to ECR / deploy to ECS from GitHub Actions.
- Reviewing an existing OIDC auth stanza (`permissions: id-token: write`, `configure-aws-credentials`, role ARN scoping).
- Authoring or auditing the IAM **trust policy** (who can assume the CI role — the `sub`/`aud` conditions).
- Authoring or auditing the IAM **permission policy** (what the CI role can do — ECR push + ECS deploy, least-privilege).
- Debugging an auth failure: `Error: Could not assume role`, `Not authorized to perform sts:AssumeRoleWithWebIdentity`, ECR `denied`, ECS `AccessDenied`.
- Adding a new repo/service to the existing OIDC pattern.

# When NOT to use

- **Overall CI/CD pipeline structure** (job/stage ordering, `workflow_run` triggers, concurrency, build matrix) — use `github-actions-cicd`.
- **Docker image build/tag/push mechanics** (Dockerfile, layer caching, tag strategy) — use `docker-build`.
- **Application-level AWS IAM**, Cognito, runtime task-role permissions the app uses at runtime, or end-user auth — out of scope (this skill is CI identity only).
- **GCP / any non-AWS cloud** — this skill is AWS-only; for GCP CI auth use `gcp-cicd-auth`.

# Required environment

- A repo with a `.github/workflows/*.yml` deploy workflow (template: the API repo's `deploy-prod.yml`).
- AWS account access to the IAM console (or `aws iam` CLI) to create/inspect the OIDC provider, role trust policy, and permission policy.
- A registered GitHub OIDC identity provider in the AWS account: `token.actions.githubusercontent.com` with audience `sts.amazonaws.com` (one per account, shared by all repos).
- GitHub repo/environment **secrets** populated: `AWS_ROLE_ARN`, `AWS_REGION`, `ECR_REPOSITORY`, `ECS_CLUSTER`, `ECS_SERVICE`.

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Target repo | Yes | `OWNER/REPO` that will run the workflow (scopes the trust policy `sub`) |
| Branch / environment | Yes | Which ref or GitHub Environment may deploy (e.g. `refs/heads/main`, `environment:production`) |
| ECR repository name | Yes | Destination ECR repo (`ECR_REPOSITORY` secret) |
| ECS cluster + service | Yes | `ECS_CLUSTER` + `ECS_SERVICE` to force-redeploy |
| AWS region | Yes | `AWS_REGION` secret (must match where ECR repo + ECS cluster live) |
| Existing role ARN | No | If extending an existing CI role rather than creating one |

# Outputs

**Length budget:** Combined IAM JSON + workflow YAML output must not exceed 120 lines. Trust policy + permission policy together should be well under 60 lines — if a policy grows beyond that, it has stopped being least-privilege.

Deliverables:
- The workflow auth stanza (`permissions:` + `configure-aws-credentials@v4` step).
- The IAM **trust policy** JSON (OIDC provider principal + `sub`/`aud` conditions).
- The IAM **permission policy** JSON (ECR push + ECS deploy, least-privilege).
- The list of GitHub secrets to set and their (non-secret) values.
- Verification evidence: `aws sts get-caller-identity` output from a workflow run showing the assumed-role ARN.

# Procedure

## Step 1: Confirm the workflow requests an OIDC token

The job MUST grant the `id-token: write` permission or `configure-aws-credentials` has no OIDC token to exchange. Keep `contents: read` for checkout.

```yaml
permissions:
  id-token: write   # REQUIRED — without this, role assumption fails
  contents: read
```

**Checkpoint:** `permissions.id-token` is `write` at the job (or workflow) level. Confirm no long-lived `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` secrets exist — OIDC replaces them entirely.

## Step 2: Configure credentials via role assumption

Use the pinned action and role ARN + region from secrets. This step performs `sts:AssumeRoleWithWebIdentity` and exports short-lived (default ~1h) credentials into the job environment.

```yaml
- name: Configure AWS credentials (OIDC)
  uses: aws-actions/configure-aws-credentials@v4
  with:
    role-to-assume: ${{ secrets.AWS_ROLE_ARN }}
    aws-region: ${{ secrets.AWS_REGION }}
```

**Checkpoint:** `role-to-assume` and `aws-region` both come from secrets (never hardcoded). No `aws-access-key-id`/`aws-secret-access-key` inputs are present.

## Step 3: Author the IAM trust policy (who may assume the role)

The role's trust policy makes the GitHub OIDC provider the principal and **scopes `sub` to the exact repo + ref (or environment)**. This is the single most security-critical line — a loose `sub` (e.g. `repo:OWNER/*:*`) lets any branch of any matching repo assume the role.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::<ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": "repo:<org>/<api-repo>:ref:refs/heads/main"
        }
      }
    }
  ]
}
```

To gate on a GitHub **Environment** instead of a branch, use:
`"token.actions.githubusercontent.com:sub": "repo:<org>/<api-repo>:environment:production"`.
To allow several refs, switch the inner operator to `StringLike` with an array of explicit patterns — never collapse to a bare `*`.

**Checkpoint:** `aud` equals `sts.amazonaws.com`. `sub` names the exact repo AND a specific ref/environment. No wildcard spans repos or all branches.

## Step 4: Author the permission policy (what the role may do)

Grant only ECR push + ECS deploy. `ecr:GetAuthorizationToken` must be on `Resource: "*"` (it is account-scoped and AWS does not support resource-level scoping for it); the ECR layer/image actions are scoped to the one repo; ECS actions are scoped to the service. **Do not attach `AdministratorAccess` or `AmazonEC2ContainerRegistryFullAccess`.**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EcrAuth",
      "Effect": "Allow",
      "Action": "ecr:GetAuthorizationToken",
      "Resource": "*"
    },
    {
      "Sid": "EcrPush",
      "Effect": "Allow",
      "Action": [
        "ecr:BatchCheckLayerAvailability",
        "ecr:InitiateLayerUpload",
        "ecr:UploadLayerPart",
        "ecr:CompleteLayerUpload",
        "ecr:PutImage"
      ],
      "Resource": "arn:aws:ecr:<REGION>:<ACCOUNT_ID>:repository/<ECR_REPOSITORY>"
    },
    {
      "Sid": "EcsDeploy",
      "Effect": "Allow",
      "Action": [
        "ecs:UpdateService",
        "ecs:DescribeServices"
      ],
      "Resource": "arn:aws:ecs:<REGION>:<ACCOUNT_ID>:service/<ECS_CLUSTER>/<ECS_SERVICE>"
    }
  ]
}
```

**Checkpoint:** Only the ECR + ECS actions above appear. `ecr:GetAuthorizationToken` is the only `Resource: "*"` entry. No `iam:*`, no `*:*`, no managed FullAccess/Admin policy attached.

## Step 5: Set GitHub secrets

Populate these as repo or environment secrets. None are sensitive AWS credentials — they are identifiers — but storing them as secrets keeps account IDs/ARNs out of the YAML.

| Secret | Example value |
|--------|---------------|
| `AWS_ROLE_ARN` | `arn:aws:iam::123456789012:role/api-deploy-role` |
| `AWS_REGION` | `eu-west-1` |
| `ECR_REPOSITORY` | `api-service` |
| `ECS_CLUSTER` | `prod-cluster` |
| `ECS_SERVICE` | `api-service` |

**Checkpoint:** `AWS_REGION` matches the region in the ECR + ECS ARNs from Step 4. `ECR_REPOSITORY` matches the repository ARN suffix.

## Step 6: Wire login, push, and deploy (reference flow)

```yaml
- name: Login to Amazon ECR
  id: login-ecr
  uses: aws-actions/amazon-ecr-login@v2

# build + docker push to $ECR_REGISTRY/$ECR_REPOSITORY  (see docker-build skill)

- name: Force new deployment (ECS pulls latest)
  run: |
    aws ecs update-service \
      --cluster "${{ secrets.ECS_CLUSTER }}" \
      --service "${{ secrets.ECS_SERVICE }}" \
      --force-new-deployment
```

**Checkpoint:** `amazon-ecr-login@v2` is the version pin. The `aws ecs update-service` cluster/service come from secrets, region is inherited from Step 2.

## Step 7: Verify the assumed identity

Add a one-off verification step (or run locally against the workflow logs) to prove which role was assumed:

```yaml
- name: Verify AWS identity
  run: aws sts get-caller-identity
```

Expect an `Arn` of the form `arn:aws:sts::<ACCOUNT_ID>:assumed-role/api-deploy-role/<session>`. If this returns an unexpected role, or fails, the trust policy or `id-token` permission is wrong.

**Checkpoint:** `get-caller-identity` returns the expected assumed-role ARN. ECR push and `ecs update-service` succeed end-to-end.

# Self-check

- [ ] Job grants `id-token: write` (and `contents: read` for checkout).
- [ ] No `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` anywhere — auth is OIDC-only.
- [ ] `configure-aws-credentials@v4` and `amazon-ecr-login@v2` are version-pinned.
- [ ] Trust policy `aud` = `sts.amazonaws.com`; `sub` names exact `repo:OWNER/REPO` + a specific ref/environment (no cross-repo or all-branch wildcard).
- [ ] Permission policy is least-privilege: ECR push actions + `ecs:UpdateService`/`ecs:DescribeServices` only; `ecr:GetAuthorizationToken` the sole `Resource:"*"`.
- [ ] No managed Admin/FullAccess policy attached to the CI role.
- [ ] `AWS_REGION` secret matches the region embedded in the ECR + ECS ARNs.
- [ ] `aws sts get-caller-identity` confirms the assumed-role ARN.

# Common mistakes

- **Forgetting `id-token: write`.** The default `GITHUB_TOKEN` cannot mint an OIDC token without it; role assumption fails with `Could not load credentials from any providers` / `Not authorized to perform sts:AssumeRoleWithWebIdentity`.
- **Loose `sub` condition.** `repo:<org>/*:*` or `repo:<org>/<api-repo>:*` lets any branch (incl. an attacker's PR branch with a malicious workflow) assume the deploy role. Scope to the exact ref or environment.
- **Region mismatch.** `AWS_REGION` differs from where the ECR repo / ECS cluster live → ECR login points at an empty registry or `RepositoryNotFoundException`, ECS update hits `ClusterNotFoundException`.
- **Over-broad permissions.** Attaching `AmazonEC2ContainerRegistryFullAccess` or admin "to make it work" defeats least-privilege and is an audit finding. Scope to the repo/service ARNs.
- **`ecr:GetAuthorizationToken` scoped to a repo ARN.** It does not support resource scoping; if you put it on the repository ARN, ECR login returns `AccessDenied`. It must be `Resource: "*"`.
- **Storing long-lived keys "as a fallback."** Defeats the entire keyless model and creates a leakable credential. Delete any `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` secrets.
- **Adding a new repo by widening an existing role's `sub`.** Prefer a dedicated role (or an explicit additional `StringLike` pattern), not a wildcard that blankets repos.

# Escalation

- **`sts:AssumeRoleWithWebIdentity` denied despite correct YAML**: confirm the account's OIDC provider `token.actions.githubusercontent.com` exists and its thumbprint/audience are current; if the provider is missing, that is an account-admin task — escalate.
- **Need a new AWS account-level OIDC provider**: requires IAM admin; surface to the platform owner with the account ID.
- **Trust-policy change widening who can deploy** (new repo, new environment, new branch): security-sensitive — get explicit sign-off and prefer `security-audit` review before merging.
- **ECS task execution/role separation questions** (the runtime task role vs the CI deploy role): that is application IAM, out of scope here — route to whoever owns the ECS task definitions.

# Examples

<example title="Add a second repo to the OIDC pattern">
Input: "The admin repo needs to deploy to its own ECS service from GitHub Actions."

1. Create a dedicated role `admin-deploy-role` (do NOT widen the API role's `sub`).
2. Trust policy `sub`: `repo:<org>/<admin-repo>:ref:refs/heads/main`, `aud`: `sts.amazonaws.com`.
3. Permission policy: same shape as Step 4, ECR repo ARN `.../admin-service`, ECS service ARN `.../prod-cluster/admin-service`.
4. Set secrets in the admin repo: `AWS_ROLE_ARN` (the new role), `AWS_REGION`, `ECR_REPOSITORY=admin-service`, `ECS_CLUSTER=prod-cluster`, `ECS_SERVICE=admin-service`.
5. Copy the `deploy-prod.yml` auth stanza unchanged; verify with `aws sts get-caller-identity`.
</example>

<example title="Review flagged an over-permissioned CI role">
Finding: the deploy role has `AmazonEC2ContainerRegistryFullAccess` + `AmazonECS_FullAccess` attached.

Fix: detach both managed policies; attach the inline least-privilege policy from Step 4 (ECR push + `ecs:UpdateService`/`ecs:DescribeServices`, scoped to the repo/service ARNs). Re-run the deploy workflow to confirm push + force-deploy still succeed, then confirm `iam:ListAttachedRolePolicies` shows only the scoped inline policy.
</example>

# Failure modes

| Failure | Symptom | Recovery |
|---------|---------|----------|
| Missing `id-token: write` | `Could not load credentials` / `Not authorized to perform sts:AssumeRoleWithWebIdentity` at the configure step | Add `permissions: id-token: write` to the job |
| Trust-policy `sub` mismatch | Role assumption denied even with `id-token: write` | Make `sub` match the actual `repo:OWNER/REPO:ref:...` / `:environment:...` of the running workflow |
| `aud` wrong/missing | Assume denied with condition failure | Set `token.actions.githubusercontent.com:aud` = `sts.amazonaws.com` |
| Region mismatch | ECR login empty / `RepositoryNotFoundException`, ECS `ClusterNotFoundException` | Align `AWS_REGION` with the region in the ECR + ECS ARNs |
| Role lacks ECR perms | `denied: ... is not authorized to perform: ecr:PutImage` on push | Add the Step 4 ECR push actions scoped to the repo ARN |
| `GetAuthorizationToken` mis-scoped | ECR login `AccessDenied` | Move `ecr:GetAuthorizationToken` to its own statement with `Resource: "*"` |
| Role lacks ECS perms | `AccessDenied` on `ecs:UpdateService` | Add `ecs:UpdateService` + `ecs:DescribeServices` scoped to the service ARN |
| OIDC provider absent | Assume fails account-wide for all repos | Create the `token.actions.githubusercontent.com` OIDC provider (IAM admin) |

# Related skills

- `github-actions-cicd` — overall workflow/pipeline structure (triggers, jobs, concurrency, build matrix). Use it for everything around the auth stanza; use this skill for the auth stanza + IAM itself.
- `docker-build` — image build, tag, and `docker push` mechanics after ECR login. This skill stops at "you can authenticate to ECR"; docker-build owns the image.
- `security-audit` — review least-privilege IAM policies and the trust-policy `sub` scoping; pair with it whenever a trust-policy change widens who can deploy.
