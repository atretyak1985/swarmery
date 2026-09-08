// How long presentation transitions take and what curve they follow.
//
// Adapted from notchling's WidgetTiming.swift (MIT license, see NOTICE.md);
// the debug slow-motion knob is renamed to this project's env-var namespace
// (SWARMERY_NOTCH_ANIM instead of notchling's own).
import SwiftUI

/// The three states the notch panel can be in: hidden (no window content
/// visible), compact (the strip), expanded (the full panel).
///
/// Defined here because `WidgetTiming` is the first ported file that needs
/// it. notchling defines the equivalent enum on its own view-state type,
/// which this package does not port (it also carries mascot/session-tree
/// state this package has no use for) -- see NOTICE.md.
public enum WidgetPresentation: Equatable, Sendable {
    case hidden
    case compact
    case expanded
}

public enum WidgetTiming {
    /// `SWARMERY_NOTCH_ANIM=6` slows every transition down 6x -- useful for
    /// visually inspecting a transition mid-flight.
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
        return presentation == .expanded ? 0.34 : 0.26
    }

    /// Opening springs, closing does not: a little overshoot on the way out
    /// reads as the panel dropping from the notch; the same overshoot on the
    /// way back in has nothing to drop into and reads as a wobble.
    public static func curve(to presentation: WidgetPresentation, hasPanel: Bool) -> Animation {
        let seconds = seconds(duration(to: presentation, hasPanel: hasPanel))
        guard hasPanel else {
            // First appearance: no previous size to travel from, so a spring
            // has nothing to say.
            return .smooth(duration: seconds)
        }
        return presentation == .expanded
            ? .bouncy(duration: seconds, extraBounce: 0.05)
            : .smooth(duration: seconds)
    }
}
