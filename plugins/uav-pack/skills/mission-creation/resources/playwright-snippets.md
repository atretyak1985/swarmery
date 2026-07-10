# Playwright MCP snippets — UAV mission creation (localdev, v2.0.0)

Copy-paste bodies for `browser_run_code_unsafe` (each receives `page`). Adjust the data
values; **never hardcode passwords** — pass the `make creds` value in at call time. All target
`https://<localdev-host>` (localdev). The Playwright MCP context already ignores the self-signed
cert for `page.goto`, but **`page.request.*` still fails it** — use in-page `fetch` or the UI.

> These mirror `SKILL.md` v2.0.0 exactly. Key contract facts baked in below (do not "fix" them
> back to the old flow): start is a **single** activation (no READY step), the flight tabs are
> **2D / 3D / Dashboard / Flight Control / Awareness**, single-drone scope comes from clicking
> the **map marker with a real mouse event**, Arm/Home/Return are **hold-to-confirm**, and
> Terminate is an **in-app DOM modal, NOT a native `confirm()`**.
>
> The **controlbox log is FSM ground truth** — after every transition, confirm with
> `kubectl logs -n <app-namespace> bee-N-0 -c bee --since=...`. UI parsing is flaky.

---

## 0. (Optional) Rebuild + redeploy the code under test — shell, not Playwright

```bash
# Snapshot current bee image tags so you can prove the rollout changed them:
kubectl get sts -n <app-namespace> -o jsonpath='{range .items[*]}{.metadata.name}={.spec.template.spec.containers[?(@.name=="bee")].image}{"\n"}{end}'
make -C <scripts-dir> rebuild-controlbox   # builds from WORKING TREE (uncommitted changes deploy), rolls all bees
make -C <scripts-dir> rebuild-next         # the web portal
kubectl get pods -n <app-namespace>                   # bees back to 2/2 Running, web portal 1/1 — do NOT trust make's early return
curl -sk https://<localdev-host>/api/health         # {"status":"healthy",...}
```

## 1. Login (clears stale cookies → Keycloak → app)

```js
async (page) => {
  // PASSWORD: paste the "Application sign-in" password from `make -C <scripts-dir> creds` — do NOT commit it.
  const USER = '<admin-user>';
  const PASS = '<<paste from: make -C <scripts-dir> creds>>';
  await page.context().clearCookies();                 // avoids ERR_TOO_MANY_REDIRECTS on /
  await page.goto('https://<localdev-host>/', { waitUntil: 'domcontentloaded', timeout: 30000 }).catch(() => {});
  await page.waitForSelector('#username, input[name="username"]', { timeout: 20000 }); // Keycloak form
  await page.fill('#username, input[name="username"]', USER);
  await page.fill('#password, input[name="password"]', PASS);
  await page.click('#kc-login, button[type="submit"]');
  await page.waitForLoadState('networkidle').catch(() => {});
  return { url: page.url() };                           // expect …/dashboard
}
```

## 2. Step 1 — General Settings (BY_ROUTE + name)

```js
async (page) => {
  await page.goto('https://<localdev-host>/missions/new', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /By Route/ }).click();   // adds the Waypoints step
  await page.getByRole('textbox', { name: /Mission Name/ }).fill('Smoke Test');
  // Start Time defaults to ~now+; Duration presets. Continue enables once type+name are set.
  await page.getByRole('button', { name: /Continue/ }).click();
  await page.waitForTimeout(1000);
  return { heading: await page.getByRole('heading', { level: 2 }).first().innerText() }; // "Start Location"
}
```

## 3. Step 2 — Start Location (lat/lon + AGL take-off altitude)

```js
async (page) => {
  await page.locator('input[placeholder="e.g. 48.8566"]').fill('50.4501'); // lat
  await page.locator('input[placeholder="e.g. 2.3522"]').fill('30.5234');  // lon
  // "AGL (m)" spinbutton = take-off altitude (default 50). Leave unless the test needs a specific climb.
  await page.getByRole('button', { name: /Continue/ }).click();
  await page.waitForTimeout(1200);
  return { heading: await page.getByRole('heading', { level: 2 }).first().innerText() }; // "Drone Setup"
}
```

## 4. Step 3 — Drone Setup (pick SITL-backed bees)

Pick bees whose `beeIdentifier` (`sim1`, `sim2`, …) maps to a **Running 2/2 SITL pod**. A bee
already enrolled in an IN_PROGRESS mission is double-booked and pre-check will later fail
("Already enrolled in mission '…'"). Only ~1 SITL pod per bee exists on localdev — pick free ones.

