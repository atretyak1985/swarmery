# Swarmery Notch

A native macOS 14+ menu-bar / notch companion for the [swarmery](../../README.md)
daemon. It lives collapsed in the notch (or docked to the top edge on displays
without one) and expands on its own the moment an operator is needed — a
permission request is pending, or a session has failed. From the panel you can
approve/deny, jump to the terminal tab that owns a session, see plan usage, and
open the dashboard.

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

```bash
cd tools/notch
make install
```

This builds a release binary, assembles `Swarmery Notch.app`, copies it to
`~/Applications/Swarmery Notch.app`, writes the LaunchAgent
(`~/Library/LaunchAgents/com.swarmery.notch.plist`), and loads it — the widget
appears in the notch/menu bar within a couple of seconds. Re-running
`make install` after a rebuild reinstalls the app and restarts the running
LaunchAgent on the new build (`launchctl kickstart -k`), the same ergonomics as
`tools/swarmery`'s own `make install`.

`make restart` kicks an already-installed LaunchAgent without rebuilding —
useful if the widget looks stuck for some other reason.

### Gatekeeper note (the app is unsigned)

Swarmery Notch ships **unsigned** for now — signing/notarization needs a paid
Apple Developer ID and is tracked as a follow-up, not a blocker (see
`plan/SUMMARY.md`'s Follow-ups). The first time you open
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

Both variables are read once at process start. Export them before `make run`
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
is best-effort (Warp via a `warp://` URL, iTerm2/Terminal.app via AppleScript
on the tty); an unrecognized terminal, or a session with no captured terminal
identity (headless/daemon-spawned sessions), falls back to opening the
dashboard in your browser instead of erroring.

**`make install` fails with `launchctl bootstrap` exit 5.** `launchctl bootout`
is asynchronous, so an install run immediately after an uninstall can race the
old service's unregistration. Wait a few seconds and run `make install` again.

## Development

```bash
cd tools/notch
swift build && swift test   # 57 tests as of phase 4
swift run SwarmeryNotch     # run unbundled, un-notch-installed, for iteration
```

Zero external dependencies by design (`Package.swift`'s `dependencies: []`):
the daemon client, WebSocket stream, and attention model are built entirely on
Foundation + URLSession + AppKit/SwiftUI.
