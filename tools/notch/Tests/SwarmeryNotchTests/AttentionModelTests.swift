// AttentionModel is a pure reducer, so every test here is
// state = reduce(state, event) + an assertion — no sleeping, no daemon, no
// clock. `now`/`at` are always literal Date values the test controls.
import XCTest
@testable import SwarmeryNotch

final class AttentionModelTests: XCTestCase {

    private func makeSession(id: Int, status: String = "active", procState: String? = "running") -> Session {
        Session(id: id, sessionUuid: "uuid-\(id)", projectName: "proj", status: status, title: "t", why: nil, procState: procState)
    }

    private func makeApproval(id: Int, sessionId: Int, status: String = "pending") -> PermissionRequest {
        PermissionRequest(
            id: id, sessionId: sessionId, toolName: "Bash", requestJson: "{}", status: status,
            requestedAt: "2026-01-01T00:00:00Z", resolvedAt: nil, resolvedVia: nil, reason: nil,
            expiresAt: "2026-01-01T00:02:00Z"
        )
    }

    // MARK: - pending approval ⇒ shouldExpand

    func testPendingApprovalExpandsThePanel() {
        let state = AttentionModel.reduce(AttentionState(), .permissionRequested(makeApproval(id: 1, sessionId: 10)))

        XCTAssertTrue(state.needsAttention)
        XCTAssertTrue(state.shouldExpand(now: Date(), linger: 3))
    }

    func testPendingApprovalSortsItsSessionIntoNeedsYou() {
        var state = AttentionModel.reduce(AttentionState(), .snapshot(sessions: [makeSession(id: 10)], approvals: [], usage: nil))
        state = AttentionModel.reduce(state, .permissionRequested(makeApproval(id: 1, sessionId: 10)))

        XCTAssertEqual(state.rows.first?.bucket, .needsYou)
    }

    // MARK: - resolved ⇒ collapses after linger

    func testResolvedApprovalCollapsesOnlyAfterTheLingerWindow() {
        var state = AttentionModel.reduce(AttentionState(), .permissionRequested(makeApproval(id: 1, sessionId: 10)))
        let resolvedAt = Date(timeIntervalSince1970: 1_000)
        state = AttentionModel.reduce(
            state, .permissionResolved(makeApproval(id: 1, sessionId: 10, status: "approved"), at: resolvedAt)
        )

        XCTAssertFalse(state.needsAttention)
        XCTAssertTrue(state.pendingApprovals.isEmpty)
        // Still inside the linger window: stays expanded.
        XCTAssertTrue(state.shouldExpand(now: resolvedAt.addingTimeInterval(1), linger: 3))
        // Past the linger window: collapses.
        XCTAssertFalse(state.shouldExpand(now: resolvedAt.addingTimeInterval(4), linger: 3))
    }

    func testANewerAttentionEventDuringTheLingerWindowKeepsItExpanded() {
        var state = AttentionModel.reduce(AttentionState(), .permissionRequested(makeApproval(id: 1, sessionId: 10)))
        let resolvedAt = Date(timeIntervalSince1970: 1_000)
        state = AttentionModel.reduce(
            state, .permissionResolved(makeApproval(id: 1, sessionId: 10, status: "approved"), at: resolvedAt)
        )
        state = AttentionModel.reduce(state, .permissionRequested(makeApproval(id: 2, sessionId: 11)))

        XCTAssertTrue(state.shouldExpand(now: resolvedAt.addingTimeInterval(10), linger: 3))
    }

    // MARK: - error session ⇒ expand

    func testDeadProcessOnAnActiveSessionExpandsThePanel() {
        let errored = makeSession(id: 5, status: "active", procState: "dead")
        let state = AttentionModel.reduce(AttentionState(), .snapshot(sessions: [errored], approvals: [], usage: nil))

        XCTAssertTrue(state.needsAttention)
        XCTAssertEqual(state.rows.first?.bucket, .error)
        XCTAssertTrue(state.shouldExpand(now: Date(), linger: 3))
    }

    func testOrphanedProcStateIsNotTreatedAsAnError() {
        // Headless/background runs routinely report `orphaned` — see the
        // comment on AttentionState.erroredSessions. This must NOT expand.
        let orphaned = makeSession(id: 6, status: "active", procState: "orphaned")
        let state = AttentionModel.reduce(AttentionState(), .snapshot(sessions: [orphaned], approvals: [], usage: nil))

        XCTAssertFalse(state.needsAttention)
        XCTAssertEqual(state.rows.first?.bucket, .working)
        XCTAssertFalse(state.shouldExpand(now: Date(), linger: 3))
    }

    // MARK: - reconnect ⇒ snapshot replaces

    func testSnapshotReplacesRatherThanMergesStaleState() {
        var state = AttentionModel.reduce(AttentionState(), .permissionRequested(makeApproval(id: 1, sessionId: 10)))
        state = AttentionModel.reduce(state, .sessionUpdated(makeSession(id: 10)))
        XCTAssertEqual(state.pendingApprovals.count, 1)
        XCTAssertEqual(state.sessions.count, 1)

        // A fresh snapshot after reconnect REPLACES state — the daemon never
        // replays frames missed while offline (docs/ws-protocol.md).
        state = AttentionModel.reduce(state, .snapshot(sessions: [], approvals: [], usage: nil))

        XCTAssertTrue(state.pendingApprovals.isEmpty)
        XCTAssertTrue(state.sessions.isEmpty)
        XCTAssertFalse(state.needsAttention)
    }

    func testSnapshotOnlyKeepsApprovalsStillPending() {
        // A snapshot can legitimately include resolved rows (the REST
        // endpoint's default filters to pending, but the reducer must not
        // assume that — it filters defensively itself).
        let stillPending = makeApproval(id: 1, sessionId: 10, status: "pending")
        let alreadyResolved = makeApproval(id: 2, sessionId: 11, status: "approved")
        let state = AttentionModel.reduce(AttentionState(), .snapshot(sessions: [], approvals: [stillPending, alreadyResolved], usage: nil))

        XCTAssertEqual(state.pendingApprovals.count, 1)
        XCTAssertNotNil(state.pendingApprovals[1])
        XCTAssertNil(state.pendingApprovals[2])
    }

    // MARK: - rows ordering

    func testRowsSortNeedsYouThenErrorThenWorkingThenIdle() {
        let idle = makeSession(id: 1, status: "completed", procState: "dead")
        let working = makeSession(id: 2, status: "active", procState: "running")
        let errored = makeSession(id: 3, status: "active", procState: "dead")
        let needsYou = makeSession(id: 4, status: "waiting_approval", procState: "running")
        var state = AttentionModel.reduce(
            AttentionState(), .snapshot(sessions: [idle, working, errored, needsYou], approvals: [], usage: nil)
        )
        state = AttentionModel.reduce(state, .permissionRequested(makeApproval(id: 100, sessionId: 4)))

        XCTAssertEqual(state.rows.map(\.bucket), [.needsYou, .error, .working, .idle])
    }
}
