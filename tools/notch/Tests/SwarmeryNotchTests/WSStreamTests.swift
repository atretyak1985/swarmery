// Backoff is asserted as a pure function (zero sleeping). The reconnect loop
// itself is exercised through the injectable `WebSocketConnecting` seam and
// `AsyncStream`'s `for await … break` termination, so it is also fully
// deterministic — no real socket, no real timer, no wall-clock waiting.
import XCTest
@testable import SwarmeryNotch

/// A tiny thread-safe box for the mutable state a `@Sendable` closure needs
/// to capture across calls (call count, recorded sleeps) — deliberately
/// minimal rather than pulling in an actor for two integers.
private final class Box<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var value: T
    init(_ value: T) { self.value = value }
    func mutate<R>(_ body: (inout T) -> R) -> R {
        lock.lock(); defer { lock.unlock() }
        return body(&value)
    }
    var current: T {
        lock.lock(); defer { lock.unlock() }
        return value
    }
}

private enum FakeWSError: Error, Equatable {
    case dropped
}

/// Replays a fixed queue of results, one per `receive()` call; once drained,
/// throws — simulating the far end going away.
private final class FakeWebSocketConnection: WebSocketConnecting, @unchecked Sendable {
    private let lock = NSLock()
    private var results: [Result<URLSessionWebSocketTask.Message, Error>]

    init(results: [Result<URLSessionWebSocketTask.Message, Error>]) {
        self.results = results
    }

    func resume() {}

    func receive() async throws -> URLSessionWebSocketTask.Message {
        let next: Result<URLSessionWebSocketTask.Message, Error>? = lock.withLock {
            results.isEmpty ? nil : results.removeFirst()
        }
        guard let next else { throw FakeWSError.dropped }
        return try next.get()
    }
}

final class WSStreamTests: XCTestCase {

    // MARK: - Backoff caps at 10s (pure function, no sleeping)

    func testBackoffDoublesThenCapsAtTenSeconds() {
        let delays = (0..<8).map { Backoff.delay(forAttempt: $0) }
        XCTAssertEqual(delays, [0.5, 1, 2, 4, 8, 10, 10, 10])
    }

    func testBackoffNeverExceedsTheCapForAnyAttempt() {
        XCTAssertEqual(Backoff.delay(forAttempt: 1_000), Backoff.cap)
    }

    func testBackoffAtAttemptZeroIsTheInitialDelay() {
        XCTAssertEqual(Backoff.delay(forAttempt: 0), Backoff.initial)
    }

    // MARK: - Reconnect emits .connected, then the owner's snapshot request

    func testReconnectEmitsConnectedFollowedByASnapshotRequest() async {
        let sessionUpdatedJSON = #"""
        {"type":"session_updated","payload":{"id":7,"sessionUuid":"u-7","projectName":"p",
         "status":"active","title":"t","why":null,"procState":"running"}}
        """#
        let callIndex = Box(0)
        let recordedSleeps = Box<[TimeInterval]>([])

        // Connection attempt 1 drops immediately (no message ever delivered).
        // Connection attempt 2 delivers one message, then also drops — enough
        // to observe one full connect → message → disconnect → reconnect cycle.
        let connectionFactories: [@Sendable () -> FakeWebSocketConnection] = [
            { FakeWebSocketConnection(results: [.failure(FakeWSError.dropped)]) },
            { FakeWebSocketConnection(results: [.success(.string(sessionUpdatedJSON)), .failure(FakeWSError.dropped)]) },
        ]

        let stream = WSStream(
            connect: {
                let i = callIndex.mutate { n -> Int in defer { n += 1 }; return n }
                return connectionFactories[min(i, connectionFactories.count - 1)]()
            },
            sleep: { seconds in recordedSleeps.mutate { $0.append(seconds) } }
        )

        var collected: [WSStreamEvent] = []
        var snapshotRequests = 0
        // Stop right after the `.message` from the SECOND connection: every
        // event up to and including it is causally ordered by a single
        // straight-line `await` sequence in WSStream.events() (yield →
        // backoff sleep → reconnect → yield → receive → yield), so this
        // checkpoint is fully deterministic. Waiting for a THIRD disconnect
        // would race the producer's next backoff call against this test's
        // teardown — not what "connected followed by a snapshot request" is
        // asserting.
        for await event in stream.events() {
            collected.append(event)
            // The OWNER's contract (docs/ws-protocol.md, AttentionModel.swift):
            // request a fresh DaemonClient.snapshot() on every `.connected`.
            // WSStream itself has no DaemonClient dependency, so that contract
            // is exercised here exactly as a real owner would honour it.
            if event == .connected { snapshotRequests += 1 }
            if collected.count == 4 { break }
        }

        let expectedSession = Session(
            id: 7, sessionUuid: "u-7", projectName: "p", status: "active", title: "t", why: nil, procState: "running"
        )
        XCTAssertEqual(collected, [
            .connected,
            .disconnected,
            .connected,
            .message(.sessionUpdated(expectedSession)),
        ])
        XCTAssertEqual(snapshotRequests, 2)
        // The producer keeps running in the background after yielding the
        // 4th event (AsyncStream applies no back-pressure), so it may have
        // already queued a SECOND backoff sleep by the time this assertion
        // runs — that race is harmless and not what this test is about.
        // What IS guaranteed, because connect() attempt 2 cannot start before
        // `await sleep(...)` for attempt 0 returns (same Task, sequential
        // control flow): the FIRST recorded sleep is exactly attempt 0's
        // delay.
        XCTAssertEqual(recordedSleeps.current.first, Backoff.delay(forAttempt: 0))
    }

    func testMalformedFrameIsDroppedWithoutTearingDownTheConnection() async {
        let callIndex = Box(0)
        let connectionFactories: [@Sendable () -> FakeWebSocketConnection] = [
            {
                FakeWebSocketConnection(results: [
                    .success(.string("not json at all")),
                    .failure(FakeWSError.dropped),
                ])
            },
        ]

        let stream = WSStream(
            connect: {
                let i = callIndex.mutate { n -> Int in defer { n += 1 }; return n }
                return connectionFactories[min(i, connectionFactories.count - 1)]()
            },
            sleep: { _ in }
        )

        var collected: [WSStreamEvent] = []
        for await event in stream.events() {
            collected.append(event)
            if collected.count == 2 { break }
        }

        // The malformed frame produced no `.message` — only `.connected` then
        // `.disconnected` once the fake's queue drains.
        XCTAssertEqual(collected, [.connected, .disconnected])
    }
}
