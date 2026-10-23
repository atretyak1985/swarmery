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
        return authorization(key: key, method: method, target: target, body: body).value
    }

    private func authorization(key: String, method: String, target: String, body: Data?) -> (value: String, nonce: String) {
        let ts = Int64(now().timeIntervalSince1970)
        let n = nonce()
        let sig = Self.sign(key: key, method: method, target: target, ts: ts, nonce: n, body: body ?? Data())
        return ("Swarmery-HMAC ts=\(ts),nonce=\(n),sig=\(sig)", n)
    }

    /// A signed copy of `request` with the key and nonce its answer must be
    /// signed over (`verifyResponse`), or nil when there is no key on disk.
    func signedForVerifiedAnswer(_ request: URLRequest) -> (request: URLRequest, key: String, nonce: String)? {
        guard let key = readKey(), let url = request.url, let target = Self.requestTarget(of: url) else { return nil }
        let auth = authorization(key: key, method: request.httpMethod ?? "GET", target: target, body: request.httpBody)
        var signed = request
        signed.setValue(auth.value, forHTTPHeaderField: "Authorization")
        return (signed, key, auth.nonce)
    }

    /// hex HMAC-SHA256(key, "swarmery-hmac-v1-response\nnonce\nstatus\nhex(SHA-256(body))")
    /// — the daemon's X-Swarmery-Sig (linksig.SignResponse).
    static func responseSignature(key: String, nonce: String, status: Int, body: Data) -> String {
        let bodyHash = hex(Array(SHA256.hash(data: body)))
        let text = ["swarmery-hmac-v1-response", nonce, String(status), bodyHash].joined(separator: "\n")
        let mac = HMAC<SHA256>.authenticationCode(for: Data(text.utf8), using: SymmetricKey(data: Data(key.utf8)))
        return hex(Array(mac))
    }

    /// Whether `signature` is the daemon's signature of this answer to the
    /// request that carried `nonce`; compared in constant time. An empty key,
    /// nonce or signature never verifies.
    static func verifyResponse(key: String, nonce: String, status: Int, body: Data, signature: String?) -> Bool {
        guard !key.isEmpty, !nonce.isEmpty, let signature, !signature.isEmpty else { return false }
        let want = Array(responseSignature(key: key, nonce: nonce, status: status, body: body).utf8)
        let got = Array(signature.lowercased().utf8)
        guard want.count == got.count else { return false }
        return zip(want, got).reduce(UInt8(0)) { $0 | ($1.0 ^ $1.1) } == 0
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

    // MARK: - Dashboard unlock ticket

    /// A one-shot ticket that unlocks the browser dashboard: `<ts>.<nonce>.<sig>`.
    /// The dashboard is opened as `<url>#swarmery-unlock=<ticket>` and trades it
    /// for a session token at `POST /api/session/unlock`; like a signed request,
    /// the ticket proves the link token without carrying it. It is bound to
    /// `origin` (canonical, see `unlockOrigin(of:)`), the origin the page is
    /// opened at: the daemon verifies it for the unlock request's Origin. Nil
    /// when there is no key on disk (the page then opens locked and says how to
    /// unlock).
    public func unlockTicket(origin: String, now: Date? = nil, nonce: String? = nil) -> String? {
        guard let key = readKey() else { return nil }
        let ts = Int64((now ?? self.now()).timeIntervalSince1970)
        let n = nonce ?? self.nonce()
        return "\(ts).\(n).\(Self.unlockSignature(key: key, origin: origin, ts: String(ts), nonce: n))"
    }

    /// The canonical origin an unlock ticket is bound to — the same form as
    /// `linksig.UnlockOrigin` in the daemon: lowercase http(s), an IP address
    /// literal host (never a name, not the IPv4-mapped IPv6 form), the port
    /// spelled out (the scheme's default when absent), IPv6 bracketed and
    /// compressed. Nil for anything else.
    public static func unlockOrigin(of url: URL) -> String? {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let scheme = components.scheme?.lowercased(), scheme == "http" || scheme == "https",
              components.user == nil,
              var host = components.host
        else { return nil }
        if host.hasPrefix("[") && host.hasSuffix("]") { host = String(host.dropFirst().dropLast()) }
        let port = components.port ?? (scheme == "https" ? 443 : 80)
        guard (1...65535).contains(port), let literal = canonicalAddress(host) else { return nil }
        return "\(scheme)://\(literal):\(port)"
    }

    /// An IPv4 literal as dotted quad, an IPv6 one compressed in brackets; nil
    /// for a name, a zone, or an IPv4-mapped IPv6 address.
    static func canonicalAddress(_ host: String) -> String? {
        var v4 = in_addr()
        if inet_pton(AF_INET, host, &v4) == 1 {
            var buf = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
            guard inet_ntop(AF_INET, &v4, &buf, socklen_t(buf.count)) != nil else { return nil }
            return cString(buf)
        }
        var v6 = in6_addr()
        guard inet_pton(AF_INET6, host, &v6) == 1 else { return nil }
        let bytes = withUnsafeBytes(of: &v6) { Array($0) }
        if bytes[0..<10].allSatisfy({ $0 == 0 }) && bytes[10] == 0xff && bytes[11] == 0xff { return nil }
        var buf = [CChar](repeating: 0, count: Int(INET6_ADDRSTRLEN))
        guard inet_ntop(AF_INET6, &v6, &buf, socklen_t(buf.count)) != nil else { return nil }
        return "[\(cString(buf))]"
    }

    private static func cString(_ buf: [CChar]) -> String {
        String(decoding: buf.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self)
    }

    /// hex HMAC-SHA256(key, "swarmery-unlock\norigin\nts\nnonce").
    static func unlockSignature(key: String, origin: String, ts: String, nonce: String) -> String {
        let text = "swarmery-unlock\n\(origin)\n\(ts)\n\(nonce)"
        let mac = HMAC<SHA256>.authenticationCode(for: Data(text.utf8), using: SymmetricKey(data: Data(key.utf8)))
        return hex(Array(mac))
    }

    static func hex(_ bytes: [UInt8]) -> String {
        bytes.map { String(format: "%02x", $0) }.joined()
    }
}
