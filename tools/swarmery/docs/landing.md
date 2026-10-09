# Landing a phase — operator guide

A plan phase runs on its own branch, `swarm/phase-<id>`. Landing takes that
branch off your machine: the daemon pushes it to `origin` and, if you ask,
opens a pull request (GitHub) or merge request (GitLab, gitlab.com or
self-hosted). It then follows the change request until it merges. This guide
covers the lifecycle, the three Review buttons, how the provider is chosen,
configuration, environment knobs, every refusal with its remedy, how tokens
are handled, and what is not supported yet.

Signing the daemon in to a code host (device code, pasted token, revoking) has
its own guide: [vcs-login.md](vcs-login.md).

## Lifecycle

Every phase carries a `landing` object (`GET /api/epics` → `phases[].landing`,
and the Review endpoint). Its `state` follows this path:

```
            run ends done                Push              Push + open PR/MR
   none ─────────────────────▶ ready ───────────▶ pushed ─────────────────────▶ pr_open ──(merge detected)──▶ merged
    │        (derived, never     │                   │                            │
    │         stored)            │                   │                            │
    └──────────── Return to agent… (any state except merged) ─────────────────────┘
                                          │
                                          ▼
                                      returned ──(the returned run ends)──▶ pr_open  if a PR/MR is already open
                                                                         └▶ none     otherwise
```

- **none**: nothing has been landed. **ready** is computed when the response
  is built (`landing_state = 'none'` and `run_state = 'done'`). It is never
  stored, so re-scanning a plan never needs a landing write.
- **pushed**: the branch reached `origin`. A Push from `none`, `ready` or
  `returned` lands here. A `returned` phase that already has an open PR/MR goes
  back to `pr_open`, because the push updated that same change request.
- **pr_open**: the change request exists. `prUrl`, `prNumber` and `prProvider`
  are stored, and `prStatus` (`{state, draft, ci, review, checkedAt}`) is
  refreshed by the poller or the Refresh button.
- **merged**: the poller (or a Refresh) saw the merge. `landedAt` is the merge
  time; a push never sets it. A dependent phase that refused with
  `deps-unmerged` now finds the work in the base branch and starts.
- **returned**: you sent the phase back with feedback. The run restarts on its
  own branch. When that run ends, the state goes back to `pr_open` if a change
  request is already open, and to `none` otherwise.

A change request that is **closed without merging** leaves the phase at
`pr_open` with a `closed` status chip. What happens next is up to you. The
daemon never deletes a branch, never ticks an acceptance criterion on merge,
and never changes `phasegate` (a phase's done/blocked verdict is independent
of landing).

The landing columns (`landing_state`, `pr_url`, `pr_number`, `pr_provider`,
`pr_status`, `pr_checked_at`, `landed_at`, `landing_error`) are owned by the
daemon (migration 0103). They survive plan re-scans and phase-doc renames.

## The Review tab and its three buttons

Open a plan, select a phase that has run, and choose the **Review** tab. It
shows the run branch's commits, files and diff against the commit the run
started from, the verification verdict, the landing status (CI, review and
state chips, plus **Refresh**), and three buttons. Every label comes from the
API's `terms`: "Pull Request"/"PR" on GitHub, "Merge Request"/"MR" on GitLab.

| Button | Request | Enabled when | Does |
|---|---|---|---|
| **Push** | `POST …/land {"action":"push"}` | the phase is not running and has run; state is none/ready/pushed/returned | `git push -u origin swarm/phase-<id>`, never `--force` |
| **Push + open PR/MR** (with **Draft**) | `POST …/land {"action":"pr","draft":true\|false}` | same as Push | pushes, then `gh pr create` / `glab mr create` with the rendered title and body |
| **Return to agent…** | `POST …/land {"action":"return","feedback":"…"}` | the phase is not running, has run, and is not merged | writes your note into the phase doc, marks the phase `returned`, and restarts its run on the same branch |

`…` stands for `/api/epics/{taskId}/phases/{phaseId}`.

