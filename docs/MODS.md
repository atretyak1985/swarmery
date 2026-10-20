# Mods

A **mod** is a Claude Code plugin whose `hooks/hooks.json` lists `modules`: one
TypeScript hooks module of *function hooks* (`$.…` calls, typed events), instead
of the shell scripts an ordinary hook runs. Function hooks run inside the
session, so a mod can see a turn start and end, measure the context, draw a band
above the prompt and answer a permission ask without a separate process. They
need Claude Code **2.1.287 or later**; the swarmery mods are built and tested on
**2.1.296** and checked in CI on a pinned build.

Swarmery ships two, both opt-in and both off until you enable them:

| Plugin | What it is | Needs |
|---|---|---|
| [`swarmery-link`](../plugins/swarmery-link/README.md) | Reports the session's live state to the local swarmery daemon. Opt-in options add Stop / Send prompt from the dashboard, approvals in Swarmery, swarm coordination between sessions, the plan band in the terminal and a per-turn forecast. | the swarmery daemon and its token file |
| [`guard-pack`](../plugins/guard-pack/README.md) | Keeps file tools inside the project's roots, redacts secret-shaped values in tool results before Claude sees them, puts production-deploy commands to the native ask dialog (and refuses them in `auto` mode, where no dialog would show), and can hide agents of packs the project has not enabled. | nothing: no daemon, no `core` |

The wire contract between `swarmery-link` and the daemon is
[`tools/swarmery/docs/mods-link-protocol.md`](../tools/swarmery/docs/mods-link-protocol.md).

## Enable a mod

Per project, in that project's `.claude/settings.json`:

```json
{
  "enabledPlugins": {
    "swarmery-link@swarmery": true,
    "guard-pack@swarmery": true
  }
}
```

Enable either one alone; they do not depend on each other. Or install from the
marketplace with `/plugin install swarmery-link@swarmery`. To try one for a single
session from a checkout: `claude --plugin-dir plugins/swarmery-link`.

Enabled with no options set, `swarmery-link` is a **sensor only**: it never
blocks, rewrites or answers anything. `guard-pack` enables its isolation,
redaction and production guards by default (see its `userConfig` below).

## The token file

`swarmery-link` talks only to `/api/link/*` on the loopback daemon, with a bearer
token read from `~/.swarmery/link-token` (mode `0600`). The daemon creates the file
when it starts. `swarmery link token` prints the token (creating the file if it is
missing), `--rotate` replaces it, and `--print-path` prints where it lives. The mod
reads it on every `session.start` and keeps it in memory only.

- **No file:** one log line, `swarmery-link: no token, sensor off`, and no request
  for the rest of the session. Deleting the file is a way to silence the mod.
- **A rotated token** is picked up on the next `401`: the mod re-reads the file once
  and retries once.
- `guard-pack` reads no token and makes no network request.

## Options (`userConfig`)

Options are set in the plugin's config menu (`/plugin` → the plugin → configure),
which reloads the mod with the new values, or under `pluginConfigs` in settings,
keyed `<plugin>@<marketplace>` with the values under `options`:

```json
{
  "pluginConfigs": {
    "swarmery-link@swarmery": { "options": { "control": true, "planBand": true } }
  }
}
```

Put that block in your **user** settings (`~/.claude/settings.json`), not in a
project's `.claude/settings.json` or `.claude/settings.local.json`. Claude Code reads
`pluginConfigs` only from user settings and from the `--settings` flag; a project or
local copy is ignored without a warning (measured on 2.1.283 for plugin options;
[`claude-cli-config-channels.md`](../tools/swarmery/docs/claude-cli-config-channels.md),
G1). A project under an `accounts-pack` estate gets its estate root's
`pluginConfigs` through the `--settings` bridge that pack builds. A plugin loaded
with `--plugin-dir` is keyed `<plugin>@inline`.
[`overlays/example/user-settings.snippet.json`](../overlays/example/user-settings.snippet.json)
is the same block as a file.

### swarmery-link

