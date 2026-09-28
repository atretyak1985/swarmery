# Getting started

Swarmery is two things that ship from one repository, and knowing which one you are
touching saves a lot of confusion later.

> [!NOTE]
> The **marketplace** is a set of Claude Code plugins — agents, skills, commands and
> hooks — that your projects install and that run inside your Claude Code sessions.
> The **control plane** is a local Go daemon with an embedded React dashboard that
> *watches* those sessions and gives you a board, plans, analytics and docs at
> `http://localhost:7777`. You can run either one without the other. They meet only
> through the files Claude Code already writes to disk.

```stats
12 | plugins in the marketplace | hot
59 | Go packages in the daemon
172 | HTTP API routes
12 | docs served at /docs
```

## What is swarmery

The marketplace ships one vendor-neutral **`core`** plugin plus eleven opt-in domain
packs (`uav-pack`, `iot-pack`, `web-pack`, `infra-pack`, `lsp-pack`,
`claude-eng-pack`, `graphify-pack`, `architecture-pack`, `jira-pack`,
`accounts-pack`, `design-pack`). Nothing project-specific is baked into the
plugins: every consumer supplies its own flavor at runtime through a
`.claude/project.json` overlay, which is why the same `core` works for a drone
fleet and a web shop.

The control plane lives in `tools/swarmery/`. It is a single binary — the React
dashboard is compiled and embedded with `go:embed`, so there is nothing to serve
separately and no second process to babysit.

## Install the control plane

```bash
git clone https://github.com/atretyak1985/swarmery.git
cd swarmery
bash scripts/install-swarmery.sh          # build the single embedded binary
./tools/swarmery/swarmery serve           # listens on :7777
```

Confirm it came up, then open the dashboard:

```bash
curl -s http://localhost:7777/api/health  # → {"status":"ok",…}
open http://localhost:7777
```

The daemon binds `127.0.0.1` and has no authentication — it is a single-user,
localhost-only tool by design. Do not expose the port.

## Bootstrap a project

Run this from the root of the project you want to onboard:

```bash
swarmery onboard <project-slug> [pack ...]
```

If the binary is not on your `PATH` yet, the pure-bash twin does the same job:

```bash
bash <swarmery-repo>/scripts/init.sh my-shop web-pack
```

Either way you get two files, and neither is overwritten if it already exists:

| File | What it carries |
|---|---|
| `.claude/settings.json` | The marketplace registration (`extraKnownMarketplaces.swarmery`), which packs are on (`enabledPlugins`), the `env` block, and permission denies for `.env` / `secrets/**` |
| `.claude/project.json` | Your flavor overlay — repos, main app, cloud, domain terms, commit scopes, enabled packs |

It also creates the workspace namespace under
`$AGENT_WORKSPACE_ROOT/<slug>/`: a `wiki/` and a `workspace/` holding
`working`, `archive`, `plans`, `specs`, `sessions`, `logs` and `metrics`. That
tree is where plans and task artifacts live — deliberately outside your code repo.

Onboarding from the dashboard also registers the directory as a project under
the slug you typed. There is one exception. If swarmery has already seen sessions
in that directory, the project **keeps its existing slug**, and the onboarding
steps say so. The slug is the project's identity: dispatch worktrees are named
after it, and sessions from those worktrees are attributed back through it, so
renaming it in place would orphan them.

**The Projects page lists onboarded projects by default.** A project counts as
onboarded when it has its own swarmery config and doesn't just sit inside another
onboarded project. That leaves out a multi-repo umbrella's sub-repos, which carry a
copied `settings.json`. `/`, `$HOME` and the onboarding roots never count as that
parent, so enabling core at user scope doesn't hide your projects. The daemon
decides this (the `onboarded` field on `GET /api/projects`).

Untick **onboarded only** to see everything. When the filter hides every project,
the page says how many are hidden and offers to untick it. The Health table
follows the same filter, and the System project stays visible. The choice is
remembered in the browser.

You never have to add the marketplace by hand: writing that registration into
`.claude/settings.json` is part of what onboarding does, so there is no separate
"add marketplace" step to run.

The overlay schema marks only three fields as required — `name`, `codePath` and
`enabledPacks` — and the skeleton leaves `TODO` markers for the ones worth
filling in:

| Field | Meaning |
|---|---|
| `name` | Kebab-case slug; also the workspace key and `AGENT_PROJECT` |
| `codePath` | Absolute path to the project's code root |
| `enabledPacks` | Domain packs this project turns on (mirrors `enabledPlugins`) |
| `repos` | Workspace-relative code paths agents are allowed to search |
| `mainApp` / `apps` | The primary app, and any siblings |
| `stack` / `domainTerms` | Technology per area, and the nouns your prompts should use |
| `commitScopes` | The scopes your conventional commits may use |

