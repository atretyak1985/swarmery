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

## GitHub inbox (swarmery daemon)

The same `prReview` block also feeds the swarmery daemon's **github-inbox** routine
(`tools/swarmery/config/routines/github-inbox.json`, seeded with
`swarmery routine seed --from config/routines/github-inbox.json`). Every two hours,
for every project with a `prReview` block, it reads each repo in `prReview.repos`
(`<owner>/<name>`) and keeps **one triage card per open issue or PR** that is new or
got a comment since the routine's last successful run (24 hours back on the first run):

- The card is labelled `github:<owner>/<repo>#<n>`; on the board that chip links to
  the item. The description quotes the item's opening text and its newest 10 comments
  as `<ts> @author: … — <link>` lines; older history stays on GitHub.
- A new comment rewrites the card and returns it to **triage** with a «new comment»
  marker — from `todo`, `in_review`, `done` or `archived` alike. A card the dispatcher
  is running keeps its column and only gets the text and the marker.
- Cards are drafts: they are never written to `todo`, so nothing starts on its own.
  Closed items are left as they are. Bot comments (CI and coverage reports) do not
  count as new comments.
- **Token requirement:** it reads with the GitHub token stored in the daemon's
  credential store (the project banner's *Sign in* on the dashboard), through an
  isolated `GH_CONFIG_DIR`, with GET requests only. It never uses your own `gh` login;
  a project whose host has no stored token is skipped, and the run says so.

Preview what a run would write, without writing anything:

```bash
swarmery ghinbox sync --project <id|slug> --dry-run
```

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
