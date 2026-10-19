# guard-pack

Guards for running many projects on one machine, written as a **mod**: one
hooks module of function hooks (`hooks/hooks.json` → `hooks/register.ts`), not
shell scripts. It needs no daemon. Each session reads its policy from the
project's own `.claude/project.json` through `$.fs` when it starts. When a hook
cannot judge, it falls through to the next hook, so a broken guard never
blocks a session.

| Guard | Hook | What it does |
|---|---|---|
| Isolation | `tool.call` | Denies `Read`, `Edit`, `Write`, `MultiEdit`, `NotebookEdit`, `Glob` and `Grep` on a path outside the project's roots. In `Bash` it denies `rm`/`mv`/`cp` of an absolute path outside the roots, an output redirect (`>`, `>>`, `&>`) to one, and `cd` into one. The deny reason is one line naming the path, the first roots, and the `extraRoots` option. |
| Redaction | `session.append` | Before Claude sees a tool result, replaces every secret-shaped value inside its `tool_result` block with `[REDACTED:<rule>]`. The raw output may still be kept in the local transcript (see Limits). Prompt rows, assistant rows and the row's other blocks are never touched. The status line counts redactions (`guard: 3 secrets redacted`). |
| Production guard | `tool.check` | Sends a call that matches a production-deploy pattern to the native ask dialog, even where the permission rules would have allowed it. In `auto`, `dontAsk` and `bypassPermissions` mode, where no dialog would reach a person, it refuses the call instead, and the reason tells the operator to run it from a session in `default` mode. A deny from beneath stays a deny. See "Permission mode" below. |
| Agent offer | `agent.offer` | Hides `<name>-pack` agents of packs this project has not enabled from the model. Off by default. |
| Heavy-tool deferral | `tool.describe` | Puts an MCP tool whose description is longer than 2048 characters behind ToolSearch. Off by default. |

## Roots

These are read on `session.start`, which fires again on every reload:

- the session root and cwd (`$.session.root()`, `$.session.cwd()`);
- with a `.claude/project.json`: the directory that holds it, plus every
  `repos[]` entry, resolved relative to that directory (`"../shared-lib"` works);
- `<AGENT_WORKSPACE_ROOT>/<AGENT_PROJECT>` when both are set (the project's
  `name` stands in for an unset `AGENT_PROJECT`);
- `~/.claude`, `/tmp`, `/private/tmp`, `$TMPDIR` (and its `/private` spelling);
- `extraRoots`.

With no `project.json` the guard still runs, with the session root,
`~/.claude` and the temp dirs as its roots.

## Patterns

- `hooks/patterns/prod.json` holds the default production-deploy globs. It has
  the same strings, in the same order, as the daemon's
  `DefaultProdDeployPatterns`. The project's own
  `approvals.prodDeployPatterns` from `project.json` are added to them. The
  syntax is `Tool` or `Tool(argGlob)`: the tool name must match exactly, the
  glob is matched case-insensitively against the tool's argument string
  (`command` for Bash), and `*` matches any run of characters.
- `hooks/patterns/secrets.json` holds the redaction rules, ported from the
  daemon's response-layer redaction. Each rule has a stable `name` (the
  `<rule>` in the mark), a JavaScript `pattern`, optional `flags`, and an
  optional `replace` template (`$1` keeps a captured prefix such as
  `Bearer `, and `{mark}` is the mark itself).

