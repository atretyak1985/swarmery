// Signs requests to the daemon with the link token (Swarmery-HMAC — the
// scheme of tools/swarmery/internal/linksig, documented in
// tools/swarmery/docs/mods-link-protocol.md "Signed requests"). The daemon
// takes a write or a WebSocket upgrade that carries no browser Origin only
// with link auth, and the notch is such a client. The token itself never goes
// on the wire. CryptoKit is a system framework, so the package keeps
// `dependencies: []`.
import CryptoKit
import Foundation

public struct LinkSigner: Sendable {
    /// The token file, normally `~/.swarmery/link-token`. Read on every
    /// request, so a `swarmery link token --rotate` needs no restart.
    public let keyPath: String
    private let now: @Sendable () -> Date
    private let nonce: @Sendable () -> String

    public init(
        keyPath: String = LinkSigner.defaultKeyPath(),
        now: @escaping @Sendable () -> Date = { Date() },
        nonce: @escaping @Sendable () -> String = LinkSigner.randomNonce
    ) {
        self.keyPath = keyPath
        self.now = now
        self.nonce = nonce
    }

    public static func defaultKeyPath() -> String {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".swarmery")
            .appendingPathComponent("link-token")
            .path
    }

    /// 16 random bytes, hex-encoded (the daemon accepts 16–128 of [0-9A-Za-z_-]).
    public static let randomNonce: @Sendable () -> String = {
        var bytes = [UInt8](repeating: 0, count: 16)
        for i in bytes.indices { bytes[i] = UInt8.random(in: 0...255) }
        return hex(bytes)
    }

    /// The trimmed token, or nil when the file is missing, not a regular file
    /// (a symlink is refused), empty, or larger than a minted token can be.
    func readKey() -> String? {
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: keyPath),
              attrs[.type] as? FileAttributeType == .typeRegular,
              let size = attrs[.size] as? Int, size <= 256,
              let data = FileManager.default.contents(atPath: keyPath),
              let text = String(data: data, encoding: .utf8)
        else { return nil }
        let key = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return key.isEmpty ? nil : key
    }

    /// The Authorization value for one request, or nil when there is no key
    /// on disk (the request then goes out unsigned and the daemon answers 401).
    public func authorization(method: String, target: String, body: Data?) -> String? {
        guard let key = readKey() else { return nil }
        let ts = Int64(now().timeIntervalSince1970)
        let n = nonce()
        let sig = Self.sign(key: key, method: method, target: target, ts: ts, nonce: n, body: body ?? Data())
        return "Swarmery-HMAC ts=\(ts),nonce=\(n),sig=\(sig)"
    }

    /// Signs `request` in place (method, path + query as on the request line,
    /// body). A request with no key on disk is left unsigned.
    public func sign(_ request: inout URLRequest) {
        guard let url = request.url,
              let target = Self.requestTarget(of: url),
              let value = authorization(method: request.httpMethod ?? "GET", target: target, body: request.httpBody)
        else { return }
        request.setValue(value, forHTTPHeaderField: "Authorization")
    }

    /// Path and query exactly as URLSession writes them on the request line.
    static func requestTarget(of url: URL) -> String? {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
        let path = c.percentEncodedPath.isEmpty ? "/" : c.percentEncodedPath
        if let q = c.percentEncodedQuery { return path + "?" + q }
        return path
    }

    /// hex HMAC-SHA256(key, "swarmery-hmac-v1\nMETHOD\ntarget\nts\nnonce\nhex(SHA-256(body))").
    static func sign(key: String, method: String, target: String, ts: Int64, nonce: String, body: Data) -> String {
        let bodyHash = hex(Array(SHA256.hash(data: body)))
        let text = ["swarmery-hmac-v1", method.uppercased(), target, String(ts), nonce, bodyHash].joined(separator: "\n")
        let mac = HMAC<SHA256>.authenticationCode(for: Data(text.utf8), using: SymmetricKey(data: Data(key.utf8)))
        return hex(Array(mac))
    }

    static func hex(_ bytes: [UInt8]) -> String {
        bytes.map { String(format: "%02x", $0) }.joined()
    }
}
