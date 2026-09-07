// One pending approval in the expanded panel: tool name, a one-line preview
// of the request, and the project it belongs to, with Approve/Deny buttons.
import SwiftUI

struct ApprovalRow: View {
    let request: PermissionRequest
    let projectName: String?
    let actions: WidgetActions

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text(request.toolName).font(.callout.bold())
                if let projectName {
                    Text(projectName)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Text(summary)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
            HStack {
                Button("Deny") { actions.deny(request.id) }
                    .buttonStyle(.bordered)
                Spacer()
                Button("Approve") { actions.approve(request.id) }
                    .buttonStyle(.borderedProminent)
            }
        }
        .padding(8)
        .background(.red.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
    }

    /// `requestJson` is opaque to this client (see Daemon/Models.swift) -- a
    /// best-effort one-line preview, not a parse.
    private var summary: String {
        let trimmed = request.requestJson.trimmingCharacters(in: .whitespacesAndNewlines)
        let collapsed = trimmed.replacingOccurrences(of: "\n", with: " ")
        return String(collapsed.prefix(120))
    }
}
