// Proves the listener at the dashboard's address is the swarmery daemon
// before the notch hands it an unlock ticket. The notch signs
// GET /api/link/health with the link token; the daemon signs its answer
// (X-Swarmery-Sig over the request's nonce, the status and the body —
// tools/swarmery/internal/linksig). A process holding the port without the
// token can neither produce that signature nor read the token off the wire,
// so an unsigned, wrongly signed or non-200 answer — or none — fails the
// proof, and the dashboard opens without a ticket.
import Foundation

public enum DaemonIdentity {
    /// The probe answers from memory; a listener that stalls longer is not
    /// waited for.
    static let timeout: TimeInterval = 3

    /// True when the listener at `origin` (scheme://host:port) proved it holds
    /// the link token in `signer`. False with no key on disk.
    public static func proves(origin: String, signer: LinkSigner, session: URLSession = .shared) async -> Bool {
        guard let url = URL(string: origin + "/api/link/health") else { return false }
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        request.timeoutInterval = timeout
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        guard let signed = signer.signedForVerifiedAnswer(request) else { return false }
        do {
            let (body, response) = try await session.data(for: signed.request, delegate: NoRedirects())
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else { return false }
            return LinkSigner.verifyResponse(key: signed.key, nonce: signed.nonce, status: http.statusCode, body: body,
                                             signature: http.value(forHTTPHeaderField: "X-Swarmery-Sig"))
        } catch {
            return false
        }
    }
}

/// Refuses every redirect: the proof must come from the listener asked.
private final class NoRedirects: NSObject, URLSessionTaskDelegate, Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        nil
    }
}
