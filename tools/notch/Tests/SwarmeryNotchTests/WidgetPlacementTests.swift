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

    func testCompactFallbackIsFlushWithTheRightEdgeAndCentredOnTheAnchor() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge, edgeAnchorFromBottom: 0.5)
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
        let geometry = WidgetWindowGeometry(placement: .rightEdge, edgeAnchorFromBottom: 0.5)
        let frame = geometry.frame(for: .compact, metrics: plainMetrics, screenFrame: screenFrame)
        XCTAssertEqual(frame.maxX, screenFrame.maxX)
        XCTAssertEqual(frame.midY, screenFrame.midY, accuracy: 1)
    }

    // MARK: - Anchor (stays clear of Grammarly's mid-height tab)

    func testDefaultAnchorSitsAboveTheVerticalMiddle() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge)
        let frame = geometry.frame(
            for: .compact, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        let expectedMidY = visibleFrame.minY + visibleFrame.height * 0.7
        XCTAssertEqual(frame.midY, expectedMidY, accuracy: 1)
        XCTAssertGreaterThan(frame.minY, visibleFrame.midY + 40)
    }

    func testAnchorEnvironmentIsAFractionFromTheTopAndClampedToTheDefaultWhenInvalid() {
        XCTAssertEqual(WidgetWindowGeometry.edgeAnchorFromBottom(environmentValue: "0.3"), 0.7, accuracy: 0.0001)
        XCTAssertEqual(WidgetWindowGeometry.edgeAnchorFromBottom(environmentValue: "0.9"), 0.1, accuracy: 0.0001)
        XCTAssertEqual(WidgetWindowGeometry.edgeAnchorFromBottom(environmentValue: nil), 0.7)
        XCTAssertEqual(WidgetWindowGeometry.edgeAnchorFromBottom(environmentValue: "1.5"), 0.7)
        XCTAssertEqual(WidgetWindowGeometry.edgeAnchorFromBottom(environmentValue: "abc"), 0.7)
    }

    // MARK: - Dormant hot strip

    func testDormantFrameIsAThinStripFlushWithTheRightEdgeAtTheAnchor() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge, edgeAnchorFromBottom: 0.5)
        let frame = geometry.frame(
            for: .dormant, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        XCTAssertEqual(frame.width, WidgetWindowGeometry.hotStripWidth)
        XCTAssertEqual(frame.maxX, visibleFrame.maxX)
        XCTAssertEqual(frame.midY, visibleFrame.midY, accuracy: 1)
    }

    func testDormantHasItsOwnSizeKey() {
        XCTAssertEqual(WidgetWindowGeometry.sizeKey(.dormant), .dormant)
        XCTAssertEqual(WidgetWindowGeometry.sizeKey(.hidden), .compact)
    }

    // MARK: - Presentation state machine

    func testRightEdgeRestsDormantRevealsTheTabOnHoverAndOpensOnClickOrAttention() {
        XCTAssertEqual(WidgetPresentation.resting(for: .rightEdge), .dormant)
        XCTAssertEqual(
            WidgetPresentation.target(placement: .rightEdge, attentionWantsOpen: false, pinnedOpen: false, hovering: false),
            .dormant
        )
        XCTAssertEqual(
            WidgetPresentation.target(placement: .rightEdge, attentionWantsOpen: false, pinnedOpen: false, hovering: true),
            .compact
        )
        XCTAssertEqual(
            WidgetPresentation.target(placement: .rightEdge, attentionWantsOpen: false, pinnedOpen: true, hovering: false),
            .expanded
        )
        XCTAssertEqual(
            WidgetPresentation.target(placement: .rightEdge, attentionWantsOpen: true, pinnedOpen: false, hovering: false),
            .expanded
        )
    }

    func testNotchKeepsItsAlwaysVisibleStripAndHoverToExpand() {
        XCTAssertEqual(WidgetPresentation.resting(for: .notch), .compact)
        XCTAssertEqual(
            WidgetPresentation.target(placement: .notch, attentionWantsOpen: false, pinnedOpen: false, hovering: false),
            .compact
        )
        XCTAssertEqual(
            WidgetPresentation.target(placement: .notch, attentionWantsOpen: false, pinnedOpen: false, hovering: true),
            .expanded
        )
    }

    // MARK: - Tab tint

    func testTabTintReflectsTheWorstStateOnScreen() {
        XCTAssertEqual(AttentionState(isConnected: false).tabTint, .secondary)
        XCTAssertEqual(AttentionState(isConnected: true).tabTint, WidgetPalette.accent)
    }
}
