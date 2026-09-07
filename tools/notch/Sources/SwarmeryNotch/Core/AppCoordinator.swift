// Wires the daemon client and the WS stream to the pure `AttentionModel`
// reducer, and republishes the result for the presenter(s) to render. Split
// out of App.swift so the reconnect-triggers-a-fresh-snapshot contract
// (docs/ws-protocol.md: the daemon never replays frames missed while
// offline) is testable against a fake `DaemonClientProtocol` without a real
// NSApplication or a real socket.
import Foundation

public struct AppCoordinatorConfig: Sendable {
    public let linger: TimeInterval

    public init(linger: TimeInterval) {
        self.linger = linger
    }

    /// `SWARMERY_NOTCH_LINGER` overrides the default 6s the panel stays open
    /// after the most recent resolution.
    public static func fromEnvironment() -> AppCoordinatorConfig {
        let raw = ProcessInfo.processInfo.environment["SWARMERY_NOTCH_LINGER"]
        let linger = raw.flatMap(TimeInterval.init) ?? 6
        return AppCoordinatorConfig(linger: linger)
    }
}

@MainActor
public final class AppCoordinator {
    public private(set) var state = AttentionState()
    public var onStateChange: (@MainActor (AttentionState) -> Void)?

    private let client: any DaemonClientProtocol
    private let stream: WSStream
    private var streamTask: Task<Void, Never>?

    public init(client: any DaemonClientProtocol, stream: WSStream) {
        self.client = client
        self.stream = stream
    }

    public func start() {
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
