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

    @objc private func screenParametersChanged() {
        for presenter in presenters { presenter.refreshMetrics() }
    }

    // MARK: - Rendering

    /// Auto-expands on `AttentionState.shouldExpand` and collapses again once
    /// the linger window has elapsed. `shouldExpand` depends on `now`, so a
    /// resolution with no further WS traffic needs its own timer to notice
    /// the linger window has passed -- nothing else would re-evaluate it.
    private func render(_ state: AttentionState) {
        collapseTask?.cancel()
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
