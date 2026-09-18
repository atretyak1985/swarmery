// Everything about one screen that the widget's window frame depends on --
// and therefore everything a `didChangeScreenParameters` notification has to
// be compared against before deciding the window can stay where it is.
//
// `WidgetMetrics` is deliberately NOT that comparison. It describes the notch
// and the menu bar, which is all the notch placement needs, but the
// right-edge placement computes its frame from `visibleFrame`
// (`WidgetWindowGeometry.edgeFrame`) -- so a resolution change that leaves
// the notch and menu bar untouched changes nothing in `WidgetMetrics` while
// moving the very edge the tab is glued to.
import AppKit

public struct ScreenSnapshot: Equatable, Sendable {
    /// What the notch placement draws against.
    public let metrics: WidgetMetrics
    /// The whole display -- the notch placement's frame is measured from it.
    public let screenFrame: CGRect
    /// The display minus menu bar and Dock -- the right-edge placement's
    /// frame is measured from it.
    public let visibleFrame: CGRect

    public init(metrics: WidgetMetrics, screenFrame: CGRect, visibleFrame: CGRect) {
        self.metrics = metrics
        self.screenFrame = screenFrame
        self.visibleFrame = visibleFrame
    }

    @MainActor
    public init(screen: NSScreen) {
        self.init(
            metrics: WidgetMetrics(screen: screen),
            screenFrame: screen.frame,
            visibleFrame: screen.visibleFrame
        )
    }
}

/// Which displays gained or lost a presenter since the last look.
///
/// The presenter set used to be built once at launch, so a display plugged in
/// afterwards never got a widget and a display unplugged left a presenter
/// holding a window on a screen that no longer exists.
public enum DisplayReconciliation {
    public struct Plan: Equatable, Sendable {
        public let added: [CGDirectDisplayID]
        public let removed: [CGDirectDisplayID]

        public init(added: [CGDirectDisplayID], removed: [CGDirectDisplayID]) {
            self.added = added
            self.removed = removed
        }

        public var hasWork: Bool { !added.isEmpty || !removed.isEmpty }
    }

    /// `added` follows the order of `current` so the presenter list stays in
    /// the window server's own display order rather than reshuffling on every
    /// notification.
    public static func plan(existing: [CGDirectDisplayID], current: [CGDirectDisplayID]) -> Plan {
        let existingSet = Set(existing)
        let currentSet = Set(current)
        return Plan(
            added: current.filter { !existingSet.contains($0) },
            removed: existing.filter { !currentSet.contains($0) }
        )
    }
}
