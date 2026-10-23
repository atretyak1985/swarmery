// Before the notch appends an unlock ticket to the dashboard URL, the listener
// must prove it is the daemon: a request signed with the link token to
// GET /api/link/health, whose answer carries the daemon's X-Swarmery-Sig. A
// listener that answers unsigned (or is down) gets the plain URL, no ticket.
import XCTest
@testable import SwarmeryNotch

/// A fake daemon: answers /api/link/health with 200, signed over the request's
/// nonce under `key` — or unsigned when `key` is nil, or not at all when
/// `down` is set.
final class IdentityURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var key: String?
    nonisolated(unsafe) static var down = false
    nonisolated(unsafe) static var hits = 0

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.hits += 1
        if Self.down {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let body = Data(#"{"ok":true}"#.utf8)
        var headers: [String: String] = [:]
        if let key = Self.key,
           let auth = request.value(forHTTPHeaderField: "Authorization"),
           let nonce = auth.split(separator: ",").compactMap({ part -> String? in
               let kv = part.split(separator: "=", maxSplits: 1)
               return kv.count == 2 && kv[0].hasSuffix("nonce") ? String(kv[1]) : nil
           }).first {
            headers["X-Swarmery-Sig"] = LinkSigner.responseSignature(key: key, nonce: nonce, status: 200, body: body)
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}

    static func makeSession() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [IdentityURLProtocol.self]
        return URLSession(configuration: config)
    }
}

final class DaemonIdentityTests: XCTestCase {
    private let key = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

    private func signer() throws -> LinkSigner {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let path = dir.appendingPathComponent("link-token").path
        try Data((key + "\n").utf8).write(to: URL(fileURLWithPath: path))
        return LinkSigner(keyPath: path)
    }

    override func setUp() {
        IdentityURLProtocol.key = nil
        IdentityURLProtocol.down = false
        IdentityURLProtocol.hits = 0
    }

    /// The response vector pinned in tools/swarmery/internal/linksig
    /// linksig_test.go TestSigCrossLanguageVector (crossVectorResponseSig).
    func testResponseSignatureMatchesTheDaemon() {
        XCTAssertEqual(
            LinkSigner.responseSignature(key: String(repeating: "k", count: 64), nonce: "0123456789abcdef0123456789abcdef",
                                         status: 200, body: Data(#"{"decision":"allow"}"#.utf8)),
            "e9b45a06929b2cf77c1cb31a157fb41cd083027126c29e4f28f3d1cd04056fcd"
        )
        let sig = LinkSigner.responseSignature(key: key, nonce: "0123456789abcdef0123456789abcdef", status: 200,
                                               body: Data(#"{"ok":true}"#.utf8))
        XCTAssertTrue(LinkSigner.verifyResponse(key: key, nonce: "0123456789abcdef0123456789abcdef", status: 200,
                                                body: Data(#"{"ok":true}"#.utf8), signature: sig))
        XCTAssertFalse(LinkSigner.verifyResponse(key: key, nonce: "0123456789abcdef0123456789abcdef", status: 201,
                                                 body: Data(#"{"ok":true}"#.utf8), signature: sig))
        XCTAssertFalse(LinkSigner.verifyResponse(key: key, nonce: "n", status: 200, body: Data(), signature: nil))
    }

    func testASignedDaemonGetsTheTicket() async throws {
        IdentityURLProtocol.key = key
        let url = await TerminalFocus.verifiedDashboardURL(URL(string: "http://localhost:7777/sessions/u")!,
                                                           signer: try signer(), session: IdentityURLProtocol.makeSession())
        XCTAssertEqual(IdentityURLProtocol.hits, 1)
        XCTAssertEqual(url.host, "127.0.0.1")
        let fragment = try XCTUnwrap(URLComponents(url: url, resolvingAgainstBaseURL: false)?.fragment)
        XCTAssertTrue(fragment.hasPrefix("swarmery-unlock="), fragment)
    }

    func testAnUnsignedListenerGetsThePlainURL() async throws {
        let base = URL(string: "http://127.0.0.1:7777/sessions/u")!
        let url = await TerminalFocus.verifiedDashboardURL(base, signer: try signer(), session: IdentityURLProtocol.makeSession())
        XCTAssertEqual(IdentityURLProtocol.hits, 1)
        XCTAssertEqual(url, base)
    }

    func testAListenerSigningWithAnotherKeyGetsThePlainURL() async throws {
        IdentityURLProtocol.key = String(repeating: "f", count: 64)
        let base = URL(string: "http://127.0.0.1:7777")!
        let url = await TerminalFocus.verifiedDashboardURL(base, signer: try signer(), session: IdentityURLProtocol.makeSession())
        XCTAssertEqual(url, base)
    }

    func testADownDaemonGetsThePlainURL() async throws {
        IdentityURLProtocol.down = true
        let base = URL(string: "http://127.0.0.1:7777")!
        let url = await TerminalFocus.verifiedDashboardURL(base, signer: try signer(), session: IdentityURLProtocol.makeSession())
        XCTAssertEqual(url, base)
    }

    func testARemoteDashboardIsNotProbed() async throws {
        IdentityURLProtocol.key = key
        let base = URL(string: "https://dashboard.example.com")!
        let url = await TerminalFocus.verifiedDashboardURL(base, signer: try signer(), session: IdentityURLProtocol.makeSession())
        XCTAssertEqual(url, base)
        XCTAssertEqual(IdentityURLProtocol.hits, 0)
    }
}
