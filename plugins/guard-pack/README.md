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
| Redaction | `session.append` | Before a row is stored or sent to the model, replaces every secret-shaped value inside a `tool_result` block with `[REDACTED:<rule>]`. Prompt rows, assistant rows and the row's other blocks are never touched. The status line counts redactions (`guard: 3 secrets redacted`). |
| Production guard | `tool.check` | Sends a call that matches a production-deploy pattern to the native ask dialog, whatever the permission mode would have allowed. A deny from beneath stays a deny. |
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

## Options (`userConfig`)

| Key | Type | Default | Effect |
|---|---|---|---|
| `isolation` | boolean | `true` | The isolation guard. Off registers no `tool.call` hook. |
| `redaction` | boolean | `true` | The redaction guard. Off registers no `session.append` hook. |
| `prodGuard` | boolean | `true` | The production guard. Off registers no `tool.check` hook. |
| `agentOffer` | boolean | `false` | Hide agents of packs this project has not enabled. A pack counts as enabled when it appears in project.json `enabledPacks` (or an `enabledPlugins` there), or in the project or local settings' `enabledPlugins`. User-level settings do not count. |
| `deferHeavyTools` | boolean | `false` | Defer MCP tools with a description over 2048 characters. |
| `extraRoots` | string | `""` | Comma-separated extra roots: absolute paths, `~/…`, or paths relative to the project directory. |

Options are stored in settings under `pluginConfigs["guard-pack"].options`,
and each one is a row in `/config`.

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
- Redaction covers the rows the session stores and sends. It does not cover
  what a tool already printed to the terminal while it ran.
