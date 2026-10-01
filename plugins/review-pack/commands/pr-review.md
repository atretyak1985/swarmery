---
description: Review a GitHub PR (post verified inline findings) or triage its reviewer comments (verify → fix or rebut → reply in-thread), for any repo declared in the project's prReview config.
allowed-tools:
  - Bash
  - Read
  - Grep
  - Glob
  - Edit
  - Write
docs:
  status: generated
  updated: 2026-09-30
---

# /pr-review — review a PR, or triage the comments on it

Arguments: `$ARGUMENTS` — `<pr-url | repo#number> [review|triage]`.

Two modes, one procedure family:

- **triage** — there are reviewer comments (a bot or a human) on the PR. Verify each
  one against the real code, fix the valid ones on the PR branch, and reply in the
  comment thread with a verdict backed by evidence.
- **review** — no comments to triage (or explicitly requested). Review the PR diff
  yourself and post verified findings as one review with inline comments.

## Step 0 — load the project's review config

Read `${CLAUDE_PROJECT_DIR}/.claude/project.json` → `prReview`:

- `owner` — the GitHub org or user that owns the repos.
- `worktreeRoot` — where throwaway worktrees go (default: a `pr-review/` dir under
  `$AGENT_WORKSPACE_ROOT`, else `$TMPDIR`).
- `repos.<name>` — per repo: `path` (the local main checkout), `setup` (commands to run
  in a fresh worktree), `gates` (commands that must pass before anything is pushed),
  `reviewSkills` (skills to load before a review), `notes` (free-text quirks).
- `knowledge` — optional path to a project-local markdown file with verification
  heuristics, known CI flake signatures and anything else a reviewer must know. Read it
  in full when present; it overrides nothing below, it adds to it.

Stop with a clear message if `prReview` is missing or the PR's repo is not under
`repos` — never guess a gate. Resolve `<repo>` from the PR URL or `repo#number`.

**Mode auto-detection** when not given: if
`gh api repos/<owner>/<repo>/pulls/<n>/comments --paginate` returns unresolved
top-level comments (`in_reply_to_id == null`) authored by someone other than the PR
author that have no reply yet → `triage`; otherwise → `review`.

## Non-negotiable rules

1. **Verify before believing — in both directions.** Never implement a reviewer
   suggestion and never post a finding without checking it against the code first.
   No "You're absolutely right", no thanks, no praise. A wrong rebuttal is worse than
   a fixed non-bug, so the evidence bar for "INVALID" is a concrete guard, constraint,
   or test — cited as `file:line`.
2. **Never touch the main checkouts.** `repos.<name>.path` may hold the user's WIP on
   another branch. All work happens in a throwaway worktree.
3. **Thread replies, not top-level comments.** Replies go to
   `gh api repos/<owner>/<repo>/pulls/<n>/comments/<id>/replies -f body=...`.
   A top-level `gh pr comment` breaks the thread and a review bot re-flags the finding.
4. **Idempotency.** Before replying, list the thread's existing replies; if the PR
   author (or this tool) already answered a comment, skip it.
5. **Scope discipline.** A real problem that predates the PR (visible via
   `git diff origin/<base>...HEAD`) is *mentioned* in the reply
   ("predates this PR, left for a separate cleanup") — not fixed in the PR.
6. **Nothing is pushed and no reply is posted until the repo's `gates` are green.**
   A gate that cannot run (no test runner configured, say) is reported as exactly
   that — never as "tests pass".

## Step 1 — resolve the PR and build a workspace

```bash
gh pr view <n> --repo <owner>/<repo> --json headRefName,baseRefName,title,state,author
git -C <repos.<repo>.path> fetch origin '<headRefName>'   # ALWAYS quote: branch names may contain '#'
git -C <repos.<repo>.path> worktree add <worktreeRoot>/<repo>-pr<n> FETCH_HEAD
```

Then run every `repos.<repo>.setup` command inside the worktree (submodules, dependency
installs — worktrees share neither), and fetch the base branch for scoping:
`git fetch origin <baseRefName>`.

