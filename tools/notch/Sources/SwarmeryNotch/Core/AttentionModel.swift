// Pure reducer turning daemon frames into "what the panel shows and whether
// it should be open". No Foundation timers, no internal clock: every time
// value the model needs (a resolution instant, "now") is passed in by the
// caller, so tests are fully deterministic and never sleep.
import Foundation

/// Everything the panel needs to render, keyed for O(1) upsert/remove on the
/// frames that arrive one at a time over the WS stream.
public struct AttentionState: Equatable, Sendable {
    public var sessions: [Int: Session]
    public var pendingApprovals: [Int: PermissionRequest]
    public var usage: UsageReport?
    public var isConnected: Bool
    /// Wall-clock instant (always caller-supplied, never `Date()` from inside
    /// this type) the most recent approval left `pending` — the anchor
    /// `shouldExpand(now:linger:)` uses to keep the panel open for a grace
    /// period after the user acts, so the resolution is visible before the
    /// panel auto-collapses.
    public var lastResolvedAt: Date?

    public init(
        sessions: [Int: Session] = [:],
        pendingApprovals: [Int: PermissionRequest] = [:],
        usage: UsageReport? = nil,
        isConnected: Bool = false,
        lastResolvedAt: Date? = nil
    ) {
        self.sessions = sessions
        self.pendingApprovals = pendingApprovals
        self.usage = usage
        self.isConnected = isConnected
        self.lastResolvedAt = lastResolvedAt
    }
}

/// The frames the reducer knows how to fold into state. `.snapshot` is the
/// only event that REPLACES rather than merges — see `AttentionModel.reduce`.
public enum AttentionEvent: Sendable {
    /// A REST snapshot (DaemonClient.snapshot()) — always issued by the owner
    /// on `.connected` (docs/ws-protocol.md: the daemon never replays frames
    /// missed while offline, so this REPLACES `sessions`/`pendingApprovals`
    /// rather than merging into them).
    case snapshot(sessions: [Session], approvals: [PermissionRequest], usage: UsageReport?)
    case sessionUpdated(Session)
    case permissionRequested(PermissionRequest)
    /// `at` is the instant the resolution was observed — always caller-
    /// supplied (the WS frame's arrival time, or `Date()` read by the owner,
    /// never by this type).
    case permissionResolved(PermissionRequest, at: Date)
    case connectionChanged(Bool)
    /// A fresh `GET /api/usage` — the only part of the snapshot that has no
    /// WS frame of its own, so it is re-fetched on a timer and on demand.
    case usageUpdated(UsageReport?)
}

/// A stateless reducer: `reduce(state, event) -> newState`. No singletons, no
/// shared mutable state — safe to call from anywhere, and trivial to unit
/// test frame-by-frame.
public enum AttentionModel {
    public static func reduce(_ state: AttentionState, _ event: AttentionEvent) -> AttentionState {
        var next = state
        switch event {
        case let .snapshot(sessions, approvals, usage):
            next.sessions = Dictionary(uniqueKeysWithValues: sessions.map { ($0.id, $0) })
            next.pendingApprovals = Dictionary(
                uniqueKeysWithValues: approvals.filter { $0.status == "pending" }.map { ($0.id, $0) }
            )
            next.usage = usage
        case let .sessionUpdated(session):
            next.sessions[session.id] = session
        case let .permissionRequested(request):
            next.pendingApprovals[request.id] = request
        case let .permissionResolved(request, at):
            next.pendingApprovals.removeValue(forKey: request.id)
            next.lastResolvedAt = at
        case let .usageUpdated(usage):
            var next = state
            next.usage = usage
            return next
        case let .connectionChanged(isConnected):
            next.isConnected = isConnected
        }
        return next
    }
}

extension AttentionState {
    /// A session counts as errored when its process is confirmed DEAD while
    /// the daemon still reports the session `active` — a genuine "something
    /// broke and the dashboard hasn't caught up yet" signal.
    ///
    /// [VERIFY] "orphaned" is deliberately EXCLUDED: capturing this phase's
    /// fixtures against the live daemon showed both real active sessions
    /// reporting `procState: "orphaned"` — the normal state for a headless
    /// background run (`claude -p`), not a failure. Only "dead" is treated as
    /// an error; the phase doc's "sessions in error/failed" language was not
    /// more precisely specified against this trimmed Session projection, so
    /// this predicate is a judgment call — reviewer should confirm it matches
    /// intent before phase 3 wires it to a visual treatment.
    public var erroredSessions: [Session] {
        sessions.values.filter { $0.status == "active" && $0.procState == "dead" }
    }

    public var needsAttention: Bool {
        !pendingApprovals.isEmpty || !erroredSessions.isEmpty
    }

    /// Sort order for `rows`: the user's own action items first, then genuine
    /// failures, then sessions actively working, then everything at rest.
    public enum RowBucket: Int, Equatable, Comparable, Sendable {
        case needsYou = 0
        case error = 1
        case working = 2
        case idle = 3

        public static func < (lhs: RowBucket, rhs: RowBucket) -> Bool { lhs.rawValue < rhs.rawValue }
    }

    public struct Row: Identifiable, Equatable, Sendable {
        public let session: Session
        public let bucket: RowBucket
        public var id: Int { session.id }
    }

    /// One row per known session, sorted needs-you → error → working → idle,
    /// stable by session id within a bucket.
    public var rows: [Row] {
        let sessionIDsAwaitingApproval = Set(pendingApprovals.values.map { $0.sessionId })
        return sessions.values
            .map { session in
                Row(session: session, bucket: bucket(for: session, awaitingApproval: sessionIDsAwaitingApproval.contains(session.id)))
            }
            .sorted { lhs, rhs in
                lhs.bucket != rhs.bucket ? lhs.bucket < rhs.bucket : lhs.session.id < rhs.session.id
            }
    }

    private func bucket(for session: Session, awaitingApproval: Bool) -> RowBucket {
        if awaitingApproval { return .needsYou }
        if session.status == "active" && session.procState == "dead" { return .error }
        if session.status == "active" || session.status == "waiting_approval" { return .working }
        return .idle
    }

    /// The panel opens immediately for anything that needs the user, and
    /// stays open for `linger` seconds after the most recent resolution so
    /// the outcome is visible before auto-collapsing. `now` is always
    /// caller-supplied — this type owns no clock of its own.
    public func shouldExpand(now: Date, linger: TimeInterval) -> Bool {
        if needsAttention { return true }
        if let lastResolvedAt, now.timeIntervalSince(lastResolvedAt) < linger {
            return true
        }
        return false
    }
}
