// The full panel: approvals first, then sessions in `AttentionModel`'s sort
// order, capped by `PanelLayout`.
import SwiftUI

struct ExpandedPanel: View {
    /// Fixed width the panel lays out at -- `WidgetWindowGeometry`'s fallback
    /// sizing needs this before anything has ever been measured.
    nonisolated static let panelWidth: CGFloat = 360

    let state: AttentionState
    let actions: WidgetActions
    /// The right-edge placement titles the panel, since it stands apart from
    /// the tab that opened it; the notch placement grows out of the strip and
    /// needs no title. Closing is a click on the tab or anywhere outside the
    /// widget, so the header carries no control of its own.
    var showsHeader: Bool = false
    /// The notch panel carries the usage strip; the right-edge sessions panel
    /// does not — usage has its own tab and panel there.
    var showsUsage: Bool = true

    private var layout: PanelLayout {
        PanelLayout(
            approvals: Array(state.pendingApprovals.values).sorted { $0.id < $1.id },
            sessionRows: state.rows
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if showsHeader {
                HStack(spacing: 6) {
                    Image(systemName: "circle.hexagongrid.fill")
                        .foregroundStyle(state.tabTint)
                    Text("Swarmery").font(.callout.bold())
                }
            }
            if !state.isConnected {
                OfflineBadge()
            }
            if showsUsage {
                UsageStrip(usage: state.usage)
            }
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
