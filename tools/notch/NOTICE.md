# Third-party notices

## notchling

The notch/top-edge window layer under `Sources/SwarmeryNotch/UI/Window/` --
`WidgetPanel.swift`, `WidgetShape.swift`, `WidgetTiming.swift`,
`WidgetWindowGeometry.swift`, `WidgetMetrics.swift`, `PanelLayout.swift`,
`WidgetPresenter.swift` -- is adapted from notchling
(https://github.com/CircleHP/notchling), (c) its authors, MIT.

`WidgetMetrics.swift` is not one of the six files the phase plan named; it
was added because `WidgetWindowGeometry.swift` needs a per-screen metrics
type to compile, and the notch-vs-plain-display detection technique in it
(`NSScreen.notchSize`, via `auxiliaryTopLeftArea`/`auxiliaryTopRightArea`
rather than `safeAreaInsets.top`) is genuinely notchling's, so it is listed
and attributed here rather than presented as original work.

`WidgetPanel.swift` and `WidgetShape.swift` are close to a direct port (pure
window configuration and pure shape geometry -- neither has any dependency
on notchling's own session/mascot model). `WidgetTiming.swift`,
`WidgetWindowGeometry.swift`, `WidgetMetrics.swift`, `PanelLayout.swift` and
`WidgetPresenter.swift` are adapted: notchling's originals are built against
its own session/subagent/mascot data model (`SessionStore`, `Session`,
`SubagentActivity`, `Scale`, `DisplayMode`, mascot pixel-art snapping); this
package has no mascot and a much simpler session model (no subagent tree),
so those files were reworked against this package's own
`AttentionState`/`Session`/`WidgetActions` while keeping the ported design
decisions: the window is sized to what it actually draws and never larger
than the display, rounded to whole/even points; the row-count cap on the
expanded panel; and the per-screen presenter's resize-before-animate
transition sequencing. Everything mascot, updater and Homebrew related was
dropped, not adapted. `WidgetPresenter.swift` also drops notchling's
debounced re-measure ("settle") and "grow the window if clipped while open"
refinements -- pure UI polish this phase had no way to visually iterate on
in a headless session; see the phase-3 Completion Report for that deviation.

No other file in this package is derived from notchling. Everything else --
`WidgetActions.swift`, `Core/TerminalFocus.swift`, `App.swift`,
`Core/AppCoordinator.swift`, `Daemon/DaemonClientProtocol.swift`, and the
SwiftUI content views (`CompactStrip.swift`, `ExpandedPanel.swift`,
`SessionRow.swift`, `ApprovalRow.swift`, `UsageStrip.swift`,
`OfflineBadge.swift`, `NotchRootView.swift`) -- is new code written for this
package against the swarmery daemon's own API. `Core/TerminalFocus.swift`
uses a well-known AppleScript-on-tty technique for iTerm2/Terminal.app that
is not specific to any one project; it is not a port.

### notchling license (MIT)

```
MIT License

Copyright (c) 2026 Notchling contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
