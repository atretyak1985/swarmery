// Decodes the pinned fixtures (Tests/Fixtures/) into the Swift models — the
// SAME fixture files tools/swarmery/internal/api/notch_fixtures_test.go
// decodes into the live Go DTO structs with DisallowUnknownFields. A DTO rename
// breaks CI on the Go side; a genuinely new/unknown field breaking THIS side
// is exactly the regression these tests guard against (Decodable must keep
// tolerating it). Also covers DaemonClient's URL resolution and its
// snapshot()/approve() wire format via a stubbed URLSession (no real
// network, no external dependency — URLProtocol is Foundation).
import XCTest
@testable import SwarmeryNotch

private enum Fixture {
    static func data(_ name: String) throws -> Data {
        let fileURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // .../Tests/SwarmeryNotchTests/
            .deletingLastPathComponent() // .../Tests/
            .appendingPathComponent("Fixtures")
            .appendingPathComponent(name)
        return try Data(contentsOf: fileURL)
    }
}

/// Records the last request it served and answers from a path-keyed table —
/// just enough of `URLProtocol` to stand in for the daemon in these tests.
final class StubURLProtocol: URLProtocol, @unchecked Sendable {
    // Test-only doubles, mutated only from the main test body before/after an
    // `await` and read back from `startLoading()` on whatever queue
    // URLSession chooses to run it — a data race in theory, never in
    // practice for these sequential, single-in-flight-request tests.
    nonisolated(unsafe) static var responses: [String: (status: Int, body: Data)] = [:]
    nonisolated(unsafe) static var lastRequestBody: Data?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lastRequestBody = Self.bodyData(from: request)
        guard let url = request.url, let stubbed = Self.responses[url.path] else {
            client?.urlProtocol(self, didFailWithError: URLError(.unsupportedURL))
            return
        }
        let response = HTTPURLResponse(
            url: url, statusCode: stubbed.status, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: stubbed.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    /// `URLSession` moves a `POST` body from `httpBody` into `httpBodyStream`
    /// by the time it reaches a custom `URLProtocol`; read whichever is set.
    private static func bodyData(from request: URLRequest) -> Data? {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let read = stream.read(&buffer, maxLength: buffer.count)
            guard read > 0 else { break }
            data.append(buffer, count: read)
        }
        return data.isEmpty ? nil : data
    }

    static func makeSession() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: config)
    }
}

final class ModelsTests: XCTestCase {

    // MARK: - Fixture decode contract

    func testSessionsPageFixtureDecodes() throws {
        struct SessionsPageDTO: Decodable { let sessions: [Session]; let nextCursor: String? }
        let page = try JSONDecoder().decode(SessionsPageDTO.self, from: Fixture.data("sessions.json"))
        XCTAssertEqual(page.sessions.count, 2)
        let first = try XCTUnwrap(page.sessions.first)
        XCTAssertEqual(first.id, 2307)
        XCTAssertEqual(first.sessionUuid, "378d69b2-9942-40b4-b34a-e702a431b3e0")
        XCTAssertEqual(first.status, "active")
        XCTAssertEqual(first.procState, "orphaned")
        // sessionTerminalDTO (migration 0068): session[0] is the populated
        // shape, session[1] the explicit-null shape — the two forms
        // sessionDTO.Terminal ever takes (Go: notch_fixtures_test.go pins the
        // same fixture on the wire side).
        let firstTerminal = try XCTUnwrap(first.terminal)
        XCTAssertEqual(firstTerminal.program, "WarpTerminal")
        XCTAssertEqual(firstTerminal.focusUrl, "warp://action/e30=?window_id=win_ABC123&tab_id=tab_XYZ789")
        XCTAssertEqual(firstTerminal.bundleId, "dev.warp.Warp-Stable")
        XCTAssertEqual(firstTerminal.tty, "ttys004")
        XCTAssertNil(page.sessions[1].terminal)
        XCTAssertNil(page.nextCursor)
    }

