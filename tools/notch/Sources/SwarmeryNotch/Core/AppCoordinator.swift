// Wires the daemon client and the WS stream to the pure `AttentionModel`
// reducer, and republishes the result for the presenter(s) to render. Split
// out of App.swift so the reconnect-triggers-a-fresh-snapshot contract
// (docs/ws-protocol.md: the daemon never replays frames missed while
// offline) is testable against a fake `DaemonClientProtocol` without a real
// NSApplication or a real socket.
import Foundation

public struct AppCoordinatorConfig: Sendable {
    public let linger: TimeInterval
    /// Seconds between background `GET /api/usage` refreshes. The daemon
    /// itself caches for 30 s, so anything below that is pointless; default
    /// 300 (the dashboard polls at the same cadence).
    public let usageRefreshInterval: TimeInterval
    public static let defaultUsageRefreshInterval: TimeInterval = 300
    public static let minimumUsageRefreshInterval: TimeInterval = 30

    public init(linger: TimeInterval, usageRefreshInterval: TimeInterval = AppCoordinatorConfig.defaultUsageRefreshInterval) {
        self.linger = linger
        self.usageRefreshInterval = max(usageRefreshInterval, Self.minimumUsageRefreshInterval)
    }

    /// `SWARMERY_NOTCH_LINGER` overrides the default 6s the panel stays open
    /// after the most recent resolution. Zero, negative, and non-numeric
    /// values all fall back to the default: a non-positive linger would make
    /// `AttentionState.shouldExpand(now:linger:)` false the instant an
    /// approval resolves, collapsing the panel before the operator sees the
    /// outcome. `environment` is overridable only so tests can exercise this
    /// without mutating the real process environment.
    public static func fromEnvironment(
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> AppCoordinatorConfig {
        let raw = environment["SWARMERY_NOTCH_LINGER"]
        let parsed = raw.flatMap(TimeInterval.init)
        let linger = parsed.flatMap { $0 > 0 ? $0 : nil } ?? 6
        let rawInterval = environment["SWARMERY_NOTCH_USAGE_REFRESH"].flatMap(TimeInterval.init)
        let interval = rawInterval.flatMap { $0 >= minimumUsageRefreshInterval ? $0 : nil } ?? defaultUsageRefreshInterval
        return AppCoordinatorConfig(linger: linger, usageRefreshInterval: interval)
    }
}

@MainActor
public final class AppCoordinator {
    public private(set) var state = AttentionState()
    public var onStateChange: (@MainActor (AttentionState) -> Void)?

    private let client: any DaemonClientProtocol
    private let stream: WSStream
    private let usageRefreshInterval: TimeInterval
    private let sleep: @Sendable (TimeInterval) async -> Void
    private var streamTask: Task<Void, Never>?
    private var usageTask: Task<Void, Never>?
    /// True while a usage refresh is in flight — a second request (timer +
    /// button, or two quick clicks) is dropped rather than queued.
    public private(set) var isRefreshingUsage = false

    public init(
        client: any DaemonClientProtocol,
        stream: WSStream,
        usageRefreshInterval: TimeInterval = AppCoordinatorConfig.defaultUsageRefreshInterval,
        sleep: @escaping @Sendable (TimeInterval) async -> Void = WSStream.defaultSleep
    ) {
        self.client = client
        self.stream = stream
        self.usageRefreshInterval = usageRefreshInterval
        self.sleep = sleep
    }

    public func start() {
        startUsageTimer()
        streamTask?.cancel()
        streamTask = Task { @MainActor [weak self] in
            guard let self else { return }
            for await event in self.stream.events() {
                await self.handle(event)
            }
        }
    }

    public func stop() {
        streamTask?.cancel()
        streamTask = nil
        usageTask?.cancel()
        usageTask = nil
    }

    /// Background cadence for `GET /api/usage`. Started by `start()`; each
    /// tick is skipped while disconnected (the reconnect snapshot brings
    /// usage along anyway).
    public func startUsageTimer() {
        usageTask?.cancel()
        usageTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.sleep(self.usageRefreshInterval)
                if Task.isCancelled { return }
                guard self.state.isConnected else { continue }
                await self.refreshUsage(fresh: false)
            }
        }
    }

    /// On-demand refresh (panel opened, refresh button). `fresh` asks the
    /// daemon to bypass its own cache, so the footer's "Updated" moves.
    public func refreshUsage(fresh: Bool) async {
        guard !isRefreshingUsage else { return }
        isRefreshingUsage = true
        defer { isRefreshingUsage = false }
        do {
            let usage = try await client.usage(fresh: fresh)
            state = AttentionModel.reduce(state, .usageUpdated(usage))
            publish()
        } catch {
            // Keep the last known report; the daemon-offline badge (via the
            // WS stream) is the signal for a dead daemon, not a missing refresh.
        }
    }

    /// Not `private`: `AppCoordinatorTests` drives this directly -- the
    /// fastest, most deterministic way to prove "a reconnect (a second
    /// `.connected`) triggers a fresh snapshot request" without
    /// reconstructing WSStream's backoff/socket machinery in this test too.
    /// (WSStreamTests.swift's own doc comment calls this test out as
    /// belonging here, against a real `AppCoordinator`, not a proxy
    /// counter.)
    func handle(_ event: WSStreamEvent) async {
        switch event {
        case .connected:
            state = AttentionModel.reduce(state, .connectionChanged(true))
            await refreshSnapshot()
        case .disconnected:
            state = AttentionModel.reduce(state, .connectionChanged(false))
            publish()
        case let .message(message):
            apply(message)
            publish()
        }
    }

    private func refreshSnapshot() async {
        do {
            let snapshot = try await client.snapshot()
            state = AttentionModel.reduce(
                state,
                .snapshot(sessions: snapshot.sessions, approvals: snapshot.approvals, usage: snapshot.usage)
            )
        } catch {
            // Best-effort: keep whatever state we had. The stream's own
            // backoff loop guarantees another `.connected` will arrive, and
            // that one tries again.
        }
        publish()
    }

    private func apply(_ message: WSMessage) {
        switch message {
        case .sessionStarted(let session), .sessionUpdated(let session):
            state = AttentionModel.reduce(state, .sessionUpdated(session))
        case .eventAppended:
            break
        case let .permissionRequested(request):
            state = AttentionModel.reduce(state, .permissionRequested(request))
        case let .permissionResolved(request):
            state = AttentionModel.reduce(state, .permissionResolved(request, at: Date()))
        case .unknown:
            break
        }
    }

    private func publish() {
        onStateChange?(state)
    }
}
