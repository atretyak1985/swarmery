# Complexity routing: turning phase runs `active`

This is the runbook for switching the complexity router from `shadow` to
`active` on the **phase-run** surface, checking that the switch took, reading
what it changed, and rolling it back. The README's "Complexity routing"
section explains the router itself.

## The policy file

`config/route-policy.phaserun.json` sets three tiers only:

| Tier | Model | Effort |
|---|---|---|
| `M` | `sonnet` | `medium` |
| `L` | `opus` | `high` |
| `XL` | `opus` | `xhigh` |

Everything the file leaves out comes from the in-code `DefaultPolicy`
(`internal/route/policy.go`). That covers the weights, the tier cut-offs, tier
`S` and every playbook. `LoadPolicy` decodes the file **on top of**
`DefaultPolicy()`:

- Objects merge key by key. Naming `tiers.M.model` keeps M's default playbook.
- A ladder's `bands` list is replaced as a whole.
- An unknown key is an error.

JSON has no comments, so this note lives here instead. The picks equal today's
defaults on purpose: the file pins model and effort for M/L/XL so that a later
change to `DefaultPolicy` cannot move them silently, and so that a rollback of
one tier is a one-line edit. `TestPhaserunPolicyFileLoads` checks the file
loads with those picks and that nothing else drifts from the defaults.

`SWARMERY_ROUTE_POLICY` is **shared** with board-card dispatch
(`SWARMERY_ROUTE_DISPATCH`). Baking this path in makes dispatch load the same
file. Because the file restates the defaults, dispatch picks do not change
today; a future edit to a tier here moves that dispatch tier too.

The daemon re-reads the policy file on **every** phase-run admission. A tier
edit therefore applies from the next run with no restart. A mode change needs
`swarmery install`, because the mode is baked into the service definition.

## Enable

Do this from the primary checkout, on `main`, after the change that ships the
policy file is merged and pulled. The baked path must keep existing: if a later
checkout lacks the file, every phase run **and** every dispatch records nothing
until it is back.

```bash
cd /Volumes/Work/swarmery/tools/swarmery
SWARMERY_ROUTE_PHASERUN=active \
SWARMERY_ROUTE_POLICY=/Volumes/Work/swarmery/tools/swarmery/config/route-policy.phaserun.json \
  make install
```

With either variable set, `make install` builds, swaps the binary, and then runs
`~/.swarmery/bin/swarmery install --route-phaserun=… --route-policy=…` in place
of the plain restart. That rewrites the plist (macOS) or unit file (Linux) with
both values baked in, keeps every other baked variable, and reloads the
service. A plain `make install` only copies the binary and kickstarts the
service; it never changes the baked environment.

The same thing without `make`:

```bash
~/.swarmery/bin/swarmery install \
  --route-phaserun active \
  --route-policy /Volumes/Work/swarmery/tools/swarmery/config/route-policy.phaserun.json
```

`swarmery install` refuses before it touches anything when:

- the mode is not `off`, `shadow` or `active`. The daemon would read a typo as
  `shadow` and log one warning, so the switch would look installed and do
  nothing;
- the policy path is relative. launchd does not start the daemon in your
  checkout;
- the policy file does not load: it is missing, it is not JSON, it has an
  unknown key, or it names a model or effort the daemon cannot run.

The check runs on the value from the flag, from your shell, or carried over
from the existing definition alike. Clear a value with `--route-phaserun ""` or
`--route-policy ""`.

## Verify

The daemon does **not** log a `route: mode=active` line at startup: the mode
is read per phase run, not once. Check the baked environment and the rows
instead.

1. The service environment:

   ```bash
   launchctl print gui/$(id -u)/com.swarmery.daemon | grep ROUTE
   # Linux: systemctl --user show swarmery.service -p Environment | tr ' ' '\n' | grep ROUTE
   ```

   Both `SWARMERY_ROUTE_PHASERUN => active` and `SWARMERY_ROUTE_POLICY => …`
   must appear. `plutil -p ~/Library/LaunchAgents/com.swarmery.daemon.plist | grep ROUTE`
   shows the same values from the file on disk.