> [!WARNING]
> Plugin changes take effect in the **next** Claude Code session, not the running
> one. After onboarding, start a fresh session and accept the trust prompt for the
> swarmery marketplace.

## Run the control plane

From `tools/swarmery/`:

| Command | What it does |
|---|---|
| `make build` | Snapshot docs → vite bundle → `go:embed` → the single `./swarmery` binary |
| `make dev` | The Go daemon plus the vite dev server, which proxies `/api` to `:7777` |
| `make test` | `go vet ./...` then `go test ./...` |

Running `./swarmery serve` by hand keeps the daemon in the foreground, which is the
easiest way to watch it while you are getting your bearings. Once you want it always
on, `make install` copies the binary to `~/.swarmery/bin/swarmery` and hands it to
launchd so it starts with your machine.

The daemon needs no instrumentation, because it reads what Claude Code already
writes: JSONL session transcripts under `~/.claude/projects/`, watched with
`fsnotify` and a periodic rescan, indexed into SQLite at `~/.swarmery/swarmery.db`.
Point it elsewhere with `SWARMERY_PROJECTS_ROOTS`; change the port with
`SWARMERY_PORT`.

## Pack toggles and provisioning

You do not have to hand-edit JSON to turn a pack on. In the dashboard, open a
project and use the **plugins** card. The toggle performs merge-only surgery on
that project's `.claude/settings.json` — it writes a `settings.json.bak` first, and
it refuses to overwrite a file it cannot parse. `core` is locked on.

Enabling a pack also enqueues a **provision job** — a real row in the daemon's SQLite
database rather than state living in a goroutine. The daemon runs it in-process: it
updates the marketplace index, installs the pack, and then, for packs that declare a
generate step, produces the artifact. Today only `architecture-pack` declares one;
every other pack is install-only.

Because the job is a row, a restart never loses track of it — but it does not resume
either: anything still in flight is marked failed with `interrupted by daemon
restart`, so you can see what happened and re-run it deliberately.

> [!TIP]
> If a toggle seems to have no effect, check for a `.claude/settings.local.json`
> pinning the same plugin to the opposite value — it overrides `settings.json` in
> sessions. The dashboard returns a warning when it detects this.
>
> If the toggle is refused outright, the daemon was started without
> `SWARMERY_ONBOARD_ROOTS`. That variable is the allow-list of directories the
> dashboard may write `.claude/` files under, and writing plugin toggles counts.

## Your first session

Start a Claude Code session in the onboarded project and let it work for a few
minutes. Then look at the dashboard:

- **Sessions** — every transcript, live, with cost and token accounting.
- **Board** — cards captured from your sessions' own TODO items, waiting in Inbox.
- **Plans** — phase documents and their progress, read straight from the workspace.
- **Docs** — this page.

A captured card is a *proposal*, not a commitment. Here is the whole journey a card
can take once you accept it:

```figure card-lifecycle
```

The board guide covers each lane, the admission gates and the review exits in
detail.

## Cheat sheet

| Path | What it controls |
|---|---|
| `.claude/settings.json` | Marketplace registration, enabled plugins, env, permissions |
| `.claude/settings.local.json` | Machine-local overrides — wins over `settings.json` |
| `.claude/project.json` | The flavor overlay agents read at runtime |
| `.claude/agents/`, `.claude/templates/` | Project-local components; a name collision overrides the plugin's |
| `~/.claude/projects/` | The JSONL transcripts the daemon indexes |
| `~/.swarmery/swarmery.db` | The daemon's SQLite database |
| `~/.claude/plugins/cache` | Where enabled packs actually load from — never your checkout |

| Environment variable | Default | Meaning |
|---|---|---|
| `AGENT_PROJECT` | — | Project slug; the workspace namespace key (plugin side) |
| `AGENT_WORKSPACE_ROOT` | `$HOME/swarmery-workspace` | Workspace repo root (plugin side) |
| `SWARMERY_WORKSPACE_ROOT` | `$HOME/swarmery-workspace` | The daemon's twin of the above |
| `SWARMERY_PORT` | `7777` | Dashboard and API port |
| `SWARMERY_PROJECTS_ROOTS` | `~/.claude/projects` | Where transcripts are read from |
| `SWARMERY_ONBOARD_ROOTS` | unset | Parent dirs the dashboard may write `.claude/` under; unset disables onboarding *and* plugin toggles |
