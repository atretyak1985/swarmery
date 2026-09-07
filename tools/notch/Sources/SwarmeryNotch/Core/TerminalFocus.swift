// Brings the terminal tab that owns a session to the front, best-effort:
// Warp via its focus URL, iTerm2/Terminal.app via AppleScript keyed on the
// controlling tty, anything else by activating the owning application by
// bundle id, and -- when the session has no terminal at all (a
// daemon-spawned/headless run) -- the dashboard page for that session.
import AppKit

/// What `TerminalFocus.focus(_:)` will do, computed as a pure value so tests
/// can assert the CHOICE without touching NSWorkspace, AppleScript, or a
/// real terminal.
public enum TerminalFocusStrategy: Equatable {
    case openURL(URL)
    case appleScript(app: TerminalApp, tty: String)
    case activateApp(bundleID: String)
    case openDashboard(URL)
}

public enum TerminalApp: Equatable {
    case iTerm
    case terminalApp
}

public enum TerminalFocus {
    /// `dashboardBaseURL` is always `DaemonClient.resolveBaseURL()` in
    /// production -- a parameter here only so tests can pin it without
    /// touching the `SWARMERY_URL` environment variable.
    public static func strategy(
        for terminal: SessionTerminal?,
        sessionUuid: String,
        dashboardBaseURL: URL
    ) -> TerminalFocusStrategy {
        guard let terminal else {
            return .openDashboard(dashboardURL(base: dashboardBaseURL, sessionUuid: sessionUuid))
        }
        if let focusUrl = terminal.focusUrl, let url = URL(string: focusUrl) {
            return .openURL(url)
        }
        if let tty = terminal.tty, isValidTTY(tty) {
            switch terminal.program {
            case "iTerm.app":
                return .appleScript(app: .iTerm, tty: tty)
            case "Apple_Terminal":
                return .appleScript(app: .terminalApp, tty: tty)
            default:
                break
            }
        }
        if let bundleId = terminal.bundleId {
            return .activateApp(bundleID: bundleId)
        }
        return .openDashboard(dashboardURL(base: dashboardBaseURL, sessionUuid: sessionUuid))
    }

    /// `SessionTerminal.tty` is an unvalidated string reported by the daemon
    /// and gets interpolated into an AppleScript source string below. A real
    /// controlling tty always looks like `/dev/tty*` or `/dev/pts*`; anything
    /// else (including AppleScript metacharacters like `"` or `\`) is
    /// rejected here so the caller falls through to the next focus strategy
    /// instead of building a script from untrusted input.
    static func isValidTTY(_ tty: String) -> Bool {
        let pattern = #"^/dev/(tty|pts)[A-Za-z0-9/_.-]*$"#
        return tty.range(of: pattern, options: .regularExpression) != nil
    }

    /// Defense in depth alongside `isValidTTY`: escapes AppleScript
    /// string-literal metacharacters so an interpolated value can never break
    /// out of the quoted literal, even if the shape check above is ever
    /// loosened.
    static func escapeAppleScriptStringLiteral(_ value: String) -> String {
        value
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
    }

    private static func dashboardURL(base: URL, sessionUuid: String) -> URL {
        base.appendingPathComponent("sessions").appendingPathComponent(sessionUuid)
    }

    @MainActor
    public static func focus(_ session: Session, dashboardBaseURL: URL = DaemonClient.resolveBaseURL()) {
        perform(strategy(for: session.terminal, sessionUuid: session.sessionUuid, dashboardBaseURL: dashboardBaseURL))
    }

    @MainActor
    public static func openDashboard(_ session: Session, dashboardBaseURL: URL = DaemonClient.resolveBaseURL()) {
        perform(.openDashboard(dashboardURL(base: dashboardBaseURL, sessionUuid: session.sessionUuid)))
    }

    // MARK: - Effects

    @MainActor
    private static func perform(_ strategy: TerminalFocusStrategy) {
        switch strategy {
        case let .openURL(url):
            NSWorkspace.shared.open(url)
        case let .appleScript(app, tty):
            runAppleScript(app == .iTerm ? iTermScript(tty: tty) : terminalScript(tty: tty))
        case let .activateApp(bundleID):
            activate(bundleID: bundleID)
        case let .openDashboard(url):
            NSWorkspace.shared.open(url)
        }
    }

    @MainActor
    @discardableResult
    private static func activate(bundleID: String) -> Bool {
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            return false
        }
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: configuration)
        return true
    }

    /// Best-effort: a permission error (the user has not granted Automation
    /// access) looks exactly like a click that did nothing -- there is
    /// nothing more useful to do here than swallow it. Runs detached off the
    /// main actor: `NSAppleScript.executeAndReturnError` is synchronous and a
    /// slow target app or an Automation-permission dialog would otherwise
    /// stall the UI thread that owns the notch panel. Fire-and-forget from
    /// the caller's point of view -- errors are still swallowed, just on a
    /// background task instead of the main thread.
    private static func runAppleScript(_ source: String) {
        Task.detached(priority: .userInitiated) {
            guard let script = NSAppleScript(source: source) else { return }
            var error: NSDictionary?
            script.executeAndReturnError(&error)
        }
    }

    private static func iTermScript(tty: String) -> String {
        let escapedTTY = escapeAppleScriptStringLiteral(tty)
        return """
        tell application "iTerm2"
            repeat with theWindow in windows
                repeat with theTab in tabs of theWindow
                    repeat with theSession in sessions of theTab
                        if tty of theSession is "\(escapedTTY)" then
                            select theWindow
                            select theTab
                            select theSession
                            activate
                            return
                        end if
                    end repeat
                end repeat
            end repeat
        end tell
        """
    }

    private static func terminalScript(tty: String) -> String {
        let escapedTTY = escapeAppleScriptStringLiteral(tty)
        return """
        tell application "Terminal"
            repeat with theWindow in windows
                repeat with theTab in tabs of theWindow
                    if tty of theTab is "\(escapedTTY)" then
                        set selected tab of theWindow to theTab
                        set index of theWindow to 1
                        activate
                        return
                    end if
                end repeat
            end repeat
        end tell
        """
    }
}
