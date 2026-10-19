# Config channels — where Claude Code reads plugin settings

Cite this file from any acceptance criterion that names a settings file
(`settings.json`, `settings.local.json`, `.claude.json`, managed settings, a
`--settings` file). A criterion that puts a value in a tier the engine never
reads cannot pass, however well the code is written.

**Measured on:** Claude Code CLI 2.1.283 (no drift on 2.1.282; baseline first
recorded on 2.1.280), 2026-09-28. **Method:** a throwaway plugin loaded with
`--plugin-dir` whose stdio MCP server's argv carries three `${…}` markers, read
back from `claude mcp list` — no model turn, no login. Each settings tier was
populated alone, in a scratch config dir and a scratch project, under the
default setting sources and under `--setting-sources project,local`.

The control plane re-runs this probe per installed CLI version and flags drift.
These facts are a measurement, not a promise: when the installed CLI version is
newer than the one above and no fresh probe verdict exists, re-measure before a
plan relies on them.

## The tiers

| Tier | File |
|---|---|
| user | `<config dir>/settings.json` (`~/.claude/settings.json` by default) |
| project | `<project>/.claude/settings.json` |
| local | `<project>/.claude/settings.local.json` |
| flag | the file passed with `--settings <file>` |
| managed | the system-wide managed settings file — **not measured** |

`.claude.json` is not a settings tier: it is the CLI's own state file. Do not
write a criterion that expects plugin options or `env` to be read from it.

## G1 — `pluginConfigs` is read from user and `--settings` only

Plugin options (`pluginConfigs["<name>@<marketplace>"].options.<key>`; a plugin
loaded with `--plugin-dir` is keyed `<name>@inline`) reach `${user_config.<key>}`
only from:

| Tier | default sources | `--setting-sources project,local` |
|---|---|---|
| user | read | ignored |
| project | **ignored** | **ignored** |
| local | **ignored** | **ignored** |
| flag (`--settings`) | read | read |

The project and local copies are ignored silently. An option that is not found
is substituted with the empty string. `--settings` is the only project-portable
channel for plugin options, and the only one that survives
`--setting-sources project,local`.

## G2 — an unset `${VAR}` is substituted literally

In a plugin's MCP config, a `${VAR}` that is not in the CLI's environment stays
in the argv verbatim (`${VAR}`), not empty. The CLI notes it only in its debug
log (`--debug-file`): a warning naming the missing variables and an
`mcp-config-invalid` error — nothing on stdout or stderr. With the variable in
the parent environment it is substituted, under either source set.

## G3 — which tiers expand a settings `env` block

A settings file's `env` block reaches a plugin's `${VAR}` only from:

| Tier | default sources | `--setting-sources project,local` |
|---|---|---|
| user | expanded | literal |
| project | literal | literal |
| local | literal | literal |
| flag (`--settings`) | expanded | expanded |

A `--settings` file's `env` expands on every source set. That makes it a working
channel, not a place for credentials: a credential in a file is a credential at
rest on disk.

## Writing the criterion

- To give a session plugin options or `env` from a project, pass a `--settings`
  file; a criterion that puts them in `.claude/settings.json` or
  `.claude/settings.local.json` is unachievable.
- Name the tier and cite the fact: "options passed via `--settings` (G1,
  `resources/config-channels.md`)".
- A criterion that needs managed settings, or behaviour on a CLI version newer
  than the one measured, first gets a spike that measures it.
