// The collapsed presentation for `WidgetPlacement.rightEdge`: a small vertical
// tab docked to the right screen edge. It carries exactly three signals — an
// accent-tinted icon whose colour is the worst state on screen, a red badge
// with the pending-approval count, and the number of live sessions — so the
// operator reads it in a glance without it competing with the IDE.
import SwiftUI

/// The widget's colours, kept in one place so the tab, the panel surface and
/// the row dots agree. The surface is `NSColor.windowBackgroundColor` — a
/// mid-dark gray in dark mode and a light gray in light mode — rather than
/// black: a black tab against a black notch and a dark editor does not
/// register, which was the operator's complaint about the first build.
enum WidgetPalette {
    static let surface = Color(nsColor: .windowBackgroundColor)
    static let border = Color.primary.opacity(0.14)
    static let accent = Color(red: 0.13, green: 0.64, blue: 0.56)
    static let shadow = Color.black.opacity(0.28)
}

extension AttentionState {
    var workingCount: Int {
        rows.filter { $0.bucket == .working || $0.bucket == .needsYou }.count
    }

    var pendingApprovalCount: Int { pendingApprovals.count }

    /// The tab's icon tint, worst state first: offline is muted, a pending
    /// approval is red, a session that needs the operator is orange, and
    /// everything-fine is the accent colour.
    var tabTint: Color {
        guard isConnected else { return .secondary }
        if pendingApprovalCount > 0 { return .red }
        if rows.contains(where: { $0.bucket == .needsYou || $0.bucket == .error }) { return .orange }
        return WidgetPalette.accent
    }
}

struct EdgeTab: View {
    nonisolated static let size = CGSize(width: 44, height: 64)

    let state: AttentionState

    var body: some View {
        VStack(spacing: 5) {
            ZStack(alignment: .topTrailing) {
                Image(systemName: state.isConnected ? "circle.hexagongrid.fill" : "wifi.slash")
                    .font(.system(size: 20, weight: .semibold))
                    .foregroundStyle(state.tabTint)
                    .frame(width: 26, height: 26)
                if state.pendingApprovalCount > 0 {
                    Text("\(state.pendingApprovalCount)")
                        .font(.caption2.bold())
                        .padding(.horizontal, 4)
                        .padding(.vertical, 1)
                        .background(.red, in: Capsule())
                        .foregroundStyle(.white)
                        .offset(x: 8, y: -6)
                }
            }
            if state.isConnected {
                Text("\(state.workingCount)")
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
        .frame(width: Self.size.width, height: Self.size.height)
        .accessibilityLabel(accessibilityText)
    }

    private var accessibilityText: String {
        guard state.isConnected else { return "Swarmery: daemon offline" }
        return "Swarmery: \(state.workingCount) sessions, \(state.pendingApprovalCount) pending approvals"
    }
}
