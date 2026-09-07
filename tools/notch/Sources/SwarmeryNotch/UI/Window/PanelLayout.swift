// What the expanded panel shows, and what it says about the rest.
//
// Adapted from notchling's PanelLayout.swift (MIT license, see NOTICE.md).
// The idea ported is the row-capping design: a session list without a cap
// runs off the bottom of the screen once there are enough rows, and stops
// being glanceable well before that. The row *shape* is different --
// notchling caps a session/subagent tree; this package has no subagent
// concept, so a row here is either a pending approval or one of
// `AttentionState`'s already-sorted session rows.
import Foundation

/// One line in the expanded panel.
public enum PanelRow: Identifiable, Equatable {
    case approval(PermissionRequest)
    case session(AttentionState.Row)

    public var id: String {
        switch self {
        case let .approval(request): "a:\(request.id)"
        case let .session(row): "s:\(row.id)"
        }
    }
}

public struct PanelLayout {
    /// Measured against the same problem notchling documented: a panel with
    /// too many rows runs off the bottom of the screen and stops being
    /// glanceable well before the rows become literally unreachable.
    public static let maximumRows = 12

    public let rows: [PanelRow]
    public let hiddenSessionCount: Int
    private let hiddenSessionsAllIdle: Bool

    /// Approvals are never trimmed -- they are the reason the panel opened.
    /// Only session rows are capped once the budget is spent.
    public init(approvals: [PermissionRequest], sessionRows: [AttentionState.Row], limit: Int = Self.maximumRows) {
        let approvalRows = approvals.map(PanelRow.approval)
        let sessionBudget = max(0, limit - approvalRows.count)
        let shownSessions = Array(sessionRows.prefix(sessionBudget))
        let hiddenSessions = Array(sessionRows.dropFirst(sessionBudget))

        rows = approvalRows + shownSessions.map(PanelRow.session)
        hiddenSessionCount = hiddenSessions.count
        hiddenSessionsAllIdle = hiddenSessions.allSatisfy { $0.bucket == .idle }
    }

    /// "3 more idle" and "3 more" are different promises: one says nothing
    /// hidden needs action, the other admits something might.
    public var summary: String? {
        guard hiddenSessionCount > 0 else { return nil }
        return "\(hiddenSessionCount) more\(hiddenSessionsAllIdle ? " idle" : "")"
    }
}
