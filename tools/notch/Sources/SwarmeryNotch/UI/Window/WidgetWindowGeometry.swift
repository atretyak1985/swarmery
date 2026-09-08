// What size the notch window should be. `WidgetPresenter` decides *when* it
// may change.
//
// The window is sized to what it actually draws, which is not a nicety: a
// window is opaque to mouse events across its whole rect regardless of what
// it painted there, so slack is a dead zone over whatever is underneath.
//
// Adapted from notchling's WidgetWindowGeometry.swift (MIT license, see
// NOTICE.md). Ported: the record/measure-before-move design, the "never
// larger than the display" cap, and rounding to whole (even, for width)
// points so the shape never lands on a half point and softens its own edges.
// Adapted: notchling measures against its own `WidgetMetrics` (mascot-aware)
// and `ExpandedView.panelWidth`; this port measures against this package's
// `WidgetMetrics` and `ExpandedPanel.panelWidth`, and the compact fallback
// height is derived from the screen's anchor (notch or menu bar) rather than
// a mascot height, since this package draws no mascot.
import AppKit

/// What the panel's SwiftUI content measured, and where it needs the window
/// put.
public struct WidgetContentGeometry: Equatable, Sendable {
    public let size: CGSize
    /// How far right of the screen centre the window's centre must sit for
    /// the gap in the compact strip to land on the cutout. Zero when the
    /// strip is symmetric, or when there is no cutout.
    public let gapOffset: CGFloat

    public init(size: CGSize, gapOffset: CGFloat = 0) {
        self.size = size
        self.gapOffset = gapOffset
    }
}

public struct WidgetWindowGeometry {
    public let placement: WidgetPlacement

    /// Where along the right edge the tab sits, as a fraction of the visible
    /// frame's height measured from the BOTTOM. The default puts it at 30%
    /// from the top — above the vertical middle, where Grammarly's own tab
    /// lives, so the two never stack on the same spot. The expanded panel
    /// grows around the same anchor.
    public let edgeAnchorFromBottom: CGFloat
    public static let defaultEdgeAnchorFromBottom: CGFloat = 0.7

    /// Width of the invisible hot strip shown while dormant.
    public static let hotStripWidth: CGFloat = 6

    public init(placement: WidgetPlacement, edgeAnchorFromBottom: CGFloat = WidgetWindowGeometry.defaultEdgeAnchorFromBottom) {
        self.placement = placement
        self.edgeAnchorFromBottom = min(max(edgeAnchorFromBottom, 0.05), 0.95)
    }

    /// `SWARMERY_NOTCH_EDGE_ANCHOR` is written the way people think about a
    /// screen — a fraction from the TOP (`0.3` = 30% down). Out-of-range or
    /// non-numeric values keep the default.
    public static func edgeAnchorFromBottom(environmentValue raw: String?) -> CGFloat {
        guard let raw = raw?.trimmingCharacters(in: .whitespacesAndNewlines),
              let fromTop = Double(raw), fromTop >= 0.05, fromTop <= 0.95
        else { return defaultEdgeAnchorFromBottom }
        return CGFloat(1 - fromTop)
    }

    /// Sizes the content settled on, so a transition can size the window
    /// before anything moves rather than chasing it.
    private var measured: [WidgetPresentation: WidgetContentGeometry] = [:]

    /// `hidden` and `compact` lay out identically -- hiding is opacity and an
    /// offset, not a size change -- so they share one measurement.
    public static func sizeKey(_ presentation: WidgetPresentation) -> WidgetPresentation {
        switch presentation {
        case .expanded: return .expanded
        case .dormant: return .dormant
        case .compact, .hidden: return .compact
        }
    }

    /// Records a measurement. Returns true when it is new information worth
    /// acting on.
    @discardableResult
    public mutating func record(_ geometry: WidgetContentGeometry, for presentation: WidgetPresentation) -> Bool {
        guard geometry.size.width > 1, geometry.size.height > 1 else { return false }
        let key = Self.sizeKey(presentation)
        guard measured[key] != geometry else { return false }
        measured[key] = geometry
        return true
    }

    public func has(_ presentation: WidgetPresentation) -> Bool {
        measured[Self.sizeKey(presentation)] != nil
    }

    /// Deliberately *not* called when the screen's metrics change: a
    /// stale-but-tight size is corrected by the next real measurement, where
    /// an empty cache falls back to the generous window and may never be
    /// corrected, because a size that does not change reports nothing.
    public mutating func forget() {
        measured = [:]
    }

