---
name: git-commit
description: "Generate conventional commit messages when staging files for commit in any of the project's repos. Do not use for git tag messages, merge commit messages, changelog entries, or automated version bump commits."
version: "1.0.0"
owner: "agentry-core"
disable-model-invocation: true
color: teal
---

# Purpose

You generate conventional commit messages following the project's commit standards. You produce a single-line subject in `<type>(<scope>): <subject>` format, with optional body and footer. This skill covers commit message generation only, not the git operations themselves. Related skills: `deployment` (pipeline changes), `release-promotion` (promotion commits).

# When to use

- Completing an implementation task and staging files for commit
- Squashing commits and need a new summary message for the squashed result
- Reviewing a commit message for project convention compliance

# When NOT to use

- Git tag messages (use release conventions, not commit conventions)
- Merge commit messages (use MR description as the merge message)
- Changelog entries (commit format != changelog format)
- Automated version bump commits in a version-pinning repo (use the release-promotion convention)
- Reviewing git history or reading `git log` (no message generation needed)

# Required environment

- Runtime: `.claude/skills/git-commit/SKILL.md`
- Tools: none (rule-based message generation from staged diff context)
- Companion file: `.claude/skills/git-commit/examples/commit-examples.md` -- contains worked examples. NOTE: that file may contain deprecated scopes (`be`, `fe`, `helm`) from a legacy stack; always use current scopes from the project's `.claude/project.json` -> `commitScopes`.

# Inputs

- `diff: string` -- the staged diff or description of changes
- `repo: string` -- which repo the commit targets (determines scope)

# Outputs

- Format: a commit message string in conventional commit format
- Length budget: subject line max 72 characters

```
<type>(<scope>): <subject>

[optional body: blank line, then bullet list of changes]

[optional footer: BREAKING CHANGE:, Closes #N]
```

# Procedure

1. **Read the diff** -- Identify the primary change: new feature, bug fix, refactor, test, docs, CI, build, or chore.
   **Checkpoint:** Single `type` selected.

2. **Select scope** -- Map the changed files to the project's scope list (`.claude/project.json` -> `commitScopes`; illustrative defaults below). If files span multiple repos, generate one commit message per repo.
   **Checkpoint:** Scope matches the repo.

3. **Security gate** -- Check whether any staged files are likely to contain secrets: `.env`, `*.populated.yaml`, `credentials.json`, `*.key`, `*.pem`, `*secret*`. If any match: REFUSE to generate a commit message. Instruct the user to unstage those files first. Do not ask for confirmation -- the answer is always no.
   **Checkpoint:** No secret files staged.

4. **Write subject** -- Imperative mood, lowercase first word, no trailing period. Describe the user-visible change, not the file touched. Max 72 characters.
   **Checkpoint:** Subject reads as "this commit will [subject]".

5. **Add body** (if the change is non-trivial) -- Blank line after subject, then bullet list of specific changes. Each bullet starts with `-`.

6. **Add footer** (if applicable) -- `BREAKING CHANGE: <description>` for breaking changes. `Closes #N` for issue references.

7. **Verify** -- Confirm type, scope, subject, body, and footer all follow the rules below.
   **Checkpoint:** All rules pass.

## Type table

| Type | Use for | Example |
|------|---------|---------|
| `feat` | new capability | `feat(app): add mission approval page` |
| `fix` | bug fix | `fix(device): handle telemetry reconnect jitter` |
| `docs` | docs only | `docs(ci): document the staging promotion flow` |
| `refactor` | structure change, no behavior change | `refactor(app): split telemetry state adapter` |
| `test` | tests only | `test(app): cover approval failure state` |
| `ci` | pipeline/CI changes | `ci(infra): add staging deploy verification job` |
| `build` | build/package/image changes | `build(app): update container build args` |
| `perf` | performance improvement | `perf(device): reduce telemetry serialization overhead` |
| `chore` | maintenance, no behavior change | `chore(versions): refresh current and previous digests` |

## Scope table

The authoritative scope list is project-specific: read `.claude/project.json` -> `commitScopes`. Illustrative defaults:

