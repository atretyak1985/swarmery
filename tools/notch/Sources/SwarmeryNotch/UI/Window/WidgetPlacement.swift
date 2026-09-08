// Where the widget lives on each display. `.rightEdge` is the default: a
// small tab docked to the right screen edge (rounded on the left, flush on
// the right) that grows into a side panel at the same height — the shape
// operators already know from tools like Grammarly's desktop widget, and one
// that stays visible against a dark IDE. `.notch` keeps the original
// top-edge/notch presentation as an opt-in via `SWARMERY_NOTCH_PLACEMENT`.
import Foundation

public enum WidgetPlacement: String, Equatable, Sendable {
    case notch
    case rightEdge = "right"

    public static let `default`: WidgetPlacement = .rightEdge

    /// Parses `SWARMERY_NOTCH_PLACEMENT`. Unknown or empty values fall back
    /// to the default rather than failing — a typo in a LaunchAgent plist
    /// must never leave the operator without a widget.
    public init(environmentValue raw: String?) {
        let value = raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() ?? ""
        switch value {
        case "notch", "top":
            self = .notch
        case "right", "right-edge", "edge":
            self = .rightEdge
        default:
            self = .default
        }
    }

    public static func fromEnvironment() -> WidgetPlacement {
        WidgetPlacement(environmentValue: ProcessInfo.processInfo.environment["SWARMERY_NOTCH_PLACEMENT"])
    }
}