    /// `visibleFrame` is the screen minus menu bar and Dock; the right-edge
    /// placement stays inside it so the panel never hides under either. The
    /// notch placement deliberately uses the full `screenFrame` (it lives in
    /// the menu bar band by design).
    public func frame(
        for presentation: WidgetPresentation,
        metrics: WidgetMetrics,
        screenFrame: CGRect,
        visibleFrame: CGRect? = nil
    ) -> NSRect {
        switch placement {
        case .notch:
            return notchFrame(for: presentation, metrics: metrics, screenFrame: screenFrame)
        case .rightEdge:
            return edgeFrame(for: presentation, visibleFrame: visibleFrame ?? screenFrame)
        }
    }

    private func edgeFrame(for presentation: WidgetPresentation, visibleFrame visible: CGRect) -> NSRect {
        let recorded = measured[Self.sizeKey(presentation)]
        let content = recorded?.size ?? edgeFallback(for: presentation, visibleFrame: visible)
        let width = min(content.width.rounded(.up), visible.width)
        let height = min(content.height.rounded(.up), visible.height)
        let anchorY = visible.minY + visible.height * edgeAnchorFromBottom
        let unclampedY = (anchorY - height / 2).rounded()
        let y = min(max(unclampedY, visible.minY), visible.maxY - height)
        return NSRect(x: (visible.maxX - width).rounded(), y: y, width: width, height: height)
    }

    private func edgeFallback(for presentation: WidgetPresentation, visibleFrame visible: CGRect) -> CGSize {
        switch presentation {
        case .expanded:
            return CGSize(
                width: min(visible.width, ExpandedPanel.panelWidth + NotchRootView.edgeInset * 2),
                height: min(visible.height * 0.8, 640)
            )
        case .compact, .hidden:
            return CGSize(
                width: EdgeTab.size.width + NotchRootView.edgeInset * 2,
                height: EdgeTab.size.height + NotchRootView.edgeInset * 2
            )
        case .dormant:
            return CGSize(width: Self.hotStripWidth, height: EdgeTab.size.height + NotchRootView.edgeInset * 2)
        }
    }

    private func notchFrame(for presentation: WidgetPresentation, metrics: WidgetMetrics, screenFrame: CGRect) -> NSRect {
        let recorded = measured[Self.sizeKey(presentation)]
        let content = recorded?.size ?? fallback(for: presentation, metrics: metrics, screenFrame: screenFrame)

        // Never larger than the display -- a measured size that exceeds it
        // would put unreachable rows past the screen edge, and in a stacked
        // display arrangement could bleed onto the neighbour.
        let size = CGSize(
            width: min(content.width, screenFrame.width),
            height: min(content.height, screenFrame.height)
        )

        // Whole points, so the shape does not land on a half point and
        // soften its own edges. Width is rounded up to an *even* number too:
        // the frame is centred by subtracting half the width, so an odd
        // width leaves the window half a point off centre.
        let width = min((size.width / 2).rounded(.up) * 2, screenFrame.width)
        let height = size.height.rounded(.up)
        // The compact strip is positioned by its *gap* rather than centred,
        // so a badge growing on one side does not drag the rest sideways.
        // The expanded panel is centred as usual.
        let offset = presentation == .expanded ? 0 : (recorded?.gapOffset ?? 0)
        return NSRect(
            x: (screenFrame.midX + offset - width / 2).rounded(),
            y: (screenFrame.maxY - height).rounded(),
            width: width,
            height: height
        )
    }

    /// Never measured in this state yet. Generous so the content has room to
    /// lay out naturally -- the width is known without measuring
    /// (`ExpandedPanel` is a fixed width plus known padding); only the
    /// height has to be guessed, and for the compact strip that guess is
    /// anchored to the screen's own notch/menu-bar height rather than a
    /// mascot's, since this package draws no mascot.
    private func fallback(for presentation: WidgetPresentation, metrics: WidgetMetrics, screenFrame: CGRect) -> CGSize {
        let width = min(screenFrame.width, ExpandedPanel.panelWidth + 60)
        switch presentation {
        case .expanded:
            return CGSize(width: width, height: min(screenFrame.height * 0.92, 760))
        case .compact, .hidden, .dormant:
            return CGSize(width: min(screenFrame.width, 260), height: metrics.anchorSize.height + 28)
        }
    }
}
