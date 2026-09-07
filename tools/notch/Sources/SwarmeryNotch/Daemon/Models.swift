// Decodable models mirroring the daemon's frozen JSON contract
// (tools/swarmery/web/src/api/types.ts, docs/ws-protocol.md). Field NAMES are
// pinned 1:1 with the TypeScript/Go DTOs so tools/swarmery/internal/api/
// notch_fixtures_test.go can decode the SAME fixture files into the live Go
// DTO structs with DisallowUnknownFields — a renamed or removed field breaks
// CI there. Swift decoding, by contrast, deliberately TOLERATES additive
// fields: `Decodable`'s default behaviour ignores JSON keys with no matching
// property, so this client survives a daemon that ships new fields before
// this package catches up. That asymmetry is intentional (see phase-2 plan
// doc, "Implementation Details").
import Foundation

/// Mirrors `Session` (web/src/api/types.ts / Go: sessionDTO), trimmed to the
/// fields the notch companion actually renders.
public struct Session: Decodable, Identifiable, Equatable, Sendable {
    public let id: Int
    public let sessionUuid: String
    public let projectName: String?
    public let status: String
    public let title: String?
    public let why: String?
    /// Process liveness from procwatch: "running" | "orphaned" | "dead" |
    /// "unknown" | nil (untracked). NOTE: "orphaned" is the NORMAL state for a
    /// headless/background run (confirmed against the live daemon while
    /// building the fixtures for this phase) — it must never be treated as an
    /// error on its own; see AttentionModel.swift.
    public let procState: String?
    /// Additive field a CONCURRENT phase-1 change is adding to the daemon's
    /// Session DTO; its shape was not yet frozen from this phase's vantage
    /// point, so it decodes best-effort in `init(from:)` below and swallows a
    /// type mismatch to nil rather than fail the whole `Session` decode.
    public let terminal: String?

    public init(
        id: Int,
        sessionUuid: String,
        projectName: String?,
        status: String,
        title: String?,
        why: String?,
        procState: String?,
        terminal: String? = nil
    ) {
        self.id = id
        self.sessionUuid = sessionUuid
        self.projectName = projectName
        self.status = status
        self.title = title
        self.why = why
        self.procState = procState
        self.terminal = terminal
    }

    private enum CodingKeys: String, CodingKey {
        case id, sessionUuid, projectName, status, title, why, procState, terminal
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(Int.self, forKey: .id)
        sessionUuid = try c.decode(String.self, forKey: .sessionUuid)
        projectName = try c.decodeIfPresent(String.self, forKey: .projectName)
        status = try c.decode(String.self, forKey: .status)
        title = try c.decodeIfPresent(String.self, forKey: .title)
        why = try c.decodeIfPresent(String.self, forKey: .why)
        procState = try c.decodeIfPresent(String.self, forKey: .procState)
        // [LOW-CONFIDENCE]: `terminal`'s real shape lands with phase 1, out of
        // scope for this worktree. `try?` turns "present but a different
        // shape than a bare string" into nil instead of an aborted decode.
        terminal = try? c.decodeIfPresent(String.self, forKey: .terminal)
    }
}

/// Mirrors `PermissionRequest` (web/src/api/types.ts / Go: permissionRequestDTO).
/// Frozen contract — no additive-field tolerance needed beyond Decodable's
/// default (ignore-unknown-keys) behaviour.
public struct PermissionRequest: Decodable, Identifiable, Equatable, Sendable {
    public let id: Int
    public let sessionId: Int
    public let toolName: String
    /// The PermissionRequest hook stdin, verbatim — opaque to this client.
    public let requestJson: String
    public let status: String
    public let requestedAt: String
    public let resolvedAt: String?
    public let resolvedVia: String?
    public let reason: String?
    public let expiresAt: String

    public init(
        id: Int,
        sessionId: Int,
        toolName: String,
        requestJson: String,
        status: String,
        requestedAt: String,
        resolvedAt: String?,
        resolvedVia: String?,
        reason: String?,
        expiresAt: String
    ) {
        self.id = id
        self.sessionId = sessionId
        self.toolName = toolName
        self.requestJson = requestJson
        self.status = status
        self.requestedAt = requestedAt
        self.resolvedAt = resolvedAt
        self.resolvedVia = resolvedVia
        self.reason = reason
        self.expiresAt = expiresAt
    }
}

/// Mirrors `UsagePace` (web/src/api/types.ts / Go: usage.Pace).
public struct UsagePace: Decodable, Equatable, Sendable {
    public let status: String
    public let percentElapsed: Double
    public let message: String

