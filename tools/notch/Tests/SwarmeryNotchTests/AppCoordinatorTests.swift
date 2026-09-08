// AppCoordinator is the app-wiring seam WSStreamTests.swift's own doc
// comment calls out as the right place for this test: does a reconnect
// (a second `.connected`) actually cause AttentionModel's DaemonClient to be
// asked for a fresh snapshot, against a real AppCoordinator rather than a
// proxy counter. `handle(_:)` is driven directly rather than through
// `stream.events()`'s backoff/socket machinery -- that machinery is already
// covered by WSStreamTests.swift, and re-driving it here would only make
// this test slower and less deterministic without adding coverage.
import XCTest
@testable import SwarmeryNotch

private actor RecordingDaemonClient: DaemonClientProtocol {
    private(set) var snapshotCallCount = 0
    var snapshotResult: DaemonSnapshot = DaemonSnapshot(sessions: [], approvals: [], usage: nil)

    func snapshot() async throws -> DaemonSnapshot {
        snapshotCallCount += 1
        return snapshotResult
    }

    private(set) var usageCalls: [Bool] = []
    var usageResult: UsageReport = UsageReport(generatedAt: "2030-01-01T00:00:00Z", providers: [], accounts: [])

    func usage(fresh: Bool) async throws -> UsageReport {
        usageCalls.append(fresh)
        return usageResult
    }

    func approve(id: Int, decision: ApprovalDecision, reason: String?) async throws {}
    func stop(sessionId: Int) async throws {}
    func kill(sessionId: Int, force: Bool) async throws {}
}

/// Never actually driven by these tests (`handle(_:)` is called directly),
/// but `WSStream`'s initializer needs a connector to exist.
private struct UnusedConnection: WebSocketConnecting {
    func resume() {}
    func receive() async throws -> URLSessionWebSocketTask.Message {
        throw CancellationError()
    }
    func cancel(with closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {}
}

private enum FakeDaemonError: Error {
    case boom
}

private actor FailingDaemonClient: DaemonClientProtocol {
    private(set) var snapshotCallCount = 0

    func snapshot() async throws -> DaemonSnapshot {
        snapshotCallCount += 1
        throw FakeDaemonError.boom
    }

    func usage(fresh: Bool) async throws -> UsageReport { throw FakeDaemonError.boom }

    func approve(id: Int, decision: ApprovalDecision, reason: String?) async throws {}
    func stop(sessionId: Int) async throws {}
    func kill(sessionId: Int, force: Bool) async throws {}
}

final class AppCoordinatorTests: XCTestCase {
    @MainActor
    func testAReconnectTriggersAFreshSnapshotRequest() async {
        let client = RecordingDaemonClient()
        let coordinator = AppCoordinator(
            client: client,
            stream: WSStream(connect: { UnusedConnection() }, sleep: { _ in })
        )

        await coordinator.handle(.connected)
        let firstCount = await client.snapshotCallCount
        XCTAssertEqual(firstCount, 1, "the initial .connected must request a snapshot")

        await coordinator.handle(.disconnected)
        await coordinator.handle(.connected)
        let secondCount = await client.snapshotCallCount
        XCTAssertEqual(secondCount, 2, "a reconnect (second .connected) must request a FRESH snapshot, not reuse the first one")
    }

    @MainActor
    func testASnapshotFailureIsSwallowedAndDoesNotPreventALaterReconnectFromTryingAgain() async {
        let client = FailingDaemonClient()
        let coordinator = AppCoordinator(
            client: client,
            stream: WSStream(connect: { UnusedConnection() }, sleep: { _ in })
        )

        // A client that always fails must not crash the coordinator, and must
        // not prevent a later reconnect from trying again.
        await coordinator.handle(.connected)
        await coordinator.handle(.disconnected)
        await coordinator.handle(.connected)
        let count = await client.snapshotCallCount
        XCTAssertEqual(count, 2)
    }
}

/// `AppCoordinatorConfig.fromEnvironment(environment:)` takes an injectable
/// dictionary (default `ProcessInfo.processInfo.environment` in production)
/// purely so these tests can exercise it without mutating the real process
/// environment.
final class AppCoordinatorConfigTests: XCTestCase {
    func testZeroNegativeAndNonNumericLingerValuesFallBackToTheSixSecondDefault() {
        for raw in ["0", "-1", "-5.5", "not-a-number"] {
            let config = AppCoordinatorConfig.fromEnvironment(environment: ["SWARMERY_NOTCH_LINGER": raw])
            XCTAssertEqual(config.linger, 6, "raw value \"\(raw)\" must fall back to the 6s default, not disable the linger")
        }
    }