## Step 2 (triage) — fetch and verify every comment

```bash
gh api repos/<owner>/<repo>/pulls/<n>/comments --paginate \
  --jq '.[] | {id, user: .user.login, path, line, in_reply_to_id, body}'
```

Thread roots have `in_reply_to_id == null`.

For **each** comment, verify the claim with the checks that catch false positives:

- **"X can happen" (lifecycle / ordering claims):** read the *caller* or parent — a
  guard around the call site often rules out whole classes of "runs before its input
  is ready" claims.
- **"IDs / keys can collide":** check the schema or migration for a `UNIQUE` constraint
  before accepting.
- **"Invalid input reaches the backend":** read the server-side validation — the claim
  is often already covered there, and only the UX round-trip is real (verdict PARTIAL:
  fix the UX, rebut the data-integrity half).
- **"Inconsistent with sibling code":** open the named sibling and confirm the pattern
  actually differs; then prefer matching it (consistency comments are usually cheap and
  right).
- **"Status code / return value wrong":** check whether the offending line is new
  (`git diff origin/<base>...HEAD -- <file>`) or pre-existing before fixing it here.
- **"Two queries / code paths drifted":** compare the two named places directly; if a
  fix changes a shape that a test pins, update that test in the same commit.

Apply the project's own heuristics from `knowledge` on top of these.

Verdict per comment: **VALID** (fix it), **PARTIAL** (fix the real half, rebut the
rest), **INVALID** (rebut, change nothing).

## Step 3 (triage) — fix, gate, push, reply

Apply all fixes in the worktree, run every command in `repos.<repo>.gates`, and only
when all are green: commit once, conventional style, with a body listing each addressed
finding, then

```bash
git push origin 'HEAD:<headRefName>'      # worktree is on a detached FETCH_HEAD; quote the '#'
```

Reply to every thread, only after the push so the SHA is real:

- VALID → `Fixed in <sha> — <one line on what changed>.`
- PARTIAL → `Partially applied in <sha>. <which half didn't hold + evidence>. <what was fixed>.`
- INVALID → `<evidence: file:line / constraint / guard>. No change made.`

### Reply style — plain and short

The evidence bar in rule 1 is about *what* a reply must contain, not how densely it
may be written. Write the way you would explain the point to a colleague out loud:

- Verdict first, in a short plain sentence: fixed, not fixed, or why not.
- One idea per sentence. Put evidence on its own line or in a short list instead of
  chaining clauses with dashes and semicolons.
- Keep every `file:line` citation and gate result — they make a rebuttal credible.
- Cut ceremony. The template lines above are a skeleton, not a required phrasing.
- Aim for the shortest version a reviewer can act on without re-reading.

Dense (avoid):

> Correct on the facts, and intentional — here is the callout. The resolved config now reports the flag as on for the app build. The override was dropped as dead config under the stricter parent setting, and it is inert here: the emitted output is already in that mode by specification. Its only other effect produces nothing: lint and build are both green on this branch. Left removed. No code change made.

Plain (prefer):

> Yes, intentional.
>
> The stricter parent setting already implies this flag, so the separate override was redundant.
>
> It makes no difference to the output, and it surfaced no errors — lint and build are green.
>
> Left removed.

## Step 2′ (review) — review the diff yourself

1. `gh pr diff <n> --repo <owner>/<repo>` for the change surface; read every touched
   file *in the worktree* with full context — never review from the diff hunks alone.
2. Load every skill named in `repos.<repo>.reviewSkills` before judging the code.
3. Verify each candidate finding with the same rigor as Step 2 — codebase reality, not
   plausibility. Drop anything you cannot back with `file:line` evidence.
4. Post all surviving findings as **one** review (not N separate comments):

```bash
gh api repos/<owner>/<repo>/pulls/<n>/reviews -f event=COMMENT \
  -f body='<one-paragraph summary>' \
  -f 'comments[][path]=<path>' -F 'comments[][line]=<line>' \
  -f 'comments[][side]=RIGHT' -f 'comments[][body]=<finding>'
```

