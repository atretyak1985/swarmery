---
description: Thin entry point for `/account [list|use <key>|clear|setup-shell [--uninstall]]` — shows which Claude Code account this project runs under and switches it. Every decision lives in the `swarmery account` CLI; no run logic lives here.
allowed-tools:
  - Bash
docs:
  status: reviewed
  source_sha: 8cf56a7fd34d
  updated: 2026-08-07
---

# /account — which account does this project run under?

## Usage

```
/account                              list the accounts and mark the one this project uses
/account use <key>                    bind this project to an account
/account clear                        drop the binding (back to the default account)
/account setup-shell [--uninstall]    install/remove the `claude` shell function in your profile
```

## What it does — and does not do

This is a **thin proxy** over the `swarmery account` CLI. It parses
`$ARGUMENTS`, picks the matching command below, and reports its output. It owns
no logic of its own: what an account is, where its config dir lives, and what
the environment delta is are all answered by the CLI, so this command and the
dashboard can never give different answers.

If a rule about accounts ever appears in this file, it leaked out of the CLI and
belongs back there.

**Requires the `swarmery` CLI on PATH.** It does **not** require the daemon:
`which`, `use`, `clear`, `env` and `exec` read and write the binding file
directly. If `swarmery` is missing, say so and stop — do not guess an account.

## Commands

### `/account` (no arguments)

```bash
swarmery account list
swarmery account which --path "${CLAUDE_PROJECT_DIR:-$PWD}"
```

Report both: the table of installed accounts (key, config dir, default?,
connected?, plan) and the account this project effectively runs under. `source:
binding` means someone chose it. `source: default` means nobody did, or that the
binding file was ignored.

If `which` prints a warning on stderr that the binding was ignored, relay that line
verbatim. It names the cause. The usual one is a `settings.local.json` that git
tracks: swarmery deliberately ignores a committed binding. The fix is
`git rm --cached .claude/settings.local.json` plus a gitignore entry. Do not
suggest re-running `use`: it would write the same tracked file again.

`connected: unknown` is not `no` — it means credential resolution is switched
off (`SWARMERY_USAGE_OAUTH=0`), so the question was never asked.

### `/account use <key>`

```bash
swarmery account use "<key>" --path "${CLAUDE_PROJECT_DIR:-$PWD}"
```

Writes the binding to `.claude/settings.local.json` (machine-local, gitignored —
two people on one repo legitimately use different accounts). The CLI refuses a
key that is not installed on this machine; report that refusal as-is rather than
falling back to another account.

An **already-running session keeps its own account** — the binding decides what
the *next* session starts under. Say so instead of implying a live switch.

### `/account clear`

```bash
swarmery account clear --path "${CLAUDE_PROJECT_DIR:-$PWD}"
```

### `/account setup-shell [--uninstall]`

```bash
"${CLAUDE_PLUGIN_ROOT}/bin/install-shell-function.sh"              # install
"${CLAUDE_PLUGIN_ROOT}/bin/install-shell-function.sh" --uninstall  # remove
"${CLAUDE_PLUGIN_ROOT}/bin/install-shell-function.sh" --status     # check
```

This edits a **login profile** (`~/.zshrc` / `~/.bashrc`), so run it only when
the operator asked for it in this turn — never as a follow-up to `use`, and
never "while we're here". It backs the profile up to `<profile>.bak` before its
first write and is idempotent.

After installing, tell the operator the function applies to **new** shells
(`source ~/.zshrc` for the current one).

## Argument parsing

1. No arguments → the listing above.
2. `use` → requires exactly one following token, the account key. Missing key →
   print usage and stop; never pick an account for the operator.
3. `clear` → takes no further arguments.
4. `setup-shell` → optional `--uninstall` or `--status`. Any other flag → usage
   error, stop.
5. Anything else → usage error, stop.

## Related

- `plugins/accounts-pack/README.md` — the two shell surfaces and what each costs.
- `plugins/accounts-pack/bin/claude-account.sh` — the explicit wrapper, for
  operators who would rather not have a `claude` shell function.

# How to use

## What it does

You have more than one Claude Code account installed on this machine and want to know — or decide — which one this project's sessions run under. This command shows the installed accounts, marks the one the current project resolves to, binds the project to a different one, or clears that binding. It is a thin proxy over the `swarmery account` CLI: every answer comes from the CLI, so this command and the dashboard can never disagree.

## When to use it

- You are not sure which account the next session in this project will start under and want it stated, with its source (an explicit binding vs the machine default).
- You want this project to run under a specific account from now on — `use <key>` writes the machine-local binding.
- A binding exists that should no longer apply, and the project should fall back to the default account — `clear`.
- You want the `claude` shell function installed into your login profile so plain `claude` picks up the binding — `setup-shell`.

## When not to use it

- You want to switch the account of the session you are already in — a binding only affects the *next* session; restart instead.
- You need to install or connect a new account — that is provisioning, done from the swarmery dashboard, not from here.
- You want per-project billing or usage numbers — read them in the dashboard; this command only reports identity and binding.

## How to invoke

```
/account
/account use <key>
/account clear
/account setup-shell [--uninstall]
```

Run it with no arguments to see the accounts and the project's effective one; the subcommands change the binding or the shell profile.

## Inputs

- `<key>` — required for `use` only. The key of an account already installed on this machine; the CLI refuses unknown keys rather than guessing.
- `--uninstall` — optional for `setup-shell`. Removes the `claude` shell function instead of installing it.

## What you get back

The no-argument form prints the account table (key, config dir, default?, connected?, plan) plus the account this project effectively runs under and why. `use` and `clear` edit `.claude/settings.local.json` — machine-local and gitignored, so nothing lands in the repo. `setup-shell` edits your login profile (`~/.zshrc` / `~/.bashrc`), backing it up to `<profile>.bak` first; the function applies to new shells only.

## Worked example

```
/account use work
```

The CLI checks that `work` is installed, then writes the binding into this project's `.claude/settings.local.json`. The command reports the new binding and reminds you that the session you are in keeps its current account — the next session started in this project runs under `work`.

## Related

- `accounts-pack` README — explains the two shell surfaces (`claude` function vs explicit wrapper) and what each costs.
- `swarmery` dashboard Accounts page — prefer it for provisioning accounts and reading per-project usage; this command only reads and writes the binding.