| Scope | Meaning |
|-------|---------|
| `app` | the main app (project.json -> `mainApp`) |
| `infra` | the infrastructure repo |
| `device` | the device/edge repo (project.json -> `device`) |
| `versions` | the version-pinning repo, if the project uses one |
| `auth` | auth flow (OIDC / sessions) |
| `telemetry` | telemetry pipeline or stream handling |
| `docs` | cross-repo docs changes |
| `db` | database migrations |

### Deprecated scopes

Projects that migrated stacks may document deprecated scopes (e.g., `be`, `fe`, `helm`). Check the project's `CLAUDE.md`; never use a deprecated scope -- map it to its documented replacement.

## Subject rules

- Imperative mood ("add", not "adds" or "added")
- Lowercase first word
- No trailing period
- Mention the user-visible change, not just the file touched
- Max 72 characters

## Multi-scope commits

When a single logical change spans multiple repos, create one commit per repo with its own scope. Do not combine scopes like `feat(app,infra)`. Cross-reference with a shared description.

# Self-check before returning

- [ ] Type correctly reflects the nature of the change (feat = new capability, fix = bug fix, refactor = no behavior change)
- [ ] Scope matches the project's current scope list (no deprecated scopes)
- [ ] Subject is imperative mood, lowercase, no period, under 72 characters
- [ ] Subject describes the user-visible change, not just the file name
- [ ] Body bullets (if present) each start with `-` and describe a specific change
- [ ] `BREAKING CHANGE:` footer present if the change breaks existing behavior
- [ ] No secret files staged (`.env`, `*.populated.yaml`, `credentials.json`, key/pem files)

# Common mistakes to avoid

- DO NOT use scopes the project has deprecated -- map them to their documented replacements
- DO NOT write "Updated file X" as the subject -- describe the user-visible effect
- DO NOT combine multiple scopes in one commit message -- create separate commits per repo
- DO NOT generate commit messages when staged files include secrets (`.env`, `*.populated.yaml`, `credentials.json`, key/pem files) -- refuse and instruct the user to unstage them
- DO NOT omit `BREAKING CHANGE:` footer when the change breaks existing APIs, CLI flags, or port assignments

# What to surface to the user

- The generated commit message
- Reasoning for type and scope selection (one sentence)
- Warning if staged files include potential secrets (and refusal to generate)

# Escalation

- **Secret files detected:** Refuse to generate a commit message. Instruct the user to unstage `.env`, `*.populated.yaml`, credential, and key files first. No exceptions.
- **Ambiguous type between `fix` and `refactor`:** Ask the user: "did user-visible behavior change?" If yes, use `fix`. If no, use `refactor`.
- **Changes span more than 2 repos:** Confirm the user wants separate commits per repo.

# Examples

<example name="single-scope-feature">
```
feat(app): add real-time telemetry panel to device detail page

- Create TelemetryPanel component with SSE subscription
- Add useTelemetry hook with auto-reconnect
- Display battery, altitude, heading, and GPS fix status
```
</example>

<example name="bug-fix-with-body">
```
fix(device): prevent WebSocket reconnection loop on gateway restart

FrontendDataAggregatorHandler was creating new connections
without closing previous ones. Added connection state tracking
and cleanup in the disconnect handler.
```
</example>

<example name="ci-change">
```
ci(infra): add staging rollback verification step

- Run smoke checks after the deploy completes
- Block promotion to staging if verification fails
```
</example>

<example name="breaking-change">
```
build(infra): bump device-gateway chart to v0.3.0

- Add EXTERNAL_DEVICE_DNS env var to values.yaml
- Update NodePort range to 30100-30112

BREAKING CHANGE: NodePort base changed from 30080 to 30100
```
</example>

# Failure modes

| Failure | Detect | Fix |
|---------|--------|-----|
| Wrong scope used | Commit history shows a deprecated scope for main-app changes | Use interactive rebase to amend if not yet pushed; note for future commits |
| Subject too vague | Subject says "update code" or "fix bug" | Rewrite to describe the user-visible effect |
| Missing BREAKING CHANGE footer | Diff contains port changes, API changes, or removed exports without footer | Add `BREAKING CHANGE:` footer describing what breaks and the migration path |

# Related skills

- `deployment` -- use for pipeline YAML changes; commit messages for pipeline changes use the `ci` type with the infra scope
- `release-promotion` -- use for promotion flow; version-pin commits use the `chore(versions)` scope