    func testApprovalsFixtureDecodes() throws {
        let approvals = try JSONDecoder().decode([PermissionRequest].self, from: Fixture.data("approvals.json"))
        XCTAssertEqual(approvals.count, 2)
        XCTAssertTrue(approvals.contains { $0.status == "pending" })
        let pending = try XCTUnwrap(approvals.first { $0.status == "pending" })
        XCTAssertEqual(pending.sessionId, 1042)
        XCTAssertEqual(pending.toolName, "Bash")
    }

    func testUsageFixtureDecodes() throws {
        let usage = try JSONDecoder().decode(UsageReport.self, from: Fixture.data("usage.json"))
        XCTAssertEqual(usage.accounts.count, 2)
        XCTAssertFalse(usage.providers.isEmpty)
        let firstWindow = try XCTUnwrap(usage.providers.first?.windows.first)
        XCTAssertEqual(firstWindow.key, "session-5h")
        XCTAssertEqual(firstWindow.pace?.status, "behind")
    }

    func testWSPermissionRequestedFixtureDecodesToPermissionRequestedCase() throws {
        let message = try JSONDecoder().decode(WSMessage.self, from: Fixture.data("ws-permission_requested.json"))
        guard case let .permissionRequested(request) = message else {
            return XCTFail("expected .permissionRequested, got \(message)")
        }
        XCTAssertEqual(request.status, "pending")
        XCTAssertEqual(request.sessionId, 1042)
    }

    func testWSSessionUpdatedFixtureDecodesToSessionUpdatedCase() throws {
        let message = try JSONDecoder().decode(WSMessage.self, from: Fixture.data("ws-session_updated.json"))
        guard case let .sessionUpdated(session) = message else {
            return XCTFail("expected .sessionUpdated, got \(message)")
        }
        XCTAssertEqual(session.id, 2307)
        XCTAssertEqual(session.status, "active")
        XCTAssertEqual(session.terminal?.program, "WarpTerminal")
        XCTAssertEqual(session.terminal?.tty, "ttys004")
    }

    // MARK: - Additive-field tolerance (the reason this model layer exists)

    func testUnknownWSMessageTypeDecodesToUnknownInsteadOfThrowing() throws {
        let json = #"{"type":"task_deleted","payload":{"taskId":1,"projectId":2}}"#
        let message = try JSONDecoder().decode(WSMessage.self, from: Data(json.utf8))
        guard case let .unknown(type) = message else {
            return XCTFail("expected .unknown, got \(message)")
        }
        XCTAssertEqual(type, "task_deleted")
    }

    func testEventAppendedDecodesWithoutModelingItsPayload() throws {
        let json = #"{"type":"event_appended","payload":{"sessionId":1,"event":{"anything":"at all"}}}"#
        let message = try JSONDecoder().decode(WSMessage.self, from: Data(json.utf8))
        XCTAssertEqual(message, .eventAppended)
    }

    func testSessionToleratesAdditiveUnknownFields() throws {
        let json = """
        {"id":1,"sessionUuid":"u","projectName":"p","status":"active","title":"t",
         "why":null,"procState":"running","brandNewFutureField":{"anything":true}}
        """
        let session = try JSONDecoder().decode(Session.self, from: Data(json.utf8))
        XCTAssertEqual(session.id, 1)
        XCTAssertNil(session.terminal)
    }

    func testSessionTerminalPopulatedObjectDecodesAllFourFields() throws {
        let json = """
        {"id":1,"sessionUuid":"u","projectName":"p","status":"active","title":"t",
         "why":null,"procState":"running",
         "terminal":{"program":"WarpTerminal","focusUrl":"warp://x","bundleId":"dev.warp.Warp-Stable","tty":"ttys004"}}
        """
        let session = try JSONDecoder().decode(Session.self, from: Data(json.utf8))
        let terminal = try XCTUnwrap(session.terminal)
        XCTAssertEqual(terminal.program, "WarpTerminal")
        XCTAssertEqual(terminal.focusUrl, "warp://x")
        XCTAssertEqual(terminal.bundleId, "dev.warp.Warp-Stable")
        XCTAssertEqual(terminal.tty, "ttys004")
    }

    func testSessionTerminalExplicitNullDecodesToNil() throws {
        let json = """
        {"id":1,"sessionUuid":"u","projectName":"p","status":"active","title":"t",
         "why":null,"procState":"running","terminal":null}
        """
        let session = try JSONDecoder().decode(Session.self, from: Data(json.utf8))
        XCTAssertNil(session.terminal)
    }

