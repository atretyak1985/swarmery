// WidgetPanel is constructed with `defer: true` (deferred window creation),
// so instantiating it never materializes a real window resource -- no
// window server round trip, safe in a headless CI runner.
import XCTest
@testable import SwarmeryNotch

final class WidgetPanelTests: XCTestCase {
    @MainActor
    func testThePanelNeverBecomesKeyOrMain() {
        let panel = WidgetPanel(
            contentRect: .zero,
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: true
        )

        XCTAssertFalse(panel.canBecomeKey)
        XCTAssertFalse(panel.canBecomeMain)
    }
}
