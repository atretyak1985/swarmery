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
            if isAllowedFocusURL(url) {
                return .openURL(url)
            }
            // Any other scheme would hand an unvalidated, daemon-reported
            // string to NSWorkspace (file:, a custom app scheme, …): ignore it
            // and fall through to the next strategy.
            NSLog("swarmery-notch: ignoring focus URL with scheme %@", url.scheme ?? "(none)")
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

    /// `SessionTerminal.focusUrl` is an unvalidated string reported by the
    /// daemon and goes straight to `NSWorkspace.open`. Only Warp's focus
    /// links (`warp://…`) and plain `https` are opened.
    static func isAllowedFocusURL(_ url: URL) -> Bool {
        switch url.scheme?.lowercased() {
        case "warp", "https": return true
        default: return false
        }
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

    /// The dashboard asks for a session token on every browser write and on
    /// the terminal socket. Opening it with `#swarmery-unlock=<ticket>` (a
    /// short-lived ticket signed with the link token) lets the page fetch one
    /// itself. Minted at open time, so the ticket is fresh. Without a readable
    /// link token -- or for a dashboard that is not on this machine -- the
    /// plain URL opens and the page's locked banner explains the way in.
    ///
    /// The unlocked URL names the daemon by its loopback address literal: a
    /// `localhost` base becomes `127.0.0.1` (the daemon's default bind), since
    /// that name may resolve to ::1 first, where the daemon does not listen
    /// and another process may.
    static func unlockedDashboardURL(_ url: URL, signer: LinkSigner = LinkSigner()) -> URL {
        guard let base = loopbackLiteralURL(url),
              let origin = LinkSigner.unlockOrigin(of: base),
              let ticket = signer.unlockTicket(origin: origin),
              var components = URLComponents(url: base, resolvingAgainstBaseURL: false)
        else { return url }
        components.fragment = "swarmery-unlock=\(ticket)"
        return components.url ?? url
    }

    /// `url` with its host as a loopback address literal: unchanged when it
    /// already is one (127.0.0.0/8, ::1), `localhost` rewritten to
    /// `127.0.0.1`, nil for any other host.
    static func loopbackLiteralURL(_ url: URL) -> URL? {
        guard var components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let rawHost = components.host?.lowercased()
        else { return nil }
        let host = rawHost.hasPrefix("[") && rawHost.hasSuffix("]") ? String(rawHost.dropFirst().dropLast()) : rawHost
        if host == "localhost" {
            components.host = "127.0.0.1"
            return components.url
        }
        return isLoopbackLiteral(host) ? url : nil
    }

    /// An IPv4 address in 127.0.0.0/8 or the IPv6 loopback, written as an
    /// address literal (never a name).
    static func isLoopbackLiteral(_ host: String) -> Bool {
        var v4 = in_addr()
        if inet_pton(AF_INET, host, &v4) == 1 {
            return UInt32(bigEndian: v4.s_addr) >> 24 == 127
        }
        var v6 = in6_addr()
        if inet_pton(AF_INET6, host, &v6) == 1 {
            return withUnsafeBytes(of: &v6) { Array($0) } == [UInt8](repeating: 0, count: 15) + [1]
        }
        return false
    }

    /// Opens the dashboard root (the menu-bar "Open dashboard" item), unlocked.
    @MainActor
    public static func openDashboardHome(dashboardBaseURL: URL = DaemonClient.resolveBaseURL()) {
        perform(.openDashboard(dashboardBaseURL))
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
            NSWorkspace.shared.open(unlockedDashboardURL(url))
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
