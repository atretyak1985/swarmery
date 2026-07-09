---
description: Branch-from-fresh-main boilerplate — checkout main, pull, create branch with a meaningful name, push empty so it can be protected
allowed-tools:
  - Bash
---

# /new-feature-branch — start work on fresh main

Automates the per-issue branch workflow that every operator task in this
project starts with, so the four-step recipe becomes one command.

## Usage

```
/new-feature-branch <slug-describing-the-work>
```

Examples:

- `/new-feature-branch fix/rollback-drift-check`
- `/new-feature-branch feat/maps-api-key-secret`
- `/new-feature-branch docs/runtime-maps-env-usage`
- `/new-feature-branch chore/check-chart-sync-helper`

## What it does

1. `git checkout main` — switch to the main branch.
2. `git pull origin main` — fast-forward local main.
3. `git checkout -b <slug>` — create feature branch.
4. `git push -u origin <slug>` — push empty branch so it can be
   protected in GitHub before substantive commits land.

Total: four commands replaced by one invocation.

## Branch naming convention

Prefix describes intent:

| Prefix | Use for |
|---|---|
| `feat/` | New functionality |
| `fix/` | Bug fix |
| `docs/` | Docs-only changes |
| `chore/` | Tooling / dev-experience / maintenance |
| `refactor/` | Code structure changes, no behaviour change |
| `test/` | Test-only changes |

Slug after prefix: dash-separated, lowercase, short but meaningful.

## Implementation

```bash
#!/usr/bin/env bash
set -Eeuo pipefail
slug="${1:?usage: new-feature-branch <prefix/slug>}"
git checkout main
git pull --ff-only origin main
git checkout -b "$slug"
git push -u origin "$slug"
echo "✓ Branch '$slug' created at main HEAD and pushed. Protect it in GitHub before substantive commits if desired."
```

## Prerequisites

- You are inside the target git repo's working tree.
- Working tree is clean (no uncommitted changes) OR your changes are stashed.
- `origin` remote is configured.

## Related

- The staging health command (if the project ships one, e.g. `/<envAlias>-health`) — first diagnostic after starting any deploy-related work
- Commit-message convention: see `skills/git-commit/`
