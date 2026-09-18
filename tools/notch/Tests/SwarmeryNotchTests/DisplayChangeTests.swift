// What a display change has to do to the widget. Two separate failures were
// observed live on a machine that switches between the built-in display and
// an external monitor:
//
//   1. The right-edge frame is computed against `visibleFrame`, but the
//      change was gated on `WidgetMetrics`, which carries no frame at all --
//      so a resolution change with the same notch and the same menu bar
//      height left the window parked at a frame computed for the OLD screen.
//      Measured on the real machine: a 54pt-wide tab window sitting at the
//      6pt dormant anchor, i.e. drawn 48pt past the right edge of a 1728pt
//      screen -- invisible, and unreachable by the cursor.
//
//   2. The presenter set was built once at launch and never reconciled, so a
//      display plugged in later got no widget, and a display unplugged left
//      an orphan presenter whose screen is gone.
//
// Both are exercised here as pure value types -- no NSScreen, no window
// server -- matching the headless style of the rest of the suite.
import XCTest
@testable import SwarmeryNotch

final class DisplayChangeTests: XCTestCase {
    private let notchMetrics = WidgetMetrics(
        anchorSize: CGSize(width: 200, height: 32),
        isPhysicalNotch: true,
        menubarHeight: 32
    )

    // MARK: - 1. The change gate sees the frame, not just the metrics

    func testASnapshotChangesWhenOnlyTheVisibleFrameMoved() {
        // The exact shape of the live bug: same display, same notch, same
        // menu bar -- only the resolution changed. `WidgetMetrics` alone
        // cannot tell these apart, which is why it is the wrong gate.
        let before = ScreenSnapshot(
            metrics: notchMetrics,
            screenFrame: CGRect(x: 0, y: 0, width: 1728, height: 1117),
            visibleFrame: CGRect(x: 0, y: 61, width: 1728, height: 1022)
        )
        let after = ScreenSnapshot(
            metrics: notchMetrics,
            screenFrame: CGRect(x: 0, y: 0, width: 1512, height: 982),
            visibleFrame: CGRect(x: 0, y: 61, width: 1512, height: 887)
        )

        XCTAssertEqual(before.metrics, after.metrics, "precondition: the metrics are identical")
        XCTAssertNotEqual(before, after, "a moved visible frame must count as a change")
    }

    func testASnapshotChangesWhenOnlyTheDockChangedTheVisibleFrame() {
        // The screen itself is untouched; the Dock moved to the right edge,
        // which is exactly the edge the tab is glued to.
        let before = ScreenSnapshot(
            metrics: notchMetrics,
            screenFrame: CGRect(x: 0, y: 0, width: 1728, height: 1117),
            visibleFrame: CGRect(x: 0, y: 61, width: 1728, height: 1022)
        )
        let after = ScreenSnapshot(
            metrics: notchMetrics,
            screenFrame: CGRect(x: 0, y: 0, width: 1728, height: 1117),
            visibleFrame: CGRect(x: 0, y: 0, width: 1648, height: 1056)
        )

        XCTAssertNotEqual(before, after)
    }

    func testASnapshotIsUnchangedWhenNothingAboutTheScreenMoved() {
        // The gate still has to suppress no-op notifications -- macOS posts
        // didChangeScreenParameters for things that do not move this window.
        let snapshot = ScreenSnapshot(
            metrics: notchMetrics,
            screenFrame: CGRect(x: 0, y: 0, width: 1728, height: 1117),
            visibleFrame: CGRect(x: 0, y: 61, width: 1728, height: 1022)
        )

        XCTAssertEqual(snapshot, snapshot)
    }

    // MARK: - 2. Reconciling the presenter set against the live displays

    func testReconcilingPlansNoWorkWhenTheDisplaySetIsUnchanged() {
        let plan = DisplayReconciliation.plan(existing: [1, 2], current: [1, 2])

        XCTAssertEqual(plan.added, [])
        XCTAssertEqual(plan.removed, [])
        XCTAssertFalse(plan.hasWork)
    }

    func testReconcilingAddsADisplayThatWasPluggedIn() {
        let plan = DisplayReconciliation.plan(existing: [1], current: [1, 7])

        XCTAssertEqual(plan.added, [7])
        XCTAssertEqual(plan.removed, [])
        XCTAssertTrue(plan.hasWork)
    }

    func testReconcilingRemovesADisplayThatWasUnplugged() {
        let plan = DisplayReconciliation.plan(existing: [1, 7], current: [1])

        XCTAssertEqual(plan.added, [])
        XCTAssertEqual(plan.removed, [7])
        XCTAssertTrue(plan.hasWork)
    }

    func testReconcilingHandlesEveryDisplayBeingSwappedAtOnce() {
        // Closing the lid while docked: the built-in goes away and the
        // external arrives in the same notification.
        let plan = DisplayReconciliation.plan(existing: [1], current: [7])

        XCTAssertEqual(plan.added, [7])
        XCTAssertEqual(plan.removed, [1])
    }

    func testReconcilingKeepsTheOrderOfTheLiveDisplayList() {
        // Deterministic ordering keeps the presenter list stable across
        // reconciliations rather than reshuffling on every notification.
        let plan = DisplayReconciliation.plan(existing: [], current: [3, 1, 2])

        XCTAssertEqual(plan.added, [3, 1, 2])
    }
}
