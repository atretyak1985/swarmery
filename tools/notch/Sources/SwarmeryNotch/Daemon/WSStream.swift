// WebSocket client for the daemon's live-update stream (GET /api/ws — see
// tools/swarmery/docs/ws-protocol.md). Delivery there is at-most-once with no
// replay: "On connect — and after any suspected gap — clients should resync
// via REST". This type owns exactly the reconnect-with-backoff loop; it is
// the CALLER's job (AttentionModel's owner) to request a fresh
// DaemonClient.snapshot() on every `.connected` event.
import Foundation

public enum WSStreamEvent: Equatable, Sendable {
    case connected
    case disconnected
    case message(WSMessage)
}

/// The minimal WebSocket surface WSStream depends on — a seam so tests can
/// simulate connects, messages and drops without a real socket or a real
/// server. `URLSessionWebSocketTask` conforms for free below.
public protocol WebSocketConnecting: Sendable {
    func resume()
    func receive() async throws -> URLSessionWebSocketTask.Message
    /// Closes the socket, unblocking any `receive()` currently suspended on
    /// it. Swift-task cancellation alone does NOT do this for a real
    /// `URLSessionWebSocketTask` — its `receive()` does not observe
    /// `Task.isCancelled` — so tearing down the stream without calling this
    /// leaks an open connection and a permanently suspended task.
    func cancel(with closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?)
}

extension URLSessionWebSocketTask: WebSocketConnecting {}

/// Pure exponential backoff schedule: 0.5s, 1s, 2s, 4s, 8s, then capped at
/// 10s. A plain, stateless function — the whole contract WSStreamTests
/// exercises directly, with no sleeping involved.
public enum Backoff {
    public static let initial: TimeInterval = 0.5
    public static let cap: TimeInterval = 10.0

    /// `attempt` is 0-based (0 = the delay before the FIRST reconnect, after
    /// the initial connection attempt failed/dropped).
    public static func delay(forAttempt attempt: Int) -> TimeInterval {
        guard attempt > 0 else { return initial }
        let scaled = initial * pow(2.0, Double(attempt))
        return min(scaled, cap)
    }
}

/// Holds whichever socket is currently in flight inside `events()`'s
/// reconnect loop, so `onTermination` — which runs outside that loop's scope
/// — can close it directly. A plain lock-protected box rather than an actor:
/// `onTermination` is a synchronous, non-async closure.
private final class CurrentSocket: @unchecked Sendable {
    private let lock = NSLock()
    private var socket: (any WebSocketConnecting)?

    func set(_ socket: any WebSocketConnecting) {
        lock.lock()
        defer { lock.unlock() }
        self.socket = socket
    }

    func closeCurrent() {
        lock.lock()
        let socket = self.socket
        lock.unlock()
        socket?.cancel(with: .goingAway, reason: nil)
    }
}

/// Reconnect-with-backoff loop over the daemon's `/api/ws` endpoint. Holds no
/// mutable state of its own — every call to `events()` is an independent run,
/// which is what makes this a plain `Sendable` struct rather than an actor.
public struct WSStream: Sendable {
    public typealias SleepFn = @Sendable (TimeInterval) async -> Void
    public typealias ConnectFn = @Sendable () -> any WebSocketConnecting

    private let connect: ConnectFn
    private let sleep: SleepFn

    /// Real-socket initializer: connects to `url` via `session` on every
    /// (re)connect attempt.
    public init(url: URL, session: URLSession = .shared, sleep: @escaping SleepFn = WSStream.defaultSleep) {
        self.connect = { session.webSocketTask(with: url) }
        self.sleep = sleep
    }

    /// Test/advanced initializer: inject the connector directly (and, in
    /// tests, a `sleep` that never really waits — the backoff VALUE is what
    /// gets asserted on, not wall-clock time).
    public init(connect: @escaping ConnectFn, sleep: @escaping SleepFn = WSStream.defaultSleep) {
        self.connect = connect
        self.sleep = sleep
    }

    public static let defaultSleep: SleepFn = { seconds in
        guard seconds > 0 else { return }
        try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
    }

    /// One event per state change: connect → `.connected` → zero or more
    /// `.message` → on drop `.disconnected` → backoff → reconnect — until the
    /// consumer stops iterating, which cancels the underlying loop via
    /// `AsyncStream`'s `onTermination`.
    public func events() -> AsyncStream<WSStreamEvent> {
        AsyncStream { continuation in
            let currentSocket = CurrentSocket()
            let task = Task {
                var attempt = 0
                while !Task.isCancelled {
                    let socket = connect()
                    currentSocket.set(socket)
                    socket.resume()
                    continuation.yield(.connected)
                    do {
                        try await receiveLoop(socket, continuation: continuation)
                    } catch {
                        // Any receive failure (closed frame, transport error,
                        // or the fake connection's queue draining in tests)
                        // falls through to `.disconnected` + backoff below.
                        // A single malformed FRAME never reaches here — decode
                        // failures are swallowed per-message in receiveLoop.
                    }
                    continuation.yield(.disconnected)
                    if Task.isCancelled { break }
                    let delay = Backoff.delay(forAttempt: attempt)
                    attempt += 1
                    await sleep(delay)
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in
                task.cancel()
                // `task.cancel()` alone does not unblock a `receive()`
                // already suspended on the current socket (see
                // WebSocketConnecting.cancel's doc) — close it explicitly so
                // tearing down the stream never leaks an open connection.
                currentSocket.closeCurrent()
            }
        }
    }

    private func receiveLoop(
        _ socket: any WebSocketConnecting,
        continuation: AsyncStream<WSStreamEvent>.Continuation
    ) async throws {
        while !Task.isCancelled {
            let message = try await socket.receive()
            guard let data = Self.payload(of: message) else { continue }
            if let decoded = try? JSONDecoder().decode(WSMessage.self, from: data) {
                continuation.yield(.message(decoded))
            }
            // A frame that fails to decode (unexpected shape from a daemon
            // ahead of this client) is silently dropped rather than tearing
            // down the connection — same "tolerate additive change" posture
            // as WSMessage.unknown for a recognized-but-new `type`.
        }
    }

    private static func payload(of message: URLSessionWebSocketTask.Message) -> Data? {
        switch message {
        case .string(let text):
            return text.data(using: .utf8)
        case .data(let data):
            return data
        @unknown default:
            return nil
        }
    }
}
