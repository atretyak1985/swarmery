// Entry point: an NSApplication accessory app (no Dock icon, no app menu --
// this SwiftPM executable ships with no Info.plist yet, so
// `NSApp.setActivationPolicy(.accessory)` is the runtime equivalent of
// `LSUIElement`; phase 4's packaging adds the real Info.plist key once the
// app is bundled), a menu-bar item with Open dashboard / Quit, and the
// wiring: DaemonClient -> WSStream -> AppCoordinator -> AttentionModel ->
// one WidgetPresenter per screen.
import AppKit

@main
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem?
    private var presenters: [WidgetPresenter] = []
    private var coordinator: AppCoordinator?
    private var collapseTask: Task<Void, Never>?
    private let config = AppCoordinatorConfig.fromEnvironment()
    private let placement = WidgetPlacement.fromEnvironment()
    private let edgeAnchorFromBottom = WidgetWindowGeometry.edgeAnchorFromBottom(
        environmentValue: ProcessInfo.processInfo.environment["SWARMERY_NOTCH_EDGE_ANCHOR"]
    )
    private var outsideClickMonitor: Any?
    /// Kept so a display plugged in after launch can be given its own
    /// presenter -- see `reconcilePresenters`.
    private var actions: WidgetActions?
    /// The last state rendered, replayed into a presenter created after
    /// launch so it shows the same thing as its siblings instead of staying
    /// blank until the next daemon event.
    private var lastState = AttentionState()

    static func main() {
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        app.run()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        let client = DaemonClient()

        let actions = WidgetActions(
            approve: { id in Task { try? await client.approve(id: id, decision: .approve, reason: nil) } },
            deny: { id in Task { try? await client.approve(id: id, decision: .deny, reason: nil) } },
            focus: { session in TerminalFocus.focus(session) },
            openDashboard: { session in TerminalFocus.openDashboard(session) },
            stop: { session in Task { try? await client.stop(sessionId: session.id) } },
            quit: { NSApp.terminate(nil) },
            refreshUsage: { [weak self] in
                guard let coordinator = self?.coordinator else { return }
                Task { @MainActor in await coordinator.refreshUsage(fresh: true) }
            }
        )

        setUpMenuBar()
        setUpPresenters(actions: actions)

        let stream = WSStream(url: client.baseURL.appendingPathComponent("api/ws"))
        let coordinator = AppCoordinator(client: client, stream: stream, usageRefreshInterval: config.usageRefreshInterval)
        coordinator.onStateChange = { [weak self] state in
            self?.render(state)
        }
        self.coordinator = coordinator
        coordinator.start()

        NotificationCenter.default.addObserver(
            self,
            selector: #selector(screenParametersChanged),
            name: NSApplication.didChangeScreenParametersNotification,
            object: nil
        )
    }

    func applicationWillTerminate(_ notification: Notification) {
        coordinator?.stop()
        collapseTask?.cancel()
        if let outsideClickMonitor { NSEvent.removeMonitor(outsideClickMonitor) }
        for presenter in presenters { presenter.teardown() }
    }

    // MARK: - Menu bar

    private func setUpMenuBar() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.button?.image = NSImage(systemSymbolName: "circle.hexagongrid", accessibilityDescription: "Swarmery Notch")

        let menu = NSMenu()
        let openItem = NSMenuItem(title: "Open dashboard", action: #selector(openDashboard), keyEquivalent: "")
        openItem.target = self
        menu.addItem(openItem)
        menu.addItem(.separator())
        let quitItem = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
        item.menu = menu
        statusItem = item
    }

    @objc private func openDashboard() {
        NSWorkspace.shared.open(DaemonClient.resolveBaseURL())
    }

    @objc private func quit() {
        NSApp.terminate(nil)
    }

    // MARK: - Presenters (one per screen, so a widget follows every display)

    private func setUpPresenters(actions: WidgetActions) {
        self.actions = actions
        presenters = NSScreen.screens.map {
            WidgetPresenter(screen: $0, actions: actions, placement: placement, edgeAnchorFromBottom: edgeAnchorFromBottom)
        }
        // A panel the operator opened by clicking the tab closes on a click
        // anywhere else — the Grammarly convention. Global monitors receive
        // mouse events from other apps without any accessibility grant.
        outsideClickMonitor = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self else { return }
                guard !self.presenters.contains(where: { $0.containsMouse }) else { return }
                for presenter in self.presenters { presenter.dismissPinned() }
            }
        }
    }

    /// Displays come and go -- a monitor is plugged in, the lid closes, the
    /// resolution changes. The presenter set used to be built once at launch
    /// and never revisited, so a display added later got no widget and a
    /// display removed left a presenter holding a window on a screen that no
    /// longer exists.
    @objc private func screenParametersChanged() {
        reconcilePresenters()
        for presenter in presenters { presenter.refreshMetrics() }
    }

    private func reconcilePresenters() {
        guard let actions else { return }
        let screens = NSScreen.screens
        let plan = DisplayReconciliation.plan(
            existing: presenters.map(\.displayID),
            current: screens.map(\.displayID)
        )
        guard plan.hasWork else { return }

        for presenter in presenters where plan.removed.contains(presenter.displayID) {
            presenter.teardown()
        }

        var surviving = presenters.filter { !plan.removed.contains($0.displayID) }
        let added = screens
            .filter { plan.added.contains($0.displayID) }
            .map {
                WidgetPresenter(
                    screen: $0,
                    actions: actions,
                    placement: placement,
                    edgeAnchorFromBottom: edgeAnchorFromBottom
                )
            }
        surviving.append(contentsOf: added)

        // Keep the list in the window server's own display order.
        let order = screens.map(\.displayID)
        presenters = surviving.sorted {
            (order.firstIndex(of: $0.displayID) ?? .max) < (order.firstIndex(of: $1.displayID) ?? .max)
        }

        // A fresh presenter has no window until its first `update` -- without
        // this it would stay invisible until the next daemon event.
        let shouldOpen = lastState.shouldExpand(now: Date(), linger: config.linger)
        for presenter in added {
            presenter.update(attention: lastState, shouldOpen: shouldOpen)
        }
    }

    // MARK: - Rendering

    /// Auto-expands on `AttentionState.shouldExpand` and collapses again once
    /// the linger window has elapsed. `shouldExpand` depends on `now`, so a
    /// resolution with no further WS traffic needs its own timer to notice
    /// the linger window has passed -- nothing else would re-evaluate it.
    private func render(_ state: AttentionState) {
        collapseTask?.cancel()
        lastState = state
        let now = Date()
        let shouldOpen = state.shouldExpand(now: now, linger: config.linger)
        for presenter in presenters {
            presenter.update(attention: state, shouldOpen: shouldOpen)
        }

        guard shouldOpen, !state.needsAttention, let lastResolvedAt = state.lastResolvedAt else { return }
        let remaining = config.linger - now.timeIntervalSince(lastResolvedAt)
        guard remaining > 0 else { return }
        collapseTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(remaining * 1_000_000_000))
            guard !Task.isCancelled, let self, let coordinator = self.coordinator else { return }
            self.render(coordinator.state)
        }
    }
}
