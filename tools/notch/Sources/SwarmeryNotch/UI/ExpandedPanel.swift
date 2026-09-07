// The full panel: approvals first, then sessions in `AttentionModel`'s sort
// order, capped by `PanelLayout`.
import SwiftUI

struct ExpandedPanel: View {
    /// Fixed width the panel lays out at -- `WidgetWindowGeometry`'s fallback
    /// sizing needs this before anything has ever been measured.
    nonisolated static let panelWidth: CGFloat = 360

    let state: AttentionState
    let actions: WidgetActions

    private var layout: PanelLayout {
        PanelLayout(
            approvals: Array(state.pendingApprovals.values).sorted { $0.id < $1.id },
            sessionRows: state.rows
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !state.isConnected {
                OfflineBadge()
            }
            UsageStrip(usage: state.usage)
            ForEach(layout.rows) { row in
                switch row {
                case let .approval(request):
                    ApprovalRow(
                        request: request,
                        projectName: state.sessions[request.sessionId]?.projectName,
                        actions: actions
                    )
                case let .session(sessionRow):
                    SessionRow(row: sessionRow, actions: actions)
                }
            }
            if let summary = layout.summary {
                Text(summary)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .frame(width: Self.panelWidth)
    }
}