| Option | Default | Effect |
|---|---|---|
| `daemonUrl` | `http://127.0.0.1:7777` | The daemon's origin. Loopback `http` only (`127.0.0.1`, `[::1]`, `localhost`, any port); anything else is refused and the default is used, so the token never leaves the machine. |
| `control` | `false` | The dashboard can stop the session's running turn and send it prompts. One background loop long-polls the daemon for this session's commands. |
| `approvals` | `false` | A permission ask goes to the Swarmery approvals queue first; an `AskUserQuestion` can be answered from the dashboard. The `PermissionRequest` hook shim and the native dialog take over whenever the daemon is down or does not decide in time. |
| `swarm` | `false` | Messages between sessions are reported, each file a session starts editing is claimed in a registry its sessions share, and a line under the answer flags a file another live session holds. With `control` on, the dashboard can message a session. |
| `claimConflicts` | `off` | With `swarm` on, what happens to an `Edit`/`Write`/`MultiEdit`/`NotebookEdit` of a file another live session holds. `off`: only the line under the answer. `ask`: the native dialog asks, naming the other session, in `default`, `acceptEdits` and `plan` mode; in `auto`, `dontAsk` and `bypassPermissions`, where no dialog would reach a person, the write is refused. `deny`: refused in every mode. Bash is never checked. The mode is read from the classic hook events' `permission_mode`; until one arrives, the write is asked. |
| `planBand` | `false` | A band above the prompt shows the session's plan phase and its open criteria, `/phase` opens the checklist, interview-style questions draw as a card, and a line under the answer flags an editing turn that looks off the running phase. |
| `forecast` | `false` | One haiku call per typed turn predicts what the turn will touch; the daemon scores it against what the turn did (paths and numbers only). |

### guard-pack

| Option | Default | Effect |
|---|---|---|
| `isolation` | `true` | Deny file tools, and `rm`/`mv`/`cp`/redirect/`cd` in Bash, on paths outside the project's roots. |
| `redaction` | `true` | Replace secret-shaped values in tool results with `[REDACTED:<rule>]` before Claude sees them. The raw output may still be kept in the local transcript. |
| `prodGuard` | `true` | Send production-deploy commands to the native ask dialog, whatever the permission rules say. In `auto`, `dontAsk` and `bypassPermissions` mode, where no dialog would reach a person, refuse them instead. The mode is read from the classic hook events' `permission_mode`. |
| `agentOffer` | `false` | Hide `<pack>:<agent>` agents of packs this project has not enabled. |
| `deferHeavyTools` | `false` | Put MCP tools with a description over 2048 characters behind ToolSearch. |
| `extraRoots` | `""` | Comma-separated extra directories the isolation guard treats as inside the project. |

## What `claude plugin validate` shows

`claude plugin validate --strict --json <plugin dir>` reads the manifest and the
module's source the way the engine will and lists, per module, the hooks it
registers, the `$` calls it makes and the environment it reads. It lists
everything the source mentions, whatever the options say: an option that is off
registers fewer hooks and makes fewer calls than the table shows.

| Plugin | Hooks | `$` calls | Env reads |
|---|---|---|---|
| `swarmery-link` | `session.start`, `turn.start`, `tool.call`, `tool.check`, `tool.call{tool=AskUserQuestion}`, `classic.SessionStart`, `classic.UserPromptSubmit`, `classic.PostToolUse`, `classic.PostToolUseFailure`, `session.measure`, `turn.complete`, `session.end`, `session.send`, `session.receive`, `ui.render{component=AbovePrompt}`, `ui.render{component=Pane, requestId=?}`, `ui.render{component=AskUserQuestion}`, `command.run{command=phase}` | `$.clock`, `$.command.register`, `$.env.get`, `$.fs.read`, `$.http.fetch`, `$.model.classify`, `$.model.complete`, `$.prompt.submit`, `$.session` (`cwd`, `id`, `send`, `usage`), `$.store`, `$.turn.abort`, `$.ui` | `HOME` |
| `guard-pack` | `session.start`, `tool.call`, `session.append`, `classic.SessionStart`, `classic.UserPromptSubmit`, `classic.PostToolUse`, `classic.PostToolUseFailure`, `tool.check`, `agent.offer`, `tool.describe` | `$.env.get`, `$.fs.read`, `$.session.cwd`, `$.session.root`, `$.settings.read`, `$.ui.notice`, `$.ui.status` | `AGENT_PROJECT`, `AGENT_WORKSPACE_ROOT`, `HOME`, `TMPDIR` |

Neither writes an environment variable. Every gating hook of both carries a
`.catch` that hands the event on unchanged, so a failing hook never blocks a
session. Each plugin's README explains which option owns which hook and call.

