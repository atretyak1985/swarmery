// The right-edge placement is exercised the same way the notch geometry is:
// as a pure function of CGRects, with no NSScreen or window server. These
// tests pin the operator-visible contract — a tab flush with the right
// screen edge, vertically centred in the visible frame, and a panel that
// never hides under the menu bar or the Dock.
import XCTest
@testable import SwarmeryNotch

final class WidgetPlacementTests: XCTestCase {
    private let screenFrame = CGRect(x: 0, y: 0, width: 1512, height: 982)
    // Menu bar (32pt) at the top and a Dock (70pt) at the bottom.
    private let visibleFrame = CGRect(x: 0, y: 70, width: 1512, height: 880)
    private let plainMetrics = WidgetMetrics(
        anchorSize: CGSize(width: 0, height: 24), isPhysicalNotch: false, menubarHeight: 24
    )

    // MARK: - Environment parsing

    func testDefaultPlacementIsTheRightEdge() {
        XCTAssertEqual(WidgetPlacement.default, .rightEdge)
        XCTAssertEqual(WidgetPlacement(environmentValue: nil), .rightEdge)
        XCTAssertEqual(WidgetPlacement(environmentValue: ""), .rightEdge)
        XCTAssertEqual(WidgetPlacement(environmentValue: "garbage"), .rightEdge)
    }

    func testNotchAndRightSpellingsParse() {
        XCTAssertEqual(WidgetPlacement(environmentValue: "notch"), .notch)
        XCTAssertEqual(WidgetPlacement(environmentValue: " NOTCH "), .notch)
        XCTAssertEqual(WidgetPlacement(environmentValue: "top"), .notch)
        XCTAssertEqual(WidgetPlacement(environmentValue: "right"), .rightEdge)
        XCTAssertEqual(WidgetPlacement(environmentValue: "right-edge"), .rightEdge)
    }

    // MARK: - Geometry

    func testCompactFallbackIsFlushWithTheRightEdgeAndCentredInTheVisibleFrame() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge)
        let frame = geometry.frame(
            for: .compact, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        XCTAssertEqual(frame.maxX, visibleFrame.maxX)
        XCTAssertEqual(frame.midY, visibleFrame.midY, accuracy: 1)
        XCTAssertEqual(frame.width, EdgeTab.size.width + NotchRootView.edgeInset * 2)
        XCTAssertEqual(frame.height, EdgeTab.size.height + NotchRootView.edgeInset * 2)
    }

    func testExpandedFallbackStaysInsideTheVisibleFrameAndFlushRight() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge)
        let frame = geometry.frame(
            for: .expanded, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        XCTAssertEqual(frame.maxX, visibleFrame.maxX)
        XCTAssertGreaterThanOrEqual(frame.minY, visibleFrame.minY)
        XCTAssertLessThanOrEqual(frame.maxY, visibleFrame.maxY)
        XCTAssertEqual(frame.width, ExpandedPanel.panelWidth + NotchRootView.edgeInset * 2)
    }

    func testAMeasuredPanelTallerThanTheVisibleFrameIsClampedToIt() {
        var geometry = WidgetWindowGeometry(placement: .rightEdge)
        geometry.record(WidgetContentGeometry(size: CGSize(width: 400, height: 5000)), for: .expanded)
        let frame = geometry.frame(
            for: .expanded, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        XCTAssertEqual(frame.minY, visibleFrame.minY)
        XCTAssertEqual(frame.height, visibleFrame.height)
        XCTAssertEqual(frame.maxX, visibleFrame.maxX)
    }

    func testAMeasuredPanelNearTheBottomIsShiftedUpRatherThanUnderTheDock() {
        var geometry = WidgetWindowGeometry(placement: .rightEdge)
        // Tall enough that centring on the anchor would push it below the Dock.
        geometry.record(WidgetContentGeometry(size: CGSize(width: 380, height: 860)), for: .expanded)
        let frame = geometry.frame(
            for: .expanded, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        XCTAssertGreaterThanOrEqual(frame.minY, visibleFrame.minY)
        XCTAssertLessThanOrEqual(frame.maxY, visibleFrame.maxY)
        XCTAssertEqual(frame.height, 860)
    }

    func testWithoutAVisibleFrameTheScreenFrameIsUsed() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge)
        let frame = geometry.frame(for: .compact, metrics: plainMetrics, screenFrame: screenFrame)
        XCTAssertEqual(frame.maxX, screenFrame.maxX)
        XCTAssertEqual(frame.midY, screenFrame.midY, accuracy: 1)
    }

    // MARK: - Tab tint

    func testTabTintReflectsTheWorstStateOnScreen() {
        XCTAssertEqual(AttentionState(isConnected: false).tabTint, .secondary)
        XCTAssertEqual(AttentionState(isConnected: true).tabTint, WidgetPalette.accent)
    }
}
