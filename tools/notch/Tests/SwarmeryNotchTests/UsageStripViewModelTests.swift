// UsageStripViewModel is a pure projection from UsageReport? -- these tests
// never touch SwiftUI.
import XCTest
@testable import SwarmeryNotch

final class UsageStripViewModelTests: XCTestCase {
    private func makeWindow(
        key: String = "5h",
        label: String = "5 hour",
        percentUsed: Double = 42,
        resetText: String? = "resets in 2h"
    ) -> UsageWindow {
        UsageWindow(key: key, label: label, percentUsed: percentUsed, percentLeft: 100 - percentUsed, resetText: resetText, source: "test")
    }

    private func makeProvider(status: String, windows: [UsageWindow]) -> UsageProvider {
        UsageProvider(account: "acct", name: "Claude", status: status, source: "test", windows: windows)
    }

    func testEveryWindowFromAVisibleProviderIsShownWithPercentAndResetText() {
        let usage = UsageReport(
            generatedAt: "2026-01-01T00:00:00Z",
            providers: [
                makeProvider(status: "ok", windows: [
                    makeWindow(key: "5h"),
                    makeWindow(key: "week", label: "week", percentUsed: 10, resetText: "resets Sunday"),
                ]),
            ],
            accounts: []
        )

        let viewModel = UsageStripViewModel(usage: usage)

        XCTAssertEqual(viewModel?.windows.count, 2)
        XCTAssertEqual(viewModel?.windows.first?.percentUsed, 42)
        XCTAssertEqual(viewModel?.windows.first?.resetText, "resets in 2h")
    }

    func testHiddenWhenTheOnlyProviderIsNoAuth() {
        let usage = UsageReport(
            generatedAt: "2026-01-01T00:00:00Z",
            providers: [makeProvider(status: "no-auth", windows: [makeWindow()])],
            accounts: []
        )

        XCTAssertNil(UsageStripViewModel(usage: usage))
    }

    func testHiddenWhenThereIsNoReportYet() {
        XCTAssertNil(UsageStripViewModel(usage: nil))
    }

    func testANoAuthProviderIsExcludedButAVisibleProviderStillShows() {
        let usage = UsageReport(
            generatedAt: "2026-01-01T00:00:00Z",
            providers: [
                makeProvider(status: "no-auth", windows: [makeWindow(key: "hidden")]),
                makeProvider(status: "ok", windows: [makeWindow(key: "visible")]),
            ],
            accounts: []
        )

        let viewModel = UsageStripViewModel(usage: usage)

        XCTAssertEqual(viewModel?.windows.map(\.id), ["visible"])
    }
}
