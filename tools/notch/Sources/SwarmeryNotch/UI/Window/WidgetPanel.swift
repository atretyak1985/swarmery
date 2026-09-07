// The window itself. Configuration only -- what it contains is
// `NotchRootView`, when it changes is `WidgetPresenter`, and how big it is is
// `WidgetWindowGeometry`.
//
// Ported near-verbatim from notchling's WidgetPanel.swift (MIT license, see
// NOTICE.md) -- this file has no dependency on notchling's session/mascot
// model, so nothing needed adapting beyond staying in this package's module.
import AppKit

final class WidgetPanel: NSPanel {
    override init(
        contentRect: NSRect,
        styleMask style: NSWindow.StyleMask,
        backing backingStoreType: NSWindow.BackingStoreType,
        defer flag: Bool
    ) {
        super.init(contentRect: contentRect, styleMask: style, backing: backingStoreType, defer: flag)

        isOpaque = false
        backgroundColor = .clear
        hasShadow = false
        // Above the menu bar (level 24) so the shape can sit against the top
        // edge of the screen, and above full-screen windows so a session
        // finishing is visible from inside one.
        level = .screenSaver
        collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle, .fullScreenAuxiliary]
        isMovableByWindowBackground = false
        hidesOnDeactivate = false
        animationBehavior = .none
    }

    /// Never key, never main. A `nonactivatingPanel` receives mouse events
    /// for the window under the pointer whether or not it is key, so rows
    /// still work as buttons -- but *becoming* key is disastrous: this is an
    /// accessory (`LSUIElement`-equivalent) app, so a key window hands it the
    /// menu bar, and the menu bar of an app with no menus is a top bar you
    /// cannot use.
    override var canBecomeKey: Bool { false }
    override var canBecomeMain: Bool { false }
}
