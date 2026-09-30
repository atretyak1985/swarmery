# review-pack

Pull-request review for GitHub repos: `/pr-review` either reviews a PR and posts
verified inline findings as one review, or triages the reviewer comments already on
it: it verifies each against the code, fixes the valid ones on the PR branch behind the
repo's gates, rebuts the rest with `file:line` evidence, and replies in the thread.

## Requirements

- The `gh` CLI, authenticated for the org that owns the repos.
- A `prReview` block in the project's `.claude/project.json` (schema:
  `overlays/_schema/project.schema.json`, mirrored in `requirements.json`).

## Enable

```json
// .claude/settings.json
{ "enabledPlugins": { "review-pack@swarmery": true } }
```

## Configure

```json
// .claude/project.json
{
  "prReview": {
    "owner": "<org>",
    "knowledge": ".claude/pr-review.md",
    "repos": {
      "api": {
        "path": "/work/<project>/api",
        "setup": ["git submodule update --init"],
        "gates": ["go vet ./...", "go test ./..."],
        "reviewSkills": ["<project>-go-standards"],
        "notes": "handlers must pair the auth check with the scope check"
      },
      "web": {
        "path": "/work/<project>/web",
        "setup": ["npm ci"],
        "gates": ["npm run lint", "npm run build"]
      }
    }
  }
}
```

- `repos.<name>.gates` are the only proof a fix is allowed to ship. If a gate cannot
  run (no test runner yet, for example), write that in `notes` so the report says so
  instead of claiming a pass.
- `knowledge` is where everything project-specific lives that the generic procedure
  cannot know: framework-specific false-positive patterns, which migrations hold
  `UNIQUE` constraints, known CI flake signatures. The command reads it in full.

## Commands

| Command | What it does |
|---|---|
| `/pr-review <pr-url \| repo#number> [review\|triage]` | Review a PR or triage its comments. The mode is auto-detected when omitted. |

## Safety

- It never touches the main checkouts. All work happens in a throwaway worktree under
  `worktreeRoot`.
- It pushes nothing and posts no reply until every gate is green.
- It replies in threads, never with top-level comments, and skips threads that already
  have an answer.
- It never approves or requests changes unless explicitly asked for a verdict.