```js
async (page) => {
  await page.getByRole('button', { name: /Swarm Alpha-1/ }).click(); // expand a swarm with sim bees
  await page.waitForTimeout(800);
  for (const name of ['sim1']) {                                     // 1 bee is enough to fly the FSM
    await page.getByText(new RegExp('^' + name + '$')).first().click().catch(() => {});
    await page.waitForTimeout(300);
  }
  const cont = page.getByRole('button', { name: /Continue/ });
  if (await cont.isEnabled()) await cont.click();
  await page.waitForTimeout(1200);
  return { heading: await page.getByRole('heading', { level: 2 }).first().innerText() }; // "Waypoints" (BY_ROUTE)
}
```

## 5. Step 4 — Waypoints (run once per drone tab; ≥300–500 m legs)

Space legs **≥300–500 m** apart so the drone spends real time flying each segment — you need a
moving drone mid-leg to make an observation divert meaningful (see §12–13).

```js
async (page) => {
  const addWp = async (lat, lon) => {
    const ins = page.locator('main input[type="number"]:visible');
    await ins.nth(0).fill(String(lat));
    await ins.nth(1).fill(String(lon));
    await page.waitForTimeout(250);
    await page.getByRole('button', { name: /^Add$/ }).click();
    await page.waitForTimeout(400);
  };
  // Drone 1 — a 3-waypoint route with ~500 m legs
  await addWp(50.4520, 30.5260); await addWp(50.4560, 30.5300); await addWp(50.4600, 30.5340);
  return { continueEnabled: await page.getByRole('button', { name: /Continue/ }).isEnabled() };
}
```

## 6. Step 5 — Review → Create (→ PLANNED; capture the id)

```js
async (page) => {
  await page.getByRole('button', { name: /Continue/ }).click();      // to Review
  await page.waitForTimeout(1000);
  await page.getByRole('button', { name: /Create Mission/ }).click();
  await page.waitForTimeout(3000);
  const url = page.url();                                            // /missions/<id>/details (PLANNED)
  return { url, id: (url.match(/missions\/([^/]+)/) || [])[1] };
}
```

## 7. Pre-flight → activate (SINGLE Start — NOT the old two-click)

```js
async (page) => {
  await page.getByRole('button', { name: /Start Pre-check/i }).click(); // → /missions/<id>/precheck
  await page.waitForTimeout(4000);                                      // wait for ALL GOOD! across DRONES/PAYLOAD/CARGO/CREW
  // Single activation: precheck-page "Start Mission" → straight to IN_PROGRESS, routes to /fly?tab=control
  await page.getByRole('button', { name: /Start Mission/i }).click();
  await page.waitForTimeout(3000);
  return { url: page.url() };                                          // …/fly?tab=control
}
```

## 8. Walk the flight tabs (2D / 3D / Dashboard / Flight Control / Awareness)

```js
async (page) => {
  const out = {};
  for (const tab of ['2D', '3D', 'Dashboard', 'Flight Control', 'Awareness']) {
    await page.getByRole('tab', { name: new RegExp(tab) }).click().catch(() => {});
    await page.waitForTimeout(2000);
    out[tab] = (await page.locator('main').last().innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 160);
  }
  return out;
}
```

## 9. Select a SINGLE drone (map marker + REAL mouse event)

The command bar is swarm-scoped until a drone is selected. Selection is wired to the map
marker's `onSelectBee` — a synthetic `.click()` does NOT fire the Google-Maps handler. Click the
marker's on-screen rect with `page.mouse`. Success = per-drone toolbar + **Deselect drone** appear.

```js
async (page) => {
  const rect = await page.evaluate(() => {
    const m = document.querySelector('.gm-style [aria-label^="Sim 0"], .gm-style [aria-label^="Sim "]');
    if (!m) return null;
    const r = m.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  });
  if (!rect) return { selected: false, reason: 'no drone marker found' };
  await page.mouse.click(rect.x, rect.y);                              // REAL mouse event
  await page.waitForTimeout(800);
  const deselect = await page.getByRole('button', { name: /Deselect drone/i }).count();
  return { selected: deselect > 0 };
}
```

## 10. Arm → Take Off (hold-to-confirm + loop to beat auto-disarm)

Arm is **hold-to-confirm** (~2 s press-and-hold; a single `.click()` does nothing). The label
contains an internal newline (`"Arm\n(hold to confirm)"`) — `getByRole` normalizes it. An
armed-on-ground FC **auto-disarms after ~10 s–2 min**, so arm and take-off in one tight call.

```js
async (page) => {
  const arm = page.getByRole('button', { name: 'Arm (hold to confirm)' });
  const box = await arm.boundingBox();
  if (box) {
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down(); await page.waitForTimeout(2400); await page.mouse.up(); // HOLD ~2.4s
  }
  const takeoff = page.getByRole('button', { name: 'Take Off' });
  for (let i = 0; i < 45; i++) {
    await page.waitForTimeout(400);
    if ((await takeoff.count()) && !(await takeoff.isDisabled())) await takeoff.click({ timeout: 2000 }).catch(() => {});
    const st = await page.evaluate(() => (document.querySelector('main')?.innerText || '').match(/HOVERING|HOVERED|FLYING/i)?.[0]);
    if (st && /HOVER|FLY/i.test(st)) return { airborne: true, st };
  }
  return { airborne: false };
}
```
Verify: `kubectl logs -n <app-namespace> bee-1-0 -c bee --since=5m | grep -iE "Sending ARM|TAKE_OFF to|REACHED_TARGET"`

