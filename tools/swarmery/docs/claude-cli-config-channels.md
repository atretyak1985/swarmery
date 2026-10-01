# Claude CLI config channels for plugin MCP servers — measured

**CLI version:** 2.1.283 (Claude Code), `~/.local/bin/claude` — re-measured with no drift (G1, G2, G3 pass; also no drift on 2.1.282, 2026-09-25); the committed baseline (`internal/channelprobe/baseline.json`) was recorded on 2.1.280
**Date measured:** 2026-09-28
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

From the installed binary, without the repo: `swarmery account doctor --probe` runs the
copy of the harness `make build` embeds (its `copy-probe` step snapshots the script and
its fixture plugin into `internal/channelprobe/script/`, gitignored), or the script at
`$SWARMERY_PROBE_SCRIPT`. The child gets a scrubbed environment (no name from any store
under `~/.swarmery/secrets/`) and a fresh temporary `CLAUDE_CONFIG_DIR`; the verdict is
saved as `~/.swarmery/probes/<cliVersion>.json` (0600, dir 0700). The daemon re-probes
only when the installed version has no verdict file yet (`SWARMERY_CHANNEL_PROBE=0`
turns that ticker off).

## The account doctor

`swarmery account doctor` answers, without the daemon and without spending a token,
whether the next session in a directory will work. Three forms:

| form | what runs | spawns |
|---|---|---|
| `--fast` | the turn-zero arms below — what the accounts-pack SessionStart preflight calls | git only: the Lock 1 `git ls-files` probe, for `Resolve` and at most once more per binding file under the estate (the pin scan and the settings-block detector share the display verdict cache) |
| bare | `--fast` plus the two findings that cost further git calls | git only |
| `--probe` | the channel probe above, then a `--fast` report | the harness (and through it `claude mcp list`) |

`--json` prints one object; `--timeout <dur>` is checked between arms (a spent budget adds
a `timeout` warn and skips the remaining arms; an arm already running finishes first);
`--no-record` leaves the first-sight ledger untouched, and a path is recorded only after its
report has been written out. Usage errors exit 2; a report with findings exits 0. No value from any
credential store ever reaches the output: every rendering passes one redaction choke
point — except where a value lies wholly inside a path the doctor resolved itself (the
project path, config dir, estate root, settings file, store, profiles — or an ancestor of
one), which keeps its spelling: such a value names the session's own working tree. Beyond the contract fields (`credentials`, `varsExpected`/`varsPresent`/
`varsMissing`, `staleDuplicates`, `findings`) the report carries `enabledPacks`,
`admission` (the line `account which` prints), `defaultProfile` (the default account's
two `.claude.json` files and which one is read), `settingsDelta` and `parity`.

Finding ids:

| id | severity | form | meaning |
|---|---|---|---|
| `vars-missing` | error | fast | an enabled pack's `.mcp.json` references a `${VAR}` this environment does not carry — the only escalation |
| `estate-unanchored` | warn | fast | the estate's store is absent, has no `# swarmery-root:` line, or its roots do not admit the estate root: no credentials and no estate settings |
| `store-rootless` | warn | fast | an account store with no root line, released to every directory bound to that account |
| `estate-settings-unusable` | error | fast | `<estateRoot>/.claude/settings.json` is malformed, not an object, carries an estate key of the wrong type, is too large, not regular, not owned, group/other-writable, hard-linked, or resolves outside the root |
| `store-refused` | warn | fast | a store in the secrets dir that the loader refuses (mode, owner, type) |
| `binding-ignored` | info | fast | a binding under the estate that the read side ignores, with its reason |
| `shadowed-pin` | info | fast | a descendant pin that overrides the estate's account |
| `first-sight` | warn | fast | first report of a path under an estate root with no projects row (once per path; ledger `~/.swarmery/doctor/estate-seen.json`) |
| `first-sight-no-db` | warn | fast | the projects index could not be read; the first-sight arm used its ledger alone |
| `probe-stale` | warn | fast | no stored channel-probe verdict for the installed CLI version (or the version cannot be read from the binary's `versions/<V>` path) |
| `probe-drift` | error | fast | the stored verdict for the installed version drifted from the embedded baseline (fact ids named) |
| `sysscan-single-account` | info | fast | the System Hub scans only `~/.claude` |
| `memory-single-account` | info | fast | the Memory surface reads only `~/.claude` |
| `binding-tracked` | warn | bare | a binding under the estate that Lock 1 ignores (git-tracked or indeterminate) |
| `estate-settings-tracked` | warn | bare | the estate's `.claude/settings.json` is git-tracked |
| `timeout` | warn | any | `--timeout` ran out; later arms were skipped |
