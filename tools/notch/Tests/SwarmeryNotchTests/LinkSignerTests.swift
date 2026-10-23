// The notch authenticates to the daemon like every other non-browser client:
// its writes and its event-stream upgrade are signed with the link token
// (Swarmery-HMAC, tools/swarmery/internal/linksig). Also: the focus URL a
// daemon reports is opened only for the warp and https schemes.
import XCTest
@testable import SwarmeryNotch

/// Records the Authorization header of the last request it served.
final class AuthRecordingURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var lastAuthorization: String?
    nonisolated(unsafe) static var lastPath: String?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lastAuthorization = request.value(forHTTPHeaderField: "Authorization")
        Self.lastPath = request.url?.path
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: [:])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}

    static func makeSession() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [AuthRecordingURLProtocol.self]
        return URLSession(configuration: config)
    }
}

final class LinkSignerTests: XCTestCase {
    private func keyFile(_ key: String) throws -> String {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let path = dir.appendingPathComponent("link-token").path
        try Data((key + "\n").utf8).write(to: URL(fileURLWithPath: path))
        return path
    }

    /// The vector pinned in tools/swarmery/internal/linksig/linksig_test.go
    /// (TestSigCrossLanguageVector): a drift on either side fails one of them.
    func testCrossLanguageVector() {
        let sig = LinkSigner.sign(
            key: String(repeating: "k", count: 64), method: "get",
            target: "/api/link/permission/7?session=s1&wait=25", ts: 1_791_920_751,
            nonce: "0123456789abcdef0123456789abcdef", body: Data()
        )
        XCTAssertEqual(sig, "182f66fc97d1e1100ad18eab89718f01571529d8e904efaec47c41c0208ebbff")
    }

    func testAuthorizationHeaderShapeAndNoKeyOnTheWire() throws {
        let key = String(repeating: "a", count: 64)
        let signer = LinkSigner(keyPath: try keyFile(key), now: { Date(timeIntervalSince1970: 1_791_920_751) },
                                nonce: { "0123456789abcdef0123456789abcdef" })
        let value = try XCTUnwrap(signer.authorization(method: "POST", target: "/api/approvals/42", body: Data("{}".utf8)))
        let expected = LinkSigner.sign(key: key, method: "POST", target: "/api/approvals/42", ts: 1_791_920_751,
                                       nonce: "0123456789abcdef0123456789abcdef", body: Data("{}".utf8))
        XCTAssertEqual(value, "Swarmery-HMAC ts=1791920751,nonce=0123456789abcdef0123456789abcdef,sig=\(expected)")
        XCTAssertFalse(value.contains(key))
    }

    func testNoKeyFileMeansUnsigned() {
        let signer = LinkSigner(keyPath: "/nonexistent/link-token")
        XCTAssertNil(signer.authorization(method: "POST", target: "/api/x", body: nil))
    }

    func testRequestTargetKeepsTheQuery() throws {
        let url = try XCTUnwrap(URL(string: "ws://127.0.0.1:7777/api/ws?x=1&y=a%20b"))
        XCTAssertEqual(LinkSigner.requestTarget(of: url), "/api/ws?x=1&y=a%20b")
    }

    func testApproveIsSigned() async throws {
        let signer = LinkSigner(keyPath: try keyFile(String(repeating: "b", count: 64)))
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777")!,
                                  session: AuthRecordingURLProtocol.makeSession(), signer: signer)
        AuthRecordingURLProtocol.lastAuthorization = nil
        try await client.approve(id: 42, decision: .approve)
        XCTAssertEqual(AuthRecordingURLProtocol.lastPath, "/api/approvals/42")
        let auth = try XCTUnwrap(AuthRecordingURLProtocol.lastAuthorization)
        XCTAssertTrue(auth.hasPrefix("Swarmery-HMAC ts="), auth)
    }

    func testKillAndStopAreSigned() async throws {
        let signer = LinkSigner(keyPath: try keyFile(String(repeating: "c", count: 64)))
        let client = DaemonClient(baseURL: URL(string: "http://127.0.0.1:7777")!,
                                  session: AuthRecordingURLProtocol.makeSession(), signer: signer)
        for call in [{ try await client.stop(sessionId: 9) }, { try await client.kill(sessionId: 9, force: true) }] {
            AuthRecordingURLProtocol.lastAuthorization = nil
            try await call()
            XCTAssertNotNil(AuthRecordingURLProtocol.lastAuthorization)
        }
    }

    func testWebSocketHandshakeIsSignedPerAttempt() throws {
        let signer = LinkSigner(keyPath: try keyFile(String(repeating: "d", count: 64)))
        let url = URL(string: "ws://127.0.0.1:7777/api/ws")!
        let first = WSStream.handshakeRequest(for: url, signer: signer).value(forHTTPHeaderField: "Authorization")
        let second = WSStream.handshakeRequest(for: url, signer: signer).value(forHTTPHeaderField: "Authorization")
        XCTAssertNotNil(first)
        XCTAssertNotEqual(first, second, "every connect attempt needs a fresh nonce")
    }

    // MARK: - focus URL allow-list (L-1)

    private let dashboard = URL(string: "http://127.0.0.1:7777")!

    func testWarpAndHTTPSFocusURLsAreOpened() {
        for raw in ["warp://action/focus_tab?id=3", "https://example.com/focus"] {
            let terminal = SessionTerminal(program: "WarpTerminal", focusUrl: raw, bundleId: nil, tty: nil)
            XCTAssertEqual(TerminalFocus.strategy(for: terminal, sessionUuid: "u", dashboardBaseURL: dashboard),
                           .openURL(URL(string: raw)!), raw)
        }
    }

    func testOtherFocusURLSchemesFallThrough() {
        for raw in ["file:///Applications/Calculator.app", "http://example.com/x", "x-custom-app://run?cmd=1",
                    "javascript:alert(1)", "smb://host/share"] {
            let terminal = SessionTerminal(program: "WarpTerminal", focusUrl: raw, bundleId: "dev.warp.Warp-Stable", tty: nil)
            XCTAssertEqual(TerminalFocus.strategy(for: terminal, sessionUuid: "u", dashboardBaseURL: dashboard),
                           .activateApp(bundleID: "dev.warp.Warp-Stable"), raw)
        }
    }
}
