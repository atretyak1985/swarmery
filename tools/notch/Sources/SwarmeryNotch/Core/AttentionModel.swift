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
    ///
    /// An `awaiting_reply` session whose process is dead is errored too: the
    /// daemon flips such rows to `completed` within one procwatch tick, but a
    /// stale snapshot must not ask the operator to reply to a dead process.
    public var erroredSessions: [Session] {
        sessions.values.filter(Self.isErrored)
    }

    /// Sessions that ended their turn and wait for a plain-text reply from
    /// the operator (daemon status `awaiting_reply`), excluding dead processes
    /// — those count as `erroredSessions` instead.
    public var awaitingReplySessions: [Session] {
        sessions.values.filter(Self.isAwaitingReply)
    }

    public var needsAttention: Bool {
        !pendingApprovals.isEmpty || !erroredSessions.isEmpty || !awaitingReplySessions.isEmpty
    }

    private static func isErrored(_ session: Session) -> Bool {
        (session.status == "active" || session.status == "awaiting_reply") && session.procState == "dead"
    }

    private static func isAwaitingReply(_ session: Session) -> Bool {
        session.status == "awaiting_reply" && session.procState != "dead"
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
        if Self.isErrored(session) { return .error }
        if session.status == "awaiting_reply" { return .needsYou }
        if session.status == "active" || session.status == "waiting_approval" { return .working }
        return .idle
    }

    /// Everything currently asking for the operator, as identities that
    /// survive from one frame to the next — what `AttentionDismissal`
    /// remembers the operator has already seen.
    public var attentionItems: Set<AttentionItem> {
        var items = Set(pendingApprovals.keys.map(AttentionItem.approval))
        for session in sessions.values {
            if Self.isErrored(session) {
                items.insert(.errored(session.id))
            } else if Self.isAwaitingReply(session) {
                items.insert(.awaitingReply(session.id))
            }
        }
        return items
    }

    /// The panel opens immediately for anything that needs the user, and
    /// stays open for `linger` seconds after the most recent resolution so
    /// the outcome is visible before auto-collapsing. `now` is always
    /// caller-supplied — this type owns no clock of its own.
    ///
    /// What the operator closed by hand stays closed: an item already in
    /// `dismissal` no longer opens the panel, and neither does the linger
    /// window of a resolution they dismissed. Anything new still does.
    public func shouldExpand(
        now: Date,
        linger: TimeInterval,
        dismissal: AttentionDismissal = AttentionDismissal()
    ) -> Bool {
        if !dismissal.covers(self) { return true }
        if let lastResolvedAt, lastResolvedAt != dismissal.resolvedAt,
           now.timeIntervalSince(lastResolvedAt) < linger {
            return true
        }
        return false
    }
}

/// One thing that asks for the operator. The same session can ask twice — a
/// reply, then another question — so identity alone is not enough; see
/// `AttentionDismissal.observe`.
public enum AttentionItem: Hashable, Sendable {
    case approval(Int)
    case awaitingReply(Int)
    case errored(Int)
}

/// What the operator closed the panel on. A session can wait for a reply for
/// hours, so "attention always wins" alone would leave the panel's × button
/// doing nothing for as long as one such session exists. A value type with no
/// clock, like the rest of this file.
public struct AttentionDismissal: Equatable, Sendable {
    public private(set) var items: Set<AttentionItem> = []
    /// `AttentionState.lastResolvedAt` at the moment of the dismissal, so the
    /// linger window of that same resolution does not reopen the panel.
    public private(set) var resolvedAt: Date?

    public init() {}

    /// True when nothing in `state` asks for the operator that they have not
    /// already closed the panel on.
    public func covers(_ state: AttentionState) -> Bool {
        state.attentionItems.isSubset(of: items)
    }

    /// The operator closed the panel while `state` was on screen.
    public mutating func dismiss(_ state: AttentionState) {
        items = state.attentionItems
        resolvedAt = state.lastResolvedAt
    }

    /// Call on every new state: forgets items that are no longer asking, so a
    /// session that asks again later opens the panel again.
    public mutating func observe(_ state: AttentionState) {
        items.formIntersection(state.attentionItems)
    }
}