The dashboard runs the same validator: **System → Mods** lists every installed
mod (the plugin cache, plus `CLAUDE_CODE_PLUGIN_DIRS` from the settings `env`),
re-validates a mod when its module files change, and shows its hooks, calls and
env reads with risk badges. CI runs `bash scripts/tests/mods-ci.test.sh`: strict
validate plus `claude plugin test` for every mod in this repo.

## Live-check a mod on a dev daemon

`claude plugin test` runs a mod against a test kit. To see it work in a real
session against a real daemon, without touching the daemon you use every day,
run a second daemon from a checkout with its own database and workspace:

```bash
make -C tools/swarmery build
SWARMERY_WTJANITOR=0 tools/swarmery/swarmery serve -port 7788 \
  -db <scratch>/dev.db -workspace-root <scratch>/workspace \
  -plugin-drift-interval 0
```

- `SWARMERY_WTJANITOR=0` turns off the worktree janitor, which would otherwise
  remove agent worktrees on the machine.
- `-plugin-drift-interval 0` turns off the plugin drift scanner.
- The dev daemon reuses `~/.swarmery/link-token`, so the mod authenticates to it
  with no extra step.

Point the mod at it with a scratch settings file. It turns off the installed
copies, so only the checkout's mod runs, and sets the options under the
`@inline` key a `--plugin-dir` plugin gets:

```json
{
  "enabledPlugins": {
    "swarmery-link@swarmery": false,
    "guard-pack@swarmery": false
  },
  "pluginConfigs": {
    "swarmery-link@inline": {
      "options": { "daemonUrl": "http://127.0.0.1:7788", "control": true }
    }
  }
}
```

Then run each check as a headless session in a scratch git repository, never in a
checkout:

```bash
cd <scratch>/repo
claude -p --model haiku --plugin-dir <swarmery-repo>/plugins/swarmery-link \
  --settings <scratch>/settings.json "<prompt>"
```

Record what each check showed (the API response, the dashboard state, the log
line) in the plan's phase doc as soon as it finishes, before you start the next
one. When you are done, stop the dev daemon and every session you started.

## Where it degrades

A mod is optional everywhere. The daemon still records every session from its
JSONL transcript, and where the `PermissionRequest` hook shim is installed
(README, "Route permission prompts to the dashboard"), approvals still reach the
dashboard through it.

- **No daemon, or no token file.** `swarmery-link` is silent: no token means no
  request at all, and a daemon that stops answering costs one log line and a retry
  every 2 s. No sensor hook waits on the daemon; only `session.end` waits, at most
  1.2 s. With `approvals` on, every wait is bounded and the engine's own verdict,
  the shim or the native dialog takes over. `guard-pack` does not use the daemon.
- **Headless (`claude -p`).** Every hook that does not draw fires, so the sensor,
  the command channel and approvals work in a headless run. Nothing draws: no plan
  band and no interview card. `/phase` and the drift line still work.
- **VS Code chat panel.** Not observed live. The test kit validates the band's tree
  for the `vscode` surface, but whether the panel loads the mod is an operator check
  ([`mods-format.md`](../tools/swarmery/docs/mods-format.md), M5).
- **Claude Code older than 2.1.287** predates function hooks. The mods were not
  tried on such a build; upgrade before enabling one.
- **Auto-mode classifier.** A `$.session.send` (a message to another session)
  from a session whose permission mode runs the auto-mode classifier can fail
  with "classifier gave no verdict". Relay the message from a session in another
  permission mode.

## Update a mod

Pull the new version into your user-level install:

```bash
claude plugin marketplace update swarmery
claude plugin update swarmery-link@swarmery --scope user
claude plugin update guard-pack@swarmery --scope user
```

Pass `--scope user`. Without it the CLI may update a project-scoped install that
belongs to some other checkout instead (observed 2026-10-19).
The new version loads in the next session.

If `claude plugin marketplace update swarmery` fails with
`Failed to clone … swarmery..clone/.git`, the marketplace was not refreshed. A
workaround is pending the operator's confirmation and is not documented yet.

## Disable

Set `"<plugin>@swarmery": false` under `enabledPlugins`, or turn it off in
`/plugin`. To keep `swarmery-link` enabled but switch an option off, unset it in
the config menu or remove it from `pluginConfigs`. Deleting
`~/.swarmery/link-token` silences `swarmery-link` on every project at once until the
daemon writes the file again.