    func testAnUnsetLingerEnvVarFallsBackToTheSixSecondDefault() {
        let config = AppCoordinatorConfig.fromEnvironment(environment: [:])
        XCTAssertEqual(config.linger, 6)
    }

    func testAValidPositiveLingerIsHonored() {
        let config = AppCoordinatorConfig.fromEnvironment(environment: ["SWARMERY_NOTCH_LINGER": "2.5"])
        XCTAssertEqual(config.linger, 2.5)
    }

    // MARK: - Usage refresh

    @MainActor
    func testRefreshUsageAsksTheDaemonToBypassItsCacheAndPublishesTheReport() async {
        let client = RecordingDaemonClient()
        let coordinator = AppCoordinator(
            client: client,
            stream: WSStream(connect: { UnusedConnection() }, sleep: { _ in })
        )
        var published: [AttentionState] = []
        coordinator.onStateChange = { published.append($0) }

        await coordinator.refreshUsage(fresh: true)

        let calls = await client.usageCalls
        XCTAssertEqual(calls, [true])
        XCTAssertEqual(coordinator.state.usage?.generatedAt, "2030-01-01T00:00:00Z")
        XCTAssertEqual(published.count, 1)
    }

    @MainActor
    func testAFailedUsageRefreshKeepsTheLastReportAndPublishesNothing() async {
        let coordinator = AppCoordinator(
            client: FailingDaemonClient(),
            stream: WSStream(connect: { UnusedConnection() }, sleep: { _ in })
        )
        var published = 0
        coordinator.onStateChange = { _ in published += 1 }

        await coordinator.refreshUsage(fresh: false)

        XCTAssertNil(coordinator.state.usage)
        XCTAssertEqual(published, 0)
    }

    @MainActor
    func testTheUsageTimerRefreshesOnlyWhileConnected() async {
        let client = RecordingDaemonClient()
        // A sleep that yields once and returns: each loop iteration is one
        // "tick" with no wall-clock time.
        let coordinator = AppCoordinator(
            client: client,
            stream: WSStream(connect: { UnusedConnection() }, sleep: { _ in }),
            usageRefreshInterval: 30,
            sleep: { _ in await Task.yield() }
        )
        coordinator.startUsageTimer()
        for _ in 0..<20 { await Task.yield() }
        coordinator.stop()
        let whileDisconnected = await client.usageCalls.count
        XCTAssertEqual(whileDisconnected, 0)

        await coordinator.handle(.connected)
        coordinator.startUsageTimer()
        var polls = 0
        while await client.usageCalls.isEmpty, polls < 200 { await Task.yield(); polls += 1 }
        coordinator.stop()
        let calls = await client.usageCalls
        XCTAssertFalse(calls.isEmpty)
        XCTAssertTrue(calls.allSatisfy { $0 == false })
    }

    func testUsageRefreshIntervalIsReadFromTheEnvironmentAndFlooredAtTheDaemonCache() {
        XCTAssertEqual(AppCoordinatorConfig.fromEnvironment(environment: [:]).usageRefreshInterval, 300)
        XCTAssertEqual(AppCoordinatorConfig.fromEnvironment(environment: ["SWARMERY_NOTCH_USAGE_REFRESH": "60"]).usageRefreshInterval, 60)
        XCTAssertEqual(AppCoordinatorConfig.fromEnvironment(environment: ["SWARMERY_NOTCH_USAGE_REFRESH": "5"]).usageRefreshInterval, 300)
        XCTAssertEqual(AppCoordinatorConfig.fromEnvironment(environment: ["SWARMERY_NOTCH_USAGE_REFRESH": "abc"]).usageRefreshInterval, 300)
        XCTAssertEqual(AppCoordinatorConfig(linger: 6, usageRefreshInterval: 1).usageRefreshInterval, 30)
    }
}