2. The build the service runs: `swarmery status` (first line) and
   `swarmery version` should name the same build.

3. The next phase run writes a row with `mode = 'active'`:

   ```bash
   sqlite3 ~/.swarmery/swarmery.db \
     "SELECT id, subject, mode, tier, pick_model, used_model, applied, won_rung, created_at
        FROM route_decisions WHERE surface = 'phaserun' ORDER BY id DESC LIMIT 5"
   ```

   A row with `mode = 'shadow'` after the switch means the service did not
   pick the new environment up. No row at all means the policy did not load;
   the daemon's error log has a `route policy` line saying why.

## What `applied` and `won_rung` mean

The route pick takes the rung just above the env/default rung and never beats
an explicit choice. For a phase run the model and effort ladders are
request → doc `**Model:**` / `**Effort:**` → **route** → env → default.

- `applied = 1`: in `active`, the router's model **or** effort won its ladder.
  In `shadow` it is always `0`.
- `won_rung` names the rung that won the **model** ladder only: `request`,
  `doc`, `route`, `env` or `default`.
- `pick_model` is the alias the router chose (`sonnet`); `used_model` is the
  full ID that ran.

So a phase whose doc carries `**Model:** opus` still runs opus in `active`:
`won_rung = 'doc'`. Its row may still be `applied = 1` if the doc set no effort
and the route effort won.

## Reading the report

```bash
curl -s 'http://127.0.0.1:7777/api/route/report?surface=phaserun&days=14' | jq
```

The same data is on Learning → Forecast honesty → routing
(`/learning?tab=honesty`).

- `rows` / `unsettled`: decisions in the window, and those whose outcome is not
  known yet (the run is still going or has not been back-filled).
- `byTier.groups[]`: per tier, `n`, `failures`, `failRate`, `meanCost`,
  `p90Cost`. A failure is an outcome of `failed`, `blocked` or `partial`, or a
  failed verification.
- `byModel` is the same, per model family that ran.
- `divergent`: rows where the pick and what ran differ, with the `pickRan`
  pairing.
- A group with fewer than `minSamples` (20) runs is hidden and counted in
  `hiddenGroups` / `hiddenRuns`.

## Fallback: compare tier M before and after directly

When the report hides tier M (fewer than 20 runs on a side), compare the raw
rows. This splits tier M phase runs by mode:

```sql
SELECT mode,
       COUNT(*)                                                    AS n,
       SUM(outcome IN ('failed','blocked','partial')
           OR verify_status = 'fail')                              AS failures,
       ROUND(AVG(cost_usd), 2)                                     AS mean_cost,
       SUM(applied)                                                AS applied,
       SUM(won_rung = 'route')                                     AS route_won_model
  FROM route_decisions
 WHERE surface = 'phaserun'
   AND tier = 'M'
   AND outcome IS NOT NULL
   AND outcome NOT IN ('superseded','deleted')
 GROUP BY mode;
```

Run it with `sqlite3 ~/.swarmery/swarmery.db '<query>'`. `outcome IS NULL`
rows are unsettled and left out, as are superseded and deleted runs, which say
nothing about how a run went. `mean_cost` averages only the rows with a known
cost.

## Rollback

Back to recording without changing picks:

```bash
~/.swarmery/bin/swarmery install --route-phaserun shadow
```

Or skip the router entirely with `--route-phaserun off`. That is identical to
the pre-router behaviour, pinned argv-for-argv by `TestRouteOffGolden`. The
policy path stays baked; clear it too with `--route-policy ""` if dispatch
should fall back to the in-code defaults.

To roll back a **single tier** instead, edit its line in
`config/route-policy.phaserun.json` in the primary checkout. The next phase run
picks it up with no restart. Remember that the edit also moves the same tier
for dispatch.
