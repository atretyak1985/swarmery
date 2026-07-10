---
name: mission-creation
description: "Use this skill to create and drive a UAV mission end-to-end through the web UI with the Playwright MCP browser tools on LOCALDEV (https://<localdev-host>) — rebuild/redeploy, Keycloak login, the multi-step BY_ROUTE/REALTIME mission wizard, pre-flight, the Start-Mission FSM, AND the full per-drone flight-command lifecycle (arm → take-off → hover → start/fly → stop → observation divert → resume → home/land/disarm), per-tab verification, and cleanup. Use it for E2E UI verification (incl. verifying a controlbox/FSM change against real SITL), demos, or reproducing mission-flow bugs. Do NOT use it against staging or production (shared/real environments), for API-only or DB-seeded mission creation (no browser), or when localdev is not running."
version: "2.0.0"
owner: "agentry-core"
---

# Purpose

Drive the full UAV mission lifecycle through the **web UI** on **localdev** using the
Playwright MCP browser tools: (optionally rebuild the code under test), authenticate, create a
mission with the wizard, run pre-flight, start it, **fly the drones through the FSM
(arm/take-off/hover/start/stop/divert/resume/home/land/disarm)**, walk every flight tab, and
clean up. Use it to verify a feature end-to-end — including a **controlbox / drone-FSM change
against real SITL** — produce a demo mission, or reproduce a mission-flow bug.

# When to use this skill

- Trigger A — Verifying a mission-related change end-to-end through the real UI (not unit/API level)
- Trigger B — Verifying a **controlbox / drone-FSM / MAVLink** change against **real SITL** by actually flying a drone
- Trigger C — Reproducing a bug that only manifests in the live mission flow (wizard, pre-flight, fly tabs, telemetry, camera, divert/resume)
- Trigger D — Producing a running demo mission with simulated drones on localdev
- Trigger E — Smoke-testing the Awareness / Flight Control / 3D View tabs against live telemetry

# When NOT to use this skill

- Anti-trigger A — Targeting **staging or production** (project.json → cloud.envAlias) → STOP. Those are shared/real environments; mission create/terminate/flight-commands there affect other people (and real drones). Get explicit human approval and follow your staging-operations runbook for any staging work.
- Anti-trigger B — Creating missions via REST/GraphQL API or seeding the DB directly → this skill is browser-driven only. (Note: `POST /api/missions/[id]/drone-command` is a **502 stub** — commands flow only over the client WebSocket, so there is no HTTP shortcut to drive the FSM.)
- Anti-trigger C — localdev is not running (`make creds` errors with "namespace/secret not found") → bring it up first (`make -C <scripts-dir> up` / `make status`); do not improvise.
- Anti-trigger D — Pure Playwright **unit/spec** authoring under `tests/e2e/` → use `testing`; this skill is interactive MCP-driven, not spec files.
- Anti-trigger E — Operating on a mission you do **not** own → RBAC/ownership blocks control of others' missions in the UI (see Diligence). Do not attempt DB/API workarounds.

# Required environment

- **Tools:** Playwright MCP browser tools — the reliable core is `browser_navigate`, `browser_run_code_unsafe` (the workhorse — `page.mouse`, normalized-text button finding, robust locators), `browser_evaluate`, `browser_snapshot`, `browser_take_screenshot`, `browser_console_messages`, `browser_close`. Plus a shell for `make` + `kubectl`.
- **Stack up:** the localdev Minikube stack running (the web portal + Keycloak + ≥1 simulated bee with a **SITL** sidecar). Verify with `kubectl get pods -n <app-namespace>` (bees are `bee-N-0`, each **2/2** = `bee` + SITL sidecar) and `kubectl get pods -n <infra-namespace>` (Keycloak/Postgres/Redis).
- **No hardcoded credentials.** Fetch them at runtime (see below).

## Localdev facts (canonical)

