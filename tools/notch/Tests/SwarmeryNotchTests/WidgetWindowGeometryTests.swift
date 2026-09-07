// WidgetWindowGeometry is exercised as a pure function of value types --
// CGRect/WidgetMetrics -- so these tests never touch a real NSScreen or the
// window server, matching the phase's headless requirement.
import XCTest
@testable import SwarmeryNotch

final class WidgetWindowGeometryTests: XCTestCase {
    private let screenFrame = CGRect(x: 0, y: 0, width: 1512, height: 982)

    private let notchMetrics = WidgetMetrics(
        anchorSize: CGSize(width: 200, height: 32),
        isPhysicalNotch: true,
        menubarHeight: 32
    )

    private let plainMetrics = WidgetMetrics(
        anchorSize: CGSize(width: 0, height: 24),
        isPhysicalNotch: false,
        menubarHeight: 24
    )

    // MARK: - Notch display

    func testCompactFallbackOnANotchDisplaySitsAtTheTopEdgeCenteredHorizontally() {
        let geometry = WidgetWindowGeometry()
        let frame = geometry.frame(for: .compact, metrics: notchMetrics, screenFrame: screenFrame)

        XCTAssertEqual(frame.maxY, screenFrame.maxY, accuracy: 0.5)
        XCTAssertEqual(frame.midX, screenFrame.midX, accuracy: 1)
        XCTAssertLessThanOrEqual(frame.width, screenFrame.width)
        XCTAssertGreaterThan(frame.height, 0)
    }

    // MARK: - Plain display (top-edge fallback)

    func testCompactFallbackOnAPlainDisplayAlsoSitsAtTheTopEdgeCenteredHorizontally() {
        let geometry = WidgetWindowGeometry()
        let frame = geometry.frame(for: .compact, metrics: plainMetrics, screenFrame: screenFrame)

        XCTAssertEqual(frame.maxY, screenFrame.maxY, accuracy: 0.5)
        XCTAssertEqual(frame.midX, screenFrame.midX, accuracy: 1)
    }

    func testAPlainDisplayFallsBackToTheMenuBarHeightRatherThanANotchHeight() {
        let geometry = WidgetWindowGeometry()
        let notchFrame = geometry.frame(for: .compact, metrics: notchMetrics, screenFrame: screenFrame)
        let plainFrame = geometry.frame(for: .compact, metrics: plainMetrics, screenFrame: screenFrame)

        // The fallback height is anchored to the screen's own anchor height
        // (notch or menu bar) plus fixed padding -- a notch display (32pt
        // anchor) and a plain display (24pt anchor) must therefore disagree.
        XCTAssertEqual(notchFrame.height, notchMetrics.anchorSize.height + 28, accuracy: 0.5)
        XCTAssertEqual(plainFrame.height, plainMetrics.anchorSize.height + 28, accuracy: 0.5)
        XCTAssertNotEqual(notchFrame.height, plainFrame.height)
    }

    // MARK: - Never larger than the screen

    func testFrameNeverExceedsTheScreenEvenWithAHugeMeasuredSize() {
        var geometry = WidgetWindowGeometry()
        geometry.record(WidgetContentGeometry(size: CGSize(width: 5000, height: 5000)), for: .expanded)
        let frame = geometry.frame(for: .expanded, metrics: notchMetrics, screenFrame: screenFrame)

        XCTAssertLessThanOrEqual(frame.width, screenFrame.width)
        XCTAssertLessThanOrEqual(frame.height, screenFrame.height)
    }

    // MARK: - record/has/forget

    func testRecordIgnoresDegenerateSizesAndForgetClearsRecordedMeasurements() {
        var geometry = WidgetWindowGeometry()
        XCTAssertFalse(geometry.record(WidgetContentGeometry(size: .zero), for: .compact))
        XCTAssertTrue(geometry.record(WidgetContentGeometry(size: CGSize(width: 100, height: 40)), for: .compact))
        XCTAssertTrue(geometry.has(.compact))
        geometry.forget()
        XCTAssertFalse(geometry.has(.compact))
    }

    func testHiddenAndCompactShareTheSameSizeKeyButExpandedDoesNot() {
        XCTAssertEqual(WidgetWindowGeometry.sizeKey(.hidden), .compact)
        XCTAssertEqual(WidgetWindowGeometry.sizeKey(.compact), .compact)
        XCTAssertEqual(WidgetWindowGeometry.sizeKey(.expanded), .expanded)
    }
}
