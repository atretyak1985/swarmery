// The full panel: approvals first, then sessions in `AttentionModel`'s sort
// order, capped by `PanelLayout`.
import SwiftUI

struct ExpandedPanel: View {
    /// Fixed width the panel lays out at -- `WidgetWindowGeometry`'s fallback
    /// sizing needs this before anything has ever been measured.
    nonisolated static let panelWidth: CGFloat = 360

    let state: AttentionState
    let actions: WidgetActions
    /// Present for the right-edge placement: the panel is opened by a click,
    /// so it needs a visible way to close. The notch placement collapses on
    /// mouse-out and passes nil.
    var onCollapse: (() -> Void)? = nil

    private var layout: PanelLayout {
        PanelLayout(
            approvals: Array(state.pendingApprovals.values).sorted { $0.id < $1.id },
            sessionRows: state.rows
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let onCollapse {
                HStack(spacing: 6) {
                    Image(systemName: "circle.hexagongrid.fill")
                        .foregroundStyle(state.tabTint)
                    Text("Swarmery").font(.callout.bold())
                    Spacer()
                    Button(action: onCollapse) {
                        Image(systemName: "chevron.right")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("Collapse")
                }
            }
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