Never `APPROVE` or `REQUEST_CHANGES` unless the user explicitly asked for a verdict.

## Step 3½ — if CI fails after your push

Do not assume guilt and do not assume innocence — establish which, in this order:

1. **Before/after contrast:** `gh run list --branch '<headRefName>' --json databaseId,conclusion,headSha`
   — was the same workflow green on the commit just before yours? If it was already
   red, stop; it's not yours.
2. **Find the first real failure, not the cascade.** Download the failing job log
   (`gh run view --job <id> --log`) and locate the *first* ERROR/FAILED; a burst of
   setup/teardown errors after one point means the stack broke there — diagnose that
   point only.
3. **Does the failing job even execute your code?** A test group that never touches
   what you changed cannot be failing because of it. Conversely, find the job that DOES
   run the tests covering your change and check it passed on your exact SHA — that is
   positive evidence; cite it.
4. **Known flake signatures** are listed in the project's `knowledge` file. Match the
   first real failure against them.
5. Only after 1–4 point at a flake: `gh run rerun <run-id> --failed`, then watch the
   result with a Monitor poll loop (not manual sleeps). If it fails the same way twice on
   your SHA, treat it as real regardless of how flaky it looks.

## Step 4 — report

End with a table — one row per comment/finding:

| # | File | Claim (short) | Verdict | Action |
|---|---|---|---|---|

plus: commit SHA(s) pushed, gates run with PASS/FAIL each, worktree path (left in place
for follow-ups; remove with `git worktree remove` when the PR merges).

# How to use

## What it does

This command handles both sides of a GitHub pull request review. In review mode it reads the PR in a throwaway worktree, checks every candidate finding against the real code, and posts the ones that survive as a single review with inline comments. In triage mode it takes the comments reviewers already left, decides for each one whether it is valid, partly valid or wrong, fixes the valid ones on the PR branch after the project's gates pass, and answers every thread with the evidence.

## When to use it

- A PR in one of the project's declared repos has review comments waiting, from a bot or a colleague, and you want each one checked and answered.
- You want a second, evidence-backed review of a PR before a human looks at it.
- You pushed a fix and CI went red, and you need to know whether your change caused it.

## When not to use it

- The repo is not listed under `prReview.repos` in the project's config — declare it first; the command refuses to guess gates.
- You want a verdict (approve or request changes) — the command only comments unless you ask for a verdict explicitly.
- You want a review of uncommitted local changes rather than a pushed PR — use a local diff review instead.

## How to invoke

```
/pr-review <pr-url | repo#number> [review|triage]
```

Leave the mode out and the command picks triage when there are unanswered reviewer comments, review otherwise.

## Inputs

- **PR reference** — a full PR URL, or `repo#number` for a repo under `prReview.repos` — required.
- **mode** — `review` or `triage` — optional; auto-detected when omitted.
- **project config** — `prReview` in `.claude/project.json`: `owner`, `repos` with `path`, `setup`, `gates`, `reviewSkills`, `notes`, and an optional `knowledge` file.

## What you get back

A table with one row per comment or finding: file, the claim in short, the verdict and what was done. Below it, the commit SHAs that were pushed, each gate with PASS or FAIL, and the worktree path, which is left in place for follow-ups.

## Worked example

```
/pr-review <owner>/api#482 triage
```

The PR has three bot comments. The command builds a worktree for `api`, runs the declared setup, and checks each comment: one claims two IDs can collide, but the migration has a `UNIQUE` constraint, so it is INVALID; one points at a missing input check that is real, so it is VALID; one mixes a real UX gap with a wrong data-integrity claim, so it is PARTIAL. It applies the two fixes, runs the repo's gates, pushes one commit, and replies in each thread — "Fixed in a1b2c3d — …", "Partially applied in a1b2c3d …", and the constraint's `file:line` for the rebuttal.