**The change request.** The title is `<plan title>: Phase <seq> — <name>`.
The body is rendered from the phase doc: its `## Goal`, its
`## Completion Report` (or *"Completion report not written yet."*), a
*How to verify* list built from the ticked acceptance criteria, and the
trailer `Swarm-Phase: <taskId>/<phaseId>` plus `Plan: <path>`. The target
branch is `vcs.baseBranch` when set, otherwise the host's default branch.
Merge policy stays on the PR/MR page: `--squash` and
`--remove-source-branch` are never passed.

**Return to agent.** Your note goes into the workspace phase doc as a
blockquote under `## Operator feedback (YYYY-MM-DD HH:MM)`, inserted just
above `## Completion Report` (oldest note first, 20 KB cap). It ends with an
instruction to re-verify, and untick, any criterion the note invalidates. The
restarted run's prompt tells the executor to read that section first. The run
bypasses only the "blocked, nothing changed" re-run guard; dependencies and
the run budget are still enforced. If the restart is refused (no free slot,
for example), the note and the `returned` state stay, and the next **Run** of
that phase is started as the returned run.

**Board cards** have the same land path (`POST /api/board/tasks/{id}/land`,
the card's Land action). Its 422 bodies are `{error, hint, detail}` with no
`code`, kept unchanged from before the provider layer.

## Provider detection

The provider is read from `origin`. The first match wins:

1. **Config:** `vcs.provider` (`github` or `gitlab`) in `.claude/project.json`,
   overridden by `swarmery.vcs.provider` in `.claude/settings.local.json`.
2. **Well-known host:** `github.com` → GitHub, `gitlab.com` → GitLab.
3. **SSH resolution:** an SSH alias is resolved with `ssh -G <alias>` (your
   `~/.ssh/config`). The port-443 endpoints `ssh.github.com` and
   `altssh.gitlab.com` map to their canonical hosts.
4. **Self-hosted GitLab probe:** for any other host the daemon sends
   `GET https://<host>/api/v4/version`. A GitLab JSON answer (200 or 401) means
   GitLab. The probe never concludes GitHub: configure a GitHub Enterprise host
   explicitly with `vcs.provider: "github"`.
5. **Ask once:** if nothing matched, `GET /api/projects/{id}/vcs` answers
   `provider: "unknown", askProvider: true`, and the project banner asks
   *"Which service hosts `<host>`?"*. Your answer goes through
   `PUT /api/projects/{id}/vcs/provider` and is stored as
   `swarmery.vcs.provider` in `.claude/settings.local.json`. The file's
   existing permissions and other keys are kept. You are not asked again.

`GET /api/projects/{id}/vcs` is cached for 60 s per project; `?fresh=1` (the
banner's Re-check) bypasses the cache. It reports `auth.status`
(`ok | missing | expired | unknown`) and `auth.source` (`cli | store | none`).
When the remote is SSH and no API token is available, the banner says so:
pushing works, but opening a PR/MR needs a token.

## Configuration

Shared defaults go in the `vcs` block of `.claude/project.json` (schema:
`overlays/_schema/project.schema.json`). Machine-local overrides go in
`.claude/settings.local.json` under `swarmery.vcs`. **The local file wins per
key.**

```jsonc
// .claude/project.json (committed)
{ "vcs": { "provider": "auto", "baseBranch": "main" } }

// .claude/settings.local.json (this machine only; wins per key)
{ "swarmery": { "vcs": {
  "provider": "gitlab",
  "baseBranch": "develop",
  "allowPushToBase": false,
  "clientIds": { "code.example.com": "<oauth application id>" }
} } }
```

| Key | Where | Default | Effect |
|---|---|---|---|
| `provider` | `vcs.provider` / `swarmery.vcs.provider` | `auto` | Pins the provider (`github` \| `gitlab`); `auto` or empty means detect. |
| `baseBranch` | `vcs.baseBranch` / `swarmery.vcs.baseBranch` | the host's default branch | Target of the PR/MR. Also used by the push-to-base guard. |
| `allowPushToBase` | `swarmery.vcs.allowPushToBase` (also read from `vcs.allowPushToBase`) | `false` | Allows landing a run branch that *is* the base branch. Keep it local: it is a per-machine decision. |
| `forkRemote` | `vcs.forkRemote` | unset | Reserved. Any value is refused with 409 `fork-workflow-unsupported` (see Deferred). |
| `clientIds.<host>` | `swarmery.vcs.clientIds.<host>` | unset | OAuth client id for the device-code sign-in on that host. Overrides the env vars below. |

## Environment knobs

| Variable | Values | Default | Effect |
|---|---|---|---|
| `SWARMERY_LANDPOLL_INTERVAL` | Go duration (`5m`, `1h`) \| `0` | `10m` | How often the daemon reads the status of open PRs/MRs: up to 20 per tick, least recently checked first, with the first pass 30 s after boot. `0` disables the poller (Refresh still works). An invalid value falls back to `10m` with a warning. The boot log prints `landpoll: every 10m0s` or `landpoll: disabled`. |
| `SWARMERY_GITHUB_CLIENT_ID` | client id | unset | Enables the device-code sign-in for GitHub hosts (an OAuth App with Device Flow enabled). |
| `SWARMERY_GITLAB_CLIENT_ID` | application id | unset | Enables the device-code sign-in for GitLab hosts (a non-confidential application with scope `api`). |
| `SWARMERY_AGENT_SCRUB_VCS_TOKENS` | `1` \| unset | unset | See below. |

**`SWARMERY_AGENT_SCRUB_VCS_TOKENS=1`: what it covers.** Agents spawned through
`internal/runcore` lose the code-host tokens carried in their environment:
`GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, `GLAB_TOKEN`, `GITLAB_TOKEN`,
and the config-dir overrides `GH_CONFIG_DIR`, `GLAB_CONFIG_DIR`. This applies
whether those values came from the daemon's environment or from an
account/estate secret store. `runcore` spawns are board dispatch, verify,
planning, plan runs and phase runs. Without the flag, the spawn environment is
byte-for-byte unchanged.

**What it does not cover.**

- **It is not a credential boundary.** Once `GH_CONFIG_DIR`/`GLAB_CONFIG_DIR`
  are removed, `gh`/`glab` fall back to their default config
  (`~/.config/gh`, `~/.config/glab-cli`) and the system keyring. Agents
  inherit `HOME`, so an operator logged in with `gh auth login` is still
  reachable from an agent.
- **Some spawns bypass `runcore` and are not scrubbed:** routines
  (`internal/routines/runner.go`), session resume (`internal/api/resume.go`),
  the one-shot system spawns (`internal/systemspawn/systemspawn.go`), and
  provisioning (`internal/provision/runner.go`).
- **Skills lose access only when it came from the environment.** Skills that
  call `gh`/`glab` themselves lose API access only if that access came from an
  environment token. A `git push` over SSH is unaffected. A `GH_TOKEN` you put
  in an estate secret store on purpose (for an MCP server, say) is removed too.

## Refusals and their remedies

Every 409 body is `{error, code, …}`. Every landing 422 body is
`{error, code, hint, detail}`: `hint` holds the exact commands to finish the
job by hand (user values are single-quoted for the shell), and `detail` holds
the tool's own output, redacted.

### `POST …/land` and `GET …/review`

| Status | `code` | When | Remedy |
|---|---|---|---|
| 400 | — | bad JSON; `action` not push/pr/return; empty or >20 KB `feedback` on return | Fix the request. |
| 404 | — | unknown phase, or the run branch no longer exists (review) | Reload the plan. |
| 409 | `no-run-branch` | the phase never ran, so it has no branch | Run the phase first. |
| 409 | `phase-running` | the phase is still running, or its last run is still finishing (return) | Wait for the run to end. |
| 409 | `phase-merged` | Return on a merged phase | Put the follow-up in a new phase. |
| 409 | `push-to-base-refused` | the run branch is the base branch (`vcs.baseBranch`, or `origin`'s HEAD); the body names `branch` and `base` | Set `swarmery.vcs.allowPushToBase=true` in `.claude/settings.local.json` if you really mean it. |
| 409 | `fork-workflow-unsupported` | `vcs.forkRemote` is set | Remove `vcs.forkRemote` to land to `origin`, or push and open the change request by hand. |
| 409 | `no-project-path` / `no-repo-root` / `repo-outside-project` | the project has no path, its path is not a checkout, or the phase's `Repo` header points outside the project | Fix the project path or the phase doc's `Repo` header (same remedies as Run). |
| 409 | `no-start-point` | review: no recorded start point and the repo has no checked-out branch | Check out a branch in the project repo. |
| 409 | `base-unreachable` | review: the recorded start point no longer resolves (force-push or gc) | Re-run the phase so it records a new base. |
| 409 | `doc-unreadable` | return: the feedback could not be written into the phase doc | Check the doc path and its permissions. |
| 422 | `no-remote` | no `origin` remote | `git -C <repo> remote add origin <url>`, then land again. |
| 422 | `provider-unknown` | the `origin` host is not recognised | Answer the banner's question, or set `vcs.provider`. |
| 422 | `not-authenticated` | the host rejected the credentials; `landing_error` is stamped and the project's banner shows *expired* | Run the `gh`/`glab auth login --hostname <host>` from the hint, or sign in from the banner; then land again. |
| 422 | `no-push-access` | the account has no write access, or the branch is protected | Get write access, or push from an account that has it. |
| 422 | `remote-diverged` | `origin/swarm/phase-<id>` has commits this branch lacks; the push is not retried and not forced | In a checkout of the run branch: `git fetch origin && git rebase origin/swarm/phase-<id>`, then land again. |
| 422 | `binary-missing` | `git`, `gh` or `glab` is not on the daemon's PATH | Install it and restart the daemon. |
| 422 | `push-failed` | any other push failure | Read `detail`; the hint has the manual push command. |
| 422 | `change-request-failed` | the push succeeded but the PR/MR was not opened (including "already exists", or the CLI printed no URL) | Run the `gh pr create` / `glab mr create` command from the hint. |
| 503 | — | return: the phase-run service is not attached | Restart the daemon. |

A refused restart on **return** answers with the same codes as **Run**
(`already-running`, `plan-running`, `deps-unmet`, `deps-unmerged`,
`blocked-unchanged`, `no-free-run-slot`, `cannot-stack`,
`start-ref-unresolved`, `branch-*`, `doc-model-unknown`, `doc-effort-unknown`,
429 `low_quota`, 409 `account-breaker`). The body also carries `landing`, and
the remedies are the Run button's. The note stays in the doc either way.

### `POST …/landing/refresh`

| Status | `code` | When | Remedy |
|---|---|---|---|
| 404 | — | unknown phase | Reload the plan. |
| 409 | `no-change-request` | the phase has no open or merged PR/MR | Open one from the Review tab first. |
| 422 | `not-authenticated` | the host rejected the credentials (the project is marked expired) | Log in as the hint says, then Refresh. |
| 422 | `binary-missing` | `gh`/`glab`/`git` is missing | Install it and restart the daemon. |
| 422 | `no-remote` | the phase's repo has no `origin` | Add one. |
| 422 | `provider-unknown` | the host is not recognised | Set `vcs.provider`. |
| 422 | `status-failed` | any other read failure (also a database error) | Try again; the hint links the PR/MR. |

The background poller records the same classes, redacted, in `landing_error`
and does not stop on one failing PR/MR. A 401 marks the project's sign-in as
expired, so the banner comes back.

### Project VCS endpoints

| Endpoint | Status | `code` | When / remedy |
|---|---|---|---|
| `PUT …/vcs/provider` | 400 | — | `provider` is not `github`/`gitlab`. |
| | 409 | `no-project-path` | The project has no path. |
| | 409 | — | `settings.local.json` could not be written; the message names the path. |
| `POST …/vcs/login` | 400 | — | Bad body, `method` not `device`/`token`, or the token is empty or multi-line. |
| | 409 | `no-project-path` | The project has no path. |
| | 409 | `device-flow-unconfigured` | No client id for the host; the body's `hint` and `envVar` name `SWARMERY_GITHUB_CLIENT_ID`/`SWARMERY_GITLAB_CLIENT_ID` or `swarmery.vcs.clientIds.<host>`. The Token tab still works. |
| | 409 | `device-flow-disabled` | The OAuth app has Device Flow switched off. Enable it, or use a token. |
| | 422 | `no-remote` | No `origin` with a host. |
| | 422 | `provider-unknown` | Answer the banner's provider question first. |
| | 422 | `not-authenticated` | The pasted token was rejected; nothing was stored. |
| | 429 | `too-many-pending-logins` | 10 device sign-ins are already pending for the project. Finish one or let it expire. |
| | 502 | `device-flow-failed` | The host refused the client id, or did not answer. Check the id, or use a token. |
| | 502 | `token-unverified` | The token could not be checked (CLI missing, or host unreachable); nothing was stored. |
| `GET …/vcs/login/{loginId}` | 404 | `login-not-found` | The sign-in finished, expired, or was lost when the daemon restarted. Start a new one. |
| | 422 | `not-authenticated` | The host issued a token and then rejected it; it was removed. Start again, or use a token. |
| | 502 | `device-flow-failed` | As above. |
| `DELETE …/vcs/token` | 409 | `no-project-path` | The project has no path. |
| | 409 | — | The secrets file or directory has insecure permissions; the message names the path. |
| | 422 | `no-remote` | No `origin` with a host. |

`…` here stands for `/api/projects/{id}`. Every mutating landing and VCS route
also answers 403 to a cross-origin browser request (`requireLocalOrigin`).

## Tokens: storage, use, redaction, revocation

- **Default: your own CLI login.** With nothing stored, the daemon runs
  `gh`/`glab` with your existing login (`gh auth login`, `glab auth login`).
  Nothing is copied.
- **Daemon-owned token.** A device-code or pasted-token sign-in (see
  [vcs-login.md](vcs-login.md)) is written to
  `~/.swarmery/secrets/vcs-<host>.env`:
  - The file is mode `0600`, written atomically. A file or directory open to
    group or other, or a symlink, is refused on both read and write.
  - From then on, every daemon `gh`/`glab` call for that host runs with
    `GH_TOKEN` (`GH_ENTERPRISE_TOKEN` on GitHub Enterprise hosts) or
    `GITLAB_TOKEN`, plus an isolated `GH_CONFIG_DIR`/`GLAB_CONFIG_DIR`, so the
    daemon neither reads nor rewrites your own CLI config.
  - A pasted token is checked against the host before anything is written.
- **Redaction.** Every piece of tool output that leaves the provider layer
  (422 `detail`, `landing_error`, logs) is masked to `***`. That covers each
  known token shape (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`,
  `glpat-`) and every token value the daemon has loaded or written, including
  opaque OAuth tokens. Output is redacted before it is truncated, so a token
  at the cut cannot survive as a fragment. Login responses return the account
  name, never the token.
- **Revoke:** use the dashboard sign-out (`DELETE /api/projects/{id}/vcs/token`)
  or `rm ~/.swarmery/secrets/vcs-<host>.env`. The next call falls back to your
  CLI login. Also revoke the token on the host itself (GitHub → Settings →
  Applications / Tokens; GitLab → Preferences → Access tokens).

## Deferred: not supported yet

- **Fork workflow.** Pushing to a fork and opening the change request against
  upstream is not implemented. `vcs.forkRemote` is reserved, and any value is
  refused with 409 `fork-workflow-unsupported`.
- **Other hosts.** Bitbucket and Gitea have no provider. Their origins read as
  `unknown`, and the banner's question offers only GitHub or GitLab.
- **GitLab device-token refresh.** GitLab device-flow access tokens expire
  after about 2 h, and the refresh token is not stored. Use a personal access
  token (scope `api`) for GitLab until refresh lands.
- **Scrub coverage.** `SWARMERY_AGENT_SCRUB_VCS_TOKENS` does not reach
  routines, session resume, system spawns or provisioning (see above).
- **Re-opening.** A second `pr` on a phase whose change request is open answers
  422 `change-request-failed` ("already exists"). There is no idempotent
  re-open, and the UI disables Push and Push + open once a PR/MR is open.
- **Branch cleanup.** Merged run branches are not deleted automatically.