A hooks module can import only code, so the runtime reads both files through
`$.fs`. The tests run with no file system, so `hooks/testkit.ts` carries a
verbatim copy of each file between `// BEGIN mirror:` and `// END mirror:`
markers. The daemon's parity tests (`go test ./internal/approvals
./internal/api -run Parity` in `tools/swarmery`) compare the JSON files with
the Go lists and fail when a testkit copy differs from its file, so a pattern
edit means editing the Go list, the JSON file and the testkit copy together.

## Permission mode

`tool.check` does not say which permission mode the session is in, but every
classic (settings) hook event except `PreToolUse` does, as `permission_mode`.
With `prodGuard` on, guard-pack hooks `classic.SessionStart`,
`classic.UserPromptSubmit`, `classic.PostToolUse` and
`classic.PostToolUseFailure`. These hooks only record the mode and pass the
event on unchanged. The mode is kept per loop: inside a subagent the event
carries `agent_id`, and a subagent that has not reported a mode yet gets the
main loop's.

| Mode | A production-deploy call |
|---|---|
| `default`, `acceptEdits`, `plan` | `ask`: the native dialog in a terminal; `-p` refuses it ("Bash needs approval") |
| `auto`, `dontAsk`, `bypassPermissions` | `deny`, with a reason that sends the operator to a `default`-mode session |
| not seen yet | `ask` (as before 0.1.2) |

Observed on Claude Code 2.1.303 with `claude -p --plugin-dir plugins/guard-pack`
and core disabled: in `--permission-mode auto`, `echo deploy-to-prod-dry-run`
was refused ("Permission to use Bash denied by plugin guard-pack: … refused in
auto permission mode …"). In `default` mode it was put to approval as before.

## Options (`userConfig`)

| Key | Type | Default | Effect |
|---|---|---|---|
| `isolation` | boolean | `true` | The isolation guard. Off registers no `tool.call` hook. |
| `redaction` | boolean | `true` | The redaction guard. Off registers no `session.append` hook. |
| `prodGuard` | boolean | `true` | The production guard. Off registers no `tool.check` hook and none of the four `classic.*` hooks that record the permission mode. |
| `agentOffer` | boolean | `false` | Hide agents of packs this project has not enabled. A pack counts as enabled when it appears in project.json `enabledPacks` (or an `enabledPlugins` there), or in the project or local settings' `enabledPlugins`. User-level settings do not count. |
| `deferHeavyTools` | boolean | `false` | Defer MCP tools with a description over 2048 characters. |
| `extraRoots` | string | `""` | Comma-separated extra roots: absolute paths, `~/…`, or paths relative to the project directory. |

Set them in the plugin's config menu (`/plugin` → guard-pack → configure;
each one is also a row in `/config`), or under `pluginConfigs` in your **user** settings,
`~/.claude/settings.json`, keyed `guard-pack@swarmery`:

```json
{ "pluginConfigs": { "guard-pack@swarmery": { "options": { "agentOffer": true } } } }
```

Claude Code reads plugin options from user settings (and from the `--settings`
flag) only. A `pluginConfigs` block in a project's `.claude/settings.json` or
`.claude/settings.local.json` is ignored without a warning. Loaded through
`--plugin-dir`, the key is `guard-pack@inline`.

## Enable

Install it from the swarmery marketplace:

```text
/plugin install guard-pack@swarmery
```

Or enable it for one project, in that project's `.claude/settings.json`:

```json
{ "enabledPlugins": { "guard-pack@swarmery": true } }
```

For one session from a checkout: `claude --plugin-dir plugins/guard-pack`.

Checks:

```bash
claude plugin validate --strict --json plugins/guard-pack
claude plugin test plugins/guard-pack
```

## Relationship to the other guards

Everything here is additive, and nothing depends on core or on the daemon:

- core's shell hooks still run. `protect-sensitive-files.sh` decides which
  files must never be edited inside a project; guard-pack decides which
  directories belong to the project at all. `prod-deploy-guard.sh` asks on
  the same default patterns, and asking twice changes nothing.
- the daemon's prod guard keeps marking those calls as local-only approvals.
  guard-pack makes sure the native dialog appears even in a session the daemon
  does not watch.

## Limits

- **Paths are lexical.** `~`, `.`, `..` and repeated `/` are folded, and no
  symlink is resolved. A symlink inside a root that points outside it is
  allowed. A path outside spelled through a symlink into a root (other than
  `/tmp` and `/var` → `/private/…`, which are listed both ways) is denied, so
  add it to `extraRoots`.
- **Comparison ignores case.** `/Work/x` and `/work/x` are the same path on a
  case-insensitive volume. On a case-sensitive one this lets a differently
  cased sibling directory through.
- **Bash is a heuristic, not a shell parser.** Only literal absolute paths
  (`/…`, `~`, `~/…`) count. A word holding `$` or a backtick, a relative path,
  `eval`, and paths built at runtime all pass. Of the commands, only `rm`,
  `mv`, `cp`, output redirects and `cd` are judged. Reading from outside with
  `cat` or `ls` is not blocked. A false deny costs more than a miss.
- Hooks that run before `session.start` has been read let every call through.
- **The production guard knows the mode only after a classic event.** An
  `ask` in `auto` mode is settled by the classifier with no dialog (observed
  on Claude Code 2.1.296, interactive and `-p`), so guard-pack refuses there
  instead. Before the first `SessionStart` or `UserPromptSubmit` has been
  seen, the mode is unknown and the guard asks. A mode switched (shift+tab)
  in the middle of a turn is picked up at the next tool result, so the call
  right after the switch is judged by the previous mode. Where an
  organization manages Claude Code, the `classic.*` hooks of a plugin a
  person installed do not run (`claude plugin validate` says so). The mode
  then stays unknown and the guard asks in every mode, as 0.1.1 did. If
  production calls must stop in `auto` mode in that setup, add a permission
  deny rule for them.
- **Redaction hides secrets from Claude, not from disk.** It rewrites the
  `tool_result` blocks Claude sees. The raw output may still be kept in the
  local transcript: observed on Claude Code 2.1.296, the row's structured
  `toolUseResult` field is not rewritten, so a Bash result's `stdout` is
  still in the stored transcript JSONL and in `--output-format stream-json`
  as `tool_use_result`. It does not cover what a tool already printed to the
  terminal while it ran, and the prompt and the tool call's own input are
  never rewritten either.
