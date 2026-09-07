// Pure-function coverage for TerminalFocus's tty validation/escaping and for
// the strategy fall-through that kicks in when a daemon-reported tty doesn't
// look like a real controlling terminal. Deliberately stays on the near side
// of `runAppleScript`/`NSAppleScript` -- this file never executes a script,
// opens NSWorkspace, or touches a real terminal, so it stays headless.
import XCTest
@testable import SwarmeryNotch

final class TerminalFocusTests: XCTestCase {
    private let dashboardBaseURL = URL(string: "http://127.0.0.1:7777")!

    // MARK: - isValidTTY

    func testRealLookingTTYPathsAreAccepted() {
        XCTAssertTrue(TerminalFocus.isValidTTY("/dev/ttys003"))
        XCTAssertTrue(TerminalFocus.isValidTTY("/dev/pts/4"))
    }

    func testEmptyOrNonTTYShapedStringsAreRejected() {
        XCTAssertFalse(TerminalFocus.isValidTTY(""))
        XCTAssertFalse(TerminalFocus.isValidTTY("not-a-tty"))
        XCTAssertFalse(TerminalFocus.isValidTTY("/etc/passwd"))
    }

    func testATTYCarryingAppleScriptMetacharactersIsRejected() {
        // A daemon-reported tty that tries to break out of the AppleScript
        // string literal must be rejected outright, not merely escaped.
        XCTAssertFalse(TerminalFocus.isValidTTY("/dev/ttys003\" activate application \"Finder\""))
    }

    // MARK: - escapeAppleScriptStringLiteral

    func testEscapingDoublesBackslashes() {
        XCTAssertEqual(TerminalFocus.escapeAppleScriptStringLiteral(#"/dev/tty\evil"#), #"/dev/tty\\evil"#)
    }

    func testEscapingBackslashEscapesDoubleQuotes() {
        XCTAssertEqual(TerminalFocus.escapeAppleScriptStringLiteral("/dev/tty\"evil"), "/dev/tty\\\"evil")
    }

    func testEscapingIsANoOpForOrdinaryTTYPaths() {
        XCTAssertEqual(TerminalFocus.escapeAppleScriptStringLiteral("/dev/ttys003"), "/dev/ttys003")
    }

    // MARK: - strategy(for:) fall-through on an invalid tty

    func testInvalidTTYFallsThroughToActivateAppWhenABundleIdIsPresent() {
        let terminal = SessionTerminal(
            program: "iTerm.app",
            focusUrl: nil,
            bundleId: "com.googlecode.iterm2",
            tty: "/dev/ttys003\" activate"
        )
        let strategy = TerminalFocus.strategy(for: terminal, sessionUuid: "uuid-1", dashboardBaseURL: dashboardBaseURL)
        XCTAssertEqual(strategy, .activateApp(bundleID: "com.googlecode.iterm2"))
    }

    func testInvalidTTYFallsThroughToDashboardWhenNoBundleIdIsPresent() {
        let terminal = SessionTerminal(program: "iTerm.app", focusUrl: nil, bundleId: nil, tty: "garbage")
        let strategy = TerminalFocus.strategy(for: terminal, sessionUuid: "uuid-2", dashboardBaseURL: dashboardBaseURL)
        XCTAssertEqual(
            strategy,
            .openDashboard(dashboardBaseURL.appendingPathComponent("sessions").appendingPathComponent("uuid-2"))
        )
    }

    func testAValidTTYStillProducesTheAppleScriptStrategy() {
        let terminal = SessionTerminal(program: "iTerm.app", focusUrl: nil, bundleId: nil, tty: "/dev/ttys003")
        let strategy = TerminalFocus.strategy(for: terminal, sessionUuid: "uuid-3", dashboardBaseURL: dashboardBaseURL)
        XCTAssertEqual(strategy, .appleScript(app: .iTerm, tty: "/dev/ttys003"))
    }
}