| Element | Value |
|---|---|
| App URL | `https://<localdev-host>` (self-signed TLS; the Playwright MCP context already ignores cert errors — `page.goto` works, but `page.request.*` does NOT — use in-page `fetch`/UI instead) |
| Keycloak | `https://keycloak.<localdev-host>` (realm `<keycloak-realm>`, client `<keycloak-client>`) |
| App sign-in user | `<admin-user>` (password from `make creds`) |
| App namespace | `<app-namespace>` · Infra namespace | `<infra-namespace>` |
| Bees / pods | StatefulSets `bee-N`; only pods that are **Running 2/2** have live SITL. Their wire **beeIdentifier** is `sim1`, `sim2`, … (NOT the pod name). |
| Drone FSM states | `disarm → arm → armed → hovering → hovered → flying → (waiting / comeback) → landing → disarm`; divert branch `flying → observing → observed → flying`. Wire values are lowercase. |
| Mission states | `PLANNED → (pre-check) → IN_PROGRESS → COMPLETED/TERMINATED` |

## Getting credentials (the only supported source)

```bash
make -C <scripts-dir> creds   # read the "Application sign-in" block: <admin-user> + password
```

The password is sourced from K8s secret `keycloak-admin-credentials` in `<infra-namespace>` (per-cluster random). Ignore the "Keycloak admin console" block (that's the KC admin UI, not the app). **NEVER** paste a staging/prod password anywhere; localdev passwords are disposable but still never commit them.

---

# 0. (Optional) Rebuild + redeploy the code under test

When verifying a **code change** (esp. controlbox/FSM/MAVLink), deploy it first. `rebuild-*`
builds from the **working tree** (`CONTROLBOX_DIR`/`NEXT_DIR` default to the edge-service and
web-portal checkouts — project.json → device / mainApp), so **uncommitted** changes deploy.

```bash
# Snapshot current image tags first, so you can prove the rollout changed them:
kubectl get sts -n <app-namespace> -o jsonpath='{range .items[*]}{.metadata.name}={.spec.template.spec.containers[?(@.name=="bee")].image}{"\n"}{end}'

make -C <scripts-dir> rebuild-controlbox   # builds control-box:local-<sha>-<ts>, updates ALL bee StatefulSets (RollingUpdate), restarts pods
make -C <scripts-dir> rebuild-next         # builds the web portal, sets image on its Deployment, waits for rollout
# make -C <scripts-dir> rebuild-all        # both
```

Verify the rollout landed (do NOT trust the make output alone — the `--wait=false` bee restart returns early):
```bash
kubectl get pods -n <app-namespace>                                   # bees back to 2/2 Running, web portal 1/1
kubectl get pod bee-1-0 -n <app-namespace> -o jsonpath='{.spec.containers[?(@.name=="bee")].image}{"\n"}'   # NEW tag
curl -sk https://<localdev-host>/api/health                          # {"status":"healthy",...}
# Prove your source is actually in the image:
kubectl exec -n <app-namespace> bee-1-0 -c bee -- grep -c "<your_new_symbol>" /app/src/services/<file>.py
```

---

# The mission-creation workflow

Drive with the Playwright MCP tools. After **every** state-changing step, verify before
continuing (see Discernment) — do not chain blind clicks. `browser_run_code_unsafe` is the
workhorse; see **Playwright automation gotchas** below (they are not optional — the UI has
newline-in-label buttons, hold-to-confirm buttons, Google-Maps markers, and swarm-vs-single
command scoping that all break naive clicks).

### 0. Authenticate (clear cookies first)
Stale Auth.js cookies cause `ERR_TOO_MANY_REDIRECTS` on `/`. One `browser_run_code_unsafe`:
```js
async (page) => {
  await page.context().clearCookies();
  await page.goto('https://<localdev-host>/', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#username, input[name="username"]', { timeout: 20000 });
  await page.fill('#username', '<admin-user>');
  await page.fill('#password', '<password from make creds>');
  await page.click('#kc-login, button[type="submit"]');
  await page.waitForLoadState('networkidle').catch(()=>{});
  return { url: page.url() };   // expect …/dashboard
}
```

### 1. General Settings (`/missions/new`)
- Pick **By Route (Autonomous)** (adds a Waypoints step) or **Real-time (Manual Control)**.
- Fill **Mission Name** (required). Start Time defaults to ~now+; Duration presets; Continue enables once type+name set.

### 2. Start Location
- Fill **Latitude** / **Longitude** spinbuttons (e.g. `50.4501` / `30.5234`). `AGL (m)` = take-off altitude (default 50). Continue.

### 3. Drone Setup
- Click a **swarm** to expand its bees. **Select the bees whose beeIdentifier maps to a Running SITL pod** — if unsure, grep the web-portal pod logs for the most-active `simN`, or note that in the wizard each bee row shows its `simN` id and "SITL Quad" type. Confirm "N selected". Continue.
- ⚠️ A bee already enrolled in an in-progress mission is double-booked — pre-check will later fail with "Already enrolled in mission '…' (IN_PROGRESS)". Only ~1 SITL pod per bee exists on localdev, so pick free ones (or terminate the blocker — see §Diligence).

### 4. Waypoints (BY_ROUTE only)
- For **each** drone tab: fill lat/lon (placeholders `e.g. 48.8566` / `2.3522`), click **Add**. **Space legs ≥300–500 m apart** so the drone spends real time flying each segment (needed to interrupt mid-leg for a divert test). Continue when no "drones have no waypoints" warning.

### 5. Review & Create
- Confirm the summary, click **Create Mission** → routes to `/missions/<id>/details` (status **PLANNED**). Capture `<id>` from the URL.

### 6. Pre-flight → activate (single Start, NOT two-click)
- On `/missions/<id>` (or `/details`) click **Start Pre-check** → routes to `/missions/<id>/precheck` → wait for **ALL GOOD!** across DRONES / PAYLOAD / CARGO / CREW.
- Click **Start Mission** (the precheck-page CTA) → the mission goes straight to **IN_PROGRESS** and routes to `/missions/<id>/fly?tab=control`. (There is a single activation click here, not the old two-click READY→IN_PROGRESS; if a confirm dialog appears, accept it.)

### 7. Verify the flight tabs
- Tabs: **2D, 3D, Dashboard, Flight Control, Awareness**. Click each; assert it renders without app errors.
- **Awareness** priority: satellite map, floating DRONES roster (battery/role), Alerts sidebar, camera strip.

---

# 8. Fly the drone — the per-drone FSM flight commands

This is the operational heart the older skill omitted. Commands are issued from the **Flight
Control** tab's bottom command bar. It operates at **swarm scope** ("SWARM ALPHA-1") until you
**select a single drone** (see gotcha 3). The button matrix mirrors
`<mainApp>/src/lib/control/command-fsm.ts` (controlbox is authoritative and NACKs illegal events).

**Lifecycle (per drone):**
`disarm →[Arm ⏱hold]→ arm →(FC confirms)→ armed →[Take Off]→ hovering →(reached alt)→ hovered →[Start]→ flying →[Stop]→ waiting / →[Home ⏱hold]→ comeback / (from hovered) →[Land]→ landing →(touchdown)→ disarm`

| From state | Enabled command(s) | Button label |
|---|---|---|
| `disarm` | Arm | **Arm (hold to confirm)** |
| `arm` (transitional) | Disarm only | Take Off is **blocked** until FC confirms armed |
| `armed` | Take Off, Disarm | **Take Off** |
| `hovering` (climbing) | Disarm | (transitional — wait) |
| `hovered` | Start, Home, **Land**, Disarm | **Start** / **Take Off↔Land** toggle |
| `flying` | **Stop**, Home, Disarm, **goto (divert)** | **Stop**; divert via Obs tile |
| `waiting` | Start, Home, Alt (echelon), Disarm, goto | **Start** |
| `comeback` | Stop, Disarm | **Stop** |
| `observing` / `observed` | **Resume (Start)**, Home, Disarm, goto | **Resume mission** button |

**SITL timing reality (critical):**
- Localdev SITL usually runs with **arming checks disabled** → the FC arms in ~1 s (no 200–274 s EKF wait). But if it must relocate/respawn at a new mission home, budget time for GPS/EKF convergence.
- **Auto-disarm:** an armed-on-ground FC **auto-disarms after ~10 s–2 min** with no take-off. So **arm and take-off in quick succession** — don't leave a long gap. If the toggle flips back to "Arm"/status `disarm`, it auto-disarmed; re-arm.
- Take-off climbs to the mission AGL (~50 m) in ~15–20 s; `REACHED_TARGET` → `hovered`. `Start` sets AUTO and the drone flies the route (`Reached command #N` per waypoint in the FC log).

**Working recipe (arm → take-off), one `browser_run_code_unsafe` call to beat auto-disarm:**
```js
async (page) => {
  const arm = page.getByRole('button', { name: 'Arm (hold to confirm)' });   // getByRole normalizes the newline in the label
  const box = await arm.boundingBox();
  if (box) { await page.mouse.move(box.x+box.width/2, box.y+box.height/2);
             await page.mouse.down(); await page.waitForTimeout(2400); await page.mouse.up(); }  // HOLD ~2.4s
  const takeoff = page.getByRole('button', { name: 'Take Off' });
  for (let i=0;i<45;i++){ await page.waitForTimeout(400);
    if ((await takeoff.count()) && !(await takeoff.isDisabled())) await takeoff.click({timeout:2000}).catch(()=>{});
    const st = await page.evaluate(()=> (document.querySelector('main')?.innerText||'').match(/HOVERING|HOVERED|FLYING/i)?.[0]);
    if (st && /HOVER|FLY/i.test(st)) return { airborne:true, st };
  }
  return { airborne:false };
}
```
Then **select the drone** (gotcha 3) and click **Start** (single-drone Start is enabled in `hovered`; the *swarm* Start is disabled whenever any other drone isn't ready — intersection rule). Verify against the controlbox log, which is the ground truth:
```bash
kubectl logs -n <app-namespace> bee-1-0 -c bee --since=5m | grep -iE "Sending ARM|TAKE_OFF to|REACHED_TARGET|Setting AUTO mode|Reached command|RESYNC|Disarm"
```

---

# 9. Observation report → divert → resume (resume-to-point path)

The **divert** ("go to observation") sends a *flying/waiting* drone to an observation marker;
**Resume** returns it to the mission. Since the resume-to-point fix, Resume first flies the drone
**back to the exact interruption point X** before handing to AUTO.

1. **Create an observation** (the divert target). Bottom-bar **Report an observation** (FAB) → pick a type (**Human detected** / Vehicle / Point of interest) → a **confirm dialog** ("Report: … / Confirm report") pins it at the **reporting drone's *current* position** → click **Confirm report**. Verify it persisted:
   ```bash
   # backend.observation (V1.0.20). Get the pw from the <infra-namespace> postgres secret, then:
   PGPASSWORD=<pw> psql -h <pg> -U postgres -d backend -tAc \
     "select id,observation_type,latitude,longitude from backend.observation where mission_id=<id> order by id desc limit 3;"
   ```
   ⚠️ **Geometry gotcha:** the FAB pins the marker at the *drone's* live position, so it overlaps the drone marker. To make a divert *targetable and meaningful*, create the observation while the drone is **mid-leg and moving** (not stopped at a waypoint), so by divert time the drone has moved away and the marker is separately clickable. If the drone already reached its route end, re-route it (waypoint mode → `NEW_ROUTE`) so it flies a fresh segment first.
2. **Select the drone** (gotcha 3) — divert is per-drone (`selectedBeeId`).
3. **Arm the divert:** with the drone selected + an observation present, its nearest observation highlights. Click the **observation marker** (real mouse, gotcha 4) → an on-map **confirm bubble** ("Divert <drone> here? · <type> · <dist>") with **[Cancel] [Send]**. The left-toolbar **Obs** tile (Crosshair) toggles highlighting of *all* observations (way-2). Click **Send** → `GO_TO_OBSERVATION` → drone → `observing`.
4. Drone flies to the point → `observed`. The per-drone **Resume mission** button appears ("Holding · resumes to WP N").
5. Click **Resume mission** → `RESUME_MISSION`. **Verify resume-to-point in the controlbox log** — the definitive proof:
   ```bash
   kubectl logs -n <app-namespace> bee-1-0 -c bee --since=3m | grep -iE "GUIDED|goto|Position target|current mission waypoint|Setting AUTO mode|RESUME|OBSERV"
   ```
   Expected sequence on Resume (resume-to-point): `set_guided_mode` → `goto_position` **to X** (the captured interruption point) → *(NOT immediate AUTO)* → on arrival: `Setting current mission waypoint: seq=N` → `Setting AUTO mode`. Pre-fix (bug) behaviour was an immediate `set_current_waypoint` + `Setting AUTO mode` with no goto-to-X. In the UI, the drone marker should track **back toward X** before continuing toward the upcoming waypoint.

---

# 10. Cleanup (mission you created)

- Open `/missions/<id>/details` → **Terminate** → an **in-app DOM modal** appears ("Terminate Mission — Are you sure? [Cancel] [Terminate]"). Click the modal's destructive **Terminate**. **This is NOT a native `confirm()`** (do not rely on `browser_handle_dialog` here). If a raw `.click()` is intercepted by the modal backdrop, click the modal button via `browser_run_code_unsafe` (`page.locator('div.fixed.inset-0.z-50 button[data-variant="destructive"]').click()`).
- Verify status **TERMINATED** on a fresh reload (the details status text can lag one render — reload to confirm) and the In-Progress count dropped. Terminating frees the bees for the next mission. Then `browser_close`.

---

# Playwright automation gotchas (hard-won — read before driving Flight Control)

1. **Button labels contain internal newlines** (icon-over-text), e.g. `"Arm\n(hold to confirm)"`. Raw `innerText` regexes with single spaces match nothing. **Normalize** (`(t||'').replace(/\s+/g,' ').trim()`) or use **`getByRole('button', { name })`** (it normalizes the accessible name).
2. **Hold-to-confirm buttons** (Arm, Disarm-airborne, Return) fire only after a ~2 s press-and-hold. A single `.click()` does nothing. Use `page.mouse.move(cx,cy)` → `mouse.down()` → `waitForTimeout(~2300)` → `mouse.up()`.
3. **Single-drone command scope = click the drone's *map marker*.** The command bar is swarm-scoped until a drone is selected; selection is wired to `onSelectBee` on the map, so click the `<div aria-label="Sim 0N">` marker inside `.gm-style` **with a real mouse event** (`page.mouse.click(rect.x, rect.y)`) — a synthetic `.click()` does NOT trigger the Maps handler. Success = the per-drone toolbar (Manual/Explode) + a **Deselect drone** button appear. (Roster chips from "Toggle Drones" did NOT reliably change scope in testing; the map marker did.)
4. **Google-Maps markers need real mouse events at their rect**, not `element.click()`. Get `getBoundingClientRect()` in-page, then `page.mouse.click(cx, cy)`.
5. **Two dialog kinds:** the observation report + terminate use **in-app DOM modals** (`div.fixed.inset-0.z-[70]/z-50`, `role=dialog`) — click their buttons directly; their backdrop intercepts clicks on anything behind them. Only some legacy flows use native `confirm()`. Don't assume `browser_handle_dialog`.
6. **The controlbox log is ground truth.** UI state parsing is flaky (re-renders, telemetry flaps). Confirm every FSM transition with `kubectl logs -n <app-namespace> bee-N-0 -c bee --since=...`.
7. **`page.request.get(...)` fails the self-signed cert** (unlike `page.goto`). Use in-page `fetch` or the UI.

# Discernment — verify, don't assume

| After step | Check (prefer the controlbox log for FSM truth) |
|---|---|
| Rebuild | new image tag on the pod; `grep` your symbol inside the container; `/api/health` healthy |
| Login | URL is `…/dashboard`; `0 errors` in console |
| Each wizard step | heading advanced; `Continue` enabled |
| Drone select (wizard) | "N selected" |
| Waypoints | per-drone "Route (N waypoints)" ≥ 1; no warning |
| Create | URL `/missions/<id>…`; PLANNED |
| Pre-check | all sections **ALL GOOD!** (else read the per-section error, e.g. double-booking) |
| Activate | URL reaches `…/fly`; mission In-Progress count +1 |
| Arm | FC log `Sending ARM command` + `Arming motors`; toggle flips to "Disarm" |
| Take Off | FC log `Sending TAKE_OFF` then `REACHED_TARGET`; state `hovered` |
| Start | FC log `Setting AUTO mode` then `Reached command #N`; state `flying` |
| Divert | FC log `goto_position` to the obs point; state `observing`→`observed` |
| Resume (resume-to-point) | FC log `goto_position` to **X** THEN (on arrival) `set current mission waypoint` + `Setting AUTO mode` |
| Terminate | status `TERMINATED` on reload; In-Progress count −1 |

# Failure modes & recovery

| Symptom | Cause | Recovery |
|---|---|---|
| `ERR_TOO_MANY_REDIRECTS` on `/` | stale Auth.js cookies | `page.context().clearCookies()` then re-navigate |
| `make creds` errors (namespace/secret not found) | localdev not running | `make -C <scripts-dir> up` (or `make status`); do not improvise creds |
| Pre-check "Already enrolled in mission '…'" | bee is double-booked in another IN_PROGRESS mission | free it (terminate that mission if you own it, §Diligence) or pick a different bee |
| Arm/Take-Off button "does nothing" | label has a newline (matcher missed it) OR it's hold-to-confirm | normalize text / `getByRole`; use press-and-hold (gotcha 1, 2) |
| Drone armed then reverted to `disarm` | FC auto-disarm (no take-off within ~10 s–2 min) | arm and take-off in one tight sequence (gotcha, §8 recipe) |
| Swarm **Start**/Take-Off disabled while a drone is ready | intersection rule (another drone not in a compatible state) | select the single drone (gotcha 3) and command it individually |
| Can't select a single drone | clicked a synthetic event / roster chip | click the **map marker** with a **real mouse event** (gotcha 3, 4) |
| Observation FAB "does nothing" | didn't click **Confirm report** in the confirm dialog | complete the dialog; verify the DB row |
| Divert marker un-clickable | observation overlaps the drone (pinned at drone's position, drone stopped) | create the obs while the drone is **moving mid-leg**, or re-route so it flies away first |
| Terminate "does nothing", no network call | it's a **DOM modal**, not native `confirm()` | click the modal's destructive **Terminate**; reload to confirm status |
| Can't terminate/control a mission | RBAC/ownership (not your mission; "Managed By —") | only operate on missions you created; do not use DB/API workarounds |
| Camera strip blank | camera WS path/route or MockCamera | check the bee pod `/ws/image`; separate infra concern |

# AI Fluency (the 4Ds for this skill)

- **Delegation** — Delegate the *mechanical* UI walk (login, wizard, tab sweep, flight commands) to Claude + Playwright. The **human** owns the consequential choices: which environment (localdev only here), whether to **terminate someone else's in-progress mission** to free bees, and any deviation toward a shared env. If the task implies staging/prod, escalate.
- **Description** — Give Claude the parameters explicitly: type, name, # of bees (and which are SITL-backed), start location, waypoints (leg length matters for divert tests), and which FSM path to exercise. The skill supplies the step order + recipes; you supply the data.
- **Discernment** — Treat each step as a claim to verify. The UI has hold-to-confirm buttons, newline labels, swarm-vs-single scope, Google-Maps markers, DOM (not native) dialogs, and telemetry flaps — all easy to get wrong silently. **The controlbox log is the FSM ground truth**; read it after every transition.
- **Diligence** — localdev only; creds via `make creds`, never committed; clean up missions you create; only terminate another mission (to free bees) with the human's explicit OK; never run against staging/production without approval. Record what you created, flew, and cleaned up.

# Example invocation

> "Use mission-creation: rebuild controlbox, then on localdev create a BY_ROUTE mission 'Resume Smoke'
> with one SITL bee on a 3-waypoint ~500 m-leg route, start it, arm→take-off→start, and while it's
> flying mid-leg report an observation and divert to it, then Resume and confirm from the controlbox
> log that it flies back to the interruption point X before continuing. Terminate when done."

# Reusable Playwright snippets

See [`resources/playwright-snippets.md`](resources/playwright-snippets.md) for copy-pasteable
`browser_run_code_unsafe` snippets (login, drone selection via map marker, hold-to-confirm arm,
arm→take-off loop, observation report, divert, resume, terminate-DOM-modal).
