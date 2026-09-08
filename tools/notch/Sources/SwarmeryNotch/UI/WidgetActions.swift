// What the panel can ask the app to do -- one value rather than a closure
// threaded through every view's initializer, so wiring the wrong action is a
// compiler error (mismatched closure type) rather than a button that
// silently does some other button's job.
//
// Every closure is `@MainActor`: these are only ever invoked from SwiftUI
// button actions (already on the main actor) or `App.swift`'s own
// MainActor-isolated wiring, and `focus`/`openDashboard`/`quit` call
// MainActor-isolated AppKit APIs (`TerminalFocus`, `NSApp`) synchronously.
import Foundation

public struct WidgetActions: Sendable {
    public let approve: @MainActor @Sendable (Int) -> Void
    public let deny: @MainActor @Sendable (Int) -> Void
    public let focus: @MainActor @Sendable (Session) -> Void
    public let openDashboard: @MainActor @Sendable (Session) -> Void
    public let stop: @MainActor @Sendable (Session) -> Void
    public let quit: @MainActor @Sendable () -> Void

    public init(
        approve: @escaping @MainActor @Sendable (Int) -> Void,
        deny: @escaping @MainActor @Sendable (Int) -> Void,
        focus: @escaping @MainActor @Sendable (Session) -> Void,
        openDashboard: @escaping @MainActor @Sendable (Session) -> Void,
        stop: @escaping @MainActor @Sendable (Session) -> Void,
        quit: @escaping @MainActor @Sendable () -> Void
    ) {
        self.approve = approve
        self.deny = deny
        self.focus = focus
        self.openDashboard = openDashboard
        self.stop = stop
        self.quit = quit
    }
}
