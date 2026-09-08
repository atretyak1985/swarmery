// Presentation states and the animation curves between them. Adapted from
// notchling's WidgetTiming (MIT, see NOTICE.md); `.dormant` and the
// `target(...)` state machine are ours.
import SwiftUI

public enum WidgetPresentation: Equatable, Sendable {
    /// No window at all (before the first apply / after teardown).
    case hidden
    /// Right-edge only: an invisible hot strip a few points wide at the screen
    /// edge. Nothing is drawn — the widget is out of the operator's way until
    /// the mouse touches the edge, exactly like Grammarly's tab.
    case dormant
    /// Notch: the strip in the menu-bar band. Right edge: the small tab.
    case compact
    /// The full panel.
    case expanded

    /// What the widget shows when nothing is happening and nobody is near it.
    public static func resting(for placement: WidgetPlacement) -> WidgetPresentation {
        placement == .rightEdge ? .dormant : .compact
    }

    /// The whole presentation decision, as a pure function so it can be
    /// tested without a window:
    /// - attention (a pending approval, a failed session, the linger window)
    ///   always wins and opens the panel;
    /// - a panel the operator pinned open by clicking the tab stays open;
    /// - otherwise the notch placement expands on hover (its strip is always
    ///   visible), while the right edge reveals only the tab on hover and
    ///   waits for a click to open the panel;
    /// - with no hover, each placement returns to its resting state.
    public static func target(
        placement: WidgetPlacement,
        attentionWantsOpen: Bool,
        pinnedOpen: Bool,
        hovering: Bool
    ) -> WidgetPresentation {
        if attentionWantsOpen || pinnedOpen { return .expanded }
        switch placement {
        case .notch:
            return hovering ? .expanded : .compact
        case .rightEdge:
            return hovering ? .compact : .dormant
        }
    }
}

public enum WidgetTiming {
    private static let factor: Double = {
        guard let raw = ProcessInfo.processInfo.environment["SWARMERY_NOTCH_ANIM"],
              let value = Double(raw), value > 0, value <= 60
        else { return 1 }
        return value
    }()

    public static func seconds(_ base: Double) -> Double { base * factor }
    public static func milliseconds(_ base: Int) -> Int { Int(Double(base) * factor) }

    public static func duration(to presentation: WidgetPresentation, hasPanel: Bool) -> Double {
        guard hasPanel else { return 0.28 }
        switch presentation {
        case .expanded: return 0.34
        case .dormant: return 0.18
        case .compact, .hidden: return 0.26
        }
    }

    public static func curve(to presentation: WidgetPresentation, hasPanel: Bool) -> Animation {
        let seconds = seconds(duration(to: presentation, hasPanel: hasPanel))
        guard hasPanel else {
            return .smooth(duration: seconds)
        }
        return presentation == .expanded
            ? .bouncy(duration: seconds, extraBounce: 0.05)
            : .smooth(duration: seconds)
    }
}
