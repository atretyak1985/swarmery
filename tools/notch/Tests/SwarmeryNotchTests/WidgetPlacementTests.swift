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
        XCTAssertEqual(frame.width, EdgeTabs.size.width + NotchRootView.edgeInset)
        XCTAssertEqual(frame.height, EdgeTabs.size.height + NotchRootView.edgeInset * 2)
        XCTAssertEqual(EdgeTabs.size.height, EdgeTab.size.height * 2 + EdgeTabs.gap)
    }

    func testExpandedPanelHangsFromTheTabsTopEdge() {
        var geometry = WidgetWindowGeometry(placement: .rightEdge, edgeAnchorFromBottom: 0.5)
        geometry.record(WidgetContentGeometry(size: CGSize(width: 370, height: 300)), for: .expanded)
        let tab = geometry.frame(for: .compact, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame)
        let panel = geometry.frame(for: .expanded, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame)
        XCTAssertEqual(panel.maxY, tab.maxY, accuracy: 1)
        XCTAssertEqual(panel.maxX, visibleFrame.maxX)
        XCTAssertEqual(panel.height, 300)
    }

    func testExpandedFallbackStaysInsideTheVisibleFrameAndFlushRight() {
        let geometry = WidgetWindowGeometry(placement: .rightEdge)
        let frame = geometry.frame(
            for: .expanded, metrics: plainMetrics, screenFrame: screenFrame, visibleFrame: visibleFrame
        )
        XCTAssertEqual(frame.maxX, visibleFrame.maxX)
        XCTAssertGreaterThanOrEqual(frame.minY, visibleFrame.minY)
        XCTAssertLessThanOrEqual(frame.maxY, visibleFrame.maxY)
        XCTAssertEqual(frame.width, ExpandedPanel.panelWidth + NotchRootView.edgeInset)
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

    // MARK: - Usage panel view model

    private func usage(percents: [Double], status: String = "ok", accounts: [String] = ["default"]) -> UsageReport {
        let windows = percents.enumerated().map { index, percent in
            UsageWindow(
                key: "w\(index)", label: "Window \(index)", percentUsed: percent, percentLeft: 100 - percent,
                resetText: "resets in 1h", resetMs: 3_600_000, resetAt: "2030-01-01T12:00:00Z",
                windowDurationMs: 18_000_000,
                pace: UsagePace(status: "behind", percentElapsed: 50, message: "10% under pace"),
                source: "oauth", used: nil, limit: nil
            )
        }
        let providers = accounts.map {
            UsageProvider(account: $0, name: "Claude", status: status, plan: "Max", source: "oauth", windows: windows)
        }
        return UsageReport(
            generatedAt: "2030-01-01T10:00:00Z",
            providers: providers,
            accounts: accounts.map { name in UsageAccount(account: name, providers: providers.filter { $0.account == name }) }
        )
    }

    func testUsageBarTintFollowsTheDashboardThresholds() {
        XCTAssertEqual(UsagePanelViewModel.tint(forPercentUsed: 8), .green)
        XCTAssertEqual(UsagePanelViewModel.tint(forPercentUsed: 69.9), .green)
        XCTAssertEqual(UsagePanelViewModel.tint(forPercentUsed: 70), .orange)
        XCTAssertEqual(UsagePanelViewModel.tint(forPercentUsed: 90), .red)
    }

    func testUsagePanelGroupsByAccountAndCarriesPaceAndResetClock() {
        let model = UsagePanelViewModel(usage: usage(percents: [8, 29], accounts: ["default", "nanitor"]))
        XCTAssertEqual(model?.accounts.map(\.id), ["default", "nanitor"])
        let window = model?.accounts[0].providers[0].windows[1]
        XCTAssertEqual(window?.percentUsed, 29)
        XCTAssertEqual(window?.pace, "10% under pace")
        XCTAssertEqual(window?.resetText, "resets in 1h")
        XCTAssertNotNil(window?.resetClock)
        XCTAssertNotNil(model?.generatedClock)
    }

    func testUsagePanelIsNilWithoutDataOrWithOnlyUnauthenticatedProviders() {
        XCTAssertNil(UsagePanelViewModel(usage: nil))
        XCTAssertNil(UsagePanelViewModel(usage: usage(percents: [50], status: "no-auth")))
    }

    func testWorstUsagePercentDrivesTheUsageTab() {
        var state = AttentionState(isConnected: true)
        XCTAssertNil(state.worstUsagePercent)
        state.usage = usage(percents: [8, 29, 19])
        XCTAssertEqual(state.worstUsagePercent, 29)
    }
}
