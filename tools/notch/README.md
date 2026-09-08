# Swarmery Notch

A native macOS 14+ companion for the [swarmery](../../README.md) daemon. It
lives as a small tab docked to the **right edge** of every display — rounded on
the left, flush on the right, the shape you know from Grammarly's desktop
widget — and expands into a side panel on its own the moment an operator is
needed: a permission request is pending, or a session has failed. From the
panel you can approve/deny, jump to the terminal tab that owns a session, see
plan usage, and open the dashboard. The original notch placement (collapsed
into the notch, or the top edge on displays without one) is still available
via `SWARMERY_NOTCH_PLACEMENT=notch`.

### What you see

- **Dormant**: nothing at all. The widget is an invisible 6-pt hot strip on
  the right edge, about 30% down the screen (above Grammarly's own tab, so the
  two never overlap; `SWARMERY_NOTCH_EDGE_ANCHOR` moves it). Touch the edge
  with the mouse and the tab slides in.
- **Two tabs** (each 44×64 pt, stacked, glued to the edge). The **sessions
  tab** on top: a hexagon-grid icon tinted by the worst state on screen — teal
  when everything is fine, **red** when an approval is pending (plus a red
  badge with the count), **orange** when a session needs you or failed, gray
  with a crossed-out Wi-Fi symbol when the daemon is offline — and the
  live-session count underneath. The **usage tab** below: a gauge icon tinted
  green / orange / red by the highest percent used across your plan windows,
  with that percent underneath. Both surfaces are the system window gray
  (adapts to light/dark mode) with a hairline border and a soft shadow,
  deliberately not black.
- **Sessions panel** (360 pt wide, hangs from the top of the tabs): one card
  per pending approval (tool name, project, request summary, `Deny` /
  `Approve`), then the session rows with a coloured status dot, the
  jump-to-terminal / open-in-dashboard button and a stop button. **Click the
  sessions tab** to open it; it stays open until you click the round `×` button in its
  header, click the tab again, or click anywhere outside the widget. It also
  opens by itself on a pending approval or a failed session and closes after
  the linger period.
- **Usage panel** — the dashboard's Usage modal, natively: click the usage tab.
  One card per provider (an account switcher appears when the daemon reports
  more than one account), each plan window with a progress bar, `N% used`,
  `M% left`, the reset countdown and clock time, and the daemon's pace line
  (`27% under pace`), and an `Updated hh:mm:ss` footer. Usage is re-fetched
  in the background every 5 minutes, again each time the panel opens, and on
  the round ↻ button in the header (which asks the daemon to bypass its own
  30-second cache). Same close gestures as the sessions panel.