    func testSessionToleratesTerminalOfAnUnexpectedShape() throws {
        // A shape that is neither the `{program,focusUrl,bundleId,tty}`
        // object nor JSON null (e.g. a stale bare string, or a future
        // daemon-side regression) must not abort the whole Session decode —
        // only `terminal` itself degrades to nil.
        let json = """
        {"id":1,"sessionUuid":"u","projectName":"p","status":"active","title":"t",
         "why":null,"procState":"running","terminal":"iTerm.app"}
        """
        let session = try JSONDecoder().decode(Session.self, from: Data(json.utf8))
        XCTAssertEqual(session.id, 1)
        XCTAssertNil(session.terminal)
    }

    // MARK: - DaemonClient

    func testResolveBaseURLDefaultsToLocalhostWhenUnset() throws {
        guard ProcessInfo.processInfo.environment["SWARMERY_URL"] == nil else {
            throw XCTSkip("SWARMERY_URL is set in this environment")
        }
        XCTAssertEqual(DaemonClient.resolveBaseURL().absoluteString, "http://127.0.0.1:7777")
    }

    func testSnapshotFetchesAllThreeEndpointsViaStubbedSession() async throws {
        StubURLProtocol.responses = [
            "/api/sessions": (200, try Fixture.data("sessions.json")),
            "/api/approvals": (200, try Fixture.data("approvals.json")),
            "/api/usage": (200, try Fixture.data("usage.json")),
        ]
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777")!, session: StubURLProtocol.makeSession())

        let snapshot = try await client.snapshot()

        XCTAssertEqual(snapshot.sessions.count, 2)
        XCTAssertEqual(snapshot.approvals.count, 2)
        XCTAssertNotNil(snapshot.usage)
    }

    func testApproveSendsActionInBody() async throws {
        StubURLProtocol.responses = ["/api/approvals/42": (200, Data("{}".utf8))]
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777")!, session: StubURLProtocol.makeSession())

        try await client.approve(id: 42, decision: .approve)

        let body = try XCTUnwrap(StubURLProtocol.lastRequestBody)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(json["action"] as? String, "approve")
    }

    func testKillSendsForceInBody() async throws {
        StubURLProtocol.responses = ["/api/sessions/2307/kill": (202, Data())]
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777")!, session: StubURLProtocol.makeSession())

        try await client.kill(sessionId: 2307, force: true)

        let body = try XCTUnwrap(StubURLProtocol.lastRequestBody)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(json["force"] as? Bool, true)
    }

    func testNonSuccessStatusThrowsHTTPError() async throws {
        StubURLProtocol.responses = ["/api/sessions/9/stop": (409, Data(#"{"error":"already finished"}"#.utf8))]
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777")!, session: StubURLProtocol.makeSession())

        do {
            try await client.stop(sessionId: 9)
            XCTFail("expected an error for a 409 response")
        } catch DaemonClientError.http(let status) {
            XCTAssertEqual(status, 409)
        }
    }

    func testTrailingSlashBaseURLProducesASingleSlashPathForGETAndPOST() async throws {
        // StubURLProtocol keys strictly on the clean request path
        // (url.path), so this fails with .unsupportedURL against the
        // un-normalized "http://127.0.0.1:7777//api/..." a trailing-slash
        // base URL used to produce — the double slash Go's
        // net/http.ServeMux 301-redirects, turning a redirected POST into a
        // GET (approve/deny/stop/kill would silently become no-op reads).
        StubURLProtocol.responses = [
            "/api/sessions": (200, try Fixture.data("sessions.json")),
            "/api/approvals": (200, try Fixture.data("approvals.json")),
            "/api/usage": (200, try Fixture.data("usage.json")),
            "/api/sessions/2307/stop": (200, Data("{}".utf8)),
        ]
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777/")!, session: StubURLProtocol.makeSession())

        // GET side.
        let snapshot = try await client.snapshot()
        XCTAssertEqual(snapshot.sessions.count, 2)

        // POST side.
        try await client.stop(sessionId: 2307)
    }
}
