# Claude CLI config channels for plugin MCP servers — measured

**CLI version:** 2.1.282 (Claude Code), `~/.local/bin/claude`
**Date measured:** 2026-09-25
**Method:** `scripts/tests/cc-channel-probe.sh` — a scratch project and a scratch
`CLAUDE_CONFIG_DIR` under `mktemp -d`, deleted afterwards, and a throwaway plugin
(`scripts/tests/fixtures/cc-channel-probe/plugin`, loaded with `--plugin-dir`). No token
material, account names or operator paths are recorded here — behaviour only.

This note is the evidence base for the rules at the top of
`internal/claudeacct/secrets.go` and for the account-switch estate work that delivers
project config through `--settings`. It is the sibling of
`claude-cli-credential-behaviour.md`, with the same contract — re-run when the installed
CLI changes before trusting it again — except that this one has a script behind it and a
baseline to compare against (`internal/channelprobe/baseline.json`, embedded in the
binary).

## How it observes

The fixture plugin ships one stdio server, `/bin/echo`, whose argv carries three markers:

```
parent=${SWARMERY_PROBE_PARENT}  settingsenv=${SWARMERY_PROBE_SETTINGS_ENV}  userconfig=${user_config.probe_marker}
```

`claude mcp list` prints each server's command and args as its detail line, so which
references were substituted is readable **without a model turn, without a login and
without spending a token**. Each settings rung is populated alone, in a fresh scratch
config, with

```json
{"pluginConfigs":{"probe-pack@inline":{"options":{"probe_marker":"<rung marker>"}}},
 "env":{"SWARMERY_PROBE_SETTINGS_ENV":"<rung marker>"}}
```

in one of: **user** (`<cfg>/settings.json`), **project** (`<proj>/.claude/settings.json`),
**local** (`<proj>/.claude/settings.local.json`), **flag** (`--settings <file>`). Every
rung is measured twice: with the default setting sources and with
`--setting-sources project,local`. A plugin loaded by `--plugin-dir` is keyed
`<name>@inline` in `pluginConfigs`, and its options live under `.options` — the same shape
`claude plugin` writes for an installed plugin (`<name>@<marketplace>`).

Marking the scratch project trusted (`hasTrustDialogAccepted`) changes none of the results
below; the harness does not do it.

## G1 — `pluginConfigs` read tiers

**Method:** the `userconfig=` field of the detail line, per rung and per source set.

**Result:**

| rung | default sources | `--setting-sources project,local` |
|---|---|---|
| user | substituted | empty |
| project | empty | empty |
| local | empty | empty |
| flag (`--settings`) | substituted | substituted |

An option that is not found is substituted with the **empty string**, not left literal
(contrast G2).

**Verdict: holds.** `pluginConfigs` is read from userSettings and flagSettings only;
project- and local-scope copies are silently ignored. `--settings` is the only
project-portable channel for plugin options, and the only one that survives
`--setting-sources project,local`. policySettings (managed settings) was not measured —
it needs a system path the probe does not write.

## G2 — an unset `${VAR}` is substituted literally

**Method:** the `parent=` field with `SWARMERY_PROBE_PARENT` removed from the child's
environment, plus `--debug-file <scratch>/debug.log` searched for two fixed strings.
Positive control: the same run with the variable set in the parent environment, under the
default sources and under `--setting-sources project,local`.

**Result:**

- the detail line carries `parent=${SWARMERY_PROBE_PARENT}` verbatim;
- the debug log carries a `[WARN] Missing environment variables in plugin MCP config:
  <names>` line and an `[ERROR] Plugin MCP server error - mcp-config-invalid: …` line —
  both name the variables, never a value; neither reaches stdout or stderr;
- with the variable in the parent environment it is substituted, on both source sets
  (rule 1 of `secrets.go`).

**Verdict: holds.** The failure is detectable, but only in the debug log — invisible in
normal use. (Whether the CLI still starts a server it flagged `mcp-config-invalid` is not
observable with this fixture: `/bin/echo` exits at once either way.)

## G3 — which tiers expand a settings `env` block

**Method:** the `settingsenv=` field, per rung and per source set, with
`SWARMERY_PROBE_SETTINGS_ENV` removed from the child's environment.

**Result:**

| rung | default sources | `--setting-sources project,local` |
|---|---|---|
| user | expanded | literal |
| project | literal | literal |
| local | literal | literal |
| flag (`--settings`) | expanded | expanded |

**Verdict: holds, with one addition.** Of the three setting sources, only `user` expands
an `env` block for a plugin `.mcp.json` — the account-keyed tier. **A `--settings` file's
`env` block also expands, on every source set.** That is not a reason to put credentials
in one (a credential in a file is a credential at rest on disk, which is what the
per-account secret store exists to avoid), but it does mean "a settings file can never
carry a `${VAR}` value to a daemon child" is false wherever a seam passes `--settings`.

## How to re-run

```bash
bash scripts/tests/cc-channel-probe.sh            # report only
bash scripts/tests/cc-channel-probe.sh --json     # also writes ~/.swarmery/probes/<cliVersion>.json (0600, dir 0700)
cd tools/swarmery && go test -tags ccprobe ./internal/channelprobe/ -run TestLiveChannelProbe -v
```

Per-fact verdicts are `pass | drift | inconclusive`; exit status 1 means a fact drifted
from the baseline. A drift is a CLI behaviour change: update this note, the baseline and
the rules in `secrets.go` together, or not at all.
