---
description: Deep-dive diagnostic on a failed GitLab pipeline — fetches failed job traces, pattern-matches common errors, proposes fixes
allowed-tools:
  - Bash
---

# /ci-diagnose — GitLab pipeline failure forensics

Given a failed pipeline ID (or auto-detect the most recent failed
pipeline on main), produce a diagnostic report: which jobs failed, the
full trace of each, pattern-matched root-cause hypothesis, and specific
recovery commands.

## Usage

```
/ci-diagnose                  # auto-find most recent failed pipeline on main
/ci-diagnose <pipeline-id>    # inspect a specific pipeline
/ci-diagnose -R <repo>        # non-default repo
```

## What it does

1. **Pipeline overview** — `glab ci get` to map job states.
2. **Failed-job traces** — `glab ci trace <job>` for each job with state
   `failed`. Tail of each (last ~100 lines is usually enough).
3. **Pattern match against known failure modes** (if the project keeps a
   curated failure-modes taxonomy in its docs, consult it first):

| Pattern in log | Root-cause hypothesis | Recovery |
|---|---|---|
| `[ERROR] Required secret '<app>-*' not found in namespace '<namespace>'` | Missing bootstrap secret | Run the project's bootstrap-secrets script/command |
| `Save error occurred: can't get a valid version for dependency` | Chart.yaml / Chart.lock drift | `bash scripts/check-chart-sync.sh` in the chart repo; `helm dependency update .` |
| `ERROR: remote payload exited 1` without other context | Remote deploy wrapper swallowed stderr — look at stdout in the remote-output capture | SSH to the VM and re-run the failing script manually; check the preceding `[ERROR]` lines |
| `helm rollback` failure or `--atomic` rollback triggered | Pod readiness probe failing; likely env-var missing or config broken | Check deployment env block; check pod logs |
| `FAILED_PRECONDITION: Secret Version is in DESTROYED state` | GCP Secret Manager destroyed-version | Repopulate the secret version via the project's bootstrap-secrets flow |
| `Too many authentication failures` | SSH agent crowded with keys | Ensure `IdentitiesOnly=yes -o IdentityAgent=none` in the SSH call (the project's SSH wrapper should set this) |
| `yaml Errors:` non-empty in pipeline metadata | CI YAML parse error | Open `.gitlab-ci.yml` / `ci/includes/*.yml`; `glab ci lint` locally |
| `IMAGE_DIGEST missing or malformed` | publish-metadata artefact didn't propagate | Retry build+publish; check `build.env` artefact |
| `[ERROR] Drift check` from rollback | Cluster-vs-versions drift (live digest ≠ version-pinning repo) | Run `helm rollback <release> -n <namespace>` on the VM first, then retry the staging rollback job |

4. **Output** — compact structured report:

```
=== Pipeline #<ID> on <ref> — <state> ===
Failed jobs: <list>

Job <name>:
  Exit code: <N>
  Last failure signal: <grep-matched pattern>
  Hypothesis: <mapped root cause>
  Recovery: <specific command>

Overall diagnosis: <one-line summary>
Suggested next action: <command to run>
```

## Implementation

Wraps:
- `glab ci list` (find most recent failed pipeline on main)
- `glab ci get --pipeline-id <ID>`
- `glab ci trace <job-name>`
- Regex scan against the pattern table above

For structured output, consider calling the `ci-incident-responder`
agent when multiple hypotheses need weighing.

## When to use

- Before retrying a failed pipeline (don't retry blindly)
- When the CI log shows only `remote payload exited 1` with no context
- When post-merge chain fails on main and you need to understand why fast

## Related

- `/env-check` — broader environment configuration snapshot
- `@ci-incident-responder` — multi-hypothesis agent
- `troubleshooting` skill — fuller treatment of the patterns above