## 11. Start (fly the route — single-drone, after `hovered`)

Select the drone first (§9). Single-drone **Start** is enabled in `hovered`; the *swarm* Start
is disabled whenever any other drone isn't ready (intersection rule).

```js
async (page) => {
  const start = page.getByRole('button', { name: /^Start$/ });
  for (let i = 0; i < 20; i++) {
    if ((await start.count()) && !(await start.isDisabled())) { await start.click().catch(() => {}); break; }
    await page.waitForTimeout(500);
  }
  await page.waitForTimeout(1500);
  return { started: true }; // confirm via log: "Setting AUTO mode" then "Reached command #N"
}
```

## 12. Report an observation (the divert target)

Create it while the drone is **moving mid-leg** so the marker (pinned at the drone's live
position) ends up separately clickable by divert time.

```js
async (page) => {
  await page.getByRole('button', { name: /Report an observation/i }).click(); // bottom-bar FAB
  await page.waitForTimeout(600);
  await page.getByRole('button', { name: /Human detected/i }).click();         // or Vehicle / Point of interest
  await page.waitForTimeout(400);
  await page.getByRole('button', { name: /Confirm report/i }).click();         // pins at reporting drone's current pos
  await page.waitForTimeout(1000);
  return { reported: true };
}
```
Verify persisted (`backend.observation`, V1.0.20): `psql … -tAc "select id,observation_type,latitude,longitude from backend.observation where mission_id=<id> order by id desc limit 3;"`

## 13. Divert to the observation (select drone → click marker → Send)

```js
async (page) => {
  // Assumes the drone is already selected (§9). The nearest observation to the selected drone highlights.
  const rect = await page.evaluate(() => {
    const m = document.querySelector('.gm-style [aria-label*="observation" i], .gm-style [aria-label*="Human" i]');
    if (!m) return null;
    const r = m.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  });
  if (!rect) return { diverted: false, reason: 'no observation marker' };
  await page.mouse.click(rect.x, rect.y);                              // opens the on-map confirm bubble
  await page.waitForTimeout(600);
  await page.getByRole('button', { name: /^Send$/ }).click();          // GO_TO_OBSERVATION → drone → observing
  await page.waitForTimeout(1200);
  return { diverted: true }; // confirm via log: "GUIDED"/"goto_position"; state observing→observed
}
```

## 14. Resume (resume-to-point — fly back to interruption point X, THEN AUTO)

```js
async (page) => {
  const resume = page.getByRole('button', { name: /Resume mission/i }); // appears once state is observed ("Holding · resumes to WP N")
  for (let i = 0; i < 30; i++) {
    if ((await resume.count()) && !(await resume.isDisabled())) { await resume.click().catch(() => {}); break; }
    await page.waitForTimeout(500);
  }
  await page.waitForTimeout(1500);
  return { resumed: true };
}
```
**Resume-to-point proof (controlbox log):** `set_guided_mode` → `goto_position` **to X** (captured interruption
point) → *(NOT immediate AUTO)* → on arrival: `Setting current mission waypoint: seq=N` → `Setting AUTO mode`.
```bash
kubectl logs -n <app-namespace> bee-1-0 -c bee --since=3m | grep -iE "GUIDED|goto|Position target|current mission waypoint|Setting AUTO mode|RESUME"
```

## 15. Terminate (own mission) — in-app DOM modal, NOT native confirm()

Terminate raises an **in-app DOM modal** ("Terminate Mission — Are you sure? [Cancel]
[Terminate]"), NOT a browser `confirm()`. Do **not** use `browser_handle_dialog` here. If a raw
`.click()` is intercepted by the modal backdrop, click the modal button directly.

```js
async (page) => {
  await page.goto('https://<localdev-host>/missions/<id>/details', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /^Terminate$/ }).click();     // opens the DOM modal
  await page.waitForTimeout(600);
  // Click the modal's destructive confirm; fall back to a direct locator if the backdrop intercepts.
  await page.locator('div.fixed.inset-0.z-50 button[data-variant="destructive"], [role="dialog"] button:has-text("Terminate")')
    .last().click().catch(() => {});
  await page.waitForTimeout(1500);
  await page.reload({ waitUntil: 'networkidle' }).catch(() => {});     // status text can lag one render
  return { url: page.url() };
}
```

After terminating, confirm the details status reads `TERMINATED` on the reload and the
In-Progress count dropped (terminating frees the bees for the next mission), then `browser_close`.