    public init(status: String, percentElapsed: Double, message: String) {
        self.status = status
        self.percentElapsed = percentElapsed
        self.message = message
    }
}

/// Mirrors `UsageWindow` (web/src/api/types.ts / Go: usage.Window).
public struct UsageWindow: Decodable, Identifiable, Equatable, Sendable {
    public let key: String
    public let label: String
    public let percentUsed: Double
    public let percentLeft: Double
    public let resetText: String?
    public let resetMs: Int64?
    public let resetAt: String?
    public let windowDurationMs: Int64?
    public let pace: UsagePace?
    public let source: String
    public let used: Int64?
    public let limit: Int64?

    public var id: String { key }

    public init(
        key: String,
        label: String,
        percentUsed: Double,
        percentLeft: Double,
        resetText: String? = nil,
        resetMs: Int64? = nil,
        resetAt: String? = nil,
        windowDurationMs: Int64? = nil,
        pace: UsagePace? = nil,
        source: String,
        used: Int64? = nil,
        limit: Int64? = nil
    ) {
        self.key = key
        self.label = label
        self.percentUsed = percentUsed
        self.percentLeft = percentLeft
        self.resetText = resetText
        self.resetMs = resetMs
        self.resetAt = resetAt
        self.windowDurationMs = windowDurationMs
        self.pace = pace
        self.source = source
        self.used = used
        self.limit = limit
    }
}

/// Mirrors `UsageProvider` (web/src/api/types.ts / Go: usage.Provider),
/// trimmed to the fields the panel needs (no `hint`/no-auth guidance UI in
/// this phase — pixels are phase 3's job).
public struct UsageProvider: Decodable, Equatable, Sendable {
    public let account: String
    public let name: String
    public let status: String
    public let error: String?
    public let plan: String?
    public let source: String
    public let windows: [UsageWindow]
    public let connectedVia: String?

    public init(
        account: String,
        name: String,
        status: String,
        error: String? = nil,
        plan: String? = nil,
        source: String,
        windows: [UsageWindow],
        connectedVia: String? = nil
    ) {
        self.account = account
        self.name = name
        self.status = status
        self.error = error
        self.plan = plan
        self.source = source
        self.windows = windows
        self.connectedVia = connectedVia
    }
}

/// Mirrors `UsageAccount` (web/src/api/types.ts / Go: usageAccount).
public struct UsageAccount: Decodable, Equatable, Sendable {
    public let account: String
    public let providers: [UsageProvider]
    public let error: String?

    public init(account: String, providers: [UsageProvider], error: String? = nil) {
        self.account = account
        self.providers = providers
        self.error = error
    }
}

/// Mirrors `UsageResp` (web/src/api/types.ts) / Go: usageResp — GET /api/usage.
public struct UsageReport: Decodable, Equatable, Sendable {
    public let generatedAt: String
    public let providers: [UsageProvider]
    public let accounts: [UsageAccount]

    public init(generatedAt: String, providers: [UsageProvider], accounts: [UsageAccount]) {
        self.generatedAt = generatedAt
        self.providers = providers
        self.accounts = accounts
    }
}

/// Mirrors the `WSMessage` union (web/src/api/types.ts, docs/ws-protocol.md),
/// keyed on `type`. `event_appended` is deliberately ignored (no payload
/// decode is even attempted — the notch companion has no use for individual
/// transcript events), and every type this phase does not know about decodes
/// to `.unknown` instead of throwing, so a daemon that ships a brand-new frame
/// kind (as it has repeatedly — system_item_updated, task_updated,
/// plan_updated, task_deleted, …) never breaks the WS stream.
public enum WSMessage: Decodable, Equatable, Sendable {
    case sessionStarted(Session)
    case sessionUpdated(Session)
    case eventAppended
    case permissionRequested(PermissionRequest)
    case permissionResolved(PermissionRequest)
    case unknown(type: String)

    private enum CodingKeys: String, CodingKey {
        case type, payload
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let type = try c.decode(String.self, forKey: .type)
        switch type {
        case "session_started":
            self = .sessionStarted(try c.decode(Session.self, forKey: .payload))
        case "session_updated":
            self = .sessionUpdated(try c.decode(Session.self, forKey: .payload))
        case "event_appended":
            self = .eventAppended
        case "permission_requested":
            self = .permissionRequested(try c.decode(PermissionRequest.self, forKey: .payload))
        case "permission_resolved":
            self = .permissionResolved(try c.decode(PermissionRequest.self, forKey: .payload))
        default:
            self = .unknown(type: type)
        }
    }
}
