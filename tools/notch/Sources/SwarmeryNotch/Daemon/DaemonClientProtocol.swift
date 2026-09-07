// The subset of DaemonClient's surface AppCoordinator depends on -- a seam
// so tests can inject a fake without a real daemon on :7777. DaemonClient
// itself conforms for free below (it already has these exact method
// signatures, see DaemonClient.swift -- default parameter values are not
// part of a protocol requirement's signature, so they carry over unchanged).
import Foundation

public protocol DaemonClientProtocol: Sendable {
    func snapshot() async throws -> DaemonSnapshot
    func approve(id: Int, decision: ApprovalDecision, reason: String?) async throws
    func stop(sessionId: Int) async throws
    func kill(sessionId: Int, force: Bool) async throws
}

extension DaemonClient: DaemonClientProtocol {}
