// REST client for the swarmery control-plane daemon (tools/swarmery). Talks
// to the same endpoints the dashboard's web client does — see
// tools/swarmery/internal/api/approvals.go, procstop.go, prockill.go and
// tools/swarmery/web/src/api/types.ts for the frozen shapes this mirrors.
import Foundation

/// A single point-in-time read of the daemon's state — the three calls
/// `snapshot()` fans out. Per docs/ws-protocol.md, this is what a client MUST
/// re-fetch on every WS reconnect: the daemon never replays frames missed
/// while offline, so a snapshot always REPLACES local state rather than
/// merging into it (see AttentionModel.reduce(_:.snapshot)).
public struct DaemonSnapshot: Equatable, Sendable {
    public let sessions: [Session]
    public let approvals: [PermissionRequest]
    public let usage: UsageReport?

    public init(sessions: [Session], approvals: [PermissionRequest], usage: UsageReport?) {
        self.sessions = sessions
        self.approvals = approvals
        self.usage = usage
    }
}

/// A dashboard decision on a pending `PermissionRequest`
/// (POST /api/approvals/{id}, `action` field — approvals.go:resolveApproval).
/// Only the two decisions this client issues are modelled; `answer` and
/// `terminal` are dashboard-only flows out of scope for the notch companion.
public enum ApprovalDecision: String, Sendable {
    case approve
    case deny
}

public enum DaemonClientError: Error, Equatable, Sendable {
    case invalidURL
    case invalidResponse
    case http(status: Int)
}

/// Talks to the daemon over plain HTTP — no external dependencies, just
/// Foundation's URLSession. Base URL defaults to the daemon's well-known local
/// port and is overridable via `SWARMERY_URL` (or an explicit `baseURL` at
/// init, which is how tests inject a stubbed `URLSession`).
public struct DaemonClient: Sendable {
    public let baseURL: URL
    private let session: URLSession
    private let decoder: JSONDecoder

    public init(baseURL: URL? = nil, session: URLSession = .shared) {
        self.baseURL = Self.normalize(baseURL ?? Self.resolveBaseURL())
        self.session = session
        self.decoder = JSONDecoder()
    }

    /// `SWARMERY_URL` overrides the default `http://127.0.0.1:7777` — the
    /// same override the rest of the swarmery tooling honours.
    public static func resolveBaseURL() -> URL {
        let env = ProcessInfo.processInfo.environment["SWARMERY_URL"]
        if let env, !env.isEmpty, let url = URL(string: env) {
            return normalize(url)
        }
        guard let fallback = URL(string: "http://127.0.0.1:7777") else {
            preconditionFailure("hardcoded fallback URL must be valid")
        }
        return fallback
    }

    /// Strips trailing "/" characters so `baseURL` never carries one. Without
    /// this, a `SWARMERY_URL` (or explicit `baseURL`) set with a trailing
    /// slash produces a double slash once `makeURL` appends an endpoint path
    /// (`http://127.0.0.1:7777//api/sessions`): Go's `net/http.ServeMux`
    /// 301-redirects that unclean path, and `URLSession` turns a redirected
    /// POST into a GET — approve/deny/stop/kill would silently become no-op
    /// reads.
    private static func normalize(_ url: URL) -> URL {
        var s = url.absoluteString
        while s.hasSuffix("/") {
            s.removeLast()
        }
        return URL(string: s) ?? url
    }

    // MARK: - Snapshot

    /// Fans out to the three read endpoints concurrently. A failure in any one
    /// fails the whole snapshot — the attention model has no meaningful
    /// partial state to render (see AttentionModel.reduce(_:.snapshot)).
    public func snapshot() async throws -> DaemonSnapshot {
        async let sessions = fetchActiveSessions()
        async let approvals = fetchApprovals()
        async let usage = fetchUsage()
        return try await DaemonSnapshot(sessions: sessions, approvals: approvals, usage: usage)
    }

    private struct SessionsPageDTO: Decodable {
        let sessions: [Session]
        let nextCursor: String?
    }

    private func fetchActiveSessions() async throws -> [Session] {
        let page: SessionsPageDTO = try await get(
            path: "/api/sessions",
            query: [URLQueryItem(name: "status", value: "active")]
        )
        return page.sessions
    }

    private func fetchApprovals() async throws -> [PermissionRequest] {
        try await get(path: "/api/approvals", query: [])
    }

    private func fetchUsage() async throws -> UsageReport {
        try await usage(fresh: false)
    }

    public func usage(fresh: Bool) async throws -> UsageReport {
        try await get(path: "/api/usage", query: fresh ? [URLQueryItem(name: "fresh", value: "1")] : [])
    }

    // MARK: - Actions

    /// POST /api/approvals/{id} — approvals.go:resolveApproval.
    public func approve(id: Int, decision: ApprovalDecision, reason: String? = nil) async throws {
        var body: [String: Any] = ["action": decision.rawValue]
        if let reason { body["reason"] = reason }
        _ = try await post(path: "/api/approvals/\(id)", body: body)
    }

    /// POST /api/sessions/{id}/stop — procstop.go:StopSession. Graceful: the
    /// session ends `completed`, not `killed`.
    public func stop(sessionId: Int) async throws {
        _ = try await post(path: "/api/sessions/\(sessionId)/stop", body: [:])
    }

    /// POST /api/sessions/{id}/kill — prockill.go:KillSession. `force: true`
    /// sends SIGKILL immediately instead of SIGTERM-then-escalate.
    public func kill(sessionId: Int, force: Bool = false) async throws {
        _ = try await post(path: "/api/sessions/\(sessionId)/kill", body: ["force": force])
    }

    // MARK: - Transport

    /// Joins `baseURL` (already trailing-slash-free, see `normalize`) and
    /// `path` with exactly one "/" between them, regardless of whether `path`
    /// itself happens to carry a leading one — proper path joining instead of
    /// raw string concatenation, so this stays correct even if a future
    /// caller passes a path without the leading slash.
    private static func joinPath(base: String, path: String) -> String {
        let trimmedBase = base.hasSuffix("/") ? String(base.dropLast()) : base
        let trimmedPath = path.hasPrefix("/") ? path : "/" + path
        return trimmedBase + trimmedPath
    }

    private func makeURL(path: String, query: [URLQueryItem]) throws -> URL {
        guard var components = URLComponents(string: Self.joinPath(base: baseURL.absoluteString, path: path)) else {
            throw DaemonClientError.invalidURL
        }
        if !query.isEmpty {
            components.queryItems = query
        }
        guard let url = components.url else {
            throw DaemonClientError.invalidURL
        }
        return url
    }

    private func get<T: Decodable>(path: String, query: [URLQueryItem]) async throws -> T {
        let url = try makeURL(path: path, query: query)
        let (data, response) = try await session.data(from: url)
        try Self.checkHTTPStatus(response)
        return try decoder.decode(T.self, from: data)
    }

    @discardableResult
    private func post(path: String, body: [String: Any]) async throws -> Data {
        let url = try makeURL(path: path, query: [])
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, response) = try await session.data(for: request)
        try Self.checkHTTPStatus(response)
        return data
    }

    private static func checkHTTPStatus(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else {
            throw DaemonClientError.invalidResponse
        }
        guard (200...299).contains(http.statusCode) else {
            throw DaemonClientError.http(status: http.statusCode)
        }
    }
}
