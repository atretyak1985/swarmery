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
        if let tty = terminal.tty {
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
    /// nothing more useful to do here than swallow it.
    private static func runAppleScript(_ source: String) {
        guard let script = NSAppleScript(source: source) else { return }
        var error: NSDictionary?
        script.executeAndReturnError(&error)
    }

    private static func iTermScript(tty: String) -> String {
        """
        tell application "iTerm2"
            repeat with theWindow in windows
                repeat with theTab in tabs of theWindow
                    repeat with theSession in sessions of theTab
                        if tty of theSession is "\(tty)" then
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
        """
        tell application "Terminal"
            repeat with theWindow in windows
                repeat with theTab in tabs of theWindow
                    if tty of theTab is "\(tty)" then
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