Swarmery Notch is a thin client: every byte of data it shows comes from the
swarmery daemon on `:7777` (`GET /api/sessions`, `/api/approvals`, `/api/usage`,
and the `GET /api/ws` event stream). **It installs no Claude Code hooks and
writes nothing outside its own LaunchAgent** — see `tools/notch/NOTICE.md` for
the (window-layer-only) code it adapts from
[notchling](https://github.com/CircleHP/notchling) (MIT).

## Coexistence with notchling

If you already run [notchling](https://github.com/CircleHP/notchling), Swarmery
Notch is safe to run alongside it. Swarmery Notch does not install, modify, or
remove notchling's own hook or its `~/.notchling/events` spool — the two are
completely independent processes reading independent data sources. Uninstalling
Swarmery Notch (`make uninstall`) never touches anything belonging to notchling.

## Install

### From source (recommended — gives you `make install`/`uninstall`/`restart`)

```bash
cd tools/notch
make install
```

This builds a release binary, assembles `Swarmery Notch.app`, copies it to
`~/Applications/Swarmery Notch.app`, writes the LaunchAgent
(`~/Library/LaunchAgents/com.swarmery.notch.plist`), and loads it — the widget
appears on the right edge within a couple of seconds. Re-running
`make install` after a rebuild stops the running LaunchAgent first, swaps the
bundle, and loads it again on the new build (`bootout` → copy → `bootstrap`);
the running process is never left with its `.app` deleted underneath it.

`make restart` kicks an already-installed LaunchAgent without rebuilding —
useful if the widget looks stuck for some other reason.

### From a GitHub release

Every `notch-v*` tag publishes `SwarmeryNotch-v<version>-macos.zip` (plus
`SHA256SUMS`) on the
[releases page](https://github.com/atretyak1985/swarmery/releases?q=notch-v).
Unzip it, move `Swarmery Notch.app` to `~/Applications`, approve it once
through Gatekeeper (below), and open it — it runs as a menu-bar accessory with
no Dock icon. The zip carries no LaunchAgent; to have it start at login, add
the app under *System Settings → General → Login Items*, or use the
from-source install above, which writes the LaunchAgent for you.

### Gatekeeper note (the app is unsigned)

Swarmery Notch ships **unsigned** for now — signing/notarization needs a paid
Apple Developer ID and is a known follow-up, not a blocker. The first time you open
`~/Applications/Swarmery Notch.app` (or right after any `make install`),
macOS Gatekeeper will refuse a plain double-click launch. To approve it once:

1. In Finder, right-click (or Control-click) `Swarmery Notch.app`.
2. Choose **Open** from the context menu.
3. Click **Open** again in the dialog that appears.

You only need to do this once per build; launchd-started launches (via
`make install`/`make restart`) are not subject to the same double-click
Gatekeeper prompt once the app has been approved this way.

## Uninstall

```bash
cd tools/notch
make uninstall
```

Unloads the LaunchAgent and removes both the plist and the app bundle from
`~/Applications`. Logs under `~/.swarmery/logs/notch.{out,err}.log` are left in
place (uninstall is a rollback, not a purge — same convention as the daemon's
own `swarmery uninstall`).

## Configuration (environment variables)

All of these are read once at process start. Export them before `make run`
for local development. To apply them to the *installed* widget, add an
`EnvironmentVariables` dict to the installed LaunchAgent at
`~/Library/LaunchAgents/com.swarmery.notch.plist` — the shipped template
(`tools/notch/Resources/com.swarmery.notch.plist`) carries no such key, so add
one at the top level of its root `<dict>`:

```xml
<key>EnvironmentVariables</key>
<dict>
    <key>SWARMERY_URL</key>
    <string>http://127.0.0.1:7777</string>
</dict>
```

then `make restart` to pick it up. Note that `make install` rewrites the
installed plist from the template, so re-apply the block after an upgrade.

- **`SWARMERY_URL`** — overrides the daemon base URL, default
  `http://127.0.0.1:7777`. Set this if the daemon runs on a non-default port
  or host. A trailing slash is tolerated (normalized internally).
- **`SWARMERY_NOTCH_PLACEMENT`** — `right` (default) docks the widget to the
  right screen edge as described above; `notch` restores the original
  top-edge/notch presentation. Anything else falls back to `right`.
- **`SWARMERY_NOTCH_EDGE_ANCHOR`** — where on the right edge the tab sits, as
  a fraction of the screen height from the top; default `0.3`. Accepted range
  `0.05`–`0.95`, anything else keeps the default.
- **`SWARMERY_NOTCH_USAGE_REFRESH`** — seconds between background usage
  refreshes, default `300`; values under `30` (the daemon's own cache window)
  and non-numeric values keep the default.
- **`SWARMERY_NOTCH_LINGER`** — seconds the panel stays expanded after the
  most recent approval/session resolution before auto-collapsing, default `6`.
  Zero, negative, or non-numeric values fall back to the default rather than
  collapsing the panel instantly.
- **`SWARMERY_NOTCH_ANIM`** *(development only)* — slows every presentation
  transition down by this factor (e.g. `6` for 6x slower), useful for visually
  inspecting an animation mid-flight with `make run`. Ignored outside
  `(0, 60]`.

## Troubleshooting

**The widget shows an offline badge.** This means the swarmery daemon on
`:7777` (or wherever `SWARMERY_URL` points) is unreachable. The widget keeps
running and keeps retrying the connection with capped backoff — it does not
exit or get restarted by launchd over this (see the `KeepAlive` comment in
`Resources/com.swarmery.notch.plist` for why). Once the daemon comes back, the
widget reconnects automatically and refreshes its snapshot; no restart of the
widget is needed. If the badge never clears, confirm the daemon is actually
running (`tools/swarmery`: `launchctl print gui/$(id -u)/com.swarmery.daemon`)
and reachable at the configured `SWARMERY_URL`.

**The widget doesn't appear after `make install`.** Check
`launchctl print gui/$(id -u)/com.swarmery.notch` for its registration state,
and `~/.swarmery/logs/notch.{out,err}.log` for anything printed at startup.
Confirm you approved the Gatekeeper prompt at least once (see above) — an
unapproved quarantine flag can prevent launchd from starting it too.

**A session row's terminal doesn't come forward when clicked.** Terminal focus
is best-effort: Warp via its `warp://` focus URL, iTerm2/Terminal.app via
AppleScript on the tty, and any other terminal (JetBrains IDEs, VS Code, …)
by activating the owning app from its bundle id — that brings the app forward
but cannot pick the tab. A session with no captured terminal identity
(headless/daemon-spawned) opens the dashboard in your browser instead of
erroring. Terminal identity is captured by the daemon's `SessionStart` hook,
so sessions started before the daemon was upgraded to a build with migration
0068 show no terminal.

**The usage panel's `Updated` time is old.** The panel re-fetches on its own
every 5 minutes, each time it opens, and immediately on the ↻ button (which
bypasses the daemon's 30-second cache). If the time still does not move, the
daemon's usage provider is not connected — check the dashboard's Usage modal.

**The tab never appears when I touch the right edge.** The hot strip sits at
30% down the screen by default (`SWARMERY_NOTCH_EDGE_ANCHOR`); on a display
where another edge widget lives there, move it. If the widget is running
(`launchctl print gui/$(id -u)/com.swarmery.notch` shows a PID) but nothing
ever slides in, a pending approval will still open the panel by itself — the
hot strip is only the manual way in.

**`make install` fails with `launchctl bootstrap` exit 5.** `launchctl bootout`
is asynchronous, so an install run immediately after an uninstall can race the
old service's unregistration. Wait a few seconds and run `make install` again.

## Development

```bash
cd tools/notch
swift build && swift test   # unit tests: models, WS stream, attention model, geometry, panels
swift run SwarmeryNotch     # run unbundled, uninstalled, for iteration
```

Layout: `Daemon/` (REST client, WebSocket stream, wire models pinned by the
fixtures in `Tests/Fixtures/` — the same files the daemon's Go tests decode),
`Core/` (the pure `AttentionModel` reducer, `AppCoordinator`, terminal focus),
`UI/` (SwiftUI views; `UI/Window/` is the window layer adapted from notchling
plus the right-edge placement). Releases: push a `notch-v*` tag and
`.github/workflows/notch-release.yml` builds, zips and attaches the bundle;
bump `Resources/Info.plist`'s `CFBundleShortVersionString` first.

Zero external dependencies by design (`Package.swift`'s `dependencies: []`):
the daemon client, WebSocket stream, and attention model are built entirely on
Foundation + URLSession + AppKit/SwiftUI.
