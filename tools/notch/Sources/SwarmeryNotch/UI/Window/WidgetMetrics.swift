// Per-screen geometry the notch panel is drawn against: whether the screen
// has a physical notch (and how big it is), and how tall its menu bar is.
//
// Adapted from notchling's WidgetMetrics.swift (MIT license, see NOTICE.md).
// Not one of the six files the phase doc names, but pulled in as a seventh
// ported file because `WidgetWindowGeometry` needs it to compile -- see
// NOTICE.md for why. The ported piece is the notch-detection technique
// (`NSScreen.notchSize`, using `auxiliaryTopLeftArea`/`auxiliaryTopRightArea`
// rather than `safeAreaInsets.top`, which is also non-zero on some external
// displays under "Show menu bar in full screen"). Everything mascot/scale-
// preference related (`Scale`, `DisplayMode`, `mascotHeight`, `multiplier`,
// pixel-snapping) is dropped -- this package draws no pixel-art mascot, so
// none of that applies.
import AppKit

/// Everything the notch panel needs to know about the one screen it draws on.
public struct WidgetMetrics: Equatable, Sendable {
    /// The physical notch on a built-in display, or the zero-width "faux
    /// notch" anchor on a screen with no cutout -- see `isPhysicalNotch`.
    public let anchorSize: CGSize
    /// True when `anchorSize` describes a real hole in the display.
    public let isPhysicalNotch: Bool
    /// Height of the menu bar -- how tall the compact strip is allowed to be.
    public let menubarHeight: CGFloat

    public init(anchorSize: CGSize, isPhysicalNotch: Bool, menubarHeight: CGFloat) {
        self.anchorSize = anchorSize
        self.isPhysicalNotch = isPhysicalNotch
        self.menubarHeight = menubarHeight
    }

    /// Real-screen initializer. The arithmetic lives in the memberwise
    /// `init` above, separated from `NSScreen` so tests can exercise display
    /// configurations this machine does not have, without touching a real
    /// screen or the window server.
    @MainActor
    public init(screen: NSScreen) {
        let menubarHeight = max(screen.frame.maxY - screen.visibleFrame.maxY, 24)
        if let notchSize = screen.notchSize {
            self.init(anchorSize: notchSize, isPhysicalNotch: true, menubarHeight: menubarHeight)
        } else {
            self.init(
                anchorSize: CGSize(width: 0, height: menubarHeight),
                isPhysicalNotch: false,
                menubarHeight: menubarHeight
            )
        }
    }

    /// Matches a notchless screen -- used before a presenter has measured a
    /// real one (SwiftUI previews, or a view rendered outside a presenter).
    public static let fallback = WidgetMetrics(
        anchorSize: CGSize(width: 0, height: 24), isPhysicalNotch: false, menubarHeight: 24
    )
}

extension NSScreen {
    /// The cutout, or nil on a screen that does not have one.
    ///
    /// `auxiliaryTopLeftArea` is only non-nil on a notched display, which
    /// makes it a more honest test than `safeAreaInsets.top` -- that is also
    /// non-zero on some external displays under "Show menu bar in full
    /// screen".
    var notchSize: CGSize? {
        guard let leftWidth = auxiliaryTopLeftArea?.width,
              let rightWidth = auxiliaryTopRightArea?.width,
              safeAreaInsets.top > 0
        else { return nil }
        return CGSize(width: frame.width - leftWidth - rightWidth, height: safeAreaInsets.top)
    }

    var displayID: CGDirectDisplayID {
        (deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber)?.uint32Value ?? 0
    }
}
